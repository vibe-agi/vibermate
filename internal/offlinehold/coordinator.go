package offlinehold

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidConfig       = errors.New("offline-hold configuration is invalid")
	ErrInvalidRequest      = errors.New("offline-hold request is invalid")
	ErrNotStarted          = errors.New("offline-hold coordinator is not started")
	ErrAlreadyStarted      = errors.New("offline-hold coordinator is already started")
	ErrCoordinatorStopping = errors.New("offline-hold coordinator is stopping")
	ErrHeldCapacity        = errors.New("offline-hold queue capacity was exceeded")
	ErrHoldTimeout         = errors.New("offline-hold request exceeded its wait limit")
	ErrDuplicateRequest    = errors.New("offline-hold request ID is already active")
	ErrRevisionConflict    = errors.New("offline-hold revision conflict")
	ErrInvalidTransition   = errors.New("offline-hold state transition is invalid")
)

const maxOpaqueIdentityBytes = 1024

type Config struct {
	MaxHeldRequests    int
	MaxHeldBytes       int64
	MaxHoldDuration    time.Duration
	ReleaseConcurrency int
}

func DefaultConfig() Config {
	return Config{
		MaxHeldRequests:    256,
		MaxHeldBytes:       64 << 20,
		MaxHoldDuration:    30 * time.Minute,
		ReleaseConcurrency: 8,
	}
}

func (config Config) validate() error {
	if config.MaxHeldRequests <= 0 ||
		config.MaxHeldBytes <= 0 ||
		config.MaxHoldDuration <= 0 ||
		config.ReleaseConcurrency <= 0 {
		return ErrInvalidConfig
	}
	return nil
}

type waiterState uint8

const (
	waiterQueued waiterState = iota
	waiterGranted
	waiterCanceled
)

type waiter struct {
	request AcquireRequest
	state   waiterState
	ready   chan struct{}
	lease   *egressLease
}

type targetKey struct {
	target ProbeTarget
}

// Gate is the production process-local coordinator. It owns no network
// transport; Resume receives the sole typed probe bypass explicitly.
type Gate struct {
	mu sync.Mutex

	config       Config
	binding      RuntimeBinding
	state        State
	since        time.Time
	revision     uint64
	active       int
	activeByKind map[EgressKind]int
	actions      map[string]*ActionLease
	entering     int
	queue        []*waiter
	heldBytes    int64
	requestIDs   map[string]struct{}
	probed       map[targetKey]struct{}
	lastProbe    ProbeReason
	changed      chan struct{}
}

var (
	_ Coordinator = (*Gate)(nil)
	_ Controller  = (*Gate)(nil)
)

func New(config Config) (*Gate, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Gate{
		config:       config,
		state:        StateUnbound,
		activeByKind: make(map[EgressKind]int),
		actions:      make(map[string]*ActionLease),
		requestIDs:   make(map[string]struct{}),
		changed:      make(chan struct{}),
	}, nil
}

