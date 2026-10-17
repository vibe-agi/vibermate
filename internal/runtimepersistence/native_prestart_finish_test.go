package runtimepersistence

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func nativePrestartAllowed(t *testing.T) (*Store, nativetool.Repository, nativetool.Operation, nativetool.Approval, uint64, time.Time) {
	t.Helper()
	store, id, authority := nativeLifecycleFixture(t, environment.ToolPolicyReview)
	repo := nativeFullRepository(t, store)
	now := time.Now().UTC().Truncate(time.Millisecond)
	nativeAllow(t, repo, id, now)
	op, e := repo.Get(t.Context(), id)
	if e != nil {
		t.Fatal(e)
	}
	approval, e := repo.GetApproval(t.Context(), *op.ApprovalID)
	if e != nil {
		t.Fatal(e)
	}
	if op.State != "validated" || approval.State != "allowed" || approval.ConsumedAt != nil {
		t.Fatalf("actual allowed fixture %+v %+v", op, approval)
	}
	return store, repo, op, approval, authority, now
}
func nativePrestartRows(t *testing.T, store *Store, id nativetool.ID, want int) {
	t.Helper()
	for _, query := range []string{`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, `SELECT COUNT(*) FROM native_tool_deliveries WHERE operation_id=?`} {
		var got int
		if e := store.database.QueryRowContext(t.Context(), query, id).Scan(&got); e != nil || got != want {
			t.Fatalf("native physical rows got=%d want=%d error=%v", got, want, e)
		}
	}
}

func TestNativePrestartFinishClosesExactUnconsumedAllow(t *testing.T) {
	for _, in := range []nativetool.TerminalInput{{Outcome: "canceled", Reason: "revoked"}, {Outcome: "canceled", Reason: "canceled"}, {Outcome: "failed", Reason: "rpc_error"}, {Outcome: "failed", Reason: "transport_failed"}} {
		t.Run(in.Outcome+"/"+in.Reason, func(t *testing.T) {
			store, repo, initial, allowed, authority, now := nativePrestartAllowed(t)
			in.ObservedAt = now.Add(time.Millisecond)
			if e := repo.Finish(t.Context(), initial.ID, in); e != nil {
				t.Fatal(e)
			}
			op, e := repo.Get(t.Context(), initial.ID)
			if e != nil {
				t.Fatal(e)
			}
			approval, e := repo.GetApproval(t.Context(), allowed.ID)
			if e != nil {
				t.Fatal(e)
			}
			// Assert exact association and closure without consuming or starting.
			if op.State != in.Outcome || op.Reason != in.Reason || op.DecisionSource != "user" || op.Revision != initial.Revision+1 || op.ApprovalID == nil || *op.ApprovalID != allowed.ID || op.BindingDigest == nil || *op.BindingDigest != allowed.BindingDigest || approval.ID != allowed.ID || approval.OperationID != initial.ID || approval.BindingDigest != allowed.BindingDigest || approval.State != "canceled" || approval.Revision != allowed.Revision+1 || approval.ConsumedAt != nil {
				t.Fatalf("pre-Start closure op=%+v approval=%+v initial=%+v allowed=%+v", op, approval, initial, allowed)
			}
			nativePrestartRows(t, store, initial.ID, 0)
			if receipt, e := repo.Start(t.Context(), initial.ID, "late-prestart-attempt", authority, in.ObservedAt); e == nil || receipt.OperationID() != "" {
				t.Fatalf("late Start receipt=%+v error=%v", receipt, e)
			}
			nativePrestartRows(t, store, initial.ID, 0)
			if e = store.Shutdown(context.Background()); e != nil {
				t.Fatalf("closed unconsumed Allow must permit Store shutdown: %v", e)
			}
		})
	}
}

func TestNativePrestartFinishInvalidInputPreservesAllow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input nativetool.TerminalInput
	}{{"completion", nativetool.TerminalInput{Outcome: "completed", Reason: "completed"}}, {"bytes_in", nativetool.TerminalInput{Outcome: "canceled", Reason: "revoked", BytesIn: 1}}, {"bytes_out", nativetool.TerminalInput{Outcome: "failed", Reason: "rpc_error", BytesOut: 1}}, {"stale_time", nativetool.TerminalInput{Outcome: "canceled", Reason: "revoked", ObservedAt: time.UnixMilli(1).UTC()}}} {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.input
			store, repo, initial, allowed, _, now := nativePrestartAllowed(t)
			if in.ObservedAt.IsZero() {
				in.ObservedAt = now.Add(time.Millisecond)
			}
			if e := repo.Finish(t.Context(), initial.ID, in); !errors.Is(e, nativetool.ErrInvalid) {
				t.Fatalf("invalid pre-Start Finish: %v", e)
			}
			op, e := repo.Get(t.Context(), initial.ID)
			if e != nil {
				t.Fatal(e)
			}
			approval, e := repo.GetApproval(t.Context(), allowed.ID)
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(op, initial) || !reflect.DeepEqual(approval, allowed) {
				t.Fatalf("invalid Finish mutated op/Allow %+v %+v", op, approval)
			}
			nativePrestartRows(t, store, initial.ID, 0)
		})
	}
}

func TestNativePrestartFinishRollbackPreservesAllowAndOperation(t *testing.T) {
	store, repo, initial, allowed, _, now := nativePrestartAllowed(t)
	store.nativeTools.committer = rollbackThenError{}
	err := repo.Finish(t.Context(), initial.ID, nativetool.TerminalInput{Outcome: "canceled", Reason: "revoked", ObservedAt: now.Add(time.Millisecond)})
	store.nativeTools.committer = sqlTransactionCommitter{}
	if !errors.Is(err, nativetool.ErrUnavailable) {
		t.Fatalf("expected contained rollback ambiguity: %v", err)
	}
	op, err := repo.Get(t.Context(), initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := repo.GetApproval(t.Context(), allowed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(op, initial) || !reflect.DeepEqual(approval, allowed) {
		t.Fatalf("failed transaction partially closed operation/Allow %+v %+v", op, approval)
	}
	nativePrestartRows(t, store, initial.ID, 0)
}

func TestNativePrestartFinishLeavesConsumedStartedAllow(t *testing.T) {
	store, repo, initial, allowed, authority, now := nativePrestartAllowed(t)
	receipt, e := repo.Start(t.Context(), initial.ID, "real-consumed-attempt", authority, now)
	if e != nil {
		t.Fatal(e)
	}
	if e = repo.VerifyStartReceipt(t.Context(), receipt, now); e != nil {
		t.Fatal(e)
	}
	consumed, e := repo.GetApproval(t.Context(), allowed.ID)
	if e != nil || consumed.ConsumedAt == nil {
		t.Fatalf("real consumed Allow %+v %v", consumed, e)
	}
	if e = repo.Finish(t.Context(), initial.ID, nativetool.TerminalInput{Outcome: "canceled", Reason: "canceled", ObservedAt: now.Add(time.Millisecond)}); e != nil {
		t.Fatal(e)
	}
	after, e := repo.GetApproval(t.Context(), allowed.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(after, consumed) {
		t.Fatalf("started Finish reset consumed Allow %+v -> %+v", consumed, after)
	}
	op, e := repo.Get(t.Context(), initial.ID)
	if e != nil || op.State != "canceled" {
		t.Fatalf("real started terminal %+v %v", op, e)
	}
	nativePrestartRows(t, store, initial.ID, 1)
}
