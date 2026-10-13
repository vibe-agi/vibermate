package productruntime

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

func TestRuntimeNativeEgressHoldUsesFrozenConcreteProbe(t *testing.T) {
	f := newRuntimeNativeEgressFixture(t, runtimeNativeEgressConfig{choice: "global", edge: "hold"})
	f.handshake(t)
	if _, e := f.r.EnterOfflineHold(t.Context(), f.r.OfflineHoldSnapshot().Revision); e != nil {
		t.Fatal(e)
	}
	call := f.begin(t, "POST", `{"jsonrpc":"2.0","id":44,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "")
	f.allow(t, call.ID)
	target := f.waitQueued(t)
	f.assertNoStart(t, call.ID)
	if revision := f.setDefault(t, &f.profileB); revision != 3 {
		t.Fatalf("native edited default revision=%d", revision)
	}
	t.Logf("native actual queued target=%+v currentGate=%+v", target, f.r.OfflineHoldSnapshot())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, e := f.r.ResumeOfflineHold(ctx, f.r.OfflineHoldSnapshot().Revision)
	if e != nil {
		f.assertNoStart(t, call.ID)
		call.Cancel()
		terminal := f.join(t, call)
		f.assertDrained(t)
		t.Logf("native failed Resume=%v terminal=%+v counts=%+v", e, terminal, f.counts())
		f.cleanup(t)
		t.Fatalf("expected actual native concrete Resume success: %v", e)
	}
	terminal := f.join(t, call)
	if terminal.Outcome != nativemcp.OutcomeCompleted {
		t.Fatalf("native resumed tool %+v", terminal)
	}
	f.assertAudit(t, call.ID, runtimeNativeExpected{ProfileID: "profile.native-a", ProfileRevision: 1, Source: egressprofile.SelectionGlobal, DefaultRevision: 2}, "completed", "")
	if c := f.counts(); c.HTTP != 4 || c.AConnects != 5 || c.AAttempts != 5 || c.TLS != 5 || c.BAttempts != 0 || c.DirectAttempts != 0 || c.Unexpected != 0 || c.Credentials != 0 {
		t.Fatalf("native frozen probe/send physical %+v", c)
	}
	next := f.begin(t, "POST", `{"jsonrpc":"2.0","id":45,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"b"}}}`, "")
	f.allow(t, next.ID)
	if terminal = f.join(t, next); terminal.Outcome != nativemcp.OutcomeCompleted {
		t.Fatal(terminal)
	}
	f.assertAudit(t, next.ID, runtimeNativeExpected{ProfileID: "profile.native-b", ProfileRevision: 1, Source: egressprofile.SelectionGlobal, DefaultRevision: 3}, "completed", "")
	if c := f.counts(); c.HTTP != 5 || c.AConnects != 5 || c.BConnects != 1 || c.BAttempts != 1 || c.TLS != 6 || c.DirectAttempts != 0 || c.Unexpected != 0 || c.Credentials != 0 {
		t.Fatalf("native new approved call physical %+v", c)
	}
	f.assertDrained(t)
}

func TestRuntimeNativeEgressHoldFailuresBeforeStart(t *testing.T) {
	for _, edge := range []string{"probe_proxy_down", "probe_wrong_name", "cancel_queued", "authority_revoked"} {
		t.Run(edge, func(t *testing.T) {
			f := newRuntimeNativeEgressFixture(t, runtimeNativeEgressConfig{choice: "global", edge: edge})
			f.handshake(t)
			if _, e := f.r.EnterOfflineHold(t.Context(), f.r.OfflineHoldSnapshot().Revision); e != nil {
				t.Fatal(e)
			}
			call := f.begin(t, "POST", `{"jsonrpc":"2.0","id":44,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "")
			f.allow(t, call.ID)
			target := f.waitQueued(t)
			f.assertNoStart(t, call.ID)
			if target.TargetRef != "https://native.example" || target.EgressPolicy.Proxy.Endpoint != f.network.a || target.PlanRevision != 0 || target.PlanDigest != "" {
				t.Fatalf("native negative frozen target %+v", target)
			}
			approvedOp, e := f.r.NativeToolRepository().Get(t.Context(), call.ID)
			if e != nil {
				t.Fatal(e)
			}
			if approvedOp.ID != call.ID || approvedOp.State != "validated" || approvedOp.DecisionSource != "user" || approvedOp.ApprovalID == nil || approvedOp.BindingDigest == nil {
				t.Fatalf("native queued approved operation %+v", approvedOp)
			}
			allowedApproval, e := f.r.NativeToolRepository().GetApproval(t.Context(), *approvedOp.ApprovalID)
			if e != nil {
				t.Fatal(e)
			}
			if allowedApproval.ID != *approvedOp.ApprovalID || allowedApproval.OperationID != call.ID || allowedApproval.BindingDigest != *approvedOp.BindingDigest || allowedApproval.State != "allowed" || allowedApproval.Revision != 2 || allowedApproval.ConsumedAt != nil {
				t.Fatalf("native queued exact Allow op=%+v approval=%+v", approvedOp, allowedApproval)
			}
			switch edge {
			case "probe_proxy_down", "probe_wrong_name":
				// The real queue is the causal barrier; initialization has completed.
				if edge == "probe_proxy_down" {
					f.proxyA.Close()
				} else {
					f.wrongName.Store(true)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_, e := f.r.ResumeOfflineHold(ctx, f.r.OfflineHoldSnapshot().Revision)
				cancel()
				if e == nil {
					t.Fatal("native failed reachability unexpectedly resumed")
				}
				t.Logf("native contained failed probe edge=%s error=%v target=%+v", edge, e, target)
				if f.gate.Snapshot().State != offlinehold.StateHeld || f.gate.Snapshot().QueuedRequests != 1 {
					t.Fatalf("native failed probe released held call %+v", f.gate.Snapshot())
				}
				f.assertNoStart(t, call.ID)
				call.Cancel()
			case "cancel_queued":
				call.Cancel()
			case "authority_revoked":
				if e := f.r.nativeTools.Revoke(t.Context(), f.capture, nativemcp.RevocationOwnerChanged); e != nil {
					t.Fatal(e)
				}
			}
			terminal := f.join(t, call)
			if terminal.OperationID != call.ID || terminal.Outcome != nativemcp.OutcomeCanceled || terminal.Reason != nativemcp.ReasonRevoked {
				t.Fatalf("native unsent terminal %+v", terminal)
			}
			f.assertNoStart(t, call.ID)
			f.assertDrained(t)
			if f.gate.Snapshot().State != offlinehold.StateHeld || len(f.gate.PendingProbeTargets()) != 0 {
				t.Fatalf("native canceled call left held target gate=%+v targets=%+v", f.gate.Snapshot(), f.gate.PendingProbeTargets())
			}
			op, e := f.r.NativeToolRepository().Get(t.Context(), call.ID)
			if e != nil {
				t.Fatal(e)
			}
			if op.ApprovalID == nil || op.BindingDigest == nil {
				t.Fatalf("native terminal lost approval/binding %+v", op)
			}
			approval, e := f.r.NativeToolRepository().GetApproval(t.Context(), *op.ApprovalID)
			if e != nil {
				t.Fatal(e)
			}
			validReason := op.Reason == "revoked"
			if edge == "authority_revoked" {
				validReason = op.Reason == "revoked" || op.Reason == "canceled"
			}
			if op.ID != approvedOp.ID || op.ApprovalID == nil || *op.ApprovalID != *approvedOp.ApprovalID || *op.BindingDigest != *approvedOp.BindingDigest || op.State != "canceled" || op.DecisionSource != "user" || !validReason || op.Revision != approvedOp.Revision+1 || op.CompletedAt == nil || approval.ID != allowedApproval.ID || approval.OperationID != allowedApproval.OperationID || approval.BindingDigest != allowedApproval.BindingDigest || approval.State != "canceled" || approval.Revision != 3 || approval.ConsumedAt != nil {
				t.Fatalf("native terminal exact closure edge=%s initial=%+v/%+v final=%+v/%+v", edge, approvedOp, allowedApproval, op, approval)
			}
			t.Logf("native unsent persisted edge=%s operationState=%s decision=%s reason=%s approvalState=%s revision=%d consumed=%v terminal=%+v", edge, op.State, op.DecisionSource, op.Reason, approval.State, approval.Revision, approval.ConsumedAt, terminal)
			c := f.counts()
			wantAttempts, wantConnects, wantTLS := int64(3), int64(3), int64(3)
			if edge == "probe_proxy_down" {
				wantAttempts = 4
			}
			if edge == "probe_wrong_name" {
				wantAttempts = 4
				wantConnects = 4
				wantTLS = 4
			}
			if c.HTTP != 3 || c.AAttempts != wantAttempts || c.AConnects != wantConnects || c.TLS != wantTLS || c.BAttempts != 0 || c.DirectAttempts != 0 || c.Unexpected != 0 || c.Credentials != 0 {
				t.Fatalf("native unsent containment %+v", c)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			resumed, e := f.r.ResumeOfflineHold(ctx, f.r.OfflineHoldSnapshot().Revision)
			cancel()
			if e != nil || resumed.State != offlinehold.StateOnline {
				t.Fatalf("native empty-queue Resume edge=%s snapshot=%+v error=%v", edge, resumed, e)
			}
			f.assertDrained(t)
			if len(f.gate.PendingProbeTargets()) != 0 || f.counts() != c {
				t.Fatalf("native late send after empty-queue Resume edge=%s targets=%+v before=%+v after=%+v", edge, f.gate.PendingProbeTargets(), c, f.counts())
			}
			f.assertNoStart(t, call.ID)
			afterOp, e := f.r.NativeToolRepository().Get(t.Context(), call.ID)
			if e != nil {
				t.Fatal(e)
			}
			afterApproval, e := f.r.NativeToolRepository().GetApproval(t.Context(), allowedApproval.ID)
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(afterOp, op) || !reflect.DeepEqual(afterApproval, approval) {
				t.Fatalf("native empty-queue Resume mutated closure edge=%s before=%+v/%+v after=%+v/%+v", edge, op, approval, afterOp, afterApproval)
			}
			t.Logf("native empty-queue Resume edge=%s state=%s counts=%+v gate=%+v", edge, resumed.State, f.counts(), f.gate.Snapshot())
		})
	}
}
