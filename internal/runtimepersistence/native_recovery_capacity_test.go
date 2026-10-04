package runtimepersistence

import (
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func receiveRecoveryCapacityChild(t *testing.T, repo nativetool.Repository, base nativetool.Binding, a nativetool.StreamAuthority, id nativetool.ID, now time.Time) {
	t.Helper()
	ctx := context.Background()
	catalog, _ := base.CatalogDigest()
	b, err := nativetool.NewBinding(nativetool.BindingInput{Kind: nativetool.RequestGetStream, Plan: base.Plan(), Capture: base.Capture(), AssignmentRevision: base.AssignmentRevision(), OwnerEpoch: base.OwnerEpoch(), SessionID: base.SessionID(), SessionRevision: base.SessionRevision(), CatalogGeneration: base.CatalogGeneration(), CatalogDigest: &catalog})
	if err != nil {
		t.Fatal(err)
	}
	plan, sid := b.Plan(), b.SessionID()
	if _, err := repo.Receive(ctx, nativetool.ReceiveInput{ID: id, Capture: b.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "capacity-recovery", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "GET", Kind: b.Kind(), SessionID: &sid, Recording: plan.ContentRecording(), ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.BindRecoveryGET(ctx, id, b, a, now); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRecoveryCapacityLifecycleAndAmbiguity(t *testing.T) {
	for _, edge := range []string{"finish", "parent", "session", "revoke", "recover", "commit-finish", "rollback-finish", "rollback-recover"} {
		t.Run(edge, func(t *testing.T) {
			store, origin, rev := nativeStoreFixture(t, environment.ToolPolicyObserve)
			repo := store.NativeToolRepository()
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			b, _ := nativeDecisionFixture(t, store, origin)
			receipt, err := repo.Start(ctx, origin, "capacity-origin", rev, now)
			if err != nil {
				t.Fatal(err)
			}
			a, err := repo.OpenObservedStream(ctx, receipt, b, time.Minute, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", ObservedAt: now}); err != nil {
				t.Fatal(err)
			}
			child := nativetool.ID("34343434343434343434343434343434")
			receiveRecoveryCapacityChild(t, repo, b, a, child, now)
			switch edge {
			case "finish":
				err = repo.Finish(ctx, child, nativetool.TerminalInput{Outcome: "canceled", Reason: "canceled", ObservedAt: now})
			case "parent":
				err = repo.RetireObservedStream(ctx, a, now)
			case "session":
				_, err = repo.CloseSession(ctx, b.SessionID(), b.SessionRevision(), now)
			case "revoke":
				err = repo.Revoke(ctx, b.Capture(), rev, "revoked", now)
			case "recover":
				err = repo.Recover(ctx, now)
			case "commit-finish", "rollback-finish":
				store.nativeTools.committer = commitThenError{}
				if edge == "rollback-finish" {
					store.nativeTools.committer = rollbackThenError{}
				}
				err = repo.Finish(ctx, child, nativetool.TerminalInput{Outcome: "canceled", Reason: "canceled", ObservedAt: now})
				store.nativeTools.committer = sqlTransactionCommitter{}
				if err == nil {
					t.Fatal("finish fault absent")
				}
				if _, err := repo.Start(ctx, child, "late-ambiguous", rev, now); err == nil {
					t.Fatal("ambiguous cancellation reopened Start")
				}
				if edge == "rollback-finish" {
					err = repo.RetireObservedStream(ctx, a, now)
				} else {
					receiveRecoveryCapacityChild(t, repo, b, a, nativetool.ID("35353535353535353535353535353535"), now)
					err = nil
				}
			case "rollback-recover":
				store.nativeTools.committer = rollbackThenError{}
				err = repo.Recover(ctx, now)
				store.nativeTools.committer = sqlTransactionCommitter{}
				if err == nil {
					t.Fatal("recover fault absent")
				}
				if _, err := repo.Start(ctx, child, "late-recovered", rev, now); err == nil {
					t.Fatal("ambiguous Recover erased recovery fence")
				}
				err = repo.Recover(ctx, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			op, err := repo.Get(ctx, child)
			if err != nil || op.CompletedAt == nil {
				t.Fatalf("lifecycle left child startable: %s %v", op.State, err)
			}
			store.nativeTools.issuerMu.Lock()
			_, retained := store.nativeTools.recovery[child]
			store.nativeTools.issuerMu.Unlock()
			if retained {
				t.Fatal("terminal child retained pending capacity")
			}
			if r, err := repo.Start(ctx, child, "late-terminal", rev, now); err == nil || r.OperationID() != "" {
				t.Fatal("pruned child allowed late Start")
			}
		})
	}
}

func TestNativeStreamDeadlineIncludesIndependentScopeAndSession(t *testing.T) {
	for _, table := range []string{"native_tool_scopes", "native_tool_sessions"} {
		t.Run(table, func(t *testing.T) {
			store, id, rev := nativeStoreFixture(t, environment.ToolPolicyObserve)
			repo := store.NativeToolRepository()
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			b, _ := nativeDecisionFixture(t, store, id)
			receipt, err := repo.Start(ctx, id, "clamped-deadline", rev, now)
			if err != nil {
				t.Fatal(err)
			}
			expires := now.Add(150 * time.Millisecond)
			if _, err := store.database.Exec(`UPDATE `+table+` SET expires_at_unix_ms=?`, expires.UnixMilli()); err != nil {
				t.Fatal(err)
			}
			a, err := repo.OpenObservedStream(ctx, receipt, b, time.Minute, now)
			if err != nil {
				t.Fatal(err)
			}
			if !a.ExpiresAt().Equal(expires) {
				t.Fatalf("ignored %s expiry: %v", table, a.ExpiresAt())
			}
			foreign, _ := nativetool.NewStartReceiptIssuer()
			forged, err := foreign.IssueStream(now.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if repo.CompleteStreamRPC(ctx, forged, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}) == nil {
				t.Fatal("foreign metadata granted authority")
			}
			if repo.CompleteStreamRPC(ctx, a, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: expires}) == nil {
				t.Fatal("deadline boundary accepted")
			}
		})
	}
}
