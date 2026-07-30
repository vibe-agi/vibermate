package productruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

func TestRuntimeResumeProberDispatchesOnlyTypedSupportedTargets(t *testing.T) {
	t.Parallel()

	provider := &recordingProber{}
	original := &recordingProber{}
	prober, err := newRuntimeResumeProber(provider, original)
	if err != nil {
		t.Fatal(err)
	}
	request := offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{
		testRuntimeProbeTarget(offlinehold.EgressOpaque, "client.example", 0),
		testRuntimeProbeTarget(offlinehold.EgressProvider, "provider.example", 1),
		testRuntimeProbeTarget(offlinehold.EgressAuxiliary, "client.example", 0),
	}}
	if err := prober.Probe(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if got := provider.Targets(); len(got) != 1 ||
		got[0] != request.Targets[1] {
		t.Fatalf("provider targets = %+v", got)
	}
	if got := original.Targets(); len(got) != 2 ||
		got[0] != request.Targets[0] ||
		got[1] != request.Targets[2] {
		t.Fatalf("original targets = %+v", got)
	}

	err = prober.Probe(
		context.Background(),
		offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{{
			Kind:          offlinehold.EgressUpdate,
			TargetRef:     "update-target",
			NetworkOrigin: "https://update.example",
			HTTPAuthority: "update.example",
			TLSServerName: "update.example",
		}}},
	)
	requireProbeFailureReason(t, err, offlinehold.ProbeReasonFailed)
}

func TestRuntimeResumeProberStopsAtFirstFailedTarget(t *testing.T) {
	t.Parallel()

	providerFailure := offlinehold.NewProbeFailure(
		offlinehold.ProbeReasonTLSRejected,
		errors.New("fixture rejection"),
	)
	provider := &recordingProber{err: providerFailure}
	original := &recordingProber{}
	prober, err := newRuntimeResumeProber(provider, original)
	if err != nil {
		t.Fatal(err)
	}
	err = prober.Probe(
		context.Background(),
		offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{
			testRuntimeProbeTarget(
				offlinehold.EgressProvider,
				"provider.example",
				1,
			),
			testRuntimeProbeTarget(
				offlinehold.EgressOpaque,
				"client.example",
				0,
			),
		}},
	)
	if !errors.Is(err, providerFailure) {
		t.Fatalf("Probe() error = %v", err)
	}
	if len(original.Targets()) != 0 {
		t.Fatalf("original probe ran after failure: %+v", original.Targets())
	}
}

