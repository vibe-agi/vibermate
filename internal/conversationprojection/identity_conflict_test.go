package conversationprojection

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

// A Codex subagent's local log can name a different actor than the wire
// headers already recorded for one Exchange. That Exchange keeps its wire
// identity; the rest of the pass must still be indexed.
func TestReindexKeepsWireIdentityWhenLocalStateConflicts(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	wire := func(response string) agentconversation.ClientIdentity {
		return agentconversation.ClientIdentity{
			Client: "codex", SessionID: "session", SessionResumable: true,
			ProviderResponseID: response,
			ProtocolIDs: []agentconversation.ClientEvidenceValue{
				{Name: "openai_responses.session_id", Value: "session"},
			},
			Source: agentconversation.ClientIdentitySourceProtocolEvidence, Confidence: "exact", ObservedAt: now,
		}
	}
	identities := &conflictingIdentities{
		identityRepository: identityRepository{values: map[string]agentconversation.ClientIdentity{
			"conflict": wire("response-conflict"),
			"clean":    wire("response-clean"),
		}},
		conflict: "conflict",
	}
	local := func(response string) agentconversation.ClientIdentity {
		identity := wire(response)
		identity.Source = agentconversation.ClientIdentitySourceLocalState
		return identity
	}
	resolver := &fixedResolver{values: map[string]agentconversation.ClientIdentity{
		"response-conflict": local("response-conflict"),
		"response-clean":    local("response-clean"),
	}}
	indexer, err := New(Options{
		Activities: listedActivities{records: []activity.Record{
			{SubjectID: "conflict", CaptureRunID: "run", SourceDisplayName: "Codex", OccurredAt: now},
			{SubjectID: "clean", CaptureRunID: "run", SourceDisplayName: "Codex", OccurredAt: now},
		}},
		Contents:   &absentContents{},
		Identities: identities, Writer: identities, CaptureRuns: runReader{},
		Resolvers: map[string]agentconversation.ClientIdentityResolver{"codex": resolver},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := indexer.Reindex(ctx, activity.ConversationIndexRequest{CaptureRunID: "run", Limit: 10}); err != nil {
		t.Fatalf("Reindex() = %v, want the conflicting Exchange skipped", err)
	}
	if identities.values["clean"].Source != agentconversation.ClientIdentitySourceLocalState {
		t.Fatalf("clean Exchange was not enriched: %+v", identities.values["clean"])
	}
	if identities.values["conflict"].Source != agentconversation.ClientIdentitySourceProtocolEvidence {
		t.Fatalf("conflicting Exchange lost its wire identity: %+v", identities.values["conflict"])
	}
}

type conflictingIdentities struct {
	identityRepository
	conflict string
}

func (repository *conflictingIdentities) PutConversationIdentity(
	ctx context.Context,
	id string,
	value agentconversation.ClientIdentity,
) error {
	if id == repository.conflict {
		return fmt.Errorf("%w: Exchange Agent identity changed", activity.ErrInvalidEvent)
	}
	return repository.identityRepository.PutConversationIdentity(ctx, id, value)
}

type listedActivities struct {
	activity.Reader
	records []activity.Record
}

func (reader listedActivities) ListExchanges(context.Context, activity.PageRequest) (activity.Page, error) {
	return activity.Page{Items: reader.records}, nil
}

type absentContents struct{ metadataOnlyReader }

func (*absentContents) GetConversationEvidence(context.Context, string) (exchangecontent.ConversationEvidence, error) {
	return exchangecontent.ConversationEvidence{}, exchangecontent.ErrNotFound
}

type fixedResolver struct {
	values map[string]agentconversation.ClientIdentity
}

func (resolver *fixedResolver) ResolveBatch(
	_ context.Context,
	_ string,
	lookups []agentconversation.ClientIdentityLookup,
) (map[string]agentconversation.ClientIdentity, error) {
	resolved := make(map[string]agentconversation.ClientIdentity, len(lookups))
	for _, lookup := range lookups {
		if identity, ok := resolver.values[lookup.ProviderResponseID]; ok {
			resolved[lookup.ProviderResponseID] = identity
		}
	}
	return resolved, nil
}
