package offlinehold

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestGateEntersDrainsProbesAndReleasesHeldRequest(t *testing.T) {
	t.Parallel()

	gate := startTestGate(t, Config{
		MaxHeldRequests:    8,
		MaxHeldBytes:       1024,
		MaxHoldDuration:    time.Second,
		ReleaseConcurrency: 1,
	})
	activeRequest := acquireRequest(t, gate, "active", "target-a", 10)
	active, err := gate.Acquire(context.Background(), activeRequest)
	if err != nil {
		t.Fatalf("Acquire(active) error = %v", err)
	}
	enterRevision := gate.Snapshot().Revision
	enterResult := make(chan struct {
		snapshot Snapshot
		err      error
	}, 1)
	go func() {
		snapshot, enterErr := gate.Enter(context.Background(), enterRevision)
		enterResult <- struct {
			snapshot Snapshot
			err      error
		}{snapshot: snapshot, err: enterErr}
	}()
	waitForState(t, gate, StateEntering)

	heldResult := make(chan acquireResult, 1)
	heldRequest := acquireRequest(t, gate, "held", "target-a", 20)
	go acquireAsync(gate, heldRequest, heldResult)
	waitForQueue(t, gate, 1)
	if got := gate.Snapshot(); got.SafeToDisconnect || got.ActiveEgress != 1 {
		t.Fatalf("entering snapshot = %+v", got)
	}

	active.Release()
	activeRequest.Action.Release()
	enter := <-enterResult
	if enter.err != nil {
		t.Fatalf("Enter() error = %v", enter.err)
	}
	if enter.snapshot.State != StateHeld || !enter.snapshot.SafeToDisconnect {
		t.Fatalf("held snapshot = %+v", enter.snapshot)
	}
	select {
	case result := <-heldResult:
		t.Fatalf("held request was released before probe: %+v", result)
	default:
	}

	failedProbe := &proberStub{
		err: NewProbeFailure(ProbeReasonTLSRejected, errors.New("fixture TLS failure")),
	}
	failed, err := gate.Resume(
		context.Background(),
		gate.Snapshot().Revision,
		ResumeRequest{Targets: []ProbeTarget{providerTarget("target-a")}},
		failedProbe,
	)
	if err == nil || failed.State != StateHeld ||
		failed.LastProbeReason != ProbeReasonTLSRejected ||
		failed.QueuedRequests != 1 {
		t.Fatalf("failed Resume() snapshot=%+v error=%v", failed, err)
	}
	select {
	case result := <-heldResult:
		t.Fatalf("failed probe released a request: %+v", result)
	default:
	}

	successfulProbe := &proberStub{}
	releasing, err := gate.Resume(
		context.Background(),
		gate.Snapshot().Revision,
		ResumeRequest{Targets: []ProbeTarget{providerTarget("target-a")}},
		successfulProbe,
	)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if releasing.State != StateReleasing ||
		releasing.ActiveEgress != 1 ||
		releasing.QueuedRequests != 0 {
		t.Fatalf("releasing snapshot = %+v", releasing)
	}
	result := <-heldResult
	if result.err != nil || result.lease == nil {
		t.Fatalf("held Acquire() result = %+v", result)
	}
	if successfulProbe.lastRequest().Targets[0] != providerTarget("target-a") {
		t.Fatalf("probe request = %+v", successfulProbe.lastRequest())
	}
	result.lease.Release()
	heldRequest.Action.Release()
	waitForState(t, gate, StateOnline)
}

