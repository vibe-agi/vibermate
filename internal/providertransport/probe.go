package providertransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"time"

	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

type TLSProber struct {
	dialer  contextDialer
	roots   *x509.CertPool
	timeout time.Duration
}

var _ offlinehold.Prober = (*TLSProber)(nil)

const DefaultTLSProbeTargetTimeout = 10 * time.Second

func NewTLSProber() (*TLSProber, error) {
	return newTLSProber(
		&net.Dialer{},
		nil,
	)
}

func newTLSProber(
	dialer contextDialer,
	roots *x509.CertPool,
) (*TLSProber, error) {
	return newTLSProberWithTimeout(
		dialer,
		roots,
		DefaultTLSProbeTargetTimeout,
	)
}

func newTLSProberWithTimeout(
	dialer contextDialer,
	roots *x509.CertPool,
	timeout time.Duration,
) (*TLSProber, error) {
	if dialer == nil || timeout <= 0 {
		return nil, errors.New("TLS probe dependencies are incomplete")
	}
	return &TLSProber{
		dialer:  dialer,
		roots:   roots,
		timeout: timeout,
	}, nil
}

func (prober *TLSProber) Probe(
	ctx context.Context,
	request offlinehold.ProbeRequest,
) error {
	if ctx == nil || len(request.Targets) == 0 {
		return offlinehold.NewProbeFailure(
			offlinehold.ProbeReasonFailed,
			errors.New("TLS probe request is invalid"),
		)
	}
	for _, reference := range request.Targets {
		if reference.Kind != offlinehold.EgressProvider {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				errors.New("TLS prober received a non-provider target"),
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
		endpointAuthority, err := target.endpointAuthority()
		if err != nil {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				err,
			)
		}
		targetContext, cancel := context.WithTimeout(ctx, prober.timeout)
		raw, err := prober.dialer.DialContext(
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
			ServerName: target.tlsServerName,
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
		reference.AccessRevision == 0 ||
		reference.PlanHash == "" {
		return Target{}, errors.New("provider TLS probe target identity is incomplete")
	}
	if err := reference.Validate(); err != nil {
		return Target{}, err
	}
	parsed, err := url.Parse(reference.NetworkOrigin)
	if err != nil {
		return Target{}, err
	}
	target := Target{
		origin:        reference.NetworkOrigin,
		scheme:        parsed.Scheme,
		httpAuthority: reference.HTTPAuthority,
		tlsServerName: reference.TLSServerName,
		basePath:      parsed.EscapedPath(),
	}
	if err := target.validate(); err != nil {
		return Target{}, err
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
