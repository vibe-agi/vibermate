package nativemcp

import (
	"context"
	"reflect"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

func TestNativeProbeRetainsFrozenPolicyAndOriginOnlyIdentity(t *testing.T) {
	f := newNativeFixtureWithChoice(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, 0, egressprofile.Inherit())
	profiles, e := egressprofile.NewManager(f.store.EgressProfileRepository(), nil)
	if e != nil {
		t.Fatal(e)
	}
	policy := egressnetwork.DefaultPolicy()
	policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:23456"}
	p, e := profiles.Publish(t.Context(), egressprofile.PublishCommand{ID: "profile.native-probe", DisplayName: "Native probe", Policy: policy})
	if e != nil {
		t.Fatal(e)
	}
	defaults, e := egressprofile.NewDefaultManager(f.store.EgressDefaultRepository(), profiles)
	if e != nil {
		t.Fatal(e)
	}
	ex := f.executor.(*executor)
	ex.dependencies.EgressDefaults = defaults
	f.handshake(t)
	if _, e = defaults.SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: p.ID, Revision: p.Revision}}); e != nil {
		t.Fatal(e)
	}
	body := `{"jsonrpc":"2.0","id":33,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`
	c := f.captured(t, "POST", body, "synthetic-session")
	defer c.lease.Release()
	ingress, _ := c.lease.Plan().ClientToolPlan()
	rpc, e := parseRequest([]byte(body), ex.limits)
	if e != nil {
		t.Fatal(e)
	}
	session, e := ex.admitSession(context.Background(), c, rpc, ingress)
	if e != nil {
		t.Fatal(e)
	}
	plan, e := ex.operationPlan(t.Context(), rpc, session)
	if e != nil {
		t.Fatal(e)
	}
	binding, _, _, e := ex.bindRequest(c, rpc, session, plan)
	if e != nil {
		t.Fatal(e)
	}
	// A later default edit cannot change the already frozen binding's probe policy.
	if _, e = defaults.SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 2}); e != nil {
		t.Fatal(e)
	}
	got := nativeProbe(binding.Plan())
	want := offlinehold.ProbeTarget{Kind: offlinehold.EgressAuxiliary, Transport: offlinehold.ProbeTransportStrictTLS, TargetRef: "https://native.example", NetworkOrigin: "https://native.example", HTTPAuthority: "native.example", TLSServerName: "native.example", EgressPolicy: policy}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native frozen reachability target got=%+v want=%+v", got, want)
	}
	if e = got.Validate(); e != nil {
		t.Fatal(e)
	}
}

func TestNativeProbeRejectsUnresolvedPlan(t *testing.T) {
	f := newNativeFixtureWithChoice(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, 0, egressprofile.Inherit())
	c := f.captured(t, "POST", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, "")
	defer c.lease.Release()
	unresolved, _ := c.lease.Plan().ClientToolPlan()
	for _, plan := range []environment.ClientToolPlan{{}, unresolved} {
		got := nativeProbe(plan)
		if !reflect.DeepEqual(got, offlinehold.ProbeTarget{}) || got.Validate() == nil {
			t.Fatalf("unresolved native plan produced valid reachability %+v", got)
		}
	}
}
