package transportprofile

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"slices"
	"time"

	utls "github.com/refraction-networking/utls"
	"github.com/vibe-agi/vibermate/internal/access"
)

const defaultTLSHandshakeTimeout = 10 * time.Second

var (
	ErrTransportPlanInvalid = errors.New("transport fingerprint plan is invalid")
	ErrNoTransportProfile   = errors.New("no transport fingerprint profile succeeded")
)

type ContextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type ConnectorOptions struct {
	Dialer           ContextDialer
	RootCAs          *x509.CertPool
	HandshakeTimeout time.Duration
}

type Connector struct {
	dialer           ContextDialer
	rootCAs          *x509.CertPool
	handshakeTimeout time.Duration
}

func NewConnector(options ConnectorOptions) (*Connector, error) {
	if options.Dialer == nil {
		return nil, errors.New("transport profile TCP dialer is nil")
	}
	if options.HandshakeTimeout == 0 {
		options.HandshakeTimeout = defaultTLSHandshakeTimeout
	}
	if options.HandshakeTimeout < 0 {
		return nil, errors.New(
			"transport profile TLS handshake timeout must be positive",
		)
	}
	var roots *x509.CertPool
	if options.RootCAs != nil {
		roots = options.RootCAs.Clone()
	}
	return &Connector{
		dialer:           options.Dialer,
		rootCAs:          roots,
		handshakeTimeout: options.HandshakeTimeout,
	}, nil
}

type ConnectRequest struct {
	Network       string
	Address       string
	TLSServerName string
	Plan          access.CompiledTransportFingerprintPlan
	Observation   Observation
}

func (connector *Connector) Connect(
	ctx context.Context,
	request ConnectRequest,
) (net.Conn, Evidence, error) {
	if ctx == nil {
		return nil, Evidence{}, errors.New(
			"transport profile connection context is nil",
		)
	}
	if connector == nil || connector.dialer == nil {
		return nil, Evidence{}, errors.New(
			"transport profile connector is not initialized",
		)
	}
	if request.Network != "tcp" &&
		request.Network != "tcp4" &&
		request.Network != "tcp6" {
		return nil, Evidence{}, errors.New(
			"transport profile network is unsupported",
		)
	}
	if request.Address == "" || request.TLSServerName == "" {
		return nil, Evidence{}, errors.New(
			"transport profile target identity is incomplete",
		)
	}
	requested := request.Plan.Requested()
	evidence := newEvidence(requested, request.Observation)
	templates := append(
		[]access.TransportFingerprintTemplate{requested},
		request.Plan.Fallbacks()...,
	)
	if err := validateTemplates(templates); err != nil {
		return nil, evidence, err
	}

	var failures []error
	fallbackReason := FallbackNone
	for index, template := range templates {
		evidence.fallbackChain = append(
			evidence.fallbackChain,
			profileEvidence(template),
		)
		if index > 0 && fallbackReason == FallbackNone {
			fallbackReason = FallbackClientHelloUnsupported
		}
		switch template.Source() {
		case access.TransportFingerprintObservedClient:
			spec, offered, reason, err := prepareObservedSpec(
				request.Observation,
				template,
				request.TLSServerName,
			)
			if err != nil {
				failures = append(failures, err)
				fallbackReason = reason
				continue
			}
			connection, negotiated, err := connector.connectCustom(
				ctx,
				request,
				spec,
				offered,
			)
			if err == nil {
				evidence.effective = profileEvidence(template)
				evidence.fallbackReason = fallbackReason
				evidence.upstreamOfferedALPN = offered
				evidence.upstreamNegotiatedALPN = negotiated
				evidence.httpTransport = template.HTTPTransport()
				return connection, evidence, nil
			}
			failures = append(failures, err)
			if strictVerificationFailure(err) || ctx.Err() != nil {
				return nil, evidence, errors.Join(
					ErrNoTransportProfile,
					errors.Join(failures...),
				)
			}
			fallbackReason = FallbackObservedTLSHandshakeRejected
		case access.TransportFingerprintCaptured:
			spec, offered, err := prepareCapturedSpec(
				template,
				request.TLSServerName,
			)
			if err != nil {
				failures = append(failures, err)
				fallbackReason = FallbackClientHelloUnsupported
				continue
			}
			connection, negotiated, err := connector.connectCustom(
				ctx,
				request,
				spec,
				offered,
			)
			if err == nil {
				evidence.effective = profileEvidence(template)
				evidence.fallbackReason = fallbackReason
				evidence.upstreamOfferedALPN = slices.Clone(offered)
				evidence.upstreamNegotiatedALPN = negotiated
				evidence.httpTransport = template.HTTPTransport()
				return connection, evidence, nil
			}
			failures = append(failures, err)
			if strictVerificationFailure(err) || ctx.Err() != nil {
				return nil, evidence, errors.Join(
					ErrNoTransportProfile,
					errors.Join(failures...),
				)
			}
			fallbackReason = FallbackCapturedTLSHandshakeRejected
		case access.TransportFingerprintStandard:
			offered := templateALPN(template)
			connection, negotiated, err := connector.connectStandard(
				ctx,
				request,
				offered,
			)
			if err == nil {
				evidence.effective = profileEvidence(template)
				evidence.fallbackReason = fallbackReason
				evidence.upstreamOfferedALPN = slices.Clone(offered)
				evidence.upstreamNegotiatedALPN = negotiated
				evidence.httpTransport = template.HTTPTransport()
				return connection, evidence, nil
			}
			failures = append(failures, err)
			if strictVerificationFailure(err) || ctx.Err() != nil {
				return nil, evidence, errors.Join(
					ErrNoTransportProfile,
					errors.Join(failures...),
				)
			}
		default:
			failures = append(
				failures,
				errors.New("transport fingerprint source is unsupported"),
			)
		}
	}
	return nil, evidence, errors.Join(
		ErrNoTransportProfile,
		errors.Join(failures...),
	)
}