func TestRuntimeResumeUsesFrozenQueueAndActiveRouteSetTargets(t *testing.T) {
	t.Parallel()

	gate, err := offlinehold.New(offlinehold.Config{
		MaxHeldRequests:    4,
		MaxHeldBytes:       1024,
		MaxHoldDuration:    time.Second,
		ReleaseConcurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Start(
		context.Background(),
		offlinehold.RuntimeBinding{InstanceID: "resume-target-test"},
	); err != nil {
		t.Fatal(err)
	}
	_, err = gate.Enter(context.Background(), gate.Snapshot().Revision)
	if err != nil {
		t.Fatal(err)
	}
	action, err := gate.BeginAction(
		context.Background(),
		offlinehold.ActionRequest{ActionID: "opaque-request"},
	)
	if err != nil {
		t.Fatal(err)
	}
	opaqueTarget := testRuntimeProbeTarget(
		offlinehold.EgressOpaque,
		"client.example",
		0,
	)
	acquired := make(chan struct {
		lease offlinehold.Lease
		err   error
	}, 1)
	go func() {
		lease, acquireErr := gate.Acquire(
			context.Background(),
			offlinehold.AcquireRequest{
				RequestID: "opaque-request",
				Action:    action,
				Target:    opaqueTarget,
				SizeBytes: 7,
			},
		)
		acquired <- struct {
			lease offlinehold.Lease
			err   error
		}{lease: lease, err: acquireErr}
	}()
	waitForQueuedTargets(t, gate, 1)
	accessID, err := access.NewAccessID("resume-provider-access")
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := productionAccessPlanCompiler()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := compiler.Compile(
		runtimeAccessAggregate(t, accessID, 1, "Resume Provider Access"),
	)
	if err != nil {
		t.Fatal(err)
	}
	projection := access.NewSnapshotProjection()
	if err := projection.Restore([]access.AccessPlanSnapshot{plan}); err != nil {
		t.Fatal(err)
	}
	prober := &recordingProber{}
	runtime := &Runtime{
		offlineHold:  gate,
		probeCatalog: projection,
		resumeProber: prober,
	}
	expectedTargets, err := runtime.resumeProbeTargets()
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := runtime.ResumeOfflineHold(
		context.Background(),
		gate.Snapshot().Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != offlinehold.StateReleasing ||
		resumed.ActiveEgress != 1 {
		t.Fatalf("resumed snapshot = %+v", resumed)
	}
	if got := prober.Targets(); len(got) != 2 ||
		got[0] != expectedTargets[0] ||
		got[1] != expectedTargets[1] {
		t.Fatalf("probe targets = %+v", got)
	}
	result := <-acquired
	if result.err != nil || result.lease == nil {
		t.Fatalf("Acquire() result = %+v", result)
	}
	result.lease.Release()
	action.Release()
}

func TestRuntimeResumeKeepsQueuedProviderTargetFrozenAcrossPlanChange(
	t *testing.T,
) {
	t.Parallel()

	gate, err := offlinehold.New(offlinehold.Config{
		MaxHeldRequests:    4,
		MaxHeldBytes:       1024,
		MaxHoldDuration:    time.Second,
		ReleaseConcurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Start(
		context.Background(),
		offlinehold.RuntimeBinding{InstanceID: "frozen-provider-target-test"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Enter(
		context.Background(),
		gate.Snapshot().Revision,
	); err != nil {
		t.Fatal(err)
	}

	accessID, err := access.NewAccessID("frozen-provider-access")
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := productionAccessPlanCompiler()
	if err != nil {
		t.Fatal(err)
	}
	revisionOne := compileRuntimePlanWithProviderOrigin(
		t,
		compiler,
		accessID,
		1,
		"https://old-provider.example:443/v1",
	)
	revisionTwo := compileRuntimePlanWithProviderOrigin(
		t,
		compiler,
		accessID,
		2,
		"https://new-provider.example:443/v1",
	)
	projection := access.NewSnapshotProjection()
	if err := projection.Restore([]access.AccessPlanSnapshot{revisionOne}); err != nil {
		t.Fatal(err)
	}

	compiledOld := revisionOne.ProviderTargets()[0]
	oldTarget, err := providertransport.NewTarget(compiledOld)
	if err != nil {
		t.Fatal(err)
	}
	targetReference := access.ProviderTargetReference(
		accessID,
		compiledOld.Target().ID,
	)
	frozenOld, err := providertransport.NewProbeTarget(
		targetReference,
		revisionOne.Revision(),
		revisionOne.PlanHash(),
		oldTarget,
	)
	if err != nil {
		t.Fatal(err)
	}
	action, err := gate.BeginAction(
		context.Background(),
		offlinehold.ActionRequest{ActionID: "held-provider"},
	)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct {
		lease offlinehold.Lease
		err   error
	}, 1)
	go func() {
		lease, acquireErr := gate.Acquire(
			context.Background(),
			offlinehold.AcquireRequest{
				RequestID: "held-provider/attempt-1",
				Action:    action,
				Target:    frozenOld,
				SizeBytes: 1,
			},
		)
		acquired <- struct {
			lease offlinehold.Lease
			err   error
		}{lease: lease, err: acquireErr}
	}()
	waitForQueuedTargets(t, gate, 1)

	if err := projection.Publish(revisionTwo); err != nil {
		t.Fatal(err)
	}
	prober := &recordingProber{}
	runtime := &Runtime{
		offlineHold:  gate,
		probeCatalog: projection,
		resumeProber: prober,
	}
	resumed, err := runtime.ResumeOfflineHold(
		context.Background(),
		gate.Snapshot().Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != offlinehold.StateReleasing {
		t.Fatalf("Resume() snapshot = %+v", resumed)
	}
	targets := prober.Targets()
	if len(targets) != 2 {
		t.Fatalf("probe targets = %+v, want old queued and current active targets", targets)
	}
	var sawOld, sawNew bool
	for _, target := range targets {
		if target.TargetRef != targetReference {
			t.Fatalf("probe target reference = %q, want %q", target.TargetRef, targetReference)
		}
		switch {
		case target.AccessRevision == 1 &&
			target.PlanHash == revisionOne.PlanHash().String() &&
			target.NetworkOrigin == "https://old-provider.example:443/v1":
			sawOld = true
		case target.AccessRevision == 2 &&
			target.PlanHash == revisionTwo.PlanHash().String() &&
			target.NetworkOrigin == "https://new-provider.example:443/v1":
			sawNew = true
		}
	}
	if !sawOld || !sawNew {
		t.Fatalf("probe targets did not preserve both identities: %+v", targets)
	}
	result := <-acquired
	if result.err != nil || result.lease == nil {
		t.Fatalf("Acquire() result = %+v", result)
	}
	result.lease.Release()
	action.Release()
}

func compileRuntimePlanWithProviderOrigin(
	t *testing.T,
	compiler *access.Compiler,
	accessID access.AccessID,
	revision access.Revision,
	rawOrigin string,
) access.AccessPlanSnapshot {
	t.Helper()
	aggregate := runtimeAccessAggregate(
		t,
		accessID,
		revision,
		"Frozen Provider Target",
	)
	origin, err := access.NewProviderOrigin(rawOrigin)
	if err != nil {
		t.Fatal(err)
	}
	aggregate.ProviderTargets[0].Origin = origin
	plan, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func testRuntimeProbeTarget(
	kind offlinehold.EgressKind,
	host string,
	revision uint64,
) offlinehold.ProbeTarget {
	target := offlinehold.ProbeTarget{
		Kind:          kind,
		TargetRef:     string(kind) + "/" + host,
		NetworkOrigin: "https://" + host,
		HTTPAuthority: host,
		TLSServerName: host,
	}
	if revision != 0 {
		target.AccessRevision = revision
		target.PlanHash = "plan-hash-" + host
	}
	return target
}

type recordingProber struct {
	mu      sync.Mutex
	targets []offlinehold.ProbeTarget
	err     error
}

func (prober *recordingProber) Probe(
	_ context.Context,
	request offlinehold.ProbeRequest,
) error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	prober.targets = append(prober.targets, request.Targets...)
	return prober.err
}

func (prober *recordingProber) Targets() []offlinehold.ProbeTarget {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	return append([]offlinehold.ProbeTarget(nil), prober.targets...)
}

func waitForQueuedTargets(
	t *testing.T,
	gate *offlinehold.Gate,
	count int,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if gate.Snapshot().QueuedRequests == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queued requests = %d, want %d", gate.Snapshot().QueuedRequests, count)
}

func requireProbeFailureReason(
	t *testing.T,
	err error,
	expected offlinehold.ProbeReason,
) {
	t.Helper()
	var failure *offlinehold.ProbeFailure
	if !errors.As(err, &failure) || failure.Reason != expected {
		t.Fatalf("probe error = %v, want reason %s", err, expected)
	}
}
