package offlinehold

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
)

func TestOriginalCleartextProbeKindsRemainScoped(t *testing.T) {
	target := ProbeTarget{Transport: ProbeTransportPrivateCleartext, TargetRef: "http://alias.test:8080", NetworkOrigin: "http://alias.test:8080", HTTPAuthority: "alias.test:8080", EgressPolicy: egressnetwork.DefaultPolicy()}
	for _, kind := range []EgressKind{EgressProvider, EgressOpaque, EgressAuxiliary, EgressUpdate, EgressBlindTunnel, EgressDatagram} {
		target.Kind = kind
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
