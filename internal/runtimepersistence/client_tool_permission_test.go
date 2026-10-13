package runtimepersistence

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"testing"
	"time"
)

// This is a real Store integration check of the existing atomic publication
// contract consumed by the new control DTO, including unconsumed Allow once.
func TestClientToolAccessPublishRevokesPendingAndAllowedWithoutResurrection(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "allowed"}[allowed], func(t *testing.T) {
			store, id, authority := nativeLifecycleFixture(t, environment.ToolPolicyReview)
			repo := store.NativeToolRepository()
			ctx := context.Background()
			now := time.Now().UTC()
			op, err := repo.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			approval, err := repo.RequestApproval(ctx, id, "55555555555555555555555555555555", now, now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if allowed {
				approval, err = repo.DecideApproval(ctx, nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "permission-allow-once-001", ObservedAt: now})
				if err != nil {
					t.Fatal(err)
				}
			}
			manager := newEnvironmentManager(t, store)
			snapshot, err := manager.Resolve(op.EnvironmentID)
			if err != nil {
				t.Fatal(err)
			}
			original := snapshot.Aggregate().ClientToolAccess.Route
			candidate := snapshot.Aggregate()
			candidate.Revision++
			candidate.ClientToolAccess = nil
			draft, err := manager.SaveDraft(ctx, environment.DraftCommand{ExpectedBaseRevision: snapshot.Revision(), Candidate: candidate})
			if err != nil {
				t.Fatal(err)
			}
			before, err := repo.GetApproval(ctx, approval.ID)
			if err != nil || before.State != approval.State {
				t.Fatal("draft save revoked pending authority")
			}
			preview, err := manager.Preview(ctx, candidate.ID, draft.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = manager.Publish(ctx, preview); err != nil {
				t.Fatal(err)
			}
			revoked, err := repo.GetApproval(ctx, approval.ID)
			if err != nil || revoked.State != "canceled" {
				t.Fatalf("publish retained authority %+v %v", revoked, err)
			}
			scope, err := repo.GetScope(ctx, op.Capture)
			if err != nil || scope.State != "revoked" || !scope.ReplayBarrier {
				t.Fatalf("publish not atomic with scope %+v %v", scope, err)
			}
			candidate.Revision++
			route := *original
			route.ID = "native.fresh.tools"
			route.Revision = 1
			candidate.ClientToolAccess = &environment.ClientToolAccessPolicy{Enabled: true, Route: &route}
			draft, err = manager.SaveDraft(ctx, environment.DraftCommand{ExpectedBaseRevision: 2, Candidate: candidate})
			if err != nil {
				t.Fatal(err)
			}
			preview, err = manager.Preview(ctx, candidate.ID, draft.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = manager.Publish(ctx, preview); err != nil {
				t.Fatal(err)
			}
			if _, err = repo.Start(ctx, id, "permission-old-start", authority, now); err == nil {
				t.Fatal("reenable resurrected old pending/allowed action")
			}
			after, err := repo.GetApproval(ctx, approval.ID)
			if err != nil || after.State != "canceled" {
				t.Fatal("reenable restored approval")
			}
			if countRows(t, store, "native_tool_contents") != 0 {
				t.Fatal("permission change recorded content")
			}
		})
	}
}
