// Package offlinehold owns the mandatory external-egress admission seam and
// the production planned-offline state machine.
package offlinehold

import (
	"context"
	"time"
)

// EgressKind identifies every approved class of external network activity.
type EgressKind string

const (
	EgressProvider    EgressKind = "provider"
	EgressOpaque      EgressKind = "opaque"
	EgressAuxiliary   EgressKind = "auxiliary"
	EgressPlugin      EgressKind = "plugin"
	EgressUpdate      EgressKind = "update"
	EgressBlindTunnel EgressKind = "blind_tunnel"
)

// RuntimeBinding fences coordinator state to one process-lifetime runtime
// incarnation.
type RuntimeBinding struct {
	InstanceID string
}

// ActionRequest identifies one logical data-plane action before it resolves
// configuration or reaches an external transport.
type ActionRequest struct {
	ActionID string
}

// ActionLease serializes data-plane admission with Enter. A lease admitted
// while Online may continue through the Entering boundary; a later lease may
// only acquire queued egress until Resume succeeds.
type ActionLease struct {
	gate        *Gate
	actionID    string
	beforeEnter bool
	release     func()
}

func (lease *ActionLease) Release() {
	if lease == nil || lease.release == nil {
		return
	}
	lease.release()
}

// AcquireRequest describes an external egress before any external byte is
// written. Target is the complete frozen, non-secret probe identity used by the
// same request after Resume; Action proves when the logical operation entered.
type AcquireRequest struct {
	RequestID string
	Action    *ActionLease
	Target    ProbeTarget
	SizeBytes int64
}

// Lease accounts for one admitted external egress. Release must be idempotent.
type Lease interface {
	Release()
}

// Coordinator is the mandatory runtime-owned egress admission boundary.
//
// Start binds one runtime incarnation and must release any partial ownership
// before returning an error. BeginShutdown atomically closes new admission,
// and Drain waits for previously admitted leases while honoring its context.
// Acquire is present now so future production dialers cannot invent a second
// boundary.
type Coordinator interface {
	StateReader
	Start(context.Context, RuntimeBinding) error
	BeginAction(context.Context, ActionRequest) (*ActionLease, error)
	Acquire(context.Context, AcquireRequest) (Lease, error)
	BeginShutdown()
	Drain(context.Context) error
}

// ActionAdmission is the narrow boundary required at the start of a logical
// Exchange. It does not expose egress acquisition or Hold control.
type ActionAdmission interface {
	BeginAction(context.Context, ActionRequest) (*ActionLease, error)
}

type State string

const (
	StateUnbound   State = "unbound"
	StateOnline    State = "online"
	StateEntering  State = "entering"
	StateHeld      State = "held"
	StateProbing   State = "probing"
	StateReleasing State = "releasing"
	StateStopping  State = "stopping"
)

type ProbeReason string

const (
	ProbeReasonTransportUnavailable ProbeReason = "transport_unavailable"
	ProbeReasonTLSRejected          ProbeReason = "tls_rejected"
	ProbeReasonCanceled             ProbeReason = "canceled"
	ProbeReasonFailed               ProbeReason = "probe_failed"
)

type ProbeTransportKind string

const (
	ProbeTransportStrictTLS         ProbeTransportKind = "strict_tls"
	ProbeTransportLoopbackCleartext ProbeTransportKind = "loopback_cleartext"
	ProbeTransportPrivateCleartext  ProbeTransportKind = "private_cleartext"
	// ProbeTransportTCP belongs to blind tunnelling. A tunnel forwards bytes
	// it never interprets, so reachability is all it can establish: there is
	// no TLS server name to verify and no protocol to speak.
	ProbeTransportTCP ProbeTransportKind = "tcp"
)

type ProbeTarget struct {
	Kind          EgressKind
	Transport     ProbeTransportKind
	TargetRef     string
	NetworkOrigin string
	HTTPAuthority string
	TLSServerName string
	PlanRevision  uint64
	PlanDigest    string
}

func (target ProbeTarget) Validate() error {
	return validateProbeTarget(target)
}

type ProbeRequest struct {
	Targets []ProbeTarget
}

type Prober interface {
	Probe(context.Context, ProbeRequest) error
}

type ResumeRequest struct {
	Targets []ProbeTarget
}

// Snapshot is the language-independent, immutable coordinator status.
type Snapshot struct {
	State            State              `json:"state"`
	Revision         uint64             `json:"revision"`
	Since            time.Time          `json:"since"`
	ActiveActions    int                `json:"activeActions"`
	EnteringActions  int                `json:"enteringActions"`
	ActiveEgress     int                `json:"activeEgress"`
	QueuedRequests   int                `json:"queuedRequests"`
	HeldBytes        int64              `json:"heldBytes"`
	SafeToDisconnect bool               `json:"safeToDisconnect"`
	ActiveByKind     map[EgressKind]int `json:"activeByKind"`
	QueuedByKind     map[EgressKind]int `json:"queuedByKind"`
	LastProbeReason  ProbeReason        `json:"lastProbeReason,omitempty"`
}

type StateReader interface {
	Snapshot() Snapshot
}

// Controller is the Desktop planned-offline control boundary. Revision checks
// make UI retries explicit rather than silently overwriting a newer state.
type Controller interface {
	StateReader
	Enter(context.Context, uint64) (Snapshot, error)
	Resume(context.Context, uint64, ResumeRequest, Prober) (Snapshot, error)
}

// PendingProbeTargetReader exposes only the frozen kind/reference pairs needed
// by the runtime-owned resume prober. It does not expose request bodies,
// ordering identities, or a mutation surface to control clients.
type PendingProbeTargetReader interface {
	PendingProbeTargets() []ProbeTarget
}

type RuntimeCoordinator interface {
	Coordinator
	Controller
	PendingProbeTargetReader
}