func (gate *Gate) Start(ctx context.Context, binding RuntimeBinding) error {
	if ctx == nil {
		return ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateOpaqueIdentity("runtime instance ID", binding.InstanceID); err != nil {
		return err
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.state != StateUnbound {
		return ErrAlreadyStarted
	}
	gate.binding = binding
	gate.transitionLocked(StateOnline)
	return nil
}

func (gate *Gate) BeginAction(
	ctx context.Context,
	request ActionRequest,
) (*ActionLease, error) {
	if ctx == nil {
		return nil, ErrInvalidRequest
	}
	if err := validateOpaqueIdentity("action ID", request.ActionID); err != nil {
		return nil, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	switch gate.state {
	case StateUnbound:
		return nil, ErrNotStarted
	case StateStopping:
		return nil, ErrCoordinatorStopping
	}
	if _, duplicate := gate.actions[request.ActionID]; duplicate {
		return nil, fmt.Errorf(
			"%w: actionId=%q",
			ErrDuplicateRequest,
			request.ActionID,
		)
	}
	lease := &ActionLease{
		gate:        gate,
		actionID:    request.ActionID,
		beforeEnter: gate.state == StateOnline,
	}
	var once sync.Once
	lease.release = func() {
		once.Do(func() {
			gate.releaseAction(lease)
		})
	}
	gate.actions[request.ActionID] = lease
	if lease.beforeEnter {
		gate.entering++
	}
	gate.mutatedLocked()
	return lease, nil
}

func (gate *Gate) Acquire(
	ctx context.Context,
	request AcquireRequest,
) (Lease, error) {
	if ctx == nil {
		return nil, ErrInvalidRequest
	}
	if err := validateAcquireRequest(request); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	gate.mu.Lock()
	if gate.state == StateUnbound {
		gate.mu.Unlock()
		return nil, ErrNotStarted
	}
	if gate.state == StateStopping {
		gate.mu.Unlock()
		return nil, ErrCoordinatorStopping
	}
	if !gate.validActionLocked(request.Action) {
		gate.mu.Unlock()
		return nil, ErrInvalidRequest
	}
	if _, duplicate := gate.requestIDs[request.RequestID]; duplicate {
		gate.mu.Unlock()
		return nil, fmt.Errorf("%w: requestId=%q", ErrDuplicateRequest, request.RequestID)
	}
	if gate.state == StateOnline ||
		(gate.state == StateEntering && request.Action.beforeEnter) {
		lease := gate.grantLocked(request)
		gate.mu.Unlock()
		return lease, nil
	}
	if len(gate.queue) >= gate.config.MaxHeldRequests ||
		request.SizeBytes > gate.config.MaxHeldBytes-gate.heldBytes {
		gate.mu.Unlock()
		return nil, ErrHeldCapacity
	}
	entry := &waiter{
		request: request,
		state:   waiterQueued,
		ready:   make(chan struct{}),
	}
	gate.queue = append(gate.queue, entry)
	gate.heldBytes += request.SizeBytes
	gate.requestIDs[request.RequestID] = struct{}{}
	gate.mutatedLocked()
	gate.mu.Unlock()

	timer := time.NewTimer(gate.config.MaxHoldDuration)
	defer timer.Stop()
	select {
	case <-entry.ready:
		return gate.finishWaiter(ctx, entry)
	case <-ctx.Done():
		return gate.cancelWaiter(entry, ctx.Err())
	case <-timer.C:
		return gate.cancelWaiter(entry, ErrHoldTimeout)
	}
}

func (gate *Gate) Enter(
	ctx context.Context,
	expectedRevision uint64,
) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	gate.mu.Lock()
	if err := gate.checkRevisionLocked(expectedRevision); err != nil {
		snapshot := gate.snapshotLocked()
		gate.mu.Unlock()
		return snapshot, err
	}
	if gate.state != StateOnline {
		state := gate.state
		snapshot := gate.snapshotLocked()
		gate.mu.Unlock()
		return snapshot, fmt.Errorf("%w: state=%q", ErrInvalidTransition, state)
	}
	gate.transitionLocked(StateEntering)
	if gate.active == 0 && gate.entering == 0 {
		gate.transitionLocked(StateHeld)
	}
	for gate.state == StateEntering {
		changed := gate.changed
		gate.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			gate.mu.Lock()
			snapshot := gate.snapshotLocked()
			gate.mu.Unlock()
			return snapshot, ctx.Err()
		}
		gate.mu.Lock()
	}
	snapshot := gate.snapshotLocked()
	if gate.state == StateStopping {
		gate.mu.Unlock()
		return snapshot, ErrCoordinatorStopping
	}
	gate.mu.Unlock()
	return snapshot, nil
}

func (gate *Gate) Resume(
	ctx context.Context,
	expectedRevision uint64,
	request ResumeRequest,
	prober Prober,
) (Snapshot, error) {
	if ctx == nil || prober == nil {
		return Snapshot{}, ErrInvalidRequest
	}
	targets, err := normalizeProbeTargets(request.Targets)
	if err != nil {
		return Snapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	gate.mu.Lock()
	if err := gate.checkRevisionLocked(expectedRevision); err != nil {
		snapshot := gate.snapshotLocked()
		gate.mu.Unlock()
		return snapshot, err
	}
	if gate.state != StateHeld {
		state := gate.state
		snapshot := gate.snapshotLocked()
		gate.mu.Unlock()
		return snapshot, fmt.Errorf("%w: state=%q", ErrInvalidTransition, state)
	}
	if len(targets) == 0 && len(gate.queue) != 0 {
		snapshot := gate.snapshotLocked()
		gate.mu.Unlock()
		return snapshot, ErrInvalidRequest
	}
	gate.transitionLocked(StateProbing)
	gate.mu.Unlock()

	var probeErr error
	if len(targets) != 0 {
		probeErr = prober.Probe(ctx, ProbeRequest{Targets: slices.Clone(targets)})
	}

	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.state == StateStopping {
		return gate.snapshotLocked(), ErrCoordinatorStopping
	}
	if gate.state != StateProbing {
		return gate.snapshotLocked(), fmt.Errorf(
			"%w: state=%q after probe",
			ErrInvalidTransition,
			gate.state,
		)
	}
	if probeErr != nil {
		gate.lastProbe = probeReasonOf(probeErr)
		gate.transitionLocked(StateHeld)
		return gate.snapshotLocked(), fmt.Errorf("offline-hold probe: %w", probeErr)
	}
	gate.lastProbe = ""
	gate.probed = make(map[targetKey]struct{}, len(targets))
	for _, target := range targets {
		gate.probed[targetKey{target: target}] = struct{}{}
	}
	gate.transitionLocked(StateReleasing)
	gate.releaseQueuedLocked()
	return gate.snapshotLocked(), nil
}

func (gate *Gate) Snapshot() Snapshot {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.snapshotLocked()
}

func (gate *Gate) PendingProbeTargets() []ProbeTarget {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	seen := make(map[targetKey]struct{}, len(gate.queue))
	targets := make([]ProbeTarget, 0, len(gate.queue))
	for _, entry := range gate.queue {
		if entry == nil || entry.state != waiterQueued {
			continue
		}
		key := targetKey{
			target: entry.request.Target,
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, ProbeTarget{
			Kind:          key.target.Kind,
			Transport:     key.target.Transport,
			TargetRef:     key.target.TargetRef,
			NetworkOrigin: key.target.NetworkOrigin,
			HTTPAuthority: key.target.HTTPAuthority,
			TLSServerName: key.target.TLSServerName,
			PlanRevision:  key.target.PlanRevision,
			PlanDigest:    key.target.PlanDigest,
		})
	}
	slices.SortFunc(targets, func(left, right ProbeTarget) int {
		leftKey := probeTargetSortKey(left)
		rightKey := probeTargetSortKey(right)
		return strings.Compare(leftKey, rightKey)
	})
	return targets
}

func (gate *Gate) BeginShutdown() {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.state == StateStopping {
		return
	}
	if gate.state == StateUnbound {
		gate.transitionLocked(StateStopping)
		return
	}
	gate.transitionLocked(StateStopping)
	for _, entry := range gate.queue {
		if entry.state != waiterQueued {
			continue
		}
		entry.state = waiterCanceled
		delete(gate.requestIDs, entry.request.RequestID)
		close(entry.ready)
	}
	gate.queue = nil
	gate.heldBytes = 0
	gate.probed = nil
	gate.mutatedLocked()
}

func (gate *Gate) Drain(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidRequest
	}
	gate.mu.Lock()
	for gate.active != 0 || len(gate.queue) != 0 || len(gate.actions) != 0 {
		changed := gate.changed
		gate.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
		gate.mu.Lock()
	}
	gate.mu.Unlock()
	return nil
}

func (gate *Gate) finishWaiter(ctx context.Context, entry *waiter) (Lease, error) {
	gate.mu.Lock()
	state := entry.state
	lease := entry.lease
	gate.mu.Unlock()
	if state == waiterCanceled {
		return nil, ErrCoordinatorStopping
	}
	if state != waiterGranted || lease == nil {
		return nil, errors.New("offline-hold waiter completed without a lease")
	}
	if err := ctx.Err(); err != nil {
		lease.Release()
		return nil, err
	}
	return lease, nil
}

func (gate *Gate) cancelWaiter(entry *waiter, cause error) (Lease, error) {
	gate.mu.Lock()
	if entry.state == waiterGranted {
		lease := entry.lease
		gate.mu.Unlock()
		lease.Release()
		return nil, cause
	}
	if entry.state == waiterCanceled {
		gate.mu.Unlock()
		if errors.Is(cause, ErrHoldTimeout) {
			return nil, cause
		}
		return nil, ErrCoordinatorStopping
	}
	for index, queued := range gate.queue {
		if queued != entry {
			continue
		}
		gate.queue = append(gate.queue[:index], gate.queue[index+1:]...)
		break
	}
	entry.state = waiterCanceled
	gate.heldBytes -= entry.request.SizeBytes
	delete(gate.requestIDs, entry.request.RequestID)
	gate.mutatedLocked()
	gate.mu.Unlock()
	return nil, cause
}

func (gate *Gate) grantLocked(request AcquireRequest) *egressLease {
	gate.requestIDs[request.RequestID] = struct{}{}
	gate.active++
	gate.activeByKind[request.Target.Kind]++
	gate.mutatedLocked()
	return &egressLease{gate: gate, request: request}
}

func (gate *Gate) releaseQueuedLocked() {
	for gate.state == StateReleasing &&
		gate.active < gate.config.ReleaseConcurrency &&
		len(gate.queue) > 0 {
		entry := gate.queue[0]
		key := targetKey{
			target: entry.request.Target,
		}
		if _, approved := gate.probed[key]; !approved {
			if gate.active == 0 {
				gate.probed = nil
				gate.transitionLocked(StateHeld)
			}
			return
		}
		gate.queue = gate.queue[1:]
		gate.heldBytes -= entry.request.SizeBytes
		entry.state = waiterGranted
		entry.lease = &egressLease{gate: gate, request: entry.request}
		gate.active++
		gate.activeByKind[entry.request.Target.Kind]++
		gate.mutatedLocked()
		close(entry.ready)
	}
	if gate.state == StateReleasing &&
		len(gate.queue) == 0 &&
		gate.active == 0 &&
		len(gate.actions) == 0 {
		gate.probed = nil
		gate.transitionLocked(StateOnline)
	}
}

func (gate *Gate) release(request AcquireRequest) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if _, exists := gate.requestIDs[request.RequestID]; !exists {
		return
	}
	delete(gate.requestIDs, request.RequestID)
	if gate.active > 0 {
		gate.active--
	}
	if gate.activeByKind[request.Target.Kind] > 1 {
		gate.activeByKind[request.Target.Kind]--
	} else {
		delete(gate.activeByKind, request.Target.Kind)
	}
	gate.mutatedLocked()
	switch gate.state {
	case StateEntering:
		if gate.active == 0 && gate.entering == 0 {
			gate.transitionLocked(StateHeld)
		}
	case StateReleasing:
		gate.releaseQueuedLocked()
	}
}

