package runtimepersistence

import (
	"database/sql"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"testing"
	"time"
)

type recoveryBoundaryCommitFault struct {
	call, target int
	committed    bool
}

func (f *recoveryBoundaryCommitFault) Commit(tx *sql.Tx) error {
	f.call++
	if f.call != f.target {
		return tx.Commit()
	}
	if f.committed {
		return (commitThenError{}).Commit(tx)
	}
	return (rollbackThenError{}).Commit(tx)
}

func TestNativeRecoveryUnavailableBoundariesPreserveOriginalPhysicalCleanup(t *testing.T) {
	for _, boundary := range []int{1, 2} {
		for _, committed := range []bool{false, true} {
			t.Run(fmt.Sprintf("boundary%d/committed=%t", boundary, committed), func(t *testing.T) {
				store, id, revision := nativeStoreFixture(t, environment.ToolPolicyObserve)
				repo := store.NativeToolRepository()
				binding, _ := nativeDecisionFixture(t, store, id)
				now := time.Now().UTC().Truncate(time.Millisecond)
				receipt, err := repo.Start(t.Context(), id, "recovery-boundary-parent", revision, now)
				if err != nil {
					t.Fatal(err)
				}
				authority, err := repo.OpenObservedStream(t.Context(), receipt, binding, time.Minute, now)
				if err != nil {
					t.Fatal(err)
				}
				catalog, _ := binding.CatalogDigest()
				get, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestGetStream, Plan: binding.Plan(), Capture: binding.Capture(), AssignmentRevision: binding.AssignmentRevision(), OwnerEpoch: binding.OwnerEpoch(), SessionID: binding.SessionID(), SessionRevision: binding.SessionRevision(), CatalogGeneration: binding.CatalogGeneration(), CatalogDigest: &catalog})
				if err != nil {
					t.Fatal(err)
				}
				child := nativetool.ID("91919191919191919191919191919191")
				plan, sid := get.Plan(), get.SessionID()
				if _, err := repo.Receive(t.Context(), nativetool.ReceiveInput{ID: child, Capture: get.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "recovery-boundary-child", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "GET", Kind: get.Kind(), SessionID: &sid, Recording: plan.ContentRecording(), ObservedAt: now}); err != nil {
					t.Fatal(err)
				}
				fault := &recoveryBoundaryCommitFault{target: boundary, committed: committed}
				store.nativeTools.committer = fault
				err = repo.BindRecoveryGET(t.Context(), child, get, authority, now)
				store.nativeTools.committer = sqlTransactionCommitter{}
				if err == nil || fault.call != boundary {
					t.Fatalf("real boundary not exercised: calls=%d err=%v", fault.call, err)
				}
				if _, err := repo.Start(t.Context(), child, "forbidden-child", revision, now); err == nil {
					t.Fatal("ambiguous recovery child became ordinary GET send")
				}
				if _, err := repo.RetireObservedStream(t.Context(), authority, now); err != nil {
					t.Fatal(err)
				}
				actual, err := scanEgressAttempt(store.database.QueryRow(egressAttemptSelect+` WHERE attempt_id=?`, receipt.AttemptID()))
				if err != nil || !actual.Attempt.Terminal() {
					t.Fatalf("BindRecoveryGET error dropped exact original cleanup: terminal=%v err=%v", actual.Attempt.Terminal(), err)
				}
			})
		}
	}
}

func TestNativeExpiredLogicalTerminalOpenPreservesPhysicalCleanupObligation(t *testing.T) {
	store, id, revision := nativeStoreFixture(t, environment.ToolPolicyObserve)
	repo := store.NativeToolRepository()
	binding, _ := nativeDecisionFixture(t, store, id)
	now := time.Now().UTC().Truncate(time.Millisecond)
	receipt, err := repo.Start(t.Context(), id, "expired-logical-open", revision, now)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := repo.OpenObservedStream(t.Context(), receipt, binding, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteStreamRPC(t.Context(), authority, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	// Keep the genuine physical audit open, as an actual held writer can.
	if _, err := repo.OpenObservedStream(t.Context(), receipt, binding, time.Minute, now.Add(2*time.Minute)); err == nil {
		t.Fatal("completed receipt reopened observation")
	}
	if _, err := repo.RetireObservedStream(t.Context(), authority, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	actual, err := scanEgressAttempt(store.database.QueryRow(egressAttemptSelect+` WHERE attempt_id=?`, receipt.AttemptID()))
	if err != nil || !actual.Attempt.Terminal() {
		t.Fatalf("expiry pruning discarded exact physical cleanup: terminal=%v err=%v", actual.Attempt.Terminal(), err)
	}
	operation, err := repo.Get(t.Context(), id)
	if err != nil || operation.State != "completed" {
		t.Fatal("physical cleanup changed known logical result", err)
	}
}
