package runtimepersistence

import (
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func TestNativeCatalogInvalidationClosesOnlyUnconsumedSessionAuthority(t *testing.T) {
	for _, state := range []string{"pending", "allowed", "started"} {
		t.Run(state, func(t *testing.T) {
			store, id, _ := nativeExecutionHandshakeFixture(t)
			repo := store.NativeToolRepository()
			ctx := context.Background()
			now := time.Now().UTC()
			binding, _ := nativeDecisionFixture(t, store, id)
			approvalID := nativetool.ID("99999999999999999999999999999999")
			approval, err := repo.RequestApproval(ctx, id, approvalID, now, now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			var receipt nativetool.StartReceipt
			if state != "pending" {
				if _, err = repo.DecideApproval(ctx, nativetool.DecisionInput{ApprovalID: approvalID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "real-invalidation-allow", ObservedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "started" {
				scope, _ := repo.GetScope(ctx, binding.Capture())
				receipt, err = repo.Start(ctx, id, "invalidation-real-start", scope.Revision, now)
				if err != nil {
					t.Fatal(err)
				}
			}
			revision, err := repo.InvalidateCatalog(ctx, binding.SessionID(), binding.SessionRevision(), now)
			if err != nil || revision != binding.SessionRevision()+1 {
				t.Fatalf("invalidate %d %v", revision, err)
			}
			question, err := repo.GetApproval(ctx, approvalID)
			if err != nil {
				t.Fatal(err)
			}
			if state == "started" {
				if question.State != "allowed" || question.ConsumedAt == nil {
					t.Fatal("consumed approval altered")
				}
				if err = repo.Finish(ctx, id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
					t.Fatalf("known started result rejected %v", err)
				}
				if repo.VerifyStartReceipt(ctx, receipt, now) == nil {
					t.Fatal("finished receipt remained sendable")
				}
			} else {
				if question.State != "canceled" {
					t.Fatalf("unconsumed question %s", question.State)
				}
				scope, _ := repo.GetScope(ctx, binding.Capture())
				if _, err = repo.Start(ctx, id, "stale-invalidation-start", scope.Revision, now); err == nil {
					t.Fatal("stale question could Start")
				}
			}
			if _, err = repo.InvalidateCatalog(ctx, binding.SessionID(), binding.SessionRevision(), now); err == nil {
				t.Fatal("stale catalog CAS accepted")
			}
		})
	}
}