// validActionLocked proves that this gate still owns the caller's action.
// Membership is established by the typed ActionLease the caller already holds;
// ADR-0015 section 10 forbids reconstructing it from an identity string, so the
// egress request identity is independent of the action identity.
func (gate *Gate) validActionLocked(action *ActionLease) bool {
	return action != nil &&
		action.gate == gate &&
		gate.actions[action.actionID] == action
}

func (gate *Gate) releaseAction(action *ActionLease) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if action == nil || gate.actions[action.actionID] != action {
		return
	}
	delete(gate.actions, action.actionID)
	if action.beforeEnter && gate.entering > 0 {
		gate.entering--
	}
	gate.mutatedLocked()
	switch gate.state {
	case StateEntering:
		if gate.active == 0 && gate.entering == 0 {
			gate.transitionLocked(StateHeld)
		}
	case StateReleasing:
		gate.releaseQueuedLocked()
	}
}

func (gate *Gate) checkRevisionLocked(expected uint64) error {
	if gate.state == StateUnbound {
		return ErrNotStarted
	}
	if gate.state == StateStopping {
		return ErrCoordinatorStopping
	}
	if expected == 0 || expected != gate.revision {
		return fmt.Errorf(
			"%w: expected=%d actual=%d",
			ErrRevisionConflict,
			expected,
			gate.revision,
		)
	}
	return nil
}

