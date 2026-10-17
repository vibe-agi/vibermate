package runtimepersistence

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"path/filepath"
	"testing"
	"time"
)

func nativeIndependentHistoryFixture(t *testing.T) (*ObservationStore, nativetool.RecordedHistory) {
	t.Helper()
	ctx := context.Background()
	directory := t.TempDir()
	control, err := OpenControl(ctx, ControlOptions{DatabasePath: filepath.Join(directory, "control.db"), LayoutID: "history-layout", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, id, rev := nativeFixtureOnStore(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, true, nil, control.store, egressprofile.Direct())
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, control.store.NativeToolRepository(), id, now)
	if _, err := control.NativeToolRepository().Start(ctx, id, "independent-history", rev, now); err != nil {
		t.Fatal(err)
	}
	if err := control.NativeToolRepository().Finish(ctx, id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", BytesIn: 7, BytesOut: 11, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	op, err := control.NativeToolRepository().Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit control retirement: optional publication cannot reload this row.
	if _, err := control.store.database.Exec(`DELETE FROM native_tool_operations WHERE operation_id=?`, id); err != nil {
		t.Fatal(err)
	}
	observations, err := OpenObservations(ctx, ObservationOptions{DatabasePath: filepath.Join(directory, "observations.db"), LayoutID: "history-layout", ArchiveID: "archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observations.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return observations, nativetool.RecordedHistory{Scope: op.RecordingScope, Observation: nativetool.HistoryObservation{Operation: op, EventKind: "completed", EventAt: now, MaximumEventAt: op.MaximumEventAt, MetadataExpiresAt: op.MetadataExpiresAt}}
}
func TestNativeHistoryTerminalSnapshotSurvivesControlRetirement(t *testing.T) {
	store, terminal := nativeIndependentHistoryFixture(t)
	ctx := context.Background()
	if err := store.PutNativeHistory(ctx, terminal); err != nil {
		t.Fatalf("terminal-only publication needs control/history predecessor: %v", err)
	}
	got, err := store.GetNativeHistory(ctx, terminal.Observation.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation.Operation.State != "completed" || got.Observation.Operation.BytesIn == nil || *got.Observation.Operation.BytesIn != 7 {
		t.Fatalf("terminal projection: %+v", got)
	}
	index, err := store.PageNativeInspection(ctx, nativetool.InspectionIndexRequest{Capture: terminal.Scope.Capture, Limit: 10})
	if err != nil || len(index.Items) != 1 || index.Items[0].Scope != terminal.Scope || index.Items[0].HistoryRevision != terminal.Observation.Operation.Revision {
		t.Fatalf("independent index: %+v %v", index, err)
	}
	if err := store.PutNativeHistory(ctx, terminal); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	repeated, err := store.PageNativeInspection(ctx, nativetool.InspectionIndexRequest{Capture: terminal.Scope.Capture, AfterPosition: index.NextPosition, Limit: 10})
	if err != nil || len(repeated.Items) != 0 {
		t.Fatal("identical retry republished discovery", err)
	}
	conflict := terminal
	different := int64(42)
	conflict.Observation.Operation.BytesIn = &different
	if err := store.PutNativeHistory(ctx, conflict); !errors.Is(err, nativetool.ErrConflict) {
		t.Fatal("same revision overwritten")
	}
	changedIdentity := terminal
	changedIdentity.Observation.Operation.Revision++
	changedIdentity.Observation.Operation.TargetOrigin = "https://foreign.example"
	if err := store.PutNativeHistory(ctx, changedIdentity); !errors.Is(err, nativetool.ErrConflict) {
		t.Fatal("immutable inspection identity overwritten", err)
	}
	changedExpiry := terminal
	changedExpiry.Observation.Operation.Revision++
	changedExpiry.Observation.MetadataExpiresAt = changedExpiry.Observation.MetadataExpiresAt.Add(time.Hour)
	changedExpiry.Observation.Operation.MetadataExpiresAt = changedExpiry.Observation.MetadataExpiresAt
	if err := store.PutNativeHistory(ctx, changedExpiry); !errors.Is(err, nativetool.ErrConflict) {
		t.Fatal("original metadata expiry extended", err)
	}
	older := terminal
	older.Observation.Operation.Revision--
	older.Observation.Operation.State = "started"
	older.Observation.Operation.CompletedAt = nil
	older.Observation.Operation.Reason = ""
	older.Observation.EventKind = "started"
	older.Observation.Operation.BytesIn = nil
	older.Observation.Operation.BytesOut = nil
	if err := store.PutNativeHistory(ctx, older); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetNativeHistory(ctx, terminal.Observation.Operation.ID)
	if err != nil || got.Observation.Operation.Revision != terminal.Observation.Operation.Revision {
		t.Fatal("older revision regressed latest projection", err)
	}
	foreign := terminal
	foreign.Scope.Fence.ArchiveEpoch++
	foreign.Observation.Operation.RecordingScope = foreign.Scope
	if err := store.PutNativeHistory(ctx, foreign); !errors.Is(err, nativetool.ErrInvalid) && !errors.Is(err, nativetool.ErrConflict) {
		t.Fatal("conflicting original scope accepted", err)
	}
}

func TestNativeHistorySchemaRejectsMaximumBeforeEvent(t *testing.T) {
	store, terminal := nativeIndependentHistoryFixture(t)
	if err := store.PutNativeHistory(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}
	_, err := store.store.database.Exec(`UPDATE native_history_snapshots SET maximum_event_at_unix_ms=event_at_unix_ms-1 WHERE operation_id=?`, terminal.Observation.Operation.ID)
	if err == nil {
		t.Fatal("schema accepted maximum event time before event")
	}
}
