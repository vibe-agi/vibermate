package conversationprojection

import (
	"context"
	"errors"
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

func TestReindexRepairsFirstThenSkipsStableLocalIdentity(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	pending, err := agentconversation.Pending("exchange")
	if err != nil {
		t.Fatal(err)
	}
	identity := agentconversation.ClientIdentity{
		Client: "codex", SessionID: "session", SessionResumable: true,
		ProviderResponseID: "response", Source: agentconversation.ClientIdentitySourceLocalState,
		Confidence: "exact", ObservedAt: now,
	}
	identities := &identityRepository{values: map[string]agentconversation.ClientIdentity{"exchange": identity}}
	record := activity.Record{SubjectID: "exchange", CaptureRunID: "run", SourceDisplayName: "Codex", OccurredAt: now, Conversation: &pending}
	var requests []activity.PageRequest
	newIndexer := func() *Indexer {
		indexer, err := New(Options{
			Activities: activityReader{record: record, requests: &requests, skipLocal: true},
			Contents:   &metadataOnlyReader{}, Identities: identities,
			Writer: identities, CaptureRuns: runReader{},
		})
		if err != nil {
			t.Fatal(err)
		}
		return indexer
	}
	request := activity.ConversationIndexRequest{CaptureRunID: "run", Limit: 10}
	indexer := newIndexer()
	if err := indexer.Reindex(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := indexer.Reindex(ctx, request); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].WithoutLocalConversationIdentity ||
		!requests[1].WithoutLocalConversationIdentity || identities.projections != 1 {
		t.Fatalf("first repair then incremental requests=%+v projections=%d", requests, identities.projections)
	}
	// A fresh Runtime must repair an identity persisted before the previous
	// process could finish its Conversation projection.
	if err := newIndexer().Reindex(ctx, request); err != nil {
		t.Fatal(err)
	}
	if requests[2].WithoutLocalConversationIdentity || identities.projections != 2 {
		t.Fatalf("restart did not repair persisted identity: requests=%+v projections=%d", requests, identities.projections)
	}

	identities.failProject = true
	failed := newIndexer()
	if err := failed.Reindex(ctx, request); err == nil {
		t.Fatal("project failure was ignored")
	}
	if err := failed.Reindex(ctx, request); err != nil {
		t.Fatal(err)
	}
	if requests[3].WithoutLocalConversationIdentity || requests[4].WithoutLocalConversationIdentity {
		t.Fatalf("failed projection incorrectly skipped repair: %+v", requests)
	}
}

func TestReindexContinuesPastItsBoundBeforeUsingIncrementalFilter(t *testing.T) {
	reader := &pagingActivityReader{remaining: maxExchangesPerRefresh + 1, next: maxExchangesPerRefresh + 1}
	identities := &identityRepository{values: map[string]agentconversation.ClientIdentity{}}
	indexer, err := New(Options{
		Activities: reader, Contents: &metadataOnlyReader{}, CaptureRuns: runReader{},
		Identities: identities, Writer: identities,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := activity.ConversationIndexRequest{CaptureRunID: "run", Limit: 10}
	for range 3 {
		if err := indexer.Reindex(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	if len(reader.requests) != 52 ||
		reader.requests[50].BeforeSequence != 2 ||
		reader.requests[50].WithoutLocalConversationIdentity ||
		!reader.requests[51].WithoutLocalConversationIdentity {
		t.Fatalf("bounded repair did not reach old records before filtering: requests=%d tail=%+v", len(reader.requests), reader.requests[max(0, len(reader.requests)-2):])
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
	record    activity.Record
	requests  *[]activity.PageRequest
	skipLocal bool
}

type pagingActivityReader struct {
	activity.Reader
	remaining int
	next      int64
	requests  []activity.PageRequest
}

func (reader *pagingActivityReader) ListExchanges(_ context.Context, request activity.PageRequest) (activity.Page, error) {
	reader.requests = append(reader.requests, request)
	count := min(request.Limit, reader.remaining)
	items := make([]activity.Record, count)
	for index := range items {
		items[index].Sequence = reader.next
		reader.next--
	}
	reader.remaining -= count
	if reader.remaining == 0 {
		return activity.Page{Items: items}, nil
	}
	return activity.Page{Items: items, NextBeforeSequence: reader.next + 1}, nil
}

func (reader activityReader) ListExchanges(_ context.Context, request activity.PageRequest) (activity.Page, error) {
	if reader.requests != nil {
		*reader.requests = append(*reader.requests, request)
	}
	if reader.skipLocal && request.WithoutLocalConversationIdentity {
		return activity.Page{Items: []activity.Record{}}, nil
	}
	return activity.Page{Items: []activity.Record{reader.record}}, nil
}

type runReader struct{ capturerun.Reader }

func (runReader) GetRun(context.Context, string) (capturerun.View, error) {
	return capturerun.View{ExecutableLabel: "codex", CWD: "/synthetic/workspace"}, nil
}

type identityRepository struct {
	values      map[string]agentconversation.ClientIdentity
	projections int
	failProject bool
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
	if repository.failProject {
		repository.failProject = false
		return errors.New("synthetic projection failure")
	}
	return nil
}

type lookupResolver struct {
	lookups []agentconversation.ClientIdentityLookup
}

func (resolver *lookupResolver) ResolveBatch(_ context.Context, _ string, lookups []agentconversation.ClientIdentityLookup) (map[string]agentconversation.ClientIdentity, error) {
	resolver.lookups = lookups
	return nil, nil
}