func (gate *Gate) transitionLocked(next State) {
	gate.state = next
	gate.since = time.Now().UTC()
	gate.mutatedLocked()
}

func (gate *Gate) mutatedLocked() {
	gate.revision++
	close(gate.changed)
	gate.changed = make(chan struct{})
}

func (gate *Gate) snapshotLocked() Snapshot {
	activeByKind := make(map[EgressKind]int, len(gate.activeByKind))
	for kind, count := range gate.activeByKind {
		activeByKind[kind] = count
	}
	queuedByKind := make(map[EgressKind]int)
	for _, entry := range gate.queue {
		if entry != nil && entry.state == waiterQueued {
			queuedByKind[entry.request.Target.Kind]++
		}
	}
	return Snapshot{
		State:           gate.state,
		Revision:        gate.revision,
		Since:           gate.since,
		ActiveActions:   len(gate.actions),
		EnteringActions: gate.entering,
		ActiveEgress:    gate.active,
		QueuedRequests:  len(gate.queue),
		HeldBytes:       gate.heldBytes,
		SafeToDisconnect: gate.state == StateHeld &&
			gate.active == 0 &&
			gate.entering == 0,
		ActiveByKind:    activeByKind,
		QueuedByKind:    queuedByKind,
		LastProbeReason: gate.lastProbe,
	}
}

type egressLease struct {
	once    sync.Once
	gate    *Gate
	request AcquireRequest
}

func (lease *egressLease) Release() {
	if lease == nil {
		return
	}
	lease.once.Do(func() {
		lease.gate.release(lease.request)
	})
}

