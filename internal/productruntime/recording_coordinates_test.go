package productruntime

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func TestGeneralRecorderPersistsOriginalRootCoordinates(t *testing.T) {
	ctx := context.Background()
	limits, _ := exchangecontent.DefaultSourceLimits()
	path := filepath.Join(t.TempDir(), "observations.db")
	store, err := runtimepersistence.OpenObservations(ctx, runtimepersistence.ObservationOptions{DatabasePath: path, LayoutID: "original", ArchiveID: "current", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second, ContentLimits: &limits})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Shutdown(ctx)
	activities, err := activity.New(activity.DefaultOptions(store.ActivityRepository()))
	if err != nil {
		t.Fatal(err)
	}
	defer activities.Shutdown(ctx)
	contents, err := exchangecontent.New(ctx, exchangecontent.Options{Repository: store.ExchangeContentRepository(), Clock: SystemClock{}, ContentLimits: &limits})
	if err != nil {
		t.Fatal(err)
	}
	defer contents.Shutdown(ctx)
	budget, _ := recording.NewBudget(recording.Limits{Records: 2048, Bytes: 64 << 20})
	queue := newObservationRecorder(ctx, budget, nil, nil)
	defer queue.Shutdown(ctx)
	queue.epoch.Advance(7)
	capture := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "original-capture"}
	token, _ := recording.NewScopeToken(queue.epoch, capture, recording.Fence{ArchiveEpoch: 7, CaptureGeneration: 9})
	stamp := token.Stamp()
	observer := activityAttemptObserver{queue: queue, recorder: activities, identities: store.ConversationIdentityRepository()}
	event := activity.Event{Kind: activity.KindExchangeStarted, SubjectID: "original-exchange", Status: activity.StatusPending, SourceKind: activity.SourceCaptureRun, SourceDisplayName: "fixture", SourceRecognition: activity.SourceRecognitionConfigured, CaptureRunID: capture.ID, ConnectionID: "connection", EnvironmentID: "env", EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("a", 64), ClientEndpointID: "ep", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1}
	event.Conversation, _ = agentconversation.Pending(event.SubjectID)
	if _, err := activities.Prepare(event); err != nil {
		t.Fatalf("invalid authored event: %v", err)
	}
	reservation, ok := observer.metadataReservation(stamp, event)
	if !ok {
		t.Fatal("metadata reservation refused")
	}
	if err = observer.offerPrepared(stamp, reservation, event, []protocolcore.ProtocolEvidenceValue{{Name: "openai_responses.session_id", Value: "original-session"}}, "", nil); err != nil {
		t.Fatal(err)
	}
	input := exchangecontent.OwnInput{ExchangeID: event.SubjectID, Frozen: exchangecontent.FrozenRef{EnvironmentID: "env", EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("a", 64), ClientEndpointID: "ep", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1}, Policy: environment.DefaultContentRecordingPolicy(), RecordedAt: time.Now().UTC(), Parent: exchangecontent.ParentRef{CaptureRunID: capture.ID}, Request: protocolcore.Request{RequestedModel: "model", EffectiveModel: "model", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "owned content"}}}}}}
	if result := queue.offerContent(ctx, stamp, input, limits, contents); result != recording.Accepted {
		t.Fatalf("content refused: %v", result)
	}
	if err = queue.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	queue.epoch.Advance(8)
	token.Revoke()
	if result := queue.offerContent(ctx, stamp, input, limits, contents); result != recording.DroppedStale {
		t.Fatal("old stamp was relabeled after epoch/generation change")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"runtime_activities", "runtime_exchange_agent_identities", "runtime_exchange_contents"} {
		var epoch, generation int64
		if err = db.QueryRow(`SELECT recording_archive_epoch,recording_capture_generation FROM `+table).Scan(&epoch, &generation); err != nil || epoch != 7 || generation != 9 {
			t.Fatalf("original coordinates lost in %s: %v", table, err)
		}
	}
	if state := budget.Snapshot(); state.ReservedBytes != 0 || state.ReservedRecords != 0 {
		t.Fatal("scope handoff leaked recorder ownership")
	}
}

// Only the Activity projection fails. The real Manager still prepares the
// event, and the queue must publish independent identity through the real
// ObservationStore with its original admitted scope and no semantic parent.
type rejectedScopedActivity struct{ activity.Repository }

func (r rejectedScopedActivity) AppendScoped(context.Context, recording.Scope, activity.Record) (activity.Record, error) {
	return activity.Record{}, errors.New("synthetic optional Activity failure")
}
func TestGeneralRecorderIdentityKeepsOriginalScopeWithoutOtherRoots(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "observations.db")
	store, err := runtimepersistence.OpenObservations(ctx, runtimepersistence.ObservationOptions{DatabasePath: path, LayoutID: "identity-root", ArchiveID: "current", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Shutdown(ctx)
	manager, err := activity.New(activity.DefaultOptions(rejectedScopedActivity{store.ActivityRepository()}))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(ctx)
	budget, _ := recording.NewBudget(recording.Limits{Records: 2048, Bytes: 64 << 20})
	queue := newObservationRecorder(ctx, budget, nil, nil)
	defer queue.Shutdown(ctx)
	queue.epoch.Advance(7)
	capture := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "identity-original"}
	token, err := recording.NewScopeToken(queue.epoch, capture, recording.Fence{ArchiveEpoch: 7, CaptureGeneration: 9})
	if err != nil {
		t.Fatal(err)
	}
	observer := activityAttemptObserver{queue: queue, recorder: manager, identities: store.ConversationIdentityRepository()}
	event := activity.Event{Kind: activity.KindExchangeStarted, SubjectID: "identity-only-exchange", Status: activity.StatusPending, SourceKind: activity.SourceCaptureRun, SourceDisplayName: "fixture", SourceRecognition: activity.SourceRecognitionConfigured, CaptureRunID: capture.ID, ConnectionID: "connection", EnvironmentID: "env", EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("a", 64), ClientEndpointID: "ep", ClientEndpointRevision: 1, ProtocolPlanID: "plan", ProtocolPlanRevision: 1}
	event.Conversation, _ = agentconversation.Pending(event.SubjectID)
	if _, err = manager.Prepare(event); err != nil {
		t.Fatal(err)
	}
	reservation, ok := observer.metadataReservation(token.Stamp(), event)
	if !ok {
		t.Fatal("metadata not admitted")
	}
	if err = observer.offerPrepared(token.Stamp(), reservation, event, []protocolcore.ProtocolEvidenceValue{{Name: "openai_responses.session_id", Value: "independent-original"}}, "", nil); err != nil {
		t.Fatal(err)
	}
	if err = queue.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"runtime_activities", "runtime_exchange_contents"} {
		var count int
		if err = db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("fixture unexpectedly has parent root", err)
		}
	}
	var epoch, generation int
	var kind, id, session string
	if err = db.QueryRow(`SELECT recording_scope_kind,recording_scope_id,recording_archive_epoch,recording_capture_generation,session_id FROM runtime_exchange_agent_identities WHERE exchange_id=?`, event.SubjectID).Scan(&kind, &id, &epoch, &generation, &session); err != nil || kind != string(capture.Kind) || id != capture.ID || epoch != 7 || generation != 9 || session != "independent-original" {
		t.Fatalf("independent identity lost original scope: %v", err)
	}
	if got := budget.Snapshot(); got.ReservedBytes != 0 || got.ReservedRecords != 0 {
		t.Fatal("independent identity leaked queued ownership")
	}
}
