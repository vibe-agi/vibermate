package offlinehold

import (
	"context"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
)

func blindAccessTarget(access egressnetwork.TargetAccess) ProbeTarget {
	return ProbeTarget{Kind: EgressBlindTunnel, Transport: ProbeTransportTCP, TargetRef: "blind.fixture:443",
		NetworkOrigin: "blind.fixture:443", HTTPAuthority: "blind.fixture:443", TargetAccess: access}
}

func TestBlindTargetAccessValidationAndIdentity(t *testing.T) {
	for _, access := range []egressnetwork.TargetAccess{0, 99} {
		if blindAccessTarget(access).Validate() == nil {
			t.Errorf("invalid allowance %d accepted", access)
		}
	}
	provider := providerTarget("provider")
	provider.TargetAccess = egressnetwork.TargetAccessOwnerConfigured
	if provider.Validate() == nil {
		t.Error("provider accepted blind allowance")
	}
	public, owner := blindAccessTarget(egressnetwork.TargetAccessPublicOnly), blindAccessTarget(egressnetwork.TargetAccessOwnerConfigured)
	if probeTargetSortKey(public) == probeTargetSortKey(owner) {
		t.Error("different allowances have identical sort identity")
	}
	if values, err := normalizeProbeTargets([]ProbeTarget{public, owner}); err != nil || len(values) != 2 {
		t.Fatalf("distinct target normalization: %v %v", values, err)
	}
}

func TestPendingBlindTargetsPreserveDistinctAllowances(t *testing.T) {
	gate := startTestGate(t, DefaultConfig())
	if _, err := gate.Enter(context.Background(), gate.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	results := make(chan acquireResult, 2)
	requests := []AcquireRequest{acquireRequest(t, gate, "public", "blind.fixture:443", 0), acquireRequest(t, gate, "owner", "blind.fixture:443", 0)}
	for i := range requests {
		requests[i].Target = blindAccessTarget(egressnetwork.TargetAccess(i + 1))
		go acquireAsync(gate, requests[i], results)
	}
	defer func() {
		gate.BeginShutdown()
		for range requests {
			result := <-results
			if result.lease != nil {
				result.lease.Release()
			}
		}
		for _, request := range requests {
			request.Action.Release()
		}
	}()
	waitForQueue(t, gate, 2)
	targets := gate.PendingProbeTargets()
	if len(targets) != 2 || targets[0].TargetAccess != egressnetwork.TargetAccessPublicOnly || targets[1].TargetAccess != egressnetwork.TargetAccessOwnerConfigured {
		t.Fatalf("pending allowances lost: %+v", targets)
	}
}