func validateTemplates(
	templates []access.TransportFingerprintTemplate,
) error {
	if len(templates) == 0 {
		return ErrTransportPlanInvalid
	}
	seen := make(map[string]struct{}, len(templates))
	for _, template := range templates {
		if template.Ref().String() == "" ||
			template.Revision() == 0 ||
			template.HTTPTransport() != access.HTTPTransportHTTP1 {
			return ErrTransportPlanInvalid
		}
		switch template.Source() {
		case access.TransportFingerprintObservedClient,
			access.TransportFingerprintStandard:
			if template.Preset() != "" {
				return ErrTransportPlanInvalid
			}
		case access.TransportFingerprintCaptured:
			if template.Preset() != access.TransportFingerprintPresetClaudeCodeH1 {
				return ErrTransportPlanInvalid
			}
		default:
			return ErrTransportPlanInvalid
		}
		if _, duplicate := seen[template.Ref().String()]; duplicate {
			return ErrTransportPlanInvalid
		}
		seen[template.Ref().String()] = struct{}{}
		alpn := templateALPN(template)
		if len(alpn) == 0 {
			return ErrTransportPlanInvalid
		}
		for _, protocol := range alpn {
			if protocol != string(access.ApplicationProtocolHTTP1) {
				return ErrTransportPlanInvalid
			}
		}
	}
	return nil
}

func prepareCapturedSpec(
	template access.TransportFingerprintTemplate,
	serverName string,
) (*utls.ClientHelloSpec, []string, error) {
	if template.Preset() != access.TransportFingerprintPresetClaudeCodeH1 {
		return nil, nil, errors.New("captured ClientHello preset is unsupported")
	}
	offered := templateALPN(template)
	if !slices.Equal(offered, []string{string(access.ApplicationProtocolHTTP1)}) {
		return nil, nil, errors.New("captured ClientHello ALPN policy is invalid")
	}
	return claudeCodeH1ClientHelloSpec(serverName), offered, nil
}

