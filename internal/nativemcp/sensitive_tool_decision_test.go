package nativemcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func TestNativeSensitiveToolEvidenceRecheckedByGateAndAuthority(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.executor.(*executor).dependencies.ContentProfiles = syntheticProfiles{argumentPaths: []string{"/proof"}}
	f.handshake(t)
	body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a","proof":"synthetic private proof"}}}`
	c := f.captured(t, "POST", body, "synthetic-session")
	defer c.lease.Release()
	e := f.executor.(*executor)
	r, err := parseRequest([]byte(body), e.limits)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := c.lease.Plan().ClientToolPlan()
	s, err := e.admitSession(context.Background(), c, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	binding, intents, _, err := e.bindRequest(c, r, s)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	witness := *s.tools[c.id]
	s.mu.Unlock()
	policy, _ := exchange.NewToolDecisionContext(plan.PolicySet(), "", false, nil, nil)
	if _, err = exchange.NewClientToolDecisionRequest(c.id, binding, policy, intents, nil); err == nil {
		t.Fatal("ordinary constructor admitted known sensitive group")
	}
	if _, err = exchange.NewSensitiveClientToolDecisionRequest(c.id, binding, policy, intents, nativetool.SensitiveToolWitness{}); err == nil {
		t.Fatal("missing sensitive witness admitted")
	}
	changed := append([]protocolcore.ToolIntent{}, intents...)
	changed[0].Call.Arguments, _ = protocolcore.NewJSONObject([]byte(`{"path":"other","proof":"different"}`), 1024)
	if _, err = exchange.NewSensitiveClientToolDecisionRequest(c.id, binding, policy, changed, witness); err == nil {
		t.Fatal("substituted exact group admitted")
	}
	request, err := exchange.NewSensitiveClientToolDecisionRequest(c.id, binding, policy, intents, witness)
	if err != nil || request.VerifyNativeEvidence() != nil {
		t.Fatalf("valid evidence %v", err)
	}
	ctx := context.Background()
	rpcID, _ := binding.RPCIDDigest()
	digest := binding.RequestDigest()
	sid := binding.SessionID()
	if _, err = e.dependencies.Store.Receive(ctx, nativetool.ReceiveInput{ID: c.id, Capture: f.capture, EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "sensitive-witness-consumer", TargetOrigin: plan.Route().TargetOrigin.String(), Method: "POST", Kind: binding.Kind(), SessionID: &sid, RPCIDDigest: &rpcID, RequestDigest: &digest, Recording: plan.ContentRecording(), ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err = e.dependencies.Store.Bind(ctx, c.id, binding, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	decisions := make(chan error, 1)
	go func() { _, err := e.dependencies.ToolDecisions.Decide(ctx, request); decisions <- err }()
	f.approve(t, c.id)
	if err = <-decisions; err != nil {
		t.Fatal(err)
	}
	attempt, _ := f.ids.NewNativeOperationID(ctx)
	receipt, err := e.dependencies.Store.Start(ctx, c.id, attempt.String(), s.owner.revision, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	outgoing, err := outbound(ctx, c.request, r.raw, plan)
	if err != nil {
		t.Fatal(err)
	}
	sent := e.sendCommitted(ctx, outgoing, plan, receipt)
	response, err := sent.response, sent.err
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if err = e.dependencies.Store.Finish(ctx, c.id, nativetool.TerminalInput{Outcome: "completed", Reason: "completed", BytesOut: int64(len(r.raw)), ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err = s.hasher.RetireToolCall(witness); err != nil {
		t.Fatal(err)
	}
	if request.VerifyNativeEvidence() == nil {
		t.Fatal("cached union accepted retired witness")
	}
	options := toolapproval.DefaultOptions(f.store.ToolApprovalRepository())
	options.NativeRepository = f.store.NativeToolRepository()
	options.Config.DecisionTimeout = time.Second
	authority, err := toolapproval.New(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = e.dependencies.ToolDecisions.Decide(context.Background(), request); !errors.Is(err, nativetool.ErrDenied) || errors.Is(err, nativetool.ErrUnavailable) {
		t.Fatal("Gate failed to recheck retired witness")
	}
	if _, err = authority.Decide(context.Background(), request); !errors.Is(err, nativetool.ErrDenied) || errors.Is(err, nativetool.ErrUnavailable) {
		t.Fatal("Authority failed to recheck retired witness")
	}
	var questions int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_approvals`).Scan(&questions); err != nil || questions != 1 {
		t.Fatalf("invalid evidence opened question%d %v", questions, err)
	}
	if f.transport.sends() != 4 {
		t.Fatal("invalid evidence reached low-level transport")
	}
}