func TestGateEnterWaitsForPreEnterActionAndQueuesLaterAction(
	t *testing.T,
) {
	t.Parallel()

	gate := startTestGate(t, Config{
		MaxHeldRequests:    4,
		MaxHeldBytes:       1024,
		MaxHoldDuration:    time.Second,
		ReleaseConcurrency: 1,
	})
	before := acquireRequest(t, gate, "before-enter", "target-a", 1)
	enterResult := make(chan struct {
		snapshot Snapshot
		err      error
	}, 1)
	go func(expected uint64) {
		snapshot, err := gate.Enter(context.Background(), expected)
		enterResult <- struct {
			snapshot Snapshot
			err      error
		}{snapshot: snapshot, err: err}
	}(gate.Snapshot().Revision)
	waitForState(t, gate, StateEntering)
	if snapshot := gate.Snapshot(); snapshot.SafeToDisconnect ||
		snapshot.EnteringActions != 1 ||
		snapshot.ActiveActions != 1 {
		t.Fatalf("entering snapshot before egress = %+v", snapshot)
	}

	after := acquireRequest(t, gate, "after-enter", "target-a", 1)
	afterResult := make(chan acquireResult, 1)
	go acquireAsync(gate, after, afterResult)
	waitForQueue(t, gate, 1)

	beforeLease, err := gate.Acquire(context.Background(), before)
	if err != nil {
		t.Fatalf("pre-enter Acquire() error = %v", err)
	}
	if snapshot := gate.Snapshot(); snapshot.SafeToDisconnect ||
		snapshot.ActiveEgress != 1 ||
		snapshot.QueuedRequests != 1 {
		t.Fatalf("entering snapshot with admitted egress = %+v", snapshot)
	}
	beforeLease.Release()
	select {
	case result := <-enterResult:
		t.Fatalf("Enter returned before the pre-enter action finished: %+v", result)
	default:
	}

	before.Action.Release()
	entered := <-enterResult
	if entered.err != nil {
		t.Fatalf("Enter() error = %v", entered.err)
	}
	if entered.snapshot.State != StateHeld ||
		!entered.snapshot.SafeToDisconnect ||
		entered.snapshot.QueuedRequests != 1 {
		t.Fatalf("held snapshot = %+v", entered.snapshot)
	}
	select {
	case result := <-afterResult:
		t.Fatalf("post-enter action bypassed Hold: %+v", result)
	default:
	}

	if _, err := gate.Resume(
		context.Background(),
		gate.Snapshot().Revision,
		ResumeRequest{Targets: []ProbeTarget{providerTarget("target-a")}},
		&proberStub{},
	); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	afterLease := waitAcquireResult(t, afterResult)
	if afterLease.err != nil || afterLease.lease == nil {
		t.Fatalf("post-enter Acquire() result = %+v", afterLease)
	}
	afterLease.lease.Release()
	after.Action.Release()
	waitForState(t, gate, StateOnline)
}

func TestGateReleaseIsFIFOAndConcurrencyBounded(t *testing.T) {
	t.Parallel()

	gate := startTestGate(t, Config{
		MaxHeldRequests:    8,
		MaxHeldBytes:       1024,
		MaxHoldDuration:    time.Second,
		ReleaseConcurrency: 1,
	})
	held, err := gate.Enter(context.Background(), gate.Snapshot().Revision)
	if err != nil {
		t.Fatalf("Enter() error = %v", err)
	}
	if held.State != StateHeld {
		t.Fatalf("Enter() snapshot = %+v", held)
	}

	results := []chan acquireResult{
		make(chan acquireResult, 1),
		make(chan acquireResult, 1),
		make(chan acquireResult, 1),
	}
	requests := make([]AcquireRequest, len(results))
	for index := range results {
		request := acquireRequest(
			t,
			gate,
			"queued-"+string(rune('a'+index)),
			"target-a",
			1,
		)
		requests[index] = request
		go acquireAsync(
			gate,
			request,
			results[index],
		)
		waitForQueue(t, gate, index+1)
	}
	if _, err := gate.Resume(
		context.Background(),
		gate.Snapshot().Revision,
		ResumeRequest{Targets: []ProbeTarget{providerTarget("target-a")}},
		&proberStub{},
	); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}

	for index := range results {
		result := waitAcquireResult(t, results[index])
		if result.err != nil {
			t.Fatalf("Acquire(%d) error = %v", index, result.err)
		}
		for later := index + 1; later < len(results); later++ {
			select {
			case unexpected := <-results[later]:
				t.Fatalf("Acquire(%d) bypassed FIFO: %+v", later, unexpected)
			default:
			}
		}
		snapshot := gate.Snapshot()
		if snapshot.ActiveEgress != 1 {
			t.Fatalf("active egress while releasing = %d, want 1", snapshot.ActiveEgress)
		}
		result.lease.Release()
		requests[index].Action.Release()
	}
	waitForState(t, gate, StateOnline)
}

