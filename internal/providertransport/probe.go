package providertransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

// ProviderProber probes a frozen provider target without sending HTTP headers,
// credentials, or a request body. Remote targets complete strict TLS; the
// explicit local/private cleartext exception completes a constrained TCP peer
// check before any HTTP header, credential, or body can be written.
type ProviderProber struct {
	dialers egressDialerBuilder
	roots   *x509.CertPool
	timeout time.Duration
}

var _ offlinehold.Prober = (*ProviderProber)(nil)

const DefaultProviderProbeTargetTimeout = 10 * time.Second

func NewProviderProber() (*ProviderProber, error) {
	return newProviderProber(
		&net.Dialer{},
		nil,
	)
}

func newProviderProber(
	dialer contextDialer,
	roots *x509.CertPool,
) (*ProviderProber, error) {
	return newProviderProberWithTimeout(
		dialer,
		roots,
		DefaultProviderProbeTargetTimeout,
	)
}

func newProviderProberWithTimeout(
	dialer contextDialer,
	roots *x509.CertPool,
	timeout time.Duration,
) (*ProviderProber, error) {
	if dialer == nil || timeout <= 0 {
		return nil, errors.New("provider probe dependencies are incomplete")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	dialers, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{
		BaseDialer: dialer, TLSClientConfig: tlsConfig,
	})
	if err != nil {
		return nil, err
	}
	return &ProviderProber{
		dialers: dialers,
		roots:   roots,
		timeout: timeout,
	}, nil
}

func (prober *ProviderProber) Probe(
	ctx context.Context,
	request offlinehold.ProbeRequest,
) error {
	if prober == nil || prober.dialers == nil ||
		ctx == nil || len(request.Targets) == 0 {
		return offlinehold.NewProbeFailure(
			offlinehold.ProbeReasonFailed,
			errors.New("provider probe request is invalid"),
		)
	}
	for _, reference := range request.Targets {
		if reference.Kind != offlinehold.EgressProvider {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				errors.New("provider prober received a non-provider target"),
			)
		}
		target, err := targetFromProbe(reference)
		if err != nil {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				err,
			)
		}
		if err := target.validate(); err != nil {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				err,
			)
		}
		egressPolicy, err := reference.EgressPolicy.Normalize()
		if err != nil {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				err,
			)
		}
		endpointAuthority, err := target.endpointAuthority()
		if err != nil {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				err,
			)
		}
		if isCleartextProviderTransport(target.TransportKind()) &&
			egressPolicy.Proxy.Kind != egressnetwork.ProxyDirect {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				errors.New("cleartext provider probe cannot use a secondary proxy"),
			)
		}
		dialer, err := prober.dialers.Dialer(egressPolicy)
		if err != nil {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				err,
			)
		}
		targetContext, cancel := context.WithTimeout(ctx, prober.timeout)
		raw, err := dialer.DialContext(
			targetContext,
			"tcp",
			endpointAuthority,
		)
		if err != nil {
			cancel()
			return offlinehold.NewProbeFailure(
				classifyProbeTransportFailure(err),
				err,
			)
		}
		if isCleartextProviderTransport(target.TransportKind()) {
			peerErr := validateCleartextPeer(raw.RemoteAddr(), target)
			closeErr := raw.Close()
			contextErr := targetContext.Err()
			cancel()
			if contextErr != nil {
				return offlinehold.NewProbeFailure(
					classifyProbeContextFailure(ctx, contextErr),
					contextErr,
				)
			}
			if peerErr != nil {
				return offlinehold.NewProbeFailure(
					offlinehold.ProbeReasonTransportUnavailable,
					peerErr,
				)
			}
			if closeErr != nil {
				return offlinehold.NewProbeFailure(
					classifyProbeTransportFailure(closeErr),
					closeErr,
				)
			}
			continue
		}
		deadline, available := targetContext.Deadline()
		if !available {
			_ = raw.Close()
			cancel()
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				errors.New("TLS probe deadline is unavailable"),
			)
		}
		if err := raw.SetDeadline(deadline); err != nil {
			_ = raw.Close()
			cancel()
			return offlinehold.NewProbeFailure(
				classifyProbeTransportFailure(err),
				err,
			)
		}
		tlsConnection := tls.Client(raw, &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    prober.roots,
			ServerName: target.TLSServerName(),
		})
		handshakeErr := tlsConnection.HandshakeContext(targetContext)
		closeErr := raw.Close()
		contextErr := targetContext.Err()
		cancel()
		if contextErr != nil {
			return offlinehold.NewProbeFailure(
				classifyProbeContextFailure(ctx, contextErr),
				contextErr,
			)
		}
		if handshakeErr != nil {
			return offlinehold.NewProbeFailure(
				classifyTLSProbeFailure(handshakeErr),
				handshakeErr,
			)
		}
		if closeErr != nil {
			return offlinehold.NewProbeFailure(
				classifyProbeTransportFailure(closeErr),
				closeErr,
			)
		}
	}
	return nil
}

func targetFromProbe(reference offlinehold.ProbeTarget) (Target, error) {
	if reference.Kind != offlinehold.EgressProvider ||
		reference.PlanRevision == 0 ||
		reference.PlanDigest == "" {
		return Target{}, errors.New("provider probe target identity is incomplete")
	}
	if err := reference.Validate(); err != nil {
		return Target{}, err
	}
	origin, err := originidentity.ParseProviderOrigin(reference.NetworkOrigin)
	if err != nil {
		return Target{}, err
	}
	target, err := NewTarget(origin)
	if err != nil {
		return Target{}, err
	}
	if target.HTTPAuthority() != reference.HTTPAuthority ||
		target.TLSServerName() != reference.TLSServerName {
		return Target{}, errors.New("provider probe target identity changed")
	}
	if err := target.validate(); err != nil {
		return Target{}, err
	}
	probeTransport, err := target.probeTransportKind()
	if err != nil {
		return Target{}, err
	}
	if probeTransport != reference.Transport {
		return Target{}, errors.New(
			"provider probe transport identity changed",
		)
	}
	return target, nil
}

func classifyProbeContextFailure(
	parent context.Context,
	err error,
) offlinehold.ProbeReason {
	if parent != nil && parent.Err() != nil {
		return offlinehold.ProbeReasonCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return offlinehold.ProbeReasonTransportUnavailable
	}
	return classifyProbeTransportFailure(err)
}

func classifyTLSProbeFailure(err error) offlinehold.ProbeReason {
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return offlinehold.ProbeReasonCanceled
	}
	var certificateInvalid x509.CertificateInvalidError
	var hostnameInvalid x509.HostnameError
	var unknownAuthority x509.UnknownAuthorityError
	var recordHeader tls.RecordHeaderError
	if errors.As(err, &certificateInvalid) ||
		errors.As(err, &hostnameInvalid) ||
		errors.As(err, &unknownAuthority) ||
		errors.As(err, &recordHeader) {
		return offlinehold.ProbeReasonTLSRejected
	}
	return offlinehold.ProbeReasonTransportUnavailable
}

func classifyProbeTransportFailure(err error) offlinehold.ProbeReason {
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return offlinehold.ProbeReasonCanceled
	}
	return offlinehold.ProbeReasonTransportUnavailable
}
