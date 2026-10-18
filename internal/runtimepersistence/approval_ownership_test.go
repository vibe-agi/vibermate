package runtimepersistence

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func ownedApproval(t *testing.T, id, capture string) toolapproval.Record {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	r := toolapproval.Record{ID: id, Revision: 1, Kind: toolapproval.KindToolIntent, AggregateKey: id, SubjectRefs: []string{"call"}, SubjectLabels: []string{"tool"}, RequestCount: 1, WaiterCount: 1, ExchangeID: "exchange-" + id, EnvironmentID: environment.EnvironmentID("env.test"), EnvironmentRevision: 1, EnvironmentDigest: environment.CandidateDigest{1}, RouteID: environment.UpstreamRouteID("route.test"), RouteRevision: 1, State: toolapproval.StatePending, CreatedAt: now, ExpiresAt: now.Add(time.Minute), OwnerKind: toolapproval.OwnershipCapture, OwnerScope: recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: capture}, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 7}}}
	if capture == "" {
		r.OwnerKind = toolapproval.OwnershipGlobal
		r.OwnerScope = recording.Scope{Fence: recording.Fence{ArchiveEpoch: 1}}
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	return r
}
func seedOwnedApprovalCaptures(t *testing.T, control *ControlStore) {
	t.Helper()
	for _, id := range []string{"doomed", "other"} {
		nativeExec(t, control.store.database, `INSERT INTO manual_captures(capture_id,owner_kind,owner_id,display_name,client_class,lifetime,state,credential_revision,proxy_credential_hash,observation,created_at_unix_ms,updated_at_unix_ms) VALUES(?,'local_installation','','Desktop','desktop_app','until_revoked','active',1,randomblob(32),'waiting_for_traffic',1,2);`, id)
		nativeExec(t, control.store.database, `INSERT INTO recording_capture_fences VALUES('manual_capture',?,7,0)`, id)
	}
}
func TestRequiredApprovalOwnershipSurvivesWithoutAnyHistory(t *testing.T) {
	control := inspectionControlFixture(t)
	seedOwnedApprovalCaptures(t, control)
	repo := control.ToolApprovalRepository()
	doomed := ownedApproval(t, "owned-doomed", "doomed")
	other := ownedApproval(t, "owned-other", "other")
	for _, r := range []toolapproval.Record{doomed, other} {
		if err := repo.Create(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.Get(t.Context(), doomed.ID)
	if err != nil || got.OwnerKind != doomed.OwnerKind || got.OwnerScope != doomed.OwnerScope {
		t.Fatalf("required original approval ownership lost: %+v %v", got.OwnerScope, err)
	}
	shared := pendingNetworkAsk(t, control.store)
	global := ownedApproval(t, "genuine-global", "")
	global.OwnerKind = toolapproval.OwnershipGlobal
	global.OwnerScope = recording.Scope{Fence: recording.Fence{ArchiveEpoch: 1}}
	if err = repo.Create(t.Context(), global); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, control.store.database, `UPDATE manual_captures SET state='revoked' WHERE capture_id='doomed'`)
	released, err := control.DeleteCapture(t.Context(), "manual_capture", "doomed")
	if err != nil || released.Approvals != 1 {
		t.Fatalf("exact required approval deletion missing: %+v %v", released, err)
	}
	if _, err = repo.Get(t.Context(), doomed.ID); !errors.Is(err, toolapproval.ErrNotFound) {
		t.Fatalf("owned approval survived deletion: %v", err)
	}
	for _, id := range []string{other.ID, shared.ID, global.ID} {
		if _, err = repo.Get(t.Context(), id); err != nil {
			t.Fatalf("unrelated/shared authority changed: %s %v", id, err)
		}
	}
	doomed.ID = "late"
	doomed.AggregateKey = "late"
	if err = repo.Create(t.Context(), doomed); err == nil {
		t.Fatal("deleted owner created a new required approval")
	}
}
func TestRequiredApprovalRejectsUnknownAndRevokedOwnership(t *testing.T) {
	control := inspectionControlFixture(t)
	seedOwnedApprovalCaptures(t, control)
	repo := control.ToolApprovalRepository()
	r := ownedApproval(t, "unknown", "doomed")
	r.OwnerKind = toolapproval.OwnershipUnknown
	r.OwnerScope = recording.Scope{}
	if err := repo.Create(t.Context(), r); err == nil {
		t.Fatal("missing ToolIntent ownership became new global authority")
	}
	nativeExec(t, control.store.database, `UPDATE manual_captures SET state='revoked' WHERE capture_id='doomed'`)
	r = ownedApproval(t, "late-revoked", "doomed")
	if err := repo.Create(t.Context(), r); err == nil {
		t.Fatal("revoked owner created a new approval")
	}
}

func TestRequiredApprovalLateDecisionCancelsRevokedOwner(t *testing.T) {
	control := inspectionControlFixture(t)
	seedOwnedApprovalCaptures(t, control)
	repo := control.ToolApprovalRepository()
	r := ownedApproval(t, "late-decision", "doomed")
	if err := repo.Create(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, control.store.database, `UPDATE manual_captures SET state='revoked' WHERE capture_id='doomed'`)
	resolved, err := repo.Decide(t.Context(), toolapproval.DecisionCommand{ApprovalID: r.ID, ExpectedRevision: 1, IdempotencyKey: "revoked-decision-idempotency", Decision: toolapproval.DecisionAllowOnce, Scope: toolapproval.ScopeRequest}, time.Now().UTC())
	if err != nil || resolved.State != toolapproval.StateCanceled {
		t.Fatalf("revoked owner yielded a late permission: %s %v", resolved.State, err)
	}
}

func TestLegacyApprovalExtractionPreservesUnknownOwnership(t *testing.T) {
	for _, revision := range []int{1, 2} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) {
			source, db := extractionFixture(t, revision)
			legacy := newToolApprovalRepository(db, newOperationGate())
			old := ownedApproval(t, "legacy-unknown", "doomed")
			old.OwnerKind = toolapproval.OwnershipUnknown
			old.OwnerScope = recording.Scope{}
			if err := legacy.Create(t.Context(), old); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "control.db")
			if _, err := ExtractControl(t.Context(), ControlExtraction{SourcePath: source, TargetPath: target, LayoutID: "layout", ArchiveID: "legacy"}); err != nil {
				t.Fatal("valid legacy unknown approval could not be retained:", err)
			}
			control, err := OpenControl(t.Context(), ControlOptions{DatabasePath: target, LayoutID: "layout", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer control.Shutdown(context.Background())
			retained, err := control.ToolApprovalRepository().Get(t.Context(), old.ID)
			if err != nil || retained.OwnerKind != toolapproval.OwnershipUnknown || retained.OwnerScope != (recording.Scope{}) {
				t.Fatalf("legacy unknown became global: %+v %v", retained, err)
			}
			old.ID = "new-unknown"
			old.AggregateKey = "new-unknown"
			if err = control.ToolApprovalRepository().Create(t.Context(), old); !errors.Is(err, toolapproval.ErrInvalidApproval) {
				t.Fatalf("legacy compatibility authorized new unknown ownership: %v", err)
			}
		})
	}
}

