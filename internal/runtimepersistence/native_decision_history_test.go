package runtimepersistence

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

type decisionHistorySnapshot struct {
	Approval             nativetool.Approval
	Operation            nativetool.Operation
	Audit                egressaudit.Record
	Key                  string
	Once, Audits, Events int
}

func snapshotDecisionHistory(t *testing.T, f callbackSQLFixture, id nativetool.ID) decisionHistorySnapshot {
	t.Helper()
	ctx := context.Background()
	var s decisionHistorySnapshot
	var err error
	s.Approval, err = f.repo.GetApproval(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	s.Operation, err = f.repo.Get(ctx, f.child)
	if err != nil {
		t.Fatal(err)
	}
	err = f.store.database.QueryRowContext(ctx, `SELECT idempotency_key,(SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?),(SELECT COUNT(*) FROM runtime_egress_attempts WHERE parent_id=?),(SELECT COUNT(*) FROM native_tool_events WHERE operation_id=?) FROM native_tool_approvals WHERE approval_id=?`, f.child, f.child, f.child, id).Scan(&s.Key, &s.Once, &s.Audits, &s.Events)
	if err != nil {
		t.Fatal(err)
	}
	if s.Audits > 0 {
		s.Audit, err = scanEgressAttempt(f.store.database.QueryRowContext(ctx, egressAttemptSelect+` WHERE parent_id=?`, f.child))
		if err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func prepareCallbackHistory(t *testing.T, stage string) (callbackSQLFixture, nativetool.Approval, nativetool.DecisionInput, nativetool.StartReceipt) {
	t.Helper()
	f := newCallbackSQLFixture(t)
	ctx := context.Background()
	if err := f.repo.BindObservedReply(ctx, f.child, f.reply, f.callback, f.witness, f.now); err != nil {
		t.Fatal(err)
	}
	a, err := f.repo.RequestApproval(ctx, f.child, "98989898989898989898989898989898", f.now, f.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	in := nativetool.DecisionInput{ApprovalID: a.ID, ExpectedRevision: a.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "decision-history-allow-0001", ObservedAt: f.now}
	if stage == "pending" {
		return f, a, in, nativetool.StartReceipt{}
	}
	if stage == "denied" {
		in.Decision = nativetool.DecisionDeny
	}
	if _, err = f.repo.DecideApproval(ctx, in); err != nil {
		t.Fatal(err)
	}
	if stage != "consumed" {
		return f, a, in, nativetool.StartReceipt{}
	}
	receipt, err := f.repo.Start(ctx, f.child, "history-child-attempt", f.revision, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.repo.VerifyStartReceipt(ctx, receipt, f.now); err != nil {
		t.Fatal(err)
	}
	if err = f.repo.Finish(ctx, f.child, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", BytesIn: 2, BytesOut: 45, ObservedAt: f.now.Add(time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	return f, a, in, receipt
}

func TestNativeCallbackDecisionHistoryAfterRetirement(t *testing.T) {
	for _, parentCompleted := range []bool{false, true} {
		name := "child_completed_parent_live"
		if parentCompleted {
			name = "parent_completed"
		}
		t.Run(name, func(t *testing.T) {
			f, a, input, receipt := prepareCallbackHistory(t, "consumed")
			ctx := context.Background()
			var err error
			if parentCompleted {
				if err = f.repo.FinishAttempt(ctx, f.receipt, nativetool.PhysicalFinishInput{Outcome: "completed", Reason: "completed", Counts: &nativetool.ObservedCounts{}, ObservedAt: f.now.Add(2 * time.Millisecond)}); err != nil {
					t.Fatal(err)
				}
				if _, err = f.repo.RetireObservedStream(ctx, f.stream, f.now.Add(3*time.Millisecond)); err != nil {
					t.Fatal(err)
				}
			}
			f.store.nativeTools.issuerMu.Lock()
			_, callbackLive := f.store.nativeTools.callbacks[f.callback]
			_, replyLive := f.store.nativeTools.replies[f.child]
			f.store.nativeTools.issuerMu.Unlock()
			if callbackLive || replyLive {
				t.Fatal("completed callback registration was not retired")
			}
			before, err := f.repo.GetApproval(ctx, a.ID)
			if err != nil || before.ConsumedAt == nil {
				t.Fatal("child did not consume its real approval")
			}
			parent, err := f.repo.Get(ctx, f.parent)
			if err != nil || parentCompleted && parent.State != "completed" || !parentCompleted && parent.State != "started" {
				t.Fatalf("parent ordering=%+v %v", parent, err)
			}
			for _, edge := range []string{"current", "expiry", "off"} {
				input.ObservedAt = f.now.Add(4 * time.Millisecond)
				if edge == "expiry" {
					input.ObservedAt = a.ExpiresAt.Add(time.Millisecond)
				}
				if edge == "off" {
					if err = f.repo.Revoke(ctx, f.binding.Capture(), f.revision, "revoked", f.now.Add(5*time.Millisecond)); err != nil {
						t.Fatal(err)
					}
				}
				snapshot := snapshotDecisionHistory(t, f, a.ID)
				after, err := f.repo.DecideApproval(ctx, input)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("%s retired callback history=%+v err=%v", edge, after, err)
				}
				if _, err = f.repo.Start(ctx, f.child, "history-duplicate-start", f.revision, input.ObservedAt); err == nil {
					t.Fatal("history renewed Start")
				}
				if f.repo.VerifyStartReceipt(ctx, receipt, input.ObservedAt) == nil {
					t.Fatal("history renewed receipt")
				}
				if _, err = f.repo.RegisterObservedCallback(ctx, f.stream, f.facts, input.ObservedAt); err == nil {
					t.Fatal("history revived consumed callback")
				}
				if snapshot.Once != 1 || snapshot.Audits != 1 || !reflect.DeepEqual(snapshot, snapshotDecisionHistory(t, f, a.ID)) {
					t.Fatal("history changed approval/key/operation/once/audit/events")
				}
			}
		})
	}
}

func TestNativeCallbackHistoryRejectsChangedOrCorruptTuple(t *testing.T) {
	for _, edge := range []string{"key", "decision", "revision", "binding", "deleted"} {
		t.Run(edge, func(t *testing.T) {
			f, a, in, _ := prepareCallbackHistory(t, "consumed")
			ctx := context.Background()
			before := snapshotDecisionHistory(t, f, a.ID)
			want := nativetool.ErrConflict
			switch edge {
			case "key":
				in.IdempotencyKey = "changed-history-key"
			case "decision":
				in.Decision = nativetool.DecisionDeny
			case "revision":
				in.ExpectedRevision++
			case "binding":
				want = nativetool.ErrInvalid
				if _, err := f.store.database.ExecContext(ctx, `UPDATE native_tool_approvals SET binding_digest=zeroblob(32) WHERE approval_id=?`, a.ID); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				want = nativetool.ErrNotFound
				if _, err := f.store.database.ExecContext(ctx, `DELETE FROM native_tool_approvals WHERE approval_id=?`, a.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.repo.DecideApproval(ctx, in); !errors.Is(err, want) {
				t.Fatalf("%s=%v want%v", edge, err, want)
			}
			if edge == "binding" {
				if _, err := f.store.database.ExecContext(ctx, `UPDATE native_tool_approvals SET binding_digest=? WHERE approval_id=?`, a.BindingDigest[:], a.ID); err != nil {
					t.Fatal(err)
				}
			}
			if edge != "deleted" && !reflect.DeepEqual(before, snapshotDecisionHistory(t, f, a.ID)) {
				t.Fatal("rejected history changed durable state")
			}
		})
	}
}

func TestNativeCallbackClosedUnconsumedHistoryCannotAuthorize(t *testing.T) {
	for _, edge := range []string{"retired", "off", "canceled", "expired"} {
		t.Run(edge, func(t *testing.T) {
			stage := "allowed"
			if edge == "expired" || edge == "canceled" {
				stage = "pending"
			}
			f, a, in, _ := prepareCallbackHistory(t, stage)
			ctx := context.Background()
			now := f.now.Add(time.Millisecond)
			var err error
			switch edge {
			case "retired":
				_, err = f.repo.RetireObservedCallback(ctx, f.callback, now)
			case "off":
				err = f.repo.Revoke(ctx, f.binding.Capture(), f.revision, "revoked", now)
			case "canceled":
				_, err = f.repo.CloseApproval(ctx, a.ID, a.Revision, nativetool.ApprovalClosureCancelled, now)
			case "expired":
				now = a.ExpiresAt
				_, err = f.repo.CloseApproval(ctx, a.ID, a.Revision, nativetool.ApprovalClosureExpired, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := snapshotDecisionHistory(t, f, a.ID)
			state := "canceled"
			if edge == "expired" {
				state = "expired"
			}
			if before.Approval.State != state || before.Key != "" || before.Once != 0 || before.Audits != 0 {
				t.Fatalf("closure did not clear unconsumed grant: %+v", before)
			}
			in.ObservedAt = now
			if _, err = f.repo.DecideApproval(ctx, in); !errors.Is(err, nativetool.ErrConflict) {
				t.Fatalf("closed historical tuple=%v", err)
			}
			if _, err = f.repo.Start(ctx, f.child, "closed-history-start", f.revision, now); err == nil {
				t.Fatal("closed history authorized Start")
			}
			if !reflect.DeepEqual(before, snapshotDecisionHistory(t, f, a.ID)) {
				t.Fatal("closed history changed durable rows")
			}
		})
	}
}

func TestNativeCallbackDeniedHistorySurvivesRetirement(t *testing.T) {
	f, a, in, _ := prepareCallbackHistory(t, "denied")
	ctx := context.Background()
	if _, err := f.repo.RetireObservedCallback(ctx, f.callback, f.now.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	before := snapshotDecisionHistory(t, f, a.ID)
	answer, err := f.repo.DecideApproval(ctx, in)
	if err != nil || answer.State != "denied" || !reflect.DeepEqual(before.Approval, answer) || !reflect.DeepEqual(before, snapshotDecisionHistory(t, f, a.ID)) {
		t.Fatalf("denied history lost: %+v %v", answer, err)
	}
	if _, err = f.repo.Start(ctx, f.child, "denied-history-start", f.revision, f.now); err == nil {
		t.Fatal("denied history authorized Start")
	}
}

func TestNativeCallbackExpiryDoesNotCreateNewDecisionOrSend(t *testing.T) {
	for _, stage := range []string{"pending", "allowed"} {
		t.Run(stage, func(t *testing.T) {
			f, a, in, _ := prepareCallbackHistory(t, stage)
			ctx := context.Background()
			in.ObservedAt = a.ExpiresAt.Add(time.Millisecond)
			before := snapshotDecisionHistory(t, f, a.ID)
			answer, err := f.repo.DecideApproval(ctx, in)
			if stage == "pending" && err == nil {
				t.Fatal("expired pending callback obtained decision")
			}
			if stage == "allowed" && (err != nil || !reflect.DeepEqual(answer, before.Approval)) {
				t.Fatalf("retained expired decision history=%+v %v", answer, err)
			}
			if _, err = f.repo.Start(ctx, f.child, "expired-history-start", f.revision, in.ObservedAt); err == nil {
				t.Fatal("expired callback obtained send")
			}
			if before.Once != 0 || before.Audits != 0 || !reflect.DeepEqual(before, snapshotDecisionHistory(t, f, a.ID)) {
				t.Fatal("expiry history changed grant or rows")
			}
		})
	}
}
