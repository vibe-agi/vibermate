package blindtunnel

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

// DefaultReachabilityTimeout bounds one blind target. It is a connect, not a
// handshake and not a request, so it is short.
const DefaultReachabilityTimeout = 5 * time.Second

type probeDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// ReachabilityProber answers whether a blind target can be reached.
//
// It exists because resume had no answer for a queued tunnel at all. The gate
// covers ordinary blind tunnels and other proxy egress (design 20 §4.1), so
// a queued CONNECT
// appears among the targets resume must clear, and the runtime's probe router
// had no arm for it — so one tunnel in the queue returned the whole gate to
// `held` and released nothing, including every provider request waiting behind
// it.
//
// Reachability is all a tunnel can establish, and the design says so: where
// there is no side-effect-free application health endpoint, a probe may prove
// reachability only, as far as TLS or transport readiness, and the product
// must display that level
// honestly (design 20 §7). A tunnel forwards bytes it never interprets, so
// there is no protocol to speak and no server name to verify — which is why
// `ProbeTransportTCP` was defined for exactly this case and why the
// coordinator refuses to let it carry a TLS server name.
//
// What it does not prove: that the peer is who anyone expected, that anything
// beyond the first TCP segment will succeed, or that the tunnel will still be
// reachable when the released request actually dials. The planned gate never
// claimed to replace the pre-semantic Hold.
type ReachabilityProber struct {
	dialer  probeDialer
	timeout time.Duration
}

func NewReachabilityProber() (*ReachabilityProber, error) {
	return newReachabilityProber(&net.Dialer{}, DefaultReachabilityTimeout)
}

func newReachabilityProber(
	dialer probeDialer,
	timeout time.Duration,
) (*ReachabilityProber, error) {
	if dialer == nil || timeout <= 0 {
		return nil, errors.New("blind tunnel reachability probe dialer is missing")
	}
	return &ReachabilityProber{dialer: dialer, timeout: timeout}, nil
}

func (prober *ReachabilityProber) Probe(
	ctx context.Context,
	request offlinehold.ProbeRequest,
) error {
	if prober == nil || prober.dialer == nil || ctx == nil ||
		len(request.Targets) == 0 {
		return offlinehold.NewProbeFailure(
			offlinehold.ProbeReasonFailed,
			errors.New("blind tunnel reachability probe request is invalid"),
		)
	}
	for _, target := range request.Targets {
		if target.Kind != offlinehold.EgressBlindTunnel {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				errors.New("blind tunnel prober received an unsupported egress kind"),
			)
		}
		if err := target.Validate(); err != nil {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				err,
			)
		}
		// A blind target is frozen from a CONNECT authority and nothing else.
		// An Access identity on it would mean something upstream had resolved
		// a plan for a connection this product never interprets.
		if target.AccessRevision != 0 || target.PlanHash != "" {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				errors.New("blind tunnel probe unexpectedly contains an Access plan identity"),
			)
		}
		if target.NetworkOrigin != target.HTTPAuthority ||
			target.TargetRef != target.HTTPAuthority {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				errors.New("blind tunnel probe network identities disagree"),
			)
		}
		if err := prober.reach(ctx, target.HTTPAuthority); err != nil {
			return err
		}
	}
	return nil
}

func (prober *ReachabilityProber) reach(
	ctx context.Context,
	authority string,
) error {
	host, port, err := net.SplitHostPort(authority)
	if err != nil || host == "" || port == "" {
		return offlinehold.NewProbeFailure(
			offlinehold.ProbeReasonFailed,
			errors.New("blind tunnel probe authority is not host and port"),
		)
	}
	targetContext, cancel := context.WithTimeout(ctx, prober.timeout)
	defer cancel()

	connection, dialErr := prober.dialer.DialContext(
		targetContext,
		"tcp",
		authority,
	)
	if dialErr != nil {
		// Unreachable is the ordinary answer while a network is still down,
		// and it is what returns the gate to `held` with the queue intact.
		return offlinehold.NewProbeFailure(
			offlinehold.ProbeReasonTransportUnavailable,
			dialErr,
		)
	}
	// Nothing is written and nothing is read. Sending anything would be this
	// product interpreting a tunnel it promised not to interpret.
	return connection.Close()
}
