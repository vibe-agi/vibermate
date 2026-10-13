package productruntime

import (
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"testing"
)

func TestPacketResumeRetainsDistinctImmutableCredentialAndDomainRules(t *testing.T) {
	gate := newEnteredOfflineGate(t, "packet-identities")
	one := offlinehold.ProbeTarget{Kind: offlinehold.EgressDatagram, Transport: offlinehold.ProbeTransportUDPSetup, TargetRef: "packet.fixture:123", NetworkOrigin: "packet.fixture:123", TargetAccess: egressnetwork.TargetAccessPublicOnly, EgressPolicy: egressnetwork.Policy{Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxyVLESS, CredentialRef: "secret://egress-vless/one", CredentialRevision: 1}}}
	two := one
	two.EgressPolicy.Proxy.CredentialRef = "secret://egress-vless/two"
	two.EgressPolicy.DirectDomains = "direct.fixture"
	a, _ := gate.BeginAction(t.Context(), offlinehold.ActionRequest{ActionID: "packet-one"})
	b, _ := gate.BeginAction(t.Context(), offlinehold.ActionRequest{ActionID: "packet-two"})
	first := acquireHeld(t, gate, a, "packet-one", one)
	second := acquireHeld(t, gate, b, "packet-two", two)
	defer func() {
		gate.BeginShutdown()
		releaseAcquiredAfterShutdown(t, <-first, a)
		releaseAcquiredAfterShutdown(t, <-second, b)
	}()
	waitForQueuedTargets(t, gate, 2)
	r := &Runtime{offlineHold: gate}
	targets, e := r.resumeProbeTargets()
	if e != nil || len(targets) != 2 {
		t.Fatal("different frozen packet exits merged at resume", len(targets), e)
	}
}