func claudeCodeH1ClientHelloSpec(serverName string) *utls.ClientHelloSpec {
	return &utls.ClientHelloSpec{
		TLSVersMin: utls.VersionTLS12,
		TLSVersMax: utls.VersionTLS13,
		CipherSuites: []uint16{
			0x1301, 0x1302, 0x1303,
			0xc02b, 0xc02f, 0xc02c, 0xc030,
			0xcca9, 0xcca8,
			0xc009, 0xc013, 0xc00a, 0xc014,
			0x009c, 0x009d, 0x002f, 0x0035,
		},
		CompressionMethods: []uint8{0},
		Extensions: []utls.TLSExtension{
			&utls.SNIExtension{ServerName: serverName},
			&utls.ExtendedMasterSecretExtension{},
			&utls.RenegotiationInfoExtension{
				Renegotiation: utls.RenegotiateOnceAsClient,
			},
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{
				utls.X25519,
				utls.CurveP256,
				utls.CurveP384,
			}},
			&utls.SupportedPointsExtension{SupportedPoints: []uint8{0}},
			&utls.SessionTicketExtension{},
			&utls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}},
			&utls.StatusRequestExtension{},
			&utls.SignatureAlgorithmsExtension{
				SupportedSignatureAlgorithms: []utls.SignatureScheme{
					0x0403, 0x0804, 0x0401,
					0x0503, 0x0805, 0x0501,
					0x0806, 0x0601, 0x0201,
				},
			},
			&utls.SCTExtension{},
			&utls.KeyShareExtension{KeyShares: []utls.KeyShare{{
				Group: utls.X25519,
			}}},
			&utls.PSKKeyExchangeModesExtension{Modes: []uint8{1}},
			&utls.SupportedVersionsExtension{Versions: []uint16{
				utls.VersionTLS13,
				utls.VersionTLS12,
			}},
			&utls.UtlsPaddingExtension{GetPaddingLen: utls.BoringPaddingStyle},
		},
	}
}

func prepareObservedSpec(
	observation Observation,
	template access.TransportFingerprintTemplate,
	serverName string,
) (*utls.ClientHelloSpec, []string, FallbackReason, error) {
	if !observation.valid {
		return nil, nil, FallbackObservationUnavailable,
			ErrClientHelloUnavailable
	}
	fingerprinter := utls.Fingerprinter{
		AllowBluntMimicry: false,
		RealPSKResumption: false,
	}
	spec, err := fingerprinter.FingerprintClientHello(
		observation.fingerprintRecord,
	)
	if err != nil {
		return nil, nil, FallbackClientHelloUnsupported,
			fmt.Errorf("compile observed ClientHello: %w", err)
	}
	if err := enforceStrictTLSVersions(spec); err != nil {
		return nil, nil, FallbackClientHelloUnsupported, err
	}
	allowed := templateALPN(template)
	offered := intersectALPN(observation.offeredALPN, allowed)
	if len(offered) == 0 {
		return nil, nil, FallbackApplicationProtocolMissing,
			errors.New("observed ClientHello has no supported ALPN")
	}

	sanitized := make([]utls.TLSExtension, 0, len(spec.Extensions))
	hasSNI := false
	hasALPN := false
	for _, extension := range spec.Extensions {
		switch extension.(type) {
		case utls.PreSharedKeyExtension:
			continue
		case *utls.SNIExtension:
			if hasSNI {
				return nil, nil, FallbackClientHelloUnsupported,
					errors.New("observed ClientHello has duplicate SNI")
			}
			hasSNI = true
			sanitized = append(
				sanitized,
				&utls.SNIExtension{ServerName: serverName},
			)
		case *utls.ALPNExtension:
			if hasALPN {
				return nil, nil, FallbackClientHelloUnsupported,
					errors.New("observed ClientHello has duplicate ALPN")
			}
			hasALPN = true
			sanitized = append(
				sanitized,
				&utls.ALPNExtension{AlpnProtocols: slices.Clone(offered)},
			)
		case *utls.SessionTicketExtension:
			sanitized = append(sanitized, &utls.SessionTicketExtension{})
		case *utls.ApplicationSettingsExtension,
			*utls.ApplicationSettingsExtensionNew,
			*utls.NPNExtension,
			*utls.QUICTransportParametersExtension,
			*utls.CookieExtension:
			continue
		default:
			sanitized = append(sanitized, extension)
		}
	}
	if !hasSNI || !hasALPN {
		return nil, nil, FallbackClientHelloUnsupported,
			errors.New("observed ClientHello lacks SNI or ALPN")
	}
	spec.Extensions = sanitized
	spec.GetSessionID = nil
	return spec, offered, FallbackNone, nil
}

