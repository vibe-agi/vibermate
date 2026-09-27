package runtimepersistence

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

func TestCaptureActivityIsScopedAndIndependentOfHeartbeats(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	clock := &manualCaptureClock{now: now}
	options := capturerun.DefaultOptions(store.CaptureRunRepository())
	options.Clock = clock
	runs, err := capturerun.NewManager(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer runs.Shutdown(ctx)
	create := func() capturerun.LaunchGrant {
		t.Helper()
		grant, err := runs.Create(ctx, capturerun.CreateCommand{
			CWD: "/workspace", CanonicalExecutablePath: "/bin/test-agent",
			ExecutableLabel: "codex", CatalogRevision: 1, Lifetime: 10 * time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runs.Attach(ctx, grant.Run.ID, grant.ControlCapability, 42); err != nil {
			t.Fatal(err)
		}
		return grant
	}
	first, second := create(), create()
	manual := newManualCaptureManager(t, store, clock, &manualCaptureRandom{})
	defer manual.Shutdown(ctx)
	grant, err := manual.Create(ctx, manualcapture.CreateCommand{
		Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Desktop", ClientClass: manualcapture.ClientDesktopApp,
		Lifetime: manualcapture.LifetimeUntilRevoked,
	})
	if err != nil {
		t.Fatal(err)
	}
	read := func(id string, want time.Time) {
		t.Helper()
		view, err := runs.GetRun(ctx, id)
		if err != nil || !view.ActivityTime().Equal(want) {
			t.Fatalf("GetRun(%s).ActivityTime = %v, want %v; %v", id, view.ActivityTime(), want, err)
		}
	}
	heartbeat := func() {
		t.Helper()
		if _, err := runs.Heartbeat(ctx, first.Run.ID, first.ControlCapability, 10*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	clock.Set(now.Add(time.Minute))
	heartbeat()
	read(first.Run.ID, now) // no traffic: creation, not the latest heartbeat
	appendActivity := func(id, runID, manualID string, minute int) {
		t.Helper()
		record := activity.Record{
			ID: id, OccurredAt: now.Add(time.Duration(minute) * time.Minute), Kind: activity.KindExchangeCompleted,
			SubjectID: id, Status: activity.StatusSucceeded, SourceKind: activity.SourceCaptureRun,
			SourceDisplayName: "codex", SourceRecognition: activity.SourceRecognitionConfigured,
			CaptureRunID: runID, ManualCaptureID: manualID, ConnectionID: "connection-" + id,
			// Both launches resume the same native session. Its directory row must
			// still use only activity attributed to that Capture.
			Conversation: &agentconversation.Ref{ProjectionID: "session:shared:main", Kind: agentconversation.KindMain, Evidence: agentconversation.EvidenceExplicitSession},
		}
		if manualID != "" {
			record.SourceKind = activity.SourceManualProxy
		}
		setFrozenExecutionEvidence(&record, "capture-activity")
		if _, err := store.ActivityRepository().Append(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	appendActivity("first-exchange", first.Run.ID, "", 2)
	appendActivity("second-exchange", second.Run.ID, "", 3)
	appendActivity("manual-exchange", "", grant.Capture.ID, 4)
	clock.Set(now.Add(5 * time.Minute))
	heartbeat()
	read(first.Run.ID, now.Add(2*time.Minute))
	read(second.Run.ID, now.Add(3*time.Minute))
	page, err := runs.ListRuns(ctx, capturerun.PageRequest{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != second.Run.ID {
		t.Fatalf("activity order = %+v, %v", page, err)
	}
	boundary := page.Items[0]
	page, err = runs.ListRuns(ctx, capturerun.PageRequest{Limit: 1, Cursor: &capturerun.PageCursor{
		Running: true, ActivityAt: boundary.ActivityTime(), IncludeAtActivityAt: true, AfterID: boundary.ID,
	}})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != first.Run.ID || !page.Items[0].ActivityTime().Equal(now.Add(2*time.Minute)) {
		t.Fatalf("activity cursor = %+v, %v", page, err)
	}
	manualID, _ := manualcapture.ParseID(grant.Capture.ID)
	manualView, err := manual.Get(ctx, manualcapture.NewLocalOwnerScope(), manualID)
	if err != nil || !manualView.ActivityTime().Equal(now.Add(4*time.Minute)) {
		t.Fatalf("manual activity = %+v, %v", manualView, err)
	}
	if err := runs.Finish(ctx, first.Run.ID, first.ControlCapability); err != nil {
		t.Fatal(err)
	}
	read(first.Run.ID, now.Add(2*time.Minute)) // history keeps actual traffic time
}
