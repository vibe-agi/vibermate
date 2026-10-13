package runtimepersistence

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func TestNativeObservedFinishRequiresExactReceiptAndAtomicFacts(t *testing.T) {
	for _, edge := range []string{"known", "unknown", "nil", "foreign", "get", "notification", "method", "closed-audit", "terminal", "commit-error", "rollback-error"} {
		t.Run(edge, func(t *testing.T) {
			store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
			repo := store.NativeToolRepository()
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			b, _ := nativeDecisionFixture(t, store, id)
			nativeAllow(t, repo, id, now)
			receipt, err := repo.Start(ctx, id, "observed-finish", rev, now)
			if err != nil {
				t.Fatal(err)
			}
			if edge == "get" || edge == "notification" {
				kind := nativetool.RequestGetStream
				raw := []byte(nil)
				if edge == "notification" {
					kind = nativetool.RequestCancel
					raw = []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}`)
				}
				catalog, _ := b.CatalogDigest()
				child, err := nativetool.NewBinding(nativetool.BindingInput{Kind: kind, Plan: b.Plan(), Capture: b.Capture(), AssignmentRevision: b.AssignmentRevision(), OwnerEpoch: b.OwnerEpoch(), SessionID: b.SessionID(), SessionRevision: b.SessionRevision(), CatalogGeneration: b.CatalogGeneration(), CatalogDigest: &catalog, ExactRPC: raw})
				if err != nil {
					t.Fatal(err)
				}
				id = nativetool.ID("88888888888888888888888888888888")
				if kind == nativetool.RequestGetStream {
					plan, sid := child.Plan(), child.SessionID()
					if _, err := repo.Receive(ctx, nativetool.ReceiveInput{ID: id, Capture: child.Capture(), EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "observed-control", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "GET", Kind: kind, SessionID: &sid, Recording: plan.ContentRecording(), ObservedAt: now}); err != nil {
						t.Fatal(err)
					}
					if err := repo.Bind(ctx, id, child, now); err != nil {
						t.Fatal(err)
					}
				} else {
					nativeReceiveBind(t, repo, id, child, now)
				}
				receipt, err = repo.Start(ctx, id, "observed-control", rev, now)
				if err != nil {
					t.Fatal(err)
				}
			}
			physical := nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", ObservedAt: now}
			logical := nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}
			if edge == "known" {
				physical.Counts = &nativetool.ObservedCounts{BytesIn: 13, BytesOut: 17}
			}
			switch edge {
			case "nil":
				receipt = nativetool.StartReceipt{}
			case "foreign":
				issuer, _ := nativetool.NewStartReceiptIssuer()
				receipt, _ = issuer.Issue(nativetool.StartFacts{OperationID: id, AttemptID: receipt.AttemptID(), AuthorityRevision: rev, OwnerEpoch: b.OwnerEpoch(), BindingDigest: b.Digest()})
			case "method":
				if _, err := store.database.Exec(`UPDATE native_tool_operations SET method='GET' WHERE operation_id=?`, id); err != nil {
					t.Fatal(err)
				}
				defer store.database.Exec(`UPDATE native_tool_operations SET method='POST' WHERE operation_id=?`, id)
			case "closed-audit":
				if err := repo.FinishAttempt(ctx, receipt, physical); err != nil {
					t.Fatal(err)
				}
			case "terminal":
				if err := repo.Finish(ctx, id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
					t.Fatal(err)
				}
			case "commit-error":
				store.nativeTools.committer = commitThenError{}
			case "rollback-error":
				store.nativeTools.committer = rollbackThenError{}
			}
			err = repo.FinishObservedRPC(ctx, receipt, physical, logical)
			store.nativeTools.committer = sqlTransactionCommitter{}
			success := edge == "known" || edge == "unknown"
			if success != (err == nil) {
				t.Fatalf("edge %s unexpected result %v", edge, err)
			}
			op, getErr := repo.Get(ctx, id)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if success || edge == "commit-error" {
				if op.State != "completed" {
					t.Fatalf("logical state=%s", op.State)
				}
				audit, err := scanEgressAttempt(store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, id))
				if err != nil || audit.Attempt.Outcome() != "failed" {
					t.Fatalf("physical outcome %v %v", audit, err)
				}
				if edge == "known" {
					if op.BytesIn == nil || *op.BytesIn != 13 || op.BytesOut == nil || *op.BytesOut != 17 {
						t.Fatal("known bytes lost")
					}
				} else if op.BytesIn != nil || op.BytesOut != nil {
					t.Fatal("NULL counts replaced")
				}
			} else if edge == "rollback-error" && op.State != "started" {
				t.Fatalf("rollback was partial: %+v", op)
			}
			if len(store.nativeTools.streams) != 0 {
				t.Fatal("ordinary terminal created stream authority")
			}
		})
	}
}

func TestNativeAmbiguousPhysicalFinishCleansKnownTerminal(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			store, id, rev := nativeStoreFixture(t, environment.ToolPolicyObserve)
			repo := store.NativeToolRepository()
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			b, _ := nativeDecisionFixture(t, store, id)
			receipt, err := repo.Start(ctx, id, "known-physical", rev, now)
			if err != nil {
				t.Fatal(err)
			}
			a, err := repo.OpenObservedStream(ctx, receipt, b, time.Minute, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.CompleteStreamRPC(ctx, a, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
				t.Fatal(err)
			}
			store.nativeTools.committer = rollbackThenError{}
			if committed {
				store.nativeTools.committer = commitThenError{}
			}
			err = repo.FinishAttempt(ctx, receipt, nativetool.PhysicalFinishInput{Outcome: "failed", Reason: "transport_failed", ObservedAt: now})
			store.nativeTools.committer = sqlTransactionCommitter{}
			if err == nil {
				t.Fatal("fault absent")
			}
			if _, err := repo.RetireObservedStream(ctx, a, now); err != nil {
				t.Fatal(err)
			}
			op, err := repo.Get(ctx, id)
			if err != nil || op.State != "completed" {
				t.Fatalf("known terminal overwritten %+v %v", op, err)
			}
			scope, err := repo.GetScope(ctx, b.Capture())
			if err != nil || scope.ReplayBarrier {
				t.Fatal("known result gained uncertainty barrier")
			}
			audit, err := scanEgressAttempt(store.database.QueryRow(egressAttemptSelect+` WHERE parent_id=?`, id))
			if err != nil || !audit.Attempt.Terminal() || audit.Attempt.Outcome() != "failed" || audit.Attempt.ByteCountsKnown() {
				t.Fatalf("physical cleanup stranded: terminal=%v err=%v", audit.Attempt.Terminal(), err)
			}
		})
	}
}
