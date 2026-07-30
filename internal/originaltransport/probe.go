package originaltransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

type probeDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

// TLSProber performs the sole no-prompt connectivity check for frozen
// original-origin opaque and auxiliary requests while normal admission stays
// closed. It sends no HTTP request and uses no credential.
type TLSProber struct {
	dialer  probeDialer
	roots   *x509.CertPool
	timeout time.Duration
}

var _ offlinehold.Prober = (*TLSProber)(nil)

const DefaultTLSProbeTargetTimeout = 10 * time.Second

func NewTLSProber() (*TLSProber, error) {
	return newTLSProber(&net.Dialer{}, nil)
}

func newTLSProber(
	dialer probeDialer,
	roots *x509.CertPool,
) (*TLSProber, error) {
	return newTLSProberWithTimeout(
		dialer,
		roots,
		DefaultTLSProbeTargetTimeout,
	)
}

func newTLSProberWithTimeout(
	dialer probeDialer,
	roots *x509.CertPool,
	timeout time.Duration,
) (*TLSProber, error) {
	if dialer == nil || timeout <= 0 {
		return nil, errors.New("original-origin TLS probe dialer is missing")
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
	if prober == nil || ctx == nil || len(request.Targets) == 0 {
		return offlinehold.NewProbeFailure(
			offlinehold.ProbeReasonFailed,
			errors.New("original-origin TLS probe request is invalid"),
		)
	}
	for _, reference := range request.Targets {
		switch reference.Kind {
		case offlinehold.EgressOpaque, offlinehold.EgressAuxiliary:
		default:
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				errors.New("original-origin TLS prober received an unsupported egress kind"),
			)
		}
		if err := reference.Validate(); err != nil {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				err,
			)
		}
		if reference.AccessRevision != 0 || reference.PlanHash != "" {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				errors.New("original-origin probe unexpectedly contains an Access plan identity"),
			)
		}
		origin, err := access.NewClientOrigin(reference.NetworkOrigin)
		if err != nil {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				err,
			)
		}
		if origin.HTTPAuthority() != reference.HTTPAuthority ||
			origin.TLSServerName() != reference.TLSServerName ||
			origin.String() != reference.TargetRef {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				errors.New("original-origin probe network identities disagree"),
			)
		}
		targetContext, cancel := context.WithTimeout(ctx, prober.timeout)
		raw, err := prober.dialer.DialContext(
			targetContext,
			"tcp",
			origin.EndpointAuthority(),
		)
		if err != nil {
			cancel()
			return offlinehold.NewProbeFailure(
				probeFailureReason(err),
				err,
			)
		}
		deadline, available := targetContext.Deadline()
		if !available {
			_ = raw.Close()
			cancel()
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				errors.New("original-origin TLS probe deadline is unavailable"),
			)
		}
		if err := raw.SetDeadline(deadline); err != nil {
			_ = raw.Close()
			cancel()
			return offlinehold.NewProbeFailure(
				probeFailureReason(err),
				err,
			)
		}
		connection := tls.Client(raw, &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    prober.roots,
			ServerName: origin.TLSServerName(),
		})
		handshakeErr := connection.HandshakeContext(targetContext)
		closeErr := raw.Close()
		contextErr := targetContext.Err()
		cancel()
		if contextErr != nil {
			return offlinehold.NewProbeFailure(
				probeContextFailureReason(ctx, contextErr),
				contextErr,
			)
		}
		if handshakeErr != nil {
			return offlinehold.NewProbeFailure(
				probeTLSFailureReason(handshakeErr),
				handshakeErr,
			)
		}
		if closeErr != nil {
			return offlinehold.NewProbeFailure(
				probeFailureReason(closeErr),
				closeErr,
			)
		}
	}
	return nil
}

func probeContextFailureReason(
	parent context.Context,
	err error,
) offlinehold.ProbeReason {
	if parent != nil && parent.Err() != nil {
		return offlinehold.ProbeReasonCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return offlinehold.ProbeReasonTransportUnavailable
	}
	return probeFailureReason(err)
}

func probeTLSFailureReason(err error) offlinehold.ProbeReason {
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

func probeFailureReason(err error) offlinehold.ProbeReason {
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return offlinehold.ProbeReasonCanceled
	}
	return offlinehold.ProbeReasonTransportUnavailable
}
