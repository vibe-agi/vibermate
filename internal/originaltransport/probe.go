package originaltransport

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

type probeDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

// TLSProber performs the no-prompt connectivity check for frozen
// original-origin opaque and auxiliary requests while normal admission stays
// closed. HTTPS verifies TLS; supported HTTP verifies only a private TCP peer.
// It sends no HTTP request and uses no credential.
type TLSProber struct {
	dialers egressDialerBuilder
	roots   *x509.CertPool
	timeout time.Duration
}

var _ offlinehold.Prober = (*TLSProber)(nil)

const DefaultTLSProbeTargetTimeout = 10 * time.Second

func NewTLSProber() (*TLSProber, error) {
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: &net.Dialer{}})
	if err != nil {
		return nil, err
	}
	return NewTLSProberWithBuilder(builder, nil)
}

// Trusted in-process composition only; it grants no target or send authority.
func NewTLSProberWithBuilder(builder *egressnetwork.Builder, roots *x509.CertPool) (*TLSProber, error) {
	if builder == nil {
		return nil, errors.New("trusted TLS probe builder is missing")
	}
	if roots != nil {
		roots = roots.Clone()
	}
	return &TLSProber{dialers: builder, roots: roots, timeout: DefaultTLSProbeTargetTimeout}, nil
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
	dialers, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{
		BaseDialer: dialer,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		},
	})
	if err != nil {
		return nil, err
	}
	return &TLSProber{
		dialers: dialers,
		roots:   roots,
		timeout: timeout,
	}, nil
}

func (prober *TLSProber) Probe(
	ctx context.Context,
	request offlinehold.ProbeRequest,
) error {
	if prober == nil || prober.dialers == nil || ctx == nil || len(request.Targets) == 0 {
		return offlinehold.NewProbeFailure(
			offlinehold.ProbeReasonFailed,
			errors.New("original-origin TLS probe request is invalid"),
		)
	}
	for _, reference := range request.Targets {
		probeContext := ctx
		switch reference.Kind {
		case offlinehold.EgressOpaque, offlinehold.EgressAuxiliary:
			if reference.TargetAccess != egressnetwork.TargetAccessPublicOnly && reference.TargetAccess != egressnetwork.TargetAccessOwnerConfigured {
				return offlinehold.NewProbeFailure(offlinehold.ProbeReasonFailed, errors.New("original probe target access is missing or invalid"))
			}
			probeContext = egressnetwork.WithTargetAccess(ctx, reference.TargetAccess)
		case offlinehold.EgressUpdate:
			if reference.NetworkOrigin != "https://api.github.com" {
				return offlinehold.NewProbeFailure(offlinehold.ProbeReasonFailed, errors.New("update probe origin is not the closed release source"))
			}
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
		if reference.PlanRevision != 0 || reference.PlanDigest != "" {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				errors.New("original-origin probe unexpectedly contains a routed plan identity"),
			)
		}
		origin, err := originidentity.ParseProviderOrigin(reference.NetworkOrigin)
		if err != nil || origin.BasePath() != "" {
			return offlinehold.NewProbeFailure(offlinehold.ProbeReasonFailed, errors.New("original probe origin is invalid"))
		}
		if origin.HTTPAuthority() != reference.HTTPAuthority ||
			origin.String() != reference.TargetRef {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				errors.New("original-origin probe network identities disagree"),
			)
		}
		expectedTransport := offlinehold.ProbeTransportStrictTLS
		expectedServerName := origin.Host()
		switch origin.Transport() {
		case originidentity.ProviderTransportLoopbackCleartext:
			expectedTransport, expectedServerName = offlinehold.ProbeTransportLoopbackCleartext, ""
		case originidentity.ProviderTransportPrivateCleartext:
			expectedTransport, expectedServerName = offlinehold.ProbeTransportPrivateCleartext, ""
		}
		if reference.Transport != expectedTransport || reference.TLSServerName != expectedServerName {
			return offlinehold.NewProbeFailure(offlinehold.ProbeReasonFailed, errors.New("original probe transport identities disagree"))
		}
		egressPolicy, err := reference.EgressPolicy.Normalize()
		if err != nil {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				err,
			)
		}
		if origin.Scheme() == "http" && egressPolicy.Proxy.Kind != egressnetwork.ProxyDirect {
			return offlinehold.NewProbeFailure(offlinehold.ProbeReasonFailed, errors.New("cleartext original probe cannot use a secondary proxy"))
		}
		dialer, err := prober.dialers.Dialer(egressPolicy)
		if err != nil {
			return offlinehold.NewProbeFailure(
				offlinehold.ProbeReasonFailed,
				err,
			)
		}
		targetContext, cancel := context.WithTimeout(probeContext, prober.timeout)
		raw, err := dialer.DialContext(
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
		if origin.Scheme() == "http" {
			peerErr := validateCleartextPeer(raw.RemoteAddr(), origin)
			closeErr := raw.Close()
			cancel()
			if peerErr != nil {
				return offlinehold.NewProbeFailure(offlinehold.ProbeReasonFailed, peerErr)
			}
			if closeErr != nil {
				return offlinehold.NewProbeFailure(probeFailureReason(closeErr), closeErr)
			}
			continue
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
			ServerName: origin.Host(),
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