func TestGateCancellationCapacityAndTimeoutNeverProduceLease(t *testing.T) {
	t.Parallel()

	gate := startTestGate(t, Config{
		MaxHeldRequests:    1,
		MaxHeldBytes:       4,
		MaxHoldDuration:    30 * time.Millisecond,
		ReleaseConcurrency: 1,
	})
	if _, err := gate.Enter(context.Background(), gate.Snapshot().Revision); err != nil {
		t.Fatalf("Enter() error = %v", err)
	}
	firstContext, cancelFirst := context.WithCancel(context.Background())
	first := make(chan acquireResult, 1)
	firstRequest := acquireRequest(t, gate, "first", "target-a", 4)
	go func() {
		lease, err := gate.Acquire(
			firstContext,
			firstRequest,
		)
		first <- acquireResult{lease: lease, err: err}
	}()
	waitForQueue(t, gate, 1)
	overflowRequest := acquireRequest(t, gate, "overflow", "target-a", 1)
	if _, err := gate.Acquire(
		context.Background(),
		overflowRequest,
	); !errors.Is(err, ErrHeldCapacity) {
		t.Fatalf("overflow Acquire() error = %v, want ErrHeldCapacity", err)
	}
	overflowRequest.Action.Release()
	cancelFirst()
	if result := waitAcquireResult(t, first); !errors.Is(result.err, context.Canceled) ||
		result.lease != nil {
		t.Fatalf("canceled Acquire() = %+v", result)
	}
	firstRequest.Action.Release()
	waitForQueue(t, gate, 0)

	timeoutRequest := acquireRequest(t, gate, "timeout", "target-a", 1)
	lease, err := gate.Acquire(
		context.Background(),
		timeoutRequest,
	)
	if !errors.Is(err, ErrHoldTimeout) || lease != nil {
		t.Fatalf("timed Acquire() = %v, %v; want nil, ErrHoldTimeout", lease, err)
	}
	timeoutRequest.Action.Release()
	if snapshot := gate.Snapshot(); snapshot.QueuedRequests != 0 ||
		snapshot.HeldBytes != 0 ||
		snapshot.ActiveEgress != 0 {
		t.Fatalf("snapshot after cancellation and timeout = %+v", snapshot)
	}
}

