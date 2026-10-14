package captureassignment

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

type leaseEgressResolver struct {
	value egressprofile.Resolved
	err   error
	reads int
}

// A managed model account does not own the caller's startup probes.
// This bodyless original-credential operation still needs one frozen exit.
func TestManagedOpaqueRequestFreezesEgressAndReleasesFailedLease(t *testing.T) {
	policy := environmentFixture(t, "managed-opaque", "adapter.shared")
	protocol := &policy.ClientEndpoints[0].ProtocolPlans[0]
	protocol.EgressProfile = egressprofile.Inherit()
	base := newTestManager(t, newMemoryRepository(), newRevisionResolver(t, policy))
	resolved, err := egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionBuiltin, 3)
	if err != nil {
		t.Fatal(err)
	}
	defaults := &leaseEgressResolver{value: resolved}
	manager, err := NewManager(Options{Repository: base.repository, Environments: base.environments, Activity: base.activity, Clock: base.clock, EgressDefaults: defaults})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(t.Context(), CreateCommand{Capture: testCapture(), EnvironmentID: policy.ID, Source: SourceLaunch}); err != nil {
		t.Fatal(err)
	}
	connection, err := manager.RegisterConnection(t.Context(), testCapture(), "egress.managed-opaque", semanticOrigin(t))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	facts := semanticRequestFacts()
	facts.Target.Method, facts.Target.Path = "GET", "/api/hello"
	lease, err := manager.BeginRequest(t.Context(), testCapture(), "egress.managed-opaque", facts)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	plan := lease.Plan()
	if plan.Operation().Kind() != protocolspec.ClientOperationOpaque || !plan.UsesUpstreamDestination() || plan.UsesOriginalAccount() {
		t.Fatal("fixture did not reach original forwarding under a managed model route")
	}
	got, err := plan.ResolvedEgress()
	if err != nil || got.DefaultRevision() != 3 || defaults.reads != 1 {
		t.Fatalf("managed opaque request missing frozen exit: err=%v reads=%d", err, defaults.reads)
	}
	defaults.value, _ = egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionBuiltin, 4)
	next, err := manager.BeginRequest(t.Context(), testCapture(), "egress.managed-opaque", facts)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := next.Plan().ResolvedEgress()
	next.Release()
	old, oldErr := lease.Plan().ResolvedEgress()
	if err != nil || oldErr != nil || selected.DefaultRevision() != 4 || old.DefaultRevision() != 3 || defaults.reads != 2 {
		t.Fatal("default edit did not affect only the next opaque request")
	}
	lease.Release()
	defaults.err = errors.New("synthetic unavailable")
	failed, err := manager.BeginRequest(t.Context(), testCapture(), "egress.managed-opaque", facts)
	if err == nil || failed != nil {
		if failed != nil {
			failed.Release()
		}
		t.Fatal("unavailable default admitted opaque request")
	}
	state := manager.state(testCapture())
	state.mu.Lock()
	active := state.connections["egress.managed-opaque"].activeRequests
	state.mu.Unlock()
	if active != 0 {
		t.Fatalf("failed resolution retained %d consumers", active)
	}
}

func (r *leaseEgressResolver) Resolve(_ context.Context, account, flow egressprofile.Choice) (egressprofile.Resolved, error) {
	if account != egressprofile.Inherit() || flow.Validate() != nil {
		return egressprofile.Resolved{}, errors.New("unexpected choice")
	}
	r.reads++
	if p, explicit := flow.ExplicitProfile(); explicit {
		return egressprofile.RestoreResolved(p, egressprofile.SelectionFlow, 0)
	}
	return r.value, r.err
}

func TestOriginalRequestLeaseFreezesOnceAndFailureReleasesActualConsumer(t *testing.T) {
	policy := environmentFixture(t, "inherit-original", "adapter.shared")
	protocol := &policy.ClientEndpoints[0].ProtocolPlans[0]
	protocol.Destination = environment.DestinationPlan{Kind: environment.DestinationKindOriginal}
	protocol.EgressProfile = egressprofile.Inherit()
	manager := newTestManager(t, newMemoryRepository(), newRevisionResolver(t, policy))
	resolved, err := egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionBuiltin, 3)
	if err != nil {
		t.Fatal(err)
	}
	defaults := &leaseEgressResolver{value: resolved}
	// Composes a real manager with one dependency, not an issued-lease mutation.
	manager, err = NewManager(Options{Repository: manager.repository, Environments: manager.environments, Activity: manager.activity, Clock: manager.clock, EgressDefaults: defaults})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Create(t.Context(), CreateCommand{Capture: testCapture(), EnvironmentID: policy.ID, Source: SourceLaunch})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := manager.RegisterConnection(t.Context(), testCapture(), "egress.original", semanticOrigin(t))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	lease, err := manager.BeginRequest(t.Context(), testCapture(), "egress.original", semanticRequestFacts())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	got, err := lease.Plan().ResolvedEgress()
	if err != nil || got != resolved || defaults.reads != 1 || lease.Plan().EgressChoice() != egressprofile.Inherit() {
		t.Fatalf("original lease has no single immutable selection: got=%v err=%v reads=%d", got, err, defaults.reads)
	}
	defaults.value, _ = egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionBuiltin, 4)
	next, err := manager.BeginRequest(t.Context(), testCapture(), "egress.original", semanticRequestFacts())
	if err != nil {
		t.Fatal(err)
	}
	selected, err := next.Plan().ResolvedEgress()
	next.Release()
	if err != nil || selected.DefaultRevision() != 4 {
		t.Fatal("next request ignored edit")
	}
	old, _ := lease.Plan().ResolvedEgress()
	if old.DefaultRevision() != 3 {
		t.Fatal("edit changed running request")
	}
	lease.Release()
	defaults.err = errors.New("synthetic unavailable")
	failed, err := manager.BeginRequest(t.Context(), testCapture(), "egress.original", semanticRequestFacts())
	if err == nil || failed != nil {
		if failed != nil {
			failed.Release()
		}
		t.Fatal("unavailable default admitted original request")
	}
	state := manager.state(testCapture())
	state.mu.Lock()
	active := state.connections["egress.original"].activeRequests
	state.mu.Unlock()
	if active != 0 {
		t.Fatalf("failed resolution retained %d active request consumers", active)
	}
}
