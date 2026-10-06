package blindtunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

// DefaultDialTimeout bounds one blind dial.
const DefaultDialTimeout = 15 * time.Second

// Dialer opens the upstream side of a blind tunnel. It goes through the same
// egress admission every other outbound uses, so a tunnel cannot become the one
// path that ignores a planned offline hold.
type Dialer struct {
	coordinator offlinehold.Coordinator
	builder     *egressnetwork.Builder
}

func NewDialer(coordinator offlinehold.Coordinator) (*Dialer, error) {
	if coordinator == nil {
		return nil, errors.New("blind tunnel requires an egress coordinator")
	}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{
		BaseDialer: &net.Dialer{Timeout: DefaultDialTimeout},
	})
	if err != nil {
		return nil, err
	}
	return NewDialerWithBuilder(coordinator, builder)
}

// NewDialerWithBuilder composes a trusted transport dependency. It grants no
// target permission and selects no policy; each Dial supplies those facts.
func NewDialerWithBuilder(coordinator offlinehold.Coordinator, builder *egressnetwork.Builder) (*Dialer, error) {
	if coordinator == nil || builder == nil {
		return nil, errors.New("blind tunnel dependencies are incomplete")
	}
	return &Dialer{
		coordinator: coordinator,
		builder:     builder,
	}, nil
}

// DialRequest is the frozen identity of one blind dial. There is no path,
// header, or body here because a blind tunnel never learns any.
type DialRequest struct {
	RequestID string
	Action    *offlinehold.ActionLease
	Authority string
	Host      string
	Port      uint16
	Egress    egressprofile.Resolved
}

// Dial acquires the egress lease before the first packet and returns the
// upstream connection together with the lease that must be released when the
// tunnel ends.
func (dialer *Dialer) Dial(
	ctx context.Context,
	request DialRequest,
) (net.Conn, offlinehold.Lease, error) {
	if dialer == nil || ctx == nil {
		return nil, nil, errors.New("blind tunnel dialer or context is nil")
	}
	if request.Authority == "" || request.Host == "" || request.Port == 0 {
		return nil, nil, errors.New("blind tunnel target is incomplete")
	}
	access := egressnetwork.EffectiveTargetAccess(ctx)
	ctx = egressnetwork.WithTargetAccess(ctx, access)
	if err := request.Egress.Validate(); err != nil {
		return nil, nil, fmt.Errorf("freeze blind tunnel egress: %w", err)
	}
	targetDialer, err := dialer.builder.Dialer(request.Egress.Profile().Policy)
	if err != nil {
		return nil, nil, fmt.Errorf("build blind tunnel egress: %w", err)
	}
	target := offlinehold.ProbeTarget{
		Kind:          offlinehold.EgressBlindTunnel,
		Transport:     offlinehold.ProbeTransportTCP,
		TargetRef:     request.Authority,
		NetworkOrigin: request.Authority,
		HTTPAuthority: request.Authority,
		EgressPolicy:  request.Egress.Profile().Policy,
		TargetAccess:  access,
	}
	if err := target.Validate(); err != nil {
		return nil, nil, fmt.Errorf("freeze blind tunnel target: %w", err)
	}
	lease, err := dialer.coordinator.Acquire(ctx, offlinehold.AcquireRequest{
		RequestID: request.RequestID,
		Action:    request.Action,
		Target:    target,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("acquire blind tunnel lease: %w", err)
	}
	// The bound covers resolver and SOCKS negotiation too, but not a planned
	// Offline wait or the lifetime of an already established tunnel.
	dialContext, cancel := context.WithTimeout(ctx, DefaultDialTimeout)
	defer cancel()
	connection, err := targetDialer.DialContext(dialContext, "tcp", request.Authority)
	if err != nil {
		lease.Release()
		return nil, nil, fmt.Errorf("dial blind tunnel target: %w", err)
	}
	return connection, lease, nil
}
