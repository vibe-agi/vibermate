package runtimepersistence

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/recording"
	"path/filepath"
	"testing"
	"time"
)

func TestScopedRecordingDeletionWithoutLinks(t *testing.T) {
	s, nativeID, revision := nativeStoreFixture(t, environment.ToolPolicyReview)
	ctx := context.Background()
	c, ok := s.ConnectionEventRepository().(interface {
		PutConnection(context.Context, recording.Scope, connectionevent.Event) error
	})
	if !ok {
		t.Fatal("connection storage lacks direct observation scope")
	}
	e, ok := s.EgressAttemptRepository().(interface {
		PutScopedAttempt(context.Context, recording.Scope, egressaudit.Attempt) error
	})
	if !ok {
		t.Fatal("egress storage lacks direct observation scope")
	}
	scope := recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: "doomed"}, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}
	other := scope
	other.Capture.ID = "other"
	nativeRepo := nativeFullRepository(t, s)
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, nativeRepo, nativeID, now)
	receipt, err := nativeRepo.Start(ctx, nativeID, "native-preserved", revision, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range []recording.Scope{scope, other} {
		nativeExec(t, s.database, `INSERT INTO manual_captures(capture_id,owner_kind,owner_id,display_name,client_class,lifetime,state,credential_revision,proxy_credential_hash,observation,created_at_unix_ms,updated_at_unix_ms) VALUES(?, 'local_installation','','Desktop','desktop_app','until_revoked','revoked',1,randomblob(32),'waiting_for_traffic',1,2)`, sc.Capture.ID)
		event := connectionevent.Event{ConnectionID: sc.Capture.ID, SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Unix(1, 0).UTC()}
		if err := c.PutConnection(ctx, sc, event); err != nil {
			t.Fatal(err)
		}
		a := providerAttempt(t, sc.Capture.ID)
		a, err := a.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, BytesOut: 1, BytesIn: 2, CompletedAt: a.StartedAt().Add(time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.PutScopedAttempt(ctx, sc, a); err != nil {
			t.Fatal(err)
		}
		conflict := sc
		conflict.Fence.CaptureGeneration++
		if c.PutConnection(ctx, conflict, event) == nil || e.PutScopedAttempt(ctx, conflict, a) == nil {
			t.Fatal("scope reassignment accepted")
		}
	}
	if countRows(t, s, "runtime_activities") != 0 || countRows(t, s, "runtime_raw_evidence_envelopes") != 0 {
		t.Fatal("fixture has linking evidence")
	}
	if _, err := s.DeleteCapture(ctx, "manual_capture", "doomed"); err != nil {
		t.Fatal(err)
	}
	page, err := s.ConnectionEventRepository().List(ctx, connectionevent.PageRequest{Limit: 20})
	if err != nil || len(page.Items) != 1 || page.Items[0].ConnectionID != "other" {
		t.Fatalf("connections: %+v %v", page, err)
	}
	attempts, err := s.EgressAttemptRepository().List(ctx, egressaudit.PageRequest{})
	if err != nil || len(attempts.Items) != 1 || attempts.Items[0].Attempt.ID() != "other" {
		t.Fatalf("attempts: %+v %v", attempts, err)
	}
	// Native delivery is required control proof, separate from optional
	// Egress inspection. Keep its physical identity and once verification.
	var deliveryOperation string
	if err := s.database.QueryRowContext(ctx, `SELECT operation_id FROM native_tool_deliveries WHERE attempt_id=?`, receipt.AttemptID()).Scan(&deliveryOperation); err != nil || deliveryOperation != string(nativeID) {
		t.Fatalf("unrelated native delivery changed: %q %v", deliveryOperation, err)
	}
	if err := nativeRepo.VerifyStartReceipt(ctx, receipt, now); err != nil {
		t.Fatalf("unrelated native proof changed: %v", err)
	}
	if _, err := nativeRepo.Start(ctx, nativeID, "duplicate-start", revision, now); err == nil {
		t.Fatal("native once guard lost after unrelated scope deletion")
	}

}

func TestScopedConnectionRecoveryPreservesOwnership(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	scope := recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "recover-scope"}, Fence: recording.Fence{ArchiveEpoch: 3, CaptureGeneration: 2}}
	event := connectionevent.Event{ConnectionID: "recover-connection", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Unix(1, 0).UTC()}
	if err := store.ConnectionEventRepository().(connectionevent.ScopedStore).PutConnection(t.Context(), scope, event); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConnectionEventRepository().Recover(t.Context(), time.Unix(2, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.database.QueryRow(`SELECT count(*) FROM runtime_connection_events WHERE recording_scope_kind='managed_run' AND recording_scope_id='recover-scope' AND recording_archive_epoch=3 AND recording_capture_generation=2`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("recovery discarded ownership: count=%d err=%v", count, err)
	}
}
