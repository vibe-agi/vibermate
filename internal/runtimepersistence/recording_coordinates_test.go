package runtimepersistence

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
)

func coordinateObservationStore(t *testing.T) *ObservationStore {
	t.Helper()
	limits, _ := exchangecontent.DefaultSourceLimits()
	store, err := OpenObservations(context.Background(), ObservationOptions{ContentLimits: &limits, DatabasePath: filepath.Join(t.TempDir(), "observations.db"), LayoutID: "coordinates", ArchiveID: "current", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Shutdown(context.Background()) })
	return store
}
func TestRawOwnerPersistsOriginalCoordinatesAndDropsInvalidatedQueue(t *testing.T) {
	store := coordinateObservationStore(t)
	ctx := context.Background()
	budget, _ := recording.NewBudget(recording.Limits{Records: 8, Bytes: 1 << 20})
	manager, err := rawevidence.Open(ctx, rawevidence.Options{Repository: store.RawEvidenceRepository(), Budget: budget, Random: bytes.NewReader(bytes.Repeat([]byte{0x73}, 64)), Clock: rawevidence.SystemClock{}, Config: rawevidence.Config{MaximumQueueRecords: 8, MaximumQueueBytes: 1 << 20, MaximumBatchRecords: 8, MaximumBatchBytes: 1 << 20, FlushInterval: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(ctx)
	capture := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "capture-original"}
	epoch, _ := recording.NewEpoch(7)
	token, _ := recording.NewScopeToken(epoch, capture, recording.Fence{ArchiveEpoch: 7, CaptureGeneration: 9})
	observation := rawevidence.Observation{Context: rawevidence.Context{ScopeKind: rawevidence.ScopeManagedRun, ScopeID: capture.ID, ExchangeID: "original", Recording: rawevidence.RecordingFull, RetentionDays: 30}, Layer: rawevidence.LayerClientIngress, Method: "POST", Scheme: "https", Authority: "example.test", Path: "/", Complete: true, Representation: "http_message", Body: []byte("synthetic scope evidence")}
	if result := manager.TryObserve(token.Stamp(), observation); result != recording.Accepted {
		t.Fatalf("original offer refused: %v", result)
	}
	if err = manager.Flush(ctx, rawevidence.Watermark{}); err != nil {
		t.Fatal(err)
	}
	var actualEpoch, generation int64
	if err = store.store.database.QueryRow(`SELECT recording_archive_epoch,recording_capture_generation FROM runtime_raw_evidence_envelopes WHERE exchange_id='original'`).Scan(&actualEpoch, &generation); err != nil {
		t.Fatal(err)
	}
	if actualEpoch != 7 || generation != 9 {
		t.Fatal("raw original coordinates replaced")
	}
	observation.ExchangeID = "stale"
	if result := manager.TryObserve(token.Stamp(), observation); result != recording.Accepted {
		t.Fatal("fixture offer refused")
	}
	token.Revoke()
	if err = manager.Flush(ctx, rawevidence.Watermark{}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = store.store.database.QueryRow(`SELECT count(*) FROM runtime_raw_evidence_envelopes WHERE exchange_id='stale'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("revoked queued raw data persisted")
	}
	if got := budget.Snapshot(); got.ReservedRecords != 0 || got.ReservedBytes != 0 {
		t.Fatal("raw ownership reservation leaked")
	}
}
func TestCurrentRawWriterRefusesUnknownCoordinates(t *testing.T) {
	store := coordinateObservationStore(t)
	record := rawEvidenceRecordForTest("unknown.1", 1, rawevidence.LayerClientIngress, []byte("synthetic unknown scope"), []byte(`{"version":1,"headers":[]}`))
	if err := store.RawEvidenceRepository().AppendBatch(context.Background(), []rawevidence.StoredEnvelope{record}, time.Now().UTC()); err == nil {
		t.Fatal("unknown original scope reported successful current write")
	}
	var count int
	if err := store.store.database.QueryRow(`SELECT count(*) FROM runtime_evidence_bodies`).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected unknown scope retained a body graph")
	}
}

func TestCurrentActivityAndIdentityRequireOriginalCoordinates(t *testing.T) {
	store := coordinateObservationStore(t)
	ctx := context.Background()
	scope := recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "captured"}, Fence: recording.Fence{ArchiveEpoch: 7, CaptureGeneration: 9}}
	record := activity.Record{ID: "activity-original", OccurredAt: time.Now().UTC(), Kind: activity.KindExchangeStarted, SubjectID: "scoped-exchange", Status: activity.StatusPending, SourceKind: activity.SourceCaptureRun, SourceDisplayName: "fixture", SourceRecognition: activity.SourceRecognitionConfigured, CaptureRunID: scope.Capture.ID, ConnectionID: "connection"}
	setFrozenExecutionEvidence(&record, "scope")
	record.AccountID = ""
	record.AccountRevision = 0
	record.CredentialEpoch = 0
	if _, err := store.ActivityRepository().Append(ctx, record); err == nil {
		t.Fatal("unscoped current Activity write succeeded")
	}
	writer, ok := store.ActivityRepository().(interface {
		AppendScoped(context.Context, recording.Scope, activity.Record) (activity.Record, error)
	})
	if !ok {
		t.Fatal("current Activity has no original-scope write port")
	}
	if _, err := writer.AppendScoped(ctx, scope, record); err != nil {
		t.Fatal(err)
	}
	identity := agentconversation.ClientIdentity{Client: "codex", SessionID: "session-original", SessionResumable: true, Source: "client_protocol_evidence", Confidence: "exact", ObservedAt: record.OccurredAt}
	if err := store.ConversationIdentityRepository().PutConversationIdentity(ctx, record.SubjectID, identity); err == nil {
		t.Fatal("unscoped current identity write succeeded")
	}
	identities, ok := store.ConversationIdentityRepository().(interface {
		PutScopedConversationIdentity(context.Context, recording.Scope, string, agentconversation.ClientIdentity) error
	})
	if !ok {
		t.Fatal("current identity association has no original-scope port")
	}
	if err := identities.PutScopedConversationIdentity(ctx, scope, record.SubjectID, identity); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"runtime_activities", "runtime_exchange_agent_identities"} {
		var epoch, generation int64
		if err := store.store.database.QueryRow(`SELECT recording_archive_epoch,recording_capture_generation FROM `+table).Scan(&epoch, &generation); err != nil || epoch != 7 || generation != 9 {
			t.Fatalf("original root coordinates missing in %s: %v", table, err)
		}
	}
	changed := scope
	changed.Fence.CaptureGeneration++
	if err := identities.PutScopedConversationIdentity(ctx, changed, record.SubjectID, identity); err == nil {
		t.Fatal("existing identity association was relabeled")
	}
}

func TestCurrentSemanticRootsPreserveScopesAndSharedGraph(t *testing.T) {
	store := coordinateObservationStore(t)
	ctx := context.Background()
	limits, _ := exchangecontent.DefaultSourceLimits()
	writer, ok := store.ExchangeContentRepository().(interface {
		PutScopedSource(context.Context, recording.Scope, *exchangecontent.Source) error
	})
	if !ok {
		t.Fatal("semantic current writer has no original-scope port")
	}
	for i, scopeID := range []string{"capture-a", "capture-b"} {
		scope := recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: scopeID}, Fence: recording.Fence{ArchiveEpoch: 7, CaptureGeneration: uint64(9 + i)}}
		record := contentRecordFixture(t, "scoped-"+scopeID, time.Now().UTC())
		record.Parent = exchangecontent.ParentRef{CaptureRunID: scopeID}
		record.Request.System = nil
		record.Request.Messages = []exchangecontent.Message{{Role: "user", Blocks: []exchangecontent.Block{sourceText("shared scoped semantic body")}}}
		record.Response = nil
		source, err := exchangecontent.SourceFromRecordWithin(limits, record)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ExchangeContentRepository().Put(ctx, record); err == nil {
			t.Fatal("unscoped current semantic write succeeded")
		}
		if err = writer.PutScopedSource(ctx, scope, source); err != nil {
			t.Fatal(err)
		}
		changed := scope
		changed.Fence.ArchiveEpoch++
		if err = writer.PutScopedSource(ctx, changed, source); err == nil {
			t.Fatal("semantic root relabeled with later epoch")
		}
	}
	var roots, messages int
	if err := store.store.database.QueryRow(`SELECT count(*) FROM runtime_exchange_contents WHERE recording_archive_epoch=7 AND recording_capture_generation IN(9,10)`).Scan(&roots); err != nil || roots != 2 {
		t.Fatal("semantic original scopes missing")
	}
	if err := store.store.database.QueryRow(`SELECT count(*) FROM runtime_exchange_content_messages`).Scan(&messages); err != nil || messages != 1 {
		t.Fatal("scope duplicated shared message graph")
	}
	nativeExec(t, store.store.database, `DELETE FROM runtime_exchange_contents WHERE exchange_id='scoped-capture-a'`)
	if _, err := store.ExchangeContentRepository().Get(ctx, "scoped-capture-b", time.Now().UTC()); err != nil {
		t.Fatal("removing one root lost another scope's shared graph")
	}
}