func enforceStrictTLSVersions(spec *utls.ClientHelloSpec) error {
	if spec.TLSVersMin != 0 || spec.TLSVersMax != 0 {
		if spec.TLSVersMax < utls.VersionTLS12 {
			return fmt.Errorf(
				"observed ClientHello TLS version range is unsupported: min=%#x max=%#x",
				spec.TLSVersMin,
				spec.TLSVersMax,
			)
		}
		if spec.TLSVersMin < utls.VersionTLS12 {
			spec.TLSVersMin = utls.VersionTLS12
		}
		return nil
	}

	supportedVersionsFound := false
	for _, extension := range spec.Extensions {
		versions, ok := extension.(*utls.SupportedVersionsExtension)
		if !ok {
			continue
		}
		if supportedVersionsFound {
			return errors.New(
				"observed ClientHello has duplicate supported versions extensions",
			)
		}
		supportedVersionsFound = true
		filtered := make([]uint16, 0, len(versions.Versions))
		secureVersionFound := false
		for _, version := range versions.Versions {
			switch {
			case version == utls.GREASE_PLACEHOLDER:
				filtered = append(filtered, version)
			case version >= utls.VersionTLS12 &&
				version <= utls.VersionTLS13:
				filtered = append(filtered, version)
				secureVersionFound = true
			case version > utls.VersionTLS13:
				return errors.New(
					"observed ClientHello contains an unsupported TLS version",
				)
			}
		}
		if !secureVersionFound {
			return errors.New(
				"observed ClientHello does not support TLS 1.2",
			)
		}
		versions.Versions = filtered
	}
	if !supportedVersionsFound {
		spec.TLSVersMin = utls.VersionTLS12
		spec.TLSVersMax = utls.VersionTLS12
	}
	return nil
}

func (connector *Connector) connectCustom(
	ctx context.Context,
	request ConnectRequest,
	spec *utls.ClientHelloSpec,
	alpn []string,
) (net.Conn, string, error) {
	raw, err := connector.dialer.DialContext(
		ctx,
		request.Network,
		request.Address,
	)
	if err != nil {
		return nil, "", err
	}
	config := &utls.Config{
		MinVersion: utls.VersionTLS12,
		RootCAs:    connector.rootCAs,
		ServerName: request.TLSServerName,
		NextProtos: slices.Clone(alpn),
	}
	secured := utls.UClient(raw, config, utls.HelloCustom)
	if err := secured.ApplyPreset(spec); err != nil {
		_ = raw.Close()
		return nil, "", fmt.Errorf("apply ClientHello profile: %w", err)
	}
	if err := connector.handshake(
		ctx,
		raw,
		secured.HandshakeContext,
	); err != nil {
		_ = raw.Close()
		return nil, "", err
	}
	return secured, secured.ConnectionState().NegotiatedProtocol, nil
}

func (connector *Connector) connectStandard(
	ctx context.Context,
	request ConnectRequest,
	alpn []string,
) (net.Conn, string, error) {
	raw, err := connector.dialer.DialContext(
		ctx,
		request.Network,
		request.Address,
	)
	if err != nil {
		return nil, "", err
	}
	secured := utls.UClient(raw, &utls.Config{
		MinVersion: utls.VersionTLS12,
		RootCAs:    connector.rootCAs,
		ServerName: request.TLSServerName,
		NextProtos: slices.Clone(alpn),
	}, utls.HelloGolang)
	if err := connector.handshake(
		ctx,
		raw,
		secured.HandshakeContext,
	); err != nil {
		_ = raw.Close()
		return nil, "", err
	}
	return secured, secured.ConnectionState().NegotiatedProtocol, nil
}

func (connector *Connector) handshake(
	ctx context.Context,
	raw net.Conn,
	handshake func(context.Context) error,
) error {
	handshakeContext, cancel := context.WithTimeout(
		ctx,
		connector.handshakeTimeout,
	)
	defer cancel()
	if deadline, ok := handshakeContext.Deadline(); ok {
		if err := raw.SetDeadline(deadline); err != nil {
			return err
		}
	}
	if err := handshake(handshakeContext); err != nil {
		if handshakeContext.Err() != nil {
			return context.Cause(handshakeContext)
		}
		return err
	}
	return raw.SetDeadline(time.Time{})
}

func strictVerificationFailure(err error) bool {
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var utlsVerification *utls.CertificateVerificationError
	return errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostname) ||
		errors.As(err, &invalid) ||
		errors.As(err, &utlsVerification)
}

func templateALPN(
	template access.TransportFingerprintTemplate,
) []string {
	protocols := template.ALPN()
	result := make([]string, len(protocols))
	for index, protocol := range protocols {
		result[index] = string(protocol)
	}
	return result
}

func intersectALPN(observed, allowed []string) []string {
	result := make([]string, 0, len(observed))
	for _, protocol := range observed {
		if slices.Contains(allowed, protocol) &&
			!slices.Contains(result, protocol) {
			result = append(result, protocol)
		}
	}
	return result
}
