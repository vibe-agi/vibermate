package runtimepersistence

import (
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// Optional history loss must not revoke a valid current native operation.
func TestNativeAuthorityExecutesWithoutHistoryRows(t *testing.T) {
	store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
	repo := store.NativeToolRepository()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, repo, id, now)
	if _, err := store.database.Exec(`DELETE FROM native_tool_events`); err != nil {
		t.Fatal(err)
	}
	receipt, err := repo.Start(ctx, id, "no-history-send", rev, now)
	if err != nil {
		t.Fatalf("optional history loss denied Start: %v", err)
	}
	if err := repo.VerifyStartReceipt(ctx, receipt, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.Finish(ctx, id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", BytesIn: 3, BytesOut: 4, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if repeat, err := repo.Start(ctx, id, "no-history-repeat", rev, now); err == nil || repeat.OperationID() != "" {
		t.Fatal("repeat issued send authority")
	}
}

// Ordinary egress audit retention cannot erase once-delivery proof.
func TestNativeAuthorityReceiptSurvivesEgressHistoryLoss(t *testing.T) {
	store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
	repo := store.NativeToolRepository()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, repo, id, now)
	receipt, err := repo.Start(ctx, id, "independent-delivery", rev, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.Exec(`DELETE FROM runtime_egress_attempts`); err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyStartReceipt(ctx, receipt, now); err != nil {
		t.Fatalf("ordinary audit loss invalidated required delivery: %v", err)
	}
	if err := repo.Finish(ctx, id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", BytesIn: 7, BytesOut: 11, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func TestNativePendingApprovalsDiscoverWithoutHistory(t *testing.T) {
	store, id, _ := nativeStoreFixture(t, environment.ToolPolicyReview)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	op, err := store.NativeToolRepository().Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := store.NativeToolRepository().RequestApproval(ctx, id, "cccccccccccccccccccccccccccccccc", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.Exec(`DELETE FROM native_tool_events`); err != nil {
		t.Fatal(err)
	}
	var authority nativetool.AuthorityRepository = store.nativeTools
	page, err := authority.ListPendingApprovals(ctx, nativetool.PendingApprovalRequest{Capture: op.Capture, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Approval.ID != approval.ID || page.Items[0].Operation.ID != id {
		t.Fatalf("pending discovery depends on history: %+v %v", page, err)
	}
}

func TestNativeAuthorityRefusesCorruptCurrentProof(t *testing.T) {
	for _, fault := range []string{"delivery", "event", "once", "approval"} {
		t.Run(fault, func(t *testing.T) {
			store, id, rev := nativeStoreFixture(t, environment.ToolPolicyReview)
			repo := store.NativeToolRepository()
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			nativeAllow(t, repo, id, now)
			receipt, err := repo.Start(ctx, id, "corrupt-required-proof", rev, now)
			if err != nil {
				t.Fatal(err)
			}
			var change, restore string
			switch fault {
			case "delivery":
				change = `UPDATE native_tool_deliveries SET connection_id='foreign' WHERE attempt_id='corrupt-required-proof'`
				restore = `UPDATE native_tool_deliveries SET connection_id='synthetic-native' WHERE attempt_id='corrupt-required-proof'`
			case "event":
				change = `UPDATE native_tool_operations SET last_event_kind='completed' WHERE operation_id=?`
				restore = `UPDATE native_tool_operations SET last_event_kind='started' WHERE operation_id=?`
			case "once":
				change = `UPDATE native_tool_once SET authority_revision=authority_revision+1 WHERE operation_id=?`
				restore = `UPDATE native_tool_once SET authority_revision=authority_revision-1 WHERE operation_id=?`
			case "approval":
				change = `UPDATE native_tool_approvals SET consumed_at_unix_ms=consumed_at_unix_ms+1 WHERE operation_id=?`
				restore = `UPDATE native_tool_approvals SET consumed_at_unix_ms=consumed_at_unix_ms-1 WHERE operation_id=?`
			}
			args := []any{id}
			if fault == "delivery" {
				args = nil
			}
			if _, err := store.database.Exec(change, args...); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := store.database.Exec(restore, args...); err != nil {
					t.Error(err)
				}
			}()
			if err := repo.VerifyStartReceipt(ctx, receipt, now); err == nil {
				t.Fatal("corrupt required proof authorized send")
			}
		})
	}
}