func TestRequiredApprovalCancellationIsBoundedAndKeepsSharedAuthority(t *testing.T) {
	control := inspectionControlFixture(t)
	seedOwnedApprovalCaptures(t, control)
	repo := control.store.approvalRepo
	for i := 0; i < 101; i++ {
		if err := repo.Create(t.Context(), ownedApproval(t, fmt.Sprintf("owned-%03d", i), "doomed")); err != nil {
			t.Fatal(err)
		}
	}
	other := ownedApproval(t, "unrelated", "other")
	if err := repo.Create(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	shared := pendingNetworkAsk(t, control.store)
	ref := captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: "doomed"}
	first, err := repo.CancelOwnedCapture(t.Context(), ref, "capture_stopped", time.Now().UTC(), 100)
	if err != nil || len(first) != 100 {
		t.Fatalf("first bound=%d %v", len(first), err)
	}
	second, err := repo.CancelOwnedCapture(t.Context(), ref, "capture_stopped", time.Now().UTC(), 100)
	if err != nil || len(second) != 1 {
		t.Fatalf("continuation=%d %v", len(second), err)
	}
	for _, id := range []string{other.ID, shared.ID} {
		r, err := repo.Get(t.Context(), id)
		if err != nil || r.State != toolapproval.StatePending {
			t.Fatalf("unrelated shared authority canceled: %s %v", id, err)
		}
	}
}

func TestRequiredApprovalIdempotentDecisionRemainsHistorical(t *testing.T) {
	control := inspectionControlFixture(t)
	seedOwnedApprovalCaptures(t, control)
	repo := control.ToolApprovalRepository()
	r := ownedApproval(t, "decided-before-stop", "doomed")
	if err := repo.Create(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	command := toolapproval.DecisionCommand{ApprovalID: r.ID, ExpectedRevision: 1, IdempotencyKey: "prior-decision-idempotency", Decision: toolapproval.DecisionAllowOnce, Scope: toolapproval.ScopeRequest}
	first, err := repo.Decide(t.Context(), command, time.Now().UTC())
	if err != nil || first.State != toolapproval.StateAllowed {
		t.Fatal(err)
	}
	nativeExec(t, control.store.database, `UPDATE manual_captures SET state='revoked' WHERE capture_id='doomed'`)
	replay, err := repo.Decide(t.Context(), command, time.Now().UTC())
	if err != nil || replay.Revision != first.Revision || replay.State != first.State || replay.OwnerScope != first.OwnerScope {
		t.Fatalf("idempotent past decision changed: %+v %v", replay, err)
	}
}
