package conversationprojection

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestIdentityAndReindexNeverReadConversationBodies(t *testing.T) {
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	evidence := exchangecontent.ConversationEvidence{
		ExchangeID: "exchange", RecordedAt: now, ExpiresAt: now.Add(time.Hour), ResponseID: "response",
		RequestProtocolEvidence: []protocolcore.ProtocolEvidenceValue{
			{Name: "openai_responses.session_id", Value: "session"},
			{Name: "openai_responses.thread_id", Value: "thread"},
			{Name: "openai_responses.turn_id", Value: "turn"},
		},
		ResponseProtocolEvidence: []protocolcore.ProtocolEvidenceValue{{Name: "provider.output.0000.id", Value: "message"}},
	}
	contents := &metadataOnlyReader{value: evidence}
	identities := &identityRepository{values: map[string]agentconversation.ClientIdentity{}}
	resolver := &lookupResolver{}
	indexer, err := New(Options{
		Activities: activityReader{record: activity.Record{SubjectID: "exchange", CaptureRunID: "run", SourceDisplayName: "Codex", OccurredAt: now}},
		Contents:   contents, Identities: identities, Writer: identities, CaptureRuns: runReader{},
		Resolvers: map[string]agentconversation.ClientIdentityResolver{"codex": resolver},
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := indexer.Identity(context.Background(), "exchange")
	if err != nil || identity.SessionID != "session" || identity.ProviderResponseID != "response" {
		t.Fatalf("identity = %+v, %v", identity, err)
	}
	if err := indexer.Reindex(context.Background(), activity.ConversationIndexRequest{CaptureRunID: "run", Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if contents.reads != 2 || identities.projections != 1 || len(resolver.lookups) != 1 {
		t.Fatalf("metadata reads=%d projections=%d lookups=%v", contents.reads, identities.projections, resolver.lookups)
	}
	lookup := resolver.lookups[0]
	if lookup.ProviderResponseID != evidence.ResponseID ||
		!reflect.DeepEqual(lookup.ProtocolEvidence, evidence.RequestProtocolEvidence) ||
		!reflect.DeepEqual(lookup.ResponseProtocolEvidence, evidence.ResponseProtocolEvidence) {
		t.Fatalf("identity lookup changed evidence: %+v", lookup)
	}
	if _, err := indexer.Identity(context.Background(), "exchange"); err != nil || contents.reads != 2 {
		t.Fatalf("persisted identity reread content: reads=%d err=%v", contents.reads, err)
	}
}

// Deliberately no implementation for full content reads: invoking one fails
// the test, so this asserts the read cost at the public reader interface.
type metadataOnlyReader struct {
	exchangecontent.Reader
	value exchangecontent.ConversationEvidence
	reads int
}

func (reader *metadataOnlyReader) GetConversationEvidence(context.Context, string) (exchangecontent.ConversationEvidence, error) {
	reader.reads++
	return reader.value.Clone(), nil
}

type activityReader struct {
	activity.Reader
	record activity.Record
}

func (reader activityReader) ListExchanges(context.Context, activity.PageRequest) (activity.Page, error) {
	return activity.Page{Items: []activity.Record{reader.record}}, nil
}

type runReader struct{ capturerun.Reader }

func (runReader) GetRun(context.Context, string) (capturerun.View, error) {
	return capturerun.View{ExecutableLabel: "codex", CWD: "/synthetic/workspace"}, nil
}

type identityRepository struct {
	values      map[string]agentconversation.ClientIdentity
	projections int
}

func (repository *identityRepository) GetConversationIdentity(_ context.Context, id string) (agentconversation.ClientIdentity, error) {
	if value, ok := repository.values[id]; ok {
		return value, nil
	}
	return agentconversation.ClientIdentity{}, activity.ErrExchangeNotFound
}

func (repository *identityRepository) PutConversationIdentity(_ context.Context, id string, value agentconversation.ClientIdentity) error {
	repository.values[id] = value
	return nil
}

func (repository *identityRepository) ReprojectConversation(context.Context, string, agentconversation.Ref) error {
	repository.projections++
	return nil
}

type lookupResolver struct {
	lookups []agentconversation.ClientIdentityLookup
}

func (resolver *lookupResolver) ResolveBatch(_ context.Context, _ string, lookups []agentconversation.ClientIdentityLookup) (map[string]agentconversation.ClientIdentity, error) {
	resolver.lookups = lookups
	return nil, nil
}
