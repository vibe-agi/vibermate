package loopbackproxy_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/certidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

type nativeFixtureRevoker struct {
	executor nativemcp.Executor
	before   func(context.Context, captureidentity.Reference, string) error
}

func (r *nativeFixtureRevoker) Revoke(ctx context.Context, capture captureidentity.Reference, reason string) error {
	if r.before != nil {
		if err := r.before(ctx, capture, reason); err != nil {
			return err
		}
	}
	if r.executor == nil {
		return nil
	}
	return r.executor.Revoke(ctx, capture, nativemcp.RevocationReason(reason))
}

func TestNativeAssignmentRevokesOutsideMutexBeforePublication(t *testing.T) {
	revoker := &nativeFixtureRevoker{}
	f := newNativeFixtureMode(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, &nativeHandlerSettings{revocation: revoker})
	revoker.executor = f.executor
	secured := f.proxy.ConnectTLS(t, f.credential, "native.example:443", "native.example")
	defer secured.Close()
	response := nativeRequest(t, secured, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"codex-mcp-client","version":"0.160.0"}}}`, "")
	_, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("initialize failed")
	}
	origin, _ := originidentity.ParseProviderOrigin("https://native.example")
	connection, err := f.assignments.RegisterProviderConnection(context.Background(), f.capture, "revocation-proof", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	old, err := f.assignments.Resolve(context.Background(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.environments.Resolve(old.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	next := snapshot.Aggregate()
	next.Revision++
	next.Name = "Applied native policy"
	draft, err := f.environments.SaveDraft(context.Background(), environment.DraftCommand{Candidate: next, ExpectedBaseRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.environments.Preview(context.Background(), next.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.environments.Publish(context.Background(), preview); err != nil {
		t.Fatal(err)
	}
	called := false
	revoker.before = func(ctx context.Context, c captureidentity.Reference, reason string) error {
		if reason != "assignment_applied" {
			return nil
		}
		current, err := f.assignments.Resolve(ctx, c)
		if err != nil {
			return err
		}
		if current.Revision != 1 {
			return errors.New("new assignment published before native revoke")
		}
		lease, err := f.assignments.BeginRequest(ctx, c, "revocation-proof", environment.RequestFacts{})
		if lease != nil {
			lease.Release()
		}
		if !errors.Is(err, captureassignment.ErrOperationInProgress) {
			return errors.New("transition admitted old request")
		}
		called = true
		return nil
	}
	if _, err = f.assignments.ApplyLatest(context.Background(), f.capture, 1); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("actual native revocation hook missing")
	}
	scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	if err != nil || scope.State != "revoked" {
		t.Fatalf("durable revoked scope %+v %v", scope, err)
	}
	revoker.before = nil
}

func TestNativeLeafAdmissionIsOneUseAndRetiresOnConnectionClose(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	origin, _ := originidentity.ParseProviderOrigin("https://native.example")
	lease, err := f.assignments.RegisterProviderConnection(context.Background(), f.capture, "native-leaf", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	wrong, _ := certidentity.NewDNSName("wrong.example")
	if _, err := lease.AdmitLeaf(context.Background(), f.proxy.authority.Identity().Revision(), wrong, certidentity.LeafKeyAlgorithmECDSAP256); !errors.Is(err, captureassignment.ErrLeafIssuanceUnauthorized) {
		t.Fatalf("wrong SAN: %v", err)
	}
	san, _ := certidentity.NewDNSName("native.example")
	admission, err := lease.AdmitLeaf(context.Background(), f.proxy.authority.Identity().Revision(), san, certidentity.LeafKeyAlgorithmECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	copied := admission
	request, err := admission.ClaimForIssuance()
	if err != nil {
		t.Fatal(err)
	}
	if request.ConnectionMode() != environment.ConnectionModeClientTools || request.ClientEndpointID() != "" || request.ClientEndpointRevision() != 0 || request.NativeCompatibilityDigest() == (environment.ConnectionCompatibilityDigest{}) {
		t.Fatal("native leaf fabricated semantic identity")
	}
	if _, err := copied.ClaimForIssuance(); !errors.Is(err, captureassignment.ErrLeafAdmissionConsumed) {
		t.Fatal("copied native leaf admission replayed")
	}
	lease.Close()
	if admission.Cacheable() {
		t.Fatal("closed native connection retained cache publication authority")
	}
}

func TestNativeLeafAdmissionRetiresOnUnchangedRouteApplyLatest(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	origin, _ := originidentity.ParseProviderOrigin("https://native.example")
	lease, err := f.assignments.RegisterProviderConnection(context.Background(), f.capture, "native-late-leaf", origin)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	san, _ := certidentity.NewDNSName("native.example")
	admission, err := lease.AdmitLeaf(context.Background(), f.proxy.authority.Identity().Revision(), san, certidentity.LeafKeyAlgorithmECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	if !admission.Cacheable() {
		t.Fatal("current native leaf cannot publish")
	}
	snapshot, err := f.environments.Resolve("native.synthetic")
	if err != nil {
		t.Fatal(err)
	}
	next := snapshot.Aggregate()
	next.Revision++
	next.Name = "Updated leaf owner"
	draft, err := f.environments.SaveDraft(context.Background(), environment.DraftCommand{Candidate: next, ExpectedBaseRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.environments.Preview(context.Background(), next.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.environments.Publish(context.Background(), preview); err != nil {
		t.Fatal(err)
	}
	if _, err = f.assignments.ApplyLatest(context.Background(), f.capture, 1); err != nil {
		t.Fatal(err)
	}
	if admission.Cacheable() {
		t.Fatal("late native worker can publish an obsolete assignment leaf")
	}
}
