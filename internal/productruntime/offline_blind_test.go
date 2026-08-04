package productruntime

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/vibe-agi/vibermate/internal/blindtunnel"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

// These reuse offline_test.go's recordingProber, which records the targets it
// was handed. "Was this arm taken" is therefore len(Targets()) > 0.

func blindProbeTarget(t *testing.T, authority string) offlinehold.ProbeTarget {
	t.Helper()

	target := offlinehold.ProbeTarget{
		Kind:          offlinehold.EgressBlindTunnel,
		Transport:     offlinehold.ProbeTransportTCP,
		TargetRef:     authority,
		NetworkOrigin: authority,
		HTTPAuthority: authority,
	}
	if err := target.Validate(); err != nil {
		t.Fatalf("the fixture target is not a valid blind target: %v", err)
	}
	return target
}

// The defect this closes: a queued blind tunnel had no arm in the probe
// router, so resume failed the whole target set and released nothing —
// including every provider request waiting behind it. A CONNECT arriving after
// Enter is queued like anything else, so this is one package install away on
// any real machine.
func TestAQueuedBlindTunnelDoesNotFailTheWholeProbeSet(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = connection.Close()
		}
	}()

	provider := &recordingProber{}
	original := &recordingProber{}
	blind, err := blindtunnel.NewReachabilityProber()
	if err != nil {
		t.Fatal(err)
	}
	prober, err := newRuntimeResumeProber(provider, original, blind)
	if err != nil {
		t.Fatal(err)
	}
	if err := prober.Probe(context.Background(), offlinehold.ProbeRequest{
		Targets: []offlinehold.ProbeTarget{
			blindProbeTarget(t, listener.Addr().String()),
		},
	}); err != nil {
		t.Fatalf("a reachable blind tunnel failed the probe set: %v", err)
	}
	if len(provider.Targets()) != 0 || len(original.Targets()) != 0 {
		t.Fatal("a blind target was routed to a prober that speaks a protocol")
	}
}

// An unreachable blind target must still fail, and must fail as a transport
// condition rather than as a programming error, because that is what returns
// the gate to `held` with a reason a person can act on.
func TestAnUnreachableBlindTunnelFailsAsATransportCondition(t *testing.T) {
	t.Parallel()

	// Port 1 on loopback refuses immediately on every platform this runs on.
	blind, err := blindtunnel.NewReachabilityProber()
	if err != nil {
		t.Fatal(err)
	}
	prober, err := newRuntimeResumeProber(
		&recordingProber{},
		&recordingProber{},
		blind,
	)
	if err != nil {
		t.Fatal(err)
	}
	err = prober.Probe(context.Background(), offlinehold.ProbeRequest{
		Targets: []offlinehold.ProbeTarget{blindProbeTarget(t, "127.0.0.1:1")},
	})
	if err == nil {
		t.Fatal("an unreachable blind tunnel passed the probe")
	}
	var failure *offlinehold.ProbeFailure
	if !errors.As(err, &failure) {
		t.Fatalf("the failure is not a typed probe failure: %v", err)
	}
	if failure.Reason != offlinehold.ProbeReasonTransportUnavailable {
		t.Fatalf("reason is %q, want a transport condition", failure.Reason)
	}
}

// Each kind still reaches the prober that belongs to it. Routing a blind
// target to the TLS prober would claim an identity a tunnel cannot have.
func TestEachEgressKindReachesItsOwnProber(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		kind offlinehold.EgressKind
		want string
	}{
		{"provider", offlinehold.EgressProvider, "provider"},
		{"opaque", offlinehold.EgressOpaque, "original"},
		{"auxiliary", offlinehold.EgressAuxiliary, "original"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			provider := &recordingProber{}
			original := &recordingProber{}
			blind := &recordingProber{}
			prober, err := newRuntimeResumeProber(provider, original, blind)
			if err != nil {
				t.Fatal(err)
			}
			// The router reads only Kind, so a minimally shaped target is
			// enough to observe which arm it takes.
			_ = prober.Probe(context.Background(), offlinehold.ProbeRequest{
				Targets: []offlinehold.ProbeTarget{{Kind: testCase.kind}},
			})
			got := map[string]bool{
				"provider": len(provider.Targets()) != 0,
				"original": len(original.Targets()) != 0,
				"blind":    len(blind.Targets()) != 0,
			}
			if !got[testCase.want] {
				t.Fatalf("%s was not routed to the %s prober", testCase.name, testCase.want)
			}
			for name, called := range got {
				if name != testCase.want && called {
					t.Fatalf("%s also reached the %s prober", testCase.name, name)
				}
			}
		})
	}
}

// A kind with a Hold class but no subsystem that can queue one is a
// programming error, not a network condition, and the reason code says so.
func TestAKindWithNoProberIsAProgrammingError(t *testing.T) {
	t.Parallel()

	blind, err := blindtunnel.NewReachabilityProber()
	if err != nil {
		t.Fatal(err)
	}
	prober, err := newRuntimeResumeProber(
		&recordingProber{},
		&recordingProber{},
		blind,
	)
	if err != nil {
		t.Fatal(err)
	}
	err = prober.Probe(context.Background(), offlinehold.ProbeRequest{
		Targets: []offlinehold.ProbeTarget{{Kind: offlinehold.EgressUpdate}},
	})
	var failure *offlinehold.ProbeFailure
	if !errors.As(err, &failure) ||
		failure.Reason != offlinehold.ProbeReasonFailed {
		t.Fatalf("an unroutable kind did not fail as a probe error: %v", err)
	}
}
