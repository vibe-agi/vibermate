// Package offlinehold defines the mandatory external-egress admission seam.
//
// M0 deliberately provides no production coordinator implementation. A
// ProductRuntime cannot start without an explicitly supplied coordinator, and
// future external dialers must acquire a typed lease through this contract.
package offlinehold

import "context"

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

// AcquireRequest describes an external egress before any external byte is
// written. TargetRef is an opaque, non-secret route identity.
type AcquireRequest struct {
	Kind      EgressKind
	TargetRef string
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
	Start(context.Context, RuntimeBinding) error
	Acquire(context.Context, AcquireRequest) (Lease, error)
	BeginShutdown()
	Drain(context.Context) error
}