func TestGateUnprobedTargetRemainsHeldWithoutBreakingFIFO(t *testing.T) {
	t.Parallel()

	gate := startTestGate(t, Config{
		MaxHeldRequests:    4,
		MaxHeldBytes:       1024,
		MaxHoldDuration:    time.Second,
		ReleaseConcurrency: 2,
	})
	if _, err := gate.Enter(context.Background(), gate.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	first := make(chan acquireResult, 1)
	second := make(chan acquireResult, 1)
	firstRequest := acquireRequest(t, gate, "first", "target-b", 1)
	secondRequest := acquireRequest(t, gate, "second", "target-a", 1)
	go acquireAsync(gate, firstRequest, first)
	waitForQueue(t, gate, 1)
	go acquireAsync(gate, secondRequest, second)
	waitForQueue(t, gate, 2)

	resumed, err := gate.Resume(
		context.Background(),
		gate.Snapshot().Revision,
		ResumeRequest{Targets: []ProbeTarget{providerTarget("target-a")}},
		&proberStub{},
	)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resumed.State != StateHeld || resumed.QueuedRequests != 2 {
		t.Fatalf("Resume() snapshot = %+v", resumed)
	}
	select {
	case result := <-first:
		t.Fatalf("unprobed head was released: %+v", result)
	case result := <-second:
		t.Fatalf("later request bypassed FIFO: %+v", result)
	default:
	}

	if _, err := gate.Resume(
		context.Background(),
		gate.Snapshot().Revision,
		ResumeRequest{Targets: []ProbeTarget{
			providerTarget("target-a"),
			providerTarget("target-b"),
		}},
		&proberStub{},
	); err != nil {
		t.Fatalf("second Resume() error = %v", err)
	}
	firstResult := waitAcquireResult(t, first)
	secondResult := waitAcquireResult(t, second)
	firstResult.lease.Release()
	firstRequest.Action.Release()
	secondResult.lease.Release()
	secondRequest.Action.Release()
	waitForState(t, gate, StateOnline)
}

func TestGateShutdownRejectsQueueAndBoundsDrain(t *testing.T) {
	t.Parallel()

	gate := startTestGate(t, Config{
		MaxHeldRequests:    4,
		MaxHeldBytes:       1024,
		MaxHoldDuration:    time.Second,
		ReleaseConcurrency: 1,
	})
	activeRequest := acquireRequest(t, gate, "active", "target-a", 1)
	active, err := gate.Acquire(context.Background(), activeRequest)
	if err != nil {
		t.Fatal(err)
	}
	enterRevision := gate.Snapshot().Revision
	enterDone := make(chan error, 1)
	go func() {
		_, enterErr := gate.Enter(context.Background(), enterRevision)
		enterDone <- enterErr
	}()
	waitForState(t, gate, StateEntering)
	queued := make(chan acquireResult, 1)
	queuedRequest := acquireRequest(t, gate, "queued", "target-a", 1)
	go acquireAsync(gate, queuedRequest, queued)
	waitForQueue(t, gate, 1)

	gate.BeginShutdown()
	if result := waitAcquireResult(t, queued); !errors.Is(
		result.err,
		ErrCoordinatorStopping,
	) || result.lease != nil {
		t.Fatalf("queued result after shutdown = %+v", result)
	}
	queuedRequest.Action.Release()
	if err := <-enterDone; !errors.Is(err, ErrCoordinatorStopping) {
		t.Fatalf("Enter() after shutdown error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := gate.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain() error = %v, want deadline", err)
	}
	active.Release()
	activeRequest.Action.Release()
	if err := gate.Drain(context.Background()); err != nil {
		t.Fatalf("Drain() after release error = %v", err)
	}
	lateRequest := AcquireRequest{
		RequestID: "late",
		Action:    &ActionLease{},
		Target:    providerTarget("target-a"),
		SizeBytes: 1,
	}
	if _, err := gate.Acquire(
		context.Background(),
		lateRequest,
	); !errors.Is(err, ErrCoordinatorStopping) {
		t.Fatalf("late Acquire() error = %v", err)
	}
}

func TestGateLeaseReleaseAndShutdownAreIdempotent(t *testing.T) {
	t.Parallel()

	gate := startTestGate(t, DefaultConfig())
	request := acquireRequest(t, gate, "one", "target-a", 1)
	lease, err := gate.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	lease.Release()
	request.Action.Release()
	request.Action.Release()
	if snapshot := gate.Snapshot(); snapshot.ActiveEgress != 0 {
		t.Fatalf("active after repeated release = %d", snapshot.ActiveEgress)
	}
	gate.BeginShutdown()
	gate.BeginShutdown()
	if err := gate.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestGateSnapshotSeparatesAndCopiesActiveAndQueuedKinds(t *testing.T) {
	t.Parallel()

	gate := startTestGate(t, Config{
		MaxHeldRequests:    4,
		MaxHeldBytes:       1024,
		MaxHoldDuration:    time.Second,
		ReleaseConcurrency: 2,
	})
	activeRequest := acquireKindRequest(
		t,
		gate,
		"active-provider",
		providerTarget("provider-target"),
		1,
	)
	active, err := gate.Acquire(context.Background(), activeRequest)
	if err != nil {
		t.Fatal(err)
	}
	activeSnapshot := gate.Snapshot()
	if activeSnapshot.ActiveByKind[EgressProvider] != 1 ||
		len(activeSnapshot.QueuedByKind) != 0 {
		t.Fatalf("active snapshot = %+v", activeSnapshot)
	}
	activeSnapshot.ActiveByKind[EgressProvider] = 99
	activeSnapshot.QueuedByKind[EgressOpaque] = 99
	freshActive := gate.Snapshot()
	if freshActive.ActiveByKind[EgressProvider] != 1 ||
		len(freshActive.QueuedByKind) != 0 {
		t.Fatalf("aliased active snapshot = %+v", freshActive)
	}
	active.Release()
	activeRequest.Action.Release()
	if _, err := gate.Enter(
		context.Background(),
		gate.Snapshot().Revision,
	); err != nil {
		t.Fatal(err)
	}

	opaqueContext, cancelOpaque := context.WithCancel(context.Background())
	defer cancelOpaque()
	providerContext, cancelProvider := context.WithCancel(context.Background())
	defer cancelProvider()
	opaqueResult := make(chan acquireResult, 1)
	providerResult := make(chan acquireResult, 1)
	opaqueRequest := acquireKindRequest(
		t,
		gate,
		"queued-opaque",
		opaqueTarget("client-origin"),
		2,
	)
	providerRequest := acquireKindRequest(
		t,
		gate,
		"queued-provider",
		providerTarget("provider-target"),
		3,
	)
	go func() {
		lease, acquireErr := gate.Acquire(
			opaqueContext,
			opaqueRequest,
		)
		opaqueResult <- acquireResult{lease: lease, err: acquireErr}
	}()
	go func() {
		lease, acquireErr := gate.Acquire(
			providerContext,
			providerRequest,
		)
		providerResult <- acquireResult{lease: lease, err: acquireErr}
	}()
	waitForQueue(t, gate, 2)
	queuedSnapshot := gate.Snapshot()
	if len(queuedSnapshot.ActiveByKind) != 0 ||
		queuedSnapshot.QueuedByKind[EgressOpaque] != 1 ||
		queuedSnapshot.QueuedByKind[EgressProvider] != 1 {
		t.Fatalf("queued snapshot = %+v", queuedSnapshot)
	}
	targets := gate.PendingProbeTargets()
	if len(targets) != 2 ||
		targets[0] != opaqueTarget("client-origin") ||
		targets[1] != providerTarget("provider-target") {
		t.Fatalf("pending probe targets = %+v", targets)
	}
	targets[0].TargetRef = "mutated"
	if fresh := gate.PendingProbeTargets(); fresh[0].TargetRef != "client-origin" {
		t.Fatalf("aliased pending probe targets = %+v", fresh)
	}
	queuedSnapshot.QueuedByKind[EgressOpaque] = 99
	if fresh := gate.Snapshot(); fresh.QueuedByKind[EgressOpaque] != 1 {
		t.Fatalf("aliased queued snapshot = %+v", fresh)
	}

	cancelOpaque()
	cancelProvider()
	for label, result := range map[string]<-chan acquireResult{
		"opaque":   opaqueResult,
		"provider": providerResult,
	} {
		resolved := waitAcquireResult(t, result)
		if !errors.Is(resolved.err, context.Canceled) ||
			resolved.lease != nil {
			t.Fatalf("%s cancellation = %+v", label, resolved)
		}
	}
	opaqueRequest.Action.Release()
	providerRequest.Action.Release()
}

type acquireResult struct {
	lease Lease
	err   error
}

func acquireAsync(gate *Gate, request AcquireRequest, result chan<- acquireResult) {
	lease, err := gate.Acquire(context.Background(), request)
	result <- acquireResult{lease: lease, err: err}
}

func acquireRequest(
	t *testing.T,
	gate *Gate,
	id string,
	target string,
	size int64,
) AcquireRequest {
	t.Helper()
	return acquireKindRequest(t, gate, id, providerTarget(target), size)
}

func acquireKindRequest(
	t *testing.T,
	gate *Gate,
	id string,
	target ProbeTarget,
	size int64,
) AcquireRequest {
	t.Helper()
	action, err := gate.BeginAction(
		context.Background(),
		ActionRequest{ActionID: id},
	)
	if err != nil {
		t.Fatalf("BeginAction(%q) error = %v", id, err)
	}
	return AcquireRequest{
		RequestID: id,
		Action:    action,
		Target:    target,
		SizeBytes: size,
	}
}

func providerTarget(target string) ProbeTarget {
	host := target + ".example"
	return ProbeTarget{
		Kind:           EgressProvider,
		TargetRef:      target,
		NetworkOrigin:  "https://" + host + "/v1",
		HTTPAuthority:  host,
		TLSServerName:  host,
		AccessRevision: 1,
		PlanHash:       "plan-hash-" + target,
	}
}

func opaqueTarget(target string) ProbeTarget {
	host := target + ".example"
	return ProbeTarget{
		Kind:          EgressOpaque,
		TargetRef:     target,
		NetworkOrigin: "https://" + host,
		HTTPAuthority: host,
		TLSServerName: host,
	}
}

func startTestGate(t *testing.T, config Config) *Gate {
	t.Helper()
	gate, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := gate.Start(
		context.Background(),
		RuntimeBinding{InstanceID: "runtime-test"},
	); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	return gate
}

func waitForState(t *testing.T, gate *Gate, state State) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if gate.Snapshot().State == state {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state = %q, want %q", gate.Snapshot().State, state)
}

func waitForQueue(t *testing.T, gate *Gate, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if gate.Snapshot().QueuedRequests == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue = %d, want %d", gate.Snapshot().QueuedRequests, count)
}

func waitAcquireResult(t *testing.T, result <-chan acquireResult) acquireResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Acquire")
		return acquireResult{}
	}
}

type proberStub struct {
	mu      sync.Mutex
	request ProbeRequest
	err     error
}

func (prober *proberStub) Probe(_ context.Context, request ProbeRequest) error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	prober.request = ProbeRequest{Targets: append([]ProbeTarget(nil), request.Targets...)}
	return prober.err
}

func (prober *proberStub) lastRequest() ProbeRequest {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	return ProbeRequest{Targets: append([]ProbeTarget(nil), prober.request.Targets...)}
}
