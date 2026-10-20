package productruntime

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

func TestRuntimeNativeRecoveryBarrierRequiresFreshDecisionUnderObserve(t *testing.T) {
	f := newRuntimeNativeEgressFixture(t, runtimeNativeEgressConfig{choice: "flow_direct", toolMode: environment.ToolPolicyObserve})

	// Reuse the real native transport/receipt fixture with a fresh managed run.
	// The retired manual connection supplies no authority to the new capture.
	f.connection.Close()
	dir := t.TempDir()
	grant, err := f.r.CaptureRuns().Create(t.Context(), capturerun.CreateCommand{CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "synthetic-agent"), ExecutableLabel: "synthetic-agent", Lifetime: 5 * time.Minute, CatalogRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	view, err := f.r.CaptureRuns().Attach(t.Context(), grant.Run.ID, grant.ControlCapability, 4242)
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := captureadmission.NewAuthorizer(f.r.captureRuns, f.r.manualCaptures)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := captureadmission.NewProxyCredential(grant.ProxyCapability.Value())
	if err != nil {
		t.Fatal(err)
	}
	f.admission, err = authorizer.Authorize(t.Context(), credential)
	if err != nil {
		t.Fatal(err)
	}
	f.capture, _ = f.admission.CaptureReference()
	if _, err = f.r.CaptureAssignments().Create(t.Context(), captureassignment.CreateCommand{Capture: f.capture, EnvironmentID: "native.egress", Source: captureassignment.SourceLaunch}); err != nil {
		t.Fatal(err)
	}
	origin, _ := originidentity.ParseProviderOrigin("https://native.example")
	f.connection, err = f.r.assignments.RegisterProviderConnection(t.Context(), f.capture, "native.egress.connection", origin)
	if err != nil {
		t.Fatal(err)
	}

	preparer, ok := any(f.r).(interface {
		PrepareNativeRecovery(context.Context, capturerun.View) error
	})
	if !ok {
		t.Fatal("Runtime does not expose the native recovery preparation barrier")
	}
	repo := f.r.NativeToolRepository()
	wrongProcess := view
	wrongProcess.ProcessID++
	if err = preparer.PrepareNativeRecovery(t.Context(), wrongProcess); err == nil {
		t.Fatal("native recovery prepared a different process")
	}
	if _, err = repo.GetScope(t.Context(), f.capture); !errors.Is(err, nativetool.ErrNotFound) {
		t.Fatalf("rejected preparation created native authority: %v", err)
	}
	if err = preparer.PrepareNativeRecovery(t.Context(), view); err != nil {
		t.Fatal(err)
	}
	before, err := repo.GetScope(t.Context(), f.capture)
	if err != nil || !before.ReplayBarrier || before.OwnerEpoch != 1 || before.State != "active" {
		t.Fatalf("native recovery did not bootstrap an active barrier: %+v %v", before, err)
	}
	if err = preparer.PrepareNativeRecovery(t.Context(), view); err != nil {
		t.Fatal(err)
	}
	if after, err := repo.GetScope(t.Context(), f.capture); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("idempotent preparation changed authority: before=%+v after=%+v err=%v", before, after, err)
	}

	f.handshake(t)
	scope, err := repo.GetScope(t.Context(), f.capture)
	if err != nil || !scope.ReplayBarrier || scope.OwnerEpoch == 1 || scope.Revision <= before.Revision {
		t.Fatalf("native executor owner replacement lost the barrier: %+v %v", scope, err)
	}
	call := f.begin(t, "POST", `{"jsonrpc":"2.0","id":44,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"first"}}}`, "")
	f.allow(t, call.ID)
	if terminal := f.join(t, call); terminal.Outcome != nativemcp.OutcomeCompleted {
		t.Fatal(terminal)
	}
	old, err := repo.Get(t.Context(), call.ID)
	if err != nil || old.ApprovalID == nil {
		t.Fatalf("missing exact old approval: %+v %v", old, err)
	}
	oldApproval, err := repo.GetApproval(t.Context(), *old.ApprovalID)
	if err != nil || oldApproval.ConsumedAt == nil {
		t.Fatalf("old allowance was not consumed: %+v %v", oldApproval, err)
	}

	counts := f.counts()
	receipt, err := repo.Start(t.Context(), call.ID, "recovery-old-operation-replay", scope.Revision, time.Now().UTC())
	if !errors.Is(err, nativetool.ErrReplay) || receipt != (nativetool.StartReceipt{}) {
		t.Fatalf("consumed native operation was replayed: receipt=%+v err=%v", receipt, err)
	}
	if after, err := repo.Get(t.Context(), call.ID); err != nil || !reflect.DeepEqual(old, after) || f.counts() != counts {
		t.Fatalf("rejected replay changed operation or transport: before=%+v after=%+v err=%v", old, after, err)
	}
	fresh := f.begin(t, "POST", `{"jsonrpc":"2.0","id":45,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"fresh"}}}`, "")
	var pending nativetool.Approval
	runtimeNativeObserve(t, "fresh recovery decision", func(ctx context.Context) bool {
		op, err := repo.Get(ctx, fresh.ID)
		if err != nil || op.ApprovalID == nil {
			return false
		}
		pending, err = repo.GetApproval(ctx, *op.ApprovalID)
		return err == nil && pending.State == "pending"
	}, func() any { return pending })
	if pending.ID == oldApproval.ID || pending.OperationID != fresh.ID || f.counts() != counts {
		t.Fatalf("fresh operation borrowed prior native authority: pending=%+v counts=%+v/%+v", pending, f.counts(), counts)
	}
	if _, err = repo.DecideApproval(t.Context(), nativetool.DecisionInput{ApprovalID: oldApproval.ID, ExpectedRevision: 1, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "native-fixture-allow-" + call.ID.String(), ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	f.assertNoStart(t, fresh.ID)
	if f.counts() != counts {
		t.Fatal("old decision replay admitted a fresh native request")
	}
	f.allow(t, fresh.ID)
	if terminal := f.join(t, fresh); terminal.Outcome != nativemcp.OutcomeCompleted {
		t.Fatal(terminal)
	}
	if f.counts().HTTP != counts.HTTP+1 {
		t.Fatal("fresh decision did not release exactly one native request")
	}
	scope, err = repo.GetScope(t.Context(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.Revoke(t.Context(), f.capture, scope.Revision, "revoked", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	revoked, err := repo.GetScope(t.Context(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	if err = preparer.PrepareNativeRecovery(t.Context(), view); err == nil {
		t.Fatal("native recovery reopened revoked authority for a live Capture")
	}
	if after, err := repo.GetScope(t.Context(), f.capture); err != nil || !reflect.DeepEqual(revoked, after) {
		t.Fatalf("failed preparation changed revoked native authority: before=%+v after=%+v err=%v", revoked, after, err)
	}

	if err = f.r.CaptureRuns().Finish(t.Context(), grant.Run.ID, grant.ControlCapability); err != nil {
		t.Fatal(err)
	}
	closed, err := repo.GetScope(t.Context(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	if err = preparer.PrepareNativeRecovery(t.Context(), view); err == nil {
		t.Fatal("native recovery acknowledged a finished Capture")
	}
	if after, err := repo.GetScope(t.Context(), f.capture); err != nil || !reflect.DeepEqual(closed, after) {
		t.Fatalf("failed preparation reopened terminal native authority: before=%+v after=%+v err=%v", closed, after, err)
	}
}
