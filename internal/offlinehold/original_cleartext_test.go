package offlinehold

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
)

func TestOriginalCleartextProbeKindsRemainScoped(t *testing.T) {
	target := ProbeTarget{Transport: ProbeTransportPrivateCleartext, TargetRef: "http://alias.test:8080", NetworkOrigin: "http://alias.test:8080", HTTPAuthority: "alias.test:8080", EgressPolicy: egressnetwork.DefaultPolicy()}
	for _, kind := range []EgressKind{EgressProvider, EgressOpaque, EgressAuxiliary, EgressUpdate, EgressBlindTunnel, EgressDatagram} {
		target.Kind = kind
		target.TargetAccess = 0
		if kind == EgressOpaque || kind == EgressAuxiliary {
			target.TargetAccess = egressnetwork.TargetAccessOwnerConfigured
		}
		allowed := kind == EgressProvider || kind == EgressOpaque || kind == EgressAuxiliary
		if err := target.Validate(); (err == nil) != allowed {
			t.Fatalf("cleartext kind %s allowed=%v err=%v", kind, allowed, err)
		}
	}
	target.Kind = EgressOpaque
	target.TLSServerName = "alias.test"
	if target.Validate() == nil {
		t.Fatal("cleartext probe claimed TLS identity")
	}
}

func TestOriginalProbeTargetAccessValidation(t *testing.T) {
	for _, kind := range []EgressKind{EgressOpaque, EgressAuxiliary} {
		for _, transport := range []ProbeTransportKind{ProbeTransportStrictTLS, ProbeTransportLoopbackCleartext, ProbeTransportPrivateCleartext} {
			target := ProbeTarget{Kind: kind, Transport: transport, TargetRef: "alias", NetworkOrigin: "alias", HTTPAuthority: "alias", EgressPolicy: egressnetwork.DefaultPolicy()}
			if transport == ProbeTransportStrictTLS {
				target.TLSServerName = "alias"
			}
			for _, access := range []egressnetwork.TargetAccess{0, egressnetwork.TargetAccessPublicOnly, egressnetwork.TargetAccessOwnerConfigured, 99} {
				target.TargetAccess = access
				allowed := access == egressnetwork.TargetAccessPublicOnly || access == egressnetwork.TargetAccessOwnerConfigured || (access == 0 && transport == ProbeTransportStrictTLS)
				if err := target.Validate(); (err == nil) != allowed {
					t.Fatalf("kind=%s transport=%s access=%d err=%v", kind, transport, access, err)
				}
			}
		}
	}
	for _, kind := range []EgressKind{EgressProvider, EgressUpdate, EgressPlugin} {
		target := ProbeTarget{Kind: kind, Transport: ProbeTransportStrictTLS, TargetRef: "alias", NetworkOrigin: "alias", HTTPAuthority: "alias", TLSServerName: "alias", TargetAccess: egressnetwork.TargetAccessOwnerConfigured}
		if target.Validate() == nil {
			t.Fatalf("unrelated kind %s gained original access", kind)
		}
	}
}
