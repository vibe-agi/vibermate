package offlinehold

import (
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"testing"
)

func TestDatagramProbeHasOnlySetupIdentity(t *testing.T) {
	target := ProbeTarget{Kind: EgressDatagram, Transport: ProbeTransportUDPSetup, TargetRef: "packet.fixture:123", NetworkOrigin: "packet.fixture:123", TargetAccess: egressnetwork.TargetAccessPublicOnly}
	if e := target.Validate(); e != nil {
		t.Fatal("packet setup rejected", e)
	}
	invalid := []ProbeTarget{}
	for _, transport := range []ProbeTransportKind{ProbeTransportTCP, ProbeTransportStrictTLS, ProbeTransportPrivateCleartext} {
		c := target
		c.Transport = transport
		c.TargetAccess = 0
		c.HTTPAuthority = "packet.fixture:123"
		c.TLSServerName = "packet.fixture"
		invalid = append(invalid, c)
	}
	c := target
	c.HTTPAuthority = "packet.fixture:123"
	invalid = append(invalid, c)
	c = target
	c.Kind = EgressProvider
	invalid = append(invalid, c)
	for _, candidate := range invalid {
		if e := candidate.Validate(); e == nil {
			t.Fatal("datagram manufactured nonpacket identity", candidate.Transport)
		}
	}
}