type ProbeFailure struct {
	Reason ProbeReason
	cause  error
}

func NewProbeFailure(reason ProbeReason, cause error) *ProbeFailure {
	if cause == nil {
		cause = errors.New("offline-hold probe failed")
	}
	return &ProbeFailure{Reason: reason, cause: cause}
}

func (failure *ProbeFailure) Error() string {
	return fmt.Sprintf("%s: %v", failure.Reason, failure.cause)
}

func (failure *ProbeFailure) Unwrap() error {
	return failure.cause
}

func probeReasonOf(err error) ProbeReason {
	var failure *ProbeFailure
	if errors.As(err, &failure) {
		switch failure.Reason {
		case ProbeReasonTransportUnavailable,
			ProbeReasonTLSRejected,
			ProbeReasonCanceled,
			ProbeReasonFailed:
			return failure.Reason
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ProbeReasonCanceled
	}
	return ProbeReasonFailed
}

func normalizeProbeTargets(targets []ProbeTarget) ([]ProbeTarget, error) {
	normalized := slices.Clone(targets)
	seen := make(map[targetKey]struct{}, len(normalized))
	for _, target := range normalized {
		if err := validateProbeTarget(target); err != nil {
			return nil, err
		}
		key := targetKey{target: target}
		if _, duplicate := seen[key]; duplicate {
			return nil, ErrInvalidRequest
		}
		seen[key] = struct{}{}
	}
	return normalized, nil
}

func validateAcquireRequest(request AcquireRequest) error {
	if err := validateOpaqueIdentity("request ID", request.RequestID); err != nil {
		return ErrInvalidRequest
	}
	if request.Action == nil {
		return ErrInvalidRequest
	}
	if err := validateProbeTarget(request.Target); err != nil {
		return ErrInvalidRequest
	}
	if request.SizeBytes < 0 {
		return ErrInvalidRequest
	}
	return nil
}

func validateProbeTarget(target ProbeTarget) error {
	if !validEgressKind(target.Kind) {
		return ErrInvalidRequest
	}
	switch target.Transport {
	case ProbeTransportStrictTLS:
		if err := validateOpaqueIdentity(
			"probe TLS server name",
			target.TLSServerName,
		); err != nil {
			return err
		}
	case ProbeTransportLoopbackCleartext:
		if target.Kind != EgressProvider || target.TLSServerName != "" {
			return ErrInvalidRequest
		}
	case ProbeTransportTCP:
		// Reachability only. A raw probe verifies nothing about the peer, so
		// it may not claim an identity, and no outbound that does terminate
		// TLS may use it to skip verification.
		if target.Kind != EgressBlindTunnel || target.TLSServerName != "" {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	for _, field := range []struct {
		label string
		value string
	}{
		{label: "probe target reference", value: target.TargetRef},
		{label: "probe network origin", value: target.NetworkOrigin},
		{label: "probe HTTP authority", value: target.HTTPAuthority},
	} {
		if err := validateOpaqueIdentity(field.label, field.value); err != nil {
			return err
		}
	}
	if (target.PlanRevision == 0) != (target.PlanDigest == "") {
		return ErrInvalidRequest
	}
	if target.PlanDigest != "" {
		if err := validatePlanDigest(target.PlanDigest); err != nil {
			return err
		}
	}
	return nil
}

func probeTargetSortKey(target ProbeTarget) string {
	return strings.Join([]string{
		string(target.Kind),
		string(target.Transport),
		target.TargetRef,
		target.NetworkOrigin,
		target.HTTPAuthority,
		target.TLSServerName,
		fmt.Sprintf("%020d", target.PlanRevision),
		target.PlanDigest,
	}, "\x00")
}

func validatePlanDigest(value string) error {
	if len(value) != 64 || strings.ToLower(value) != value {
		return fmt.Errorf("%w: probe plan digest is invalid", ErrInvalidRequest)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("%w: probe plan digest is invalid", ErrInvalidRequest)
	}
	return nil
}

func validEgressKind(kind EgressKind) bool {
	switch kind {
	case EgressProvider,
		EgressOpaque,
		EgressAuxiliary,
		EgressPlugin,
		EgressUpdate,
		EgressBlindTunnel:
		return true
	default:
		return false
	}
}

func validateOpaqueIdentity(label, value string) error {
	if value == "" ||
		len(value) > maxOpaqueIdentityBytes ||
		!utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s is invalid", ErrInvalidRequest, label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: %s contains a control character", ErrInvalidRequest, label)
		}
	}
	return nil
}
