package offlinehold

import "testing"

// A blind tunnel forwards bytes it never interprets, so its resume probe can
// only be a TCP connect: there is no TLS server name to verify and no protocol
// to speak. It still has to pass through the same admission as every other
// outbound, so the taxonomy needs the raw-TCP kind rather than an exception.
func TestBlindTunnelUsesARawTCPProbeTarget(t *testing.T) {
	t.Parallel()

	target := ProbeTarget{
		Kind:          EgressBlindTunnel,
		Transport:     ProbeTransportTCP,
		TargetRef:     "files.example.com:443",
		NetworkOrigin: "files.example.com:443",
		HTTPAuthority: "files.example.com:443",
	}
	if err := target.Validate(); err != nil {
		t.Fatalf("raw TCP blind tunnel target was rejected: %v", err)
	}
}

// A raw TCP probe verifies nothing about the peer's identity, so it may not
// claim one.
func TestRawTCPProbeCannotClaimATLSServerName(t *testing.T) {
	t.Parallel()

	target := ProbeTarget{
		Kind:          EgressBlindTunnel,
		Transport:     ProbeTransportTCP,
		TargetRef:     "files.example.com:443",
		NetworkOrigin: "files.example.com:443",
		HTTPAuthority: "files.example.com:443",
		TLSServerName: "files.example.com",
	}
	if err := target.Validate(); err == nil {
		t.Fatal("a raw TCP probe claimed a verified TLS server name")
	}
}

// The raw TCP probe belongs to blind tunnelling; a provider or opaque outbound
// must still prove its TLS identity.
func TestRawTCPProbeIsLimitedToBlindTunnels(t *testing.T) {
	t.Parallel()

	for _, kind := range []EgressKind{
		EgressProvider,
		EgressOpaque,
		EgressAuxiliary,
		EgressUpdate,
	} {
		target := ProbeTarget{
			Kind:          kind,
			Transport:     ProbeTransportTCP,
			TargetRef:     "files.example.com:443",
			NetworkOrigin: "files.example.com:443",
			HTTPAuthority: "files.example.com:443",
		}
		if err := target.Validate(); err == nil {
			t.Fatalf("%q was allowed to skip TLS verification", kind)
		}
	}
}
