package nativemcp

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func prepareRealReceipt(t *testing.T, f *nativeFixture, wireOverride ...string) (nativetool.StartReceipt, *http.Request, environment.ClientToolPlan) {
	t.Helper()
	wire := "synthetic-session"
	if len(wireOverride) > 0 {
		wire = wireOverride[0]
	}
	body := `{"jsonrpc":"2.0","id":33,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"callId":"receipt-call-` + wire + `"}}}`
	c := f.captured(t, "POST", body, wire)
	defer c.lease.Release()
	e := f.executor.(*executor)
	plan, _ := c.lease.Plan().ClientToolPlan()
	r, err := parseRequest([]byte(body), e.limits)
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.admitSession(context.Background(), c, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	binding, intents, _, err := e.bindRequest(c, r, s)
	if err != nil {
		t.Fatal(err)
	}
	rpcID, _ := binding.RPCIDDigest()
	digest := binding.RequestDigest()
	sid := binding.SessionID()
	ctx := context.Background()
	if _, err = e.dependencies.Store.Receive(ctx, nativetool.ReceiveInput{ID: c.id, Capture: f.capture, EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "real-native-receipt", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "POST", Kind: binding.Kind(), SessionID: &sid, RPCIDDigest: &rpcID, RequestDigest: &digest, Recording: plan.ContentRecording(), ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err = e.dependencies.Store.Bind(ctx, c.id, binding, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	policy, _ := exchange.NewToolDecisionContext(plan.PolicySet(), "", false, nil, nil)
	decision, _ := exchange.NewClientToolDecisionRequest(c.id, binding, policy, intents, nil)
	errors := make(chan error, 1)
	go func() { _, err := e.dependencies.ToolDecisions.Decide(ctx, decision); errors <- err }()
	f.approve(t, c.id)
	if err = <-errors; err != nil {
		t.Fatal(err)
	}
	attempt, _ := f.ids.NewNativeOperationID(ctx)
	receipt, err := e.dependencies.Store.Start(ctx, c.id, attempt.String(), s.owner.revision, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	request, err := outbound(ctx, c.request, r.raw, plan)
	if err != nil {
		t.Fatal(err)
	}
	return receipt, request, plan
}
func TestNativeSendRequiresSameStoreCommittedReceipt(t *testing.T) {
	for _, condition := range []string{"committed", "zero", "public_issuer", "foreign_store", "revoked", "expired", "recovered", "closed"} {
		t.Run(condition, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			f.handshake(t)
			receipt, request, plan := prepareRealReceipt(t, f)
			e := f.executor.(*executor)
			switch condition {
			case "zero":
				receipt = nativetool.StartReceipt{}
			case "public_issuer":
				issuer, _ := nativetool.NewStartReceiptIssuer()
				op, _ := f.store.NativeToolRepository().Get(context.Background(), receipt.OperationID())
				scope, _ := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
				receipt, _ = issuer.Issue(nativetool.StartFacts{OperationID: receipt.OperationID(), AttemptID: receipt.AttemptID(), AuthorityRevision: scope.Revision, OwnerEpoch: scope.OwnerEpoch, BindingDigest: *op.BindingDigest})
			case "foreign_store":
				other, err := runtimepersistence.Open(context.Background(), runtimepersistence.Options{DatabasePath: f.storePath(t), BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
				if err != nil {
					t.Fatal(err)
				}
				defer other.Shutdown(context.Background())
				e.dependencies.Store = other.NativeToolRepository()
			case "revoked":
				if err := e.Revoke(context.Background(), f.capture, RevocationPermissionOff); err != nil {
					t.Fatal(err)
				}
			case "expired":
				e.dependencies.Clock = func() time.Time { return time.Now().Add(time.Hour) }
			case "recovered":
				if err := e.dependencies.Store.Recover(context.Background(), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			case "closed":
				if err := f.store.Shutdown(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			before := f.transport.sends()
			response, err := e.sendCommitted(context.Background(), request, plan, receipt)
			if response != nil {
				response.Body.Close()
			}
			if condition == "committed" {
				if err != nil || f.transport.sends() != before+1 {
					t.Fatalf("committed receipt %v sends%d", err, f.transport.sends())
				}
			} else if err == nil || f.transport.sends() != before {
				t.Fatalf("%s receipt sent: %v sends%d", condition, err, f.transport.sends())
			}
			response, err = e.sendCommitted(context.Background(), request, plan, receipt)
			if response != nil {
				response.Body.Close()
			}
			if err == nil || f.transport.sends() > before+1 {
				t.Fatal("copied receipt sent twice")
			}
		})
	}
}
func (f *nativeFixture) storePath(t *testing.T) string {
	t.Helper()
	rows, err := f.db.Query(`PRAGMA database_list`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var name, path string
		if err = rows.Scan(&seq, &name, &path); err != nil {
			t.Fatal(err)
		}
		if name == "main" {
			return path
		}
	}
	t.Fatal("main Store path absent")
	return ""
}
