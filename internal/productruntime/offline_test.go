package productruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

func TestRuntimeResumeProberDispatchesOnlyTypedSupportedTargets(t *testing.T) {
	t.Parallel()

	provider := &recordingProber{}
	original := &recordingProber{}
	prober, err := newRuntimeResumeProber(provider, original, &recordingProber{})
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
	if got := provider.Targets(); len(got) != 1 || got[0] != request.Targets[1] {
		t.Fatalf("provider targets = %+v", got)
	}
	if got := original.Targets(); len(got) != 2 || got[0] != request.Targets[0] || got[1] != request.Targets[2] {
		t.Fatalf("original targets = %+v", got)
	}

	err = prober.Probe(context.Background(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{{
		Kind: offlinehold.EgressUpdate, Transport: offlinehold.ProbeTransportStrictTLS,
		TargetRef: "update-target", NetworkOrigin: "https://update.example",
		HTTPAuthority: "update.example", TLSServerName: "update.example",
	}}})
	requireProbeFailureReason(t, err, offlinehold.ProbeReasonFailed)
}

func TestRuntimeResumeProberStopsAtFirstFailedTarget(t *testing.T) {
	t.Parallel()

	providerFailure := offlinehold.NewProbeFailure(offlinehold.ProbeReasonTLSRejected, errors.New("fixture rejection"))
	provider := &recordingProber{err: providerFailure}
	original := &recordingProber{}
	prober, err := newRuntimeResumeProber(provider, original, &recordingProber{})
	if err != nil {
		t.Fatal(err)
	}
	err = prober.Probe(context.Background(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{
		testRuntimeProbeTarget(offlinehold.EgressProvider, "provider.example", 1),
		testRuntimeProbeTarget(offlinehold.EgressOpaque, "client.example", 0),
	}})
	if !errors.Is(err, providerFailure) {
		t.Fatalf("Probe() error = %v", err)
	}
	if len(original.Targets()) != 0 {
		t.Fatalf("original probe ran after failure: %+v", original.Targets())
	}
}

func TestRuntimeResumeUsesOnlyFrozenQueuedTargets(t *testing.T) {
	t.Parallel()

	gate := newEnteredOfflineGate(t, "resume-target-test")
	action, err := gate.BeginAction(context.Background(), offlinehold.ActionRequest{ActionID: "opaque-request"})
	if err != nil {
		t.Fatal(err)
	}
	target := testRuntimeProbeTarget(offlinehold.EgressOpaque, "client.example", 0)
	acquired := acquireHeld(t, gate, action, "opaque-request", target)
	waitForQueuedTargets(t, gate, 1)

	prober := &recordingProber{}
	runtime := &Runtime{offlineHold: gate, resumeProber: prober}
	targets, err := runtime.resumeProbeTargets()
	if err != nil || len(targets) != 1 || targets[0] != target {
		t.Fatalf("resume targets = %+v, %v", targets, err)
	}
	resumed, err := runtime.ResumeOfflineHold(context.Background(), gate.Snapshot().Revision)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != offlinehold.StateReleasing || resumed.ActiveEgress != 1 {
		t.Fatalf("resumed snapshot = %+v", resumed)
	}
	if got := prober.Targets(); len(got) != 1 || got[0] != target {
		t.Fatalf("probed targets = %+v", got)
	}
	releaseAcquired(t, <-acquired, action)
}

func TestRuntimeResumeKeepsDistinctFrozenPlanIdentities(t *testing.T) {
	t.Parallel()

	gate := newEnteredOfflineGate(t, "frozen-plan-target-test")
	targetOne := testRuntimeProbeTarget(offlinehold.EgressProvider, "provider.example", 1)
	targetTwo := testRuntimeProbeTarget(offlinehold.EgressProvider, "provider.example", 2)
	actionOne, err := gate.BeginAction(context.Background(), offlinehold.ActionRequest{ActionID: "held-provider-one"})
	if err != nil {
		t.Fatal(err)
	}
	actionTwo, err := gate.BeginAction(context.Background(), offlinehold.ActionRequest{ActionID: "held-provider-two"})
	if err != nil {
		t.Fatal(err)
	}
	acquiredOne := acquireHeld(t, gate, actionOne, "held-provider-one", targetOne)
	acquiredTwo := acquireHeld(t, gate, actionTwo, "held-provider-two", targetTwo)
	waitForQueuedTargets(t, gate, 2)

	prober := &recordingProber{}
	runtime := &Runtime{offlineHold: gate, resumeProber: prober}
	targets, err := runtime.resumeProbeTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].PlanRevision == targets[1].PlanRevision || targets[0].PlanDigest == targets[1].PlanDigest {
		t.Fatalf("frozen plan identities were collapsed: %+v", targets)
	}
	if _, err := runtime.ResumeOfflineHold(context.Background(), gate.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	if got := prober.Targets(); len(got) != 2 {
		t.Fatalf("probed targets = %+v", got)
	}
	releaseAcquired(t, <-acquiredOne, actionOne)
	releaseAcquired(t, <-acquiredTwo, actionTwo)
}

func newEnteredOfflineGate(t *testing.T, instanceID string) *offlinehold.Gate {
	t.Helper()
	gate, err := offlinehold.New(offlinehold.Config{
		MaxHeldRequests: 4, MaxHeldBytes: 1024, MaxHoldDuration: time.Second,
		ReleaseConcurrency: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Start(context.Background(), offlinehold.RuntimeBinding{InstanceID: instanceID}); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Enter(context.Background(), gate.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	return gate
}

type acquireResult struct {
	lease offlinehold.Lease
	err   error
}

func acquireHeld(t *testing.T, gate *offlinehold.Gate, action *offlinehold.ActionLease, requestID string, target offlinehold.ProbeTarget) <-chan acquireResult {
	t.Helper()
	result := make(chan acquireResult, 1)
	go func() {
		lease, err := gate.Acquire(context.Background(), offlinehold.AcquireRequest{
			RequestID: requestID, Action: action, Target: target, SizeBytes: 1,
		})
		result <- acquireResult{lease: lease, err: err}
	}()
	return result
}

func releaseAcquired(t *testing.T, result acquireResult, action *offlinehold.ActionLease) {
	t.Helper()
	if result.err != nil || result.lease == nil {
		t.Fatalf("Acquire() = %+v", result)
	}
	result.lease.Release()
	action.Release()
}

func testRuntimeProbeTarget(kind offlinehold.EgressKind, host string, revision uint64) offlinehold.ProbeTarget {
	target := offlinehold.ProbeTarget{
		Kind: kind, Transport: offlinehold.ProbeTransportStrictTLS,
		TargetRef: string(kind) + "/" + host, NetworkOrigin: "https://" + host,
		HTTPAuthority: host, TLSServerName: host, EgressPolicy: egressnetwork.DefaultPolicy(),
	}
	if revision != 0 {
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", host, revision)))
		target.PlanRevision = revision
		target.PlanDigest = hex.EncodeToString(digest[:])
	}
	return target
}

type recordingProber struct {
	mu      sync.Mutex
	targets []offlinehold.ProbeTarget
	err     error
}

func (prober *recordingProber) Probe(_ context.Context, request offlinehold.ProbeRequest) error {
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

func waitForQueuedTargets(t *testing.T, gate *offlinehold.Gate, count int) {
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

func requireProbeFailureReason(t *testing.T, err error, expected offlinehold.ProbeReason) {
	t.Helper()
	var failure *offlinehold.ProbeFailure
	if !errors.As(err, &failure) || failure.Reason != expected {
		t.Fatalf("probe error = %v, want reason %s", err, expected)
	}
}
