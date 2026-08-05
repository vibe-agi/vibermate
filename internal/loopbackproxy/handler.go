// Package loopbackproxy implements authenticated HTTP CONNECT ingress for
// exact registered AgentEndpoints. It owns handler and hijacked-connection
// lifecycle, but the Host owns the loopback listener and external publication.
package loopbackproxy

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/blindtunnel"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/certidentity"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/pathcapability"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/ssewire"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
)

const (
	maxInnerHeaderBytes         = 1 << 20
	defaultHandshakeLimit       = 10 * time.Second
	defaultAuditCompletionLimit = 2 * time.Second
	exchangeIDBytes             = 20
	blindTunnelFailureClass     = "tunnel_failed"
)

type ReasonCode string

const (
	ReasonProxyAuthenticationRequired    ReasonCode = "proxy_authentication_required"
	ReasonCaptureAdmissionRejected       ReasonCode = "capture_admission_rejected"
	ReasonAgentEndpointNotConfigured     ReasonCode = "agent_endpoint_not_configured"
	ReasonConnectAuthorityInvalid        ReasonCode = "connect_authority_invalid"
	ReasonConnectSNIMismatch             ReasonCode = "connect_sni_mismatch"
	ReasonApplicationProtocolUnavailable ReasonCode = "application_protocol_unavailable"
	ReasonMITMUnavailable                ReasonCode = "mitm_unavailable"
	ReasonRequestAuthorityMismatch       ReasonCode = "request_authority_mismatch"
	ReasonAgentEndpointChanged           ReasonCode = "agent_endpoint_changed"
	ReasonPathUnsupported                ReasonCode = "path_unsupported"
	ReasonResponsesWebSocketUnsupported  ReasonCode = "responses_websocket_unsupported"
	ReasonRequestBodyInvalid             ReasonCode = "request_body_invalid"
	ReasonExchangeFailed                 ReasonCode = "exchange_failed"
	ReasonOriginalEgressFailed           ReasonCode = "original_egress_failed"
	ReasonProxyStopping                  ReasonCode = "proxy_stopping"
	ReasonConnectOnly                    ReasonCode = "connect_only"
	ReasonConnectionAuditUnavailable     ReasonCode = "connection_audit_unavailable"
	ReasonProfileOperationUnsupported    ReasonCode = "profile_operation_unsupported"
	ReasonBlindTunnelFailed              ReasonCode = "blind_tunnel_failed"
	ReasonUnsupportedUpgrade             ReasonCode = "unsupported_upgrade"
	ReasonConnectionDenied               ReasonCode = "connection_denied"
)

var ErrProxyStopping = errors.New("loopback proxy is stopping")

type CertificateAuthority interface {
	Identity() localca.RootIdentity
	Issue(context.Context, access.LeafIssuanceAdmission) (tls.Certificate, error)
}

type IngressAuthority interface {
	access.IngressResolver
	access.LeafIssuanceAdmitter
	access.DownstreamProtocolResolver
}

type OriginalClient interface {
	Do(context.Context, originaltransport.Request) (*http.Response, error)
}

type ExchangeIDSource interface {
	NewExchangeID(context.Context) (string, error)
}

// BlindTunnelDialer opens the upstream side of a connection that is forwarded
// without decryption. It is separate from the model and original-origin
// transports because it never learns a path, header, or protocol.
type BlindTunnelDialer interface {
	Dial(
		context.Context,
		blindtunnel.DialRequest,
	) (net.Conn, offlinehold.Lease, error)
	BeginAction(
		context.Context,
		offlinehold.ActionRequest,
	) (*offlinehold.ActionLease, error)
}

type ConnectionJournal interface {
	Start(context.Context, connectionevent.Attempt) (*connectionevent.Connection, error)
}

type RandomExchangeIDSource struct {
	random io.Reader
}

func NewRandomExchangeIDSource(random io.Reader) RandomExchangeIDSource {
	return RandomExchangeIDSource{random: random}
}

func NewCryptographicExchangeIDSource() RandomExchangeIDSource {
	return NewRandomExchangeIDSource(rand.Reader)
}

func (source RandomExchangeIDSource) NewExchangeID(
	ctx context.Context,
) (string, error) {
	if source.random == nil {
		return "", errors.New("Exchange ID random source is nil")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data := make([]byte, exchangeIDBytes)
	if _, err := io.ReadFull(source.random, data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

type Options struct {
	OwnerContext   context.Context
	Admissions     captureadmission.Authorizer
	Ingress        IngressAuthority
	AccessRequests access.RequestUseAdmitter
	Paths          *pathcapability.Catalog
	Exchanges      exchange.Executor
	Original       OriginalClient
	Certificates   CertificateAuthority
	Connections    ConnectionJournal
	// Policy decides every proxied connection before any dial, DNS
	// resolution, or certificate issuance. An AgentEndpoint is not exempt. It
	// is read once per connection, so a rule change reaches the next
	// connection and never splits one across two revisions.
	Policy connectionpolicy.Source
	// Approvals answers a policy `ask`. Without it an ask cannot block on a
	// person, so it denies rather than degrading into an allow.
	Approvals        NetworkApprovals
	BlindTunnels     BlindTunnelDialer
	EgressAudit      egressaudit.Writer
	ExchangeIDs      ExchangeIDSource
	HandshakeTimeout time.Duration
}

// terminalFailureReporter is implemented by the production audit boundary.
// A blind tunnel has already moved bytes when its terminal is constructed, so
// an invalid terminal must cross this explicit durability boundary.
type terminalFailureReporter interface {
	ReportTerminalFailure(error)
}

type operation struct {
	connection net.Conn
}

type Handler struct {
	admissions     captureadmission.Authorizer
	ingress        IngressAuthority
	accessRequests access.RequestUseAdmitter
	paths          *pathcapability.Catalog
	exchanges      exchange.Executor
	original       OriginalClient
	certificates   CertificateAuthority
	connections    ConnectionJournal
	policy         connectionpolicy.Source
	approvals      NetworkApprovals
	blindTunnels   BlindTunnelDialer
	egressAudit    egressaudit.Writer
	exchangeIDs    ExchangeIDSource
	handshake      time.Duration
	clock          func() time.Time
	auditTimeout   time.Duration
	ownerContext   context.Context

	mu        sync.Mutex
	accepting bool
	active    map[*operation]struct{}
	changed   chan struct{}
	stopOwner func() bool
}

func New(options Options) (*Handler, error) {
	if options.OwnerContext == nil ||
		options.Admissions == nil ||
		options.Ingress == nil ||
		options.AccessRequests == nil ||
		options.Paths == nil ||
		options.Exchanges == nil ||
		options.Original == nil ||
		options.Certificates == nil ||
		options.Connections == nil ||
		options.ExchangeIDs == nil {
		return nil, errors.New("loopback proxy dependencies are incomplete")
	}
	if options.HandshakeTimeout == 0 {
		options.HandshakeTimeout = defaultHandshakeLimit
	}
	if options.HandshakeTimeout < 0 {
		return nil, errors.New("TLS handshake timeout must be positive")
	}
	handler := &Handler{
		admissions:     options.Admissions,
		ingress:        options.Ingress,
		accessRequests: options.AccessRequests,
		paths:          options.Paths,
		exchanges:      options.Exchanges,
		original:       options.Original,
		certificates:   options.Certificates,
		connections:    options.Connections,
		policy:         options.Policy,
		approvals:      options.Approvals,
		blindTunnels:   options.BlindTunnels,
		egressAudit:    options.EgressAudit,
		exchangeIDs:    options.ExchangeIDs,
		handshake:      options.HandshakeTimeout,
		clock:          time.Now,
		auditTimeout:   defaultAuditCompletionLimit,
		ownerContext:   options.OwnerContext,
		accepting:      true,
		active:         make(map[*operation]struct{}),
		changed:        make(chan struct{}),
	}
	handler.stopOwner = context.AfterFunc(options.OwnerContext, handler.BeginShutdown)
	return handler, nil
}

func (handler *Handler) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	active, err := handler.begin()
	if err != nil {
		writeReason(writer, http.StatusServiceUnavailable, ReasonProxyStopping, "")
		return
	}
	defer handler.finish(active)
	if request == nil {
		writeReason(writer, http.StatusMethodNotAllowed, ReasonConnectOnly, "")
		return
	}
	// The host and the port are separate facts on the record. Writing the
	// authority into the host field would state the port twice, and every
	// reader that joins them would show it twice.
	// The split here is syntactic on purpose. The journal records what the
	// client asked for, including an authority that is not a usable name: an
	// operator investigating "my client cannot reach anything" has to be able
	// to see the refusal, and a record that refused to hold the request would
	// hide exactly the case worth seeing.
	requestedHost := request.Host
	port := uint16(0)
	if splitHost, splitPort, splitErr := net.SplitHostPort(request.Host); splitErr == nil {
		requestedHost = splitHost
		if parsedPort, parseErr := strconv.ParseUint(splitPort, 10, 16); parseErr == nil {
			port = uint16(parsedPort)
		}
	}
	// The journal opens before the method check so a rejection is recorded
	// rather than invisible. Design 06 section 4.1 requires allowed, denied,
	// timed-out, and failed attempts alike to leave evidence; an operator
	// investigating "my client cannot reach anything" must be able to see the
	// refusal.
	audit, err := handler.connections.Start(
		request.Context(),
		connectionevent.Attempt{
			Source: connectionevent.Source{
				Confidence: connectionevent.SourceConfidenceUnknown,
			},
			RequestedHost: requestedHost,
			Port:          port,
		},
	)
	if err != nil {
		writeReason(
			writer,
			http.StatusServiceUnavailable,
			ReasonConnectionAuditUnavailable,
			"",
		)
		return
	}
	var counted *countingConnection
	terminal := false
	terminalEvidence := connectionevent.TerminalEvidence{
		Outcome:    connectionevent.OutcomeFailed,
		ErrorClass: string(ReasonMITMUnavailable),
	}
	defer func() {
		if terminal {
			return
		}
		if counted != nil {
			terminalEvidence.BytesUp = counted.bytesUp.Load()
			terminalEvidence.BytesDown = counted.bytesDown.Load()
		}
		handler.finishConnectionAudit(audit, terminalEvidence)
	}()

	cleartextForward := request.Method != http.MethodConnect &&
		request.URL != nil &&
		request.URL.IsAbs()
	if request.Method != http.MethodConnect && !cleartextForward {
		if handler.denyConnection(
			request.Context(),
			audit,
			connectionevent.Source{
				Confidence: connectionevent.SourceConfidenceUnknown,
			},
			ReasonConnectOnly,
		) != nil {
			writeReason(
				writer,
				http.StatusServiceUnavailable,
				ReasonConnectionAuditUnavailable,
				"",
			)
			return
		}
		terminal = true
		writeReason(writer, http.StatusMethodNotAllowed, ReasonConnectOnly, "")
		return
	}
	rawCapability, authErr := consumeProxyAuthorization(request.Header)
	if authErr != nil {
		if handler.denyConnection(
			request.Context(),
			audit,
			connectionevent.Source{
				Confidence: connectionevent.SourceConfidenceUnknown,
			},
			ReasonProxyAuthenticationRequired,
		) != nil {
			writeReason(
				writer,
				http.StatusServiceUnavailable,
				ReasonConnectionAuditUnavailable,
				"",
			)
			return
		}
		terminal = true
		writeReason(
			writer,
			http.StatusProxyAuthRequired,
			ReasonProxyAuthenticationRequired,
			"",
		)
		return
	}
	admission, err := authorizeCaptureAdmission(
		request.Context(),
		handler.admissions,
		rawCapability,
	)
	if err != nil {
		if handler.denyConnection(
			request.Context(),
			audit,
			connectionevent.Source{
				Confidence: connectionevent.SourceConfidenceUnknown,
			},
			ReasonCaptureAdmissionRejected,
		) != nil {
			writeReason(
				writer,
				http.StatusServiceUnavailable,
				ReasonConnectionAuditUnavailable,
				"",
			)
			return
		}
		terminal = true
		writeReason(writer, http.StatusForbidden, ReasonCaptureAdmissionRejected, "")
		return
	}
	source := connectionSource(admission)
	// Every proxied connection is decided here, before any dial, DNS
	// resolution, or certificate issuance, and before the AgentEndpoint match
	// that would otherwise exempt it.
	policyHost, policyPort := policyTarget(request, cleartextForward)
	outcome := handler.rules().Evaluate(connectionpolicy.Request{
		Host: policyHost,
		Port: policyPort,
	})
	// An ask blocks here, still before any dial. It resolves to exactly one of
	// allow or deny; there is no third answer this far in front of a socket.
	denialReason := ReasonCode(outcome.RuleID)
	if outcome.Decision == connectionpolicy.DecisionAsk {
		resolution := handler.resolveAsk(
			request.Context(),
			outcome,
			source.IngressID,
			policyHost,
			policyPort,
		)
		outcome = resolution.outcome
		if resolution.reason != "" {
			denialReason = resolution.reason
		}
	}
	if outcome.Decision != connectionpolicy.DecisionAllow {
		if handler.denyConnection(
			request.Context(),
			audit,
			source,
			denialReason,
		) != nil {
			writeReason(
				writer,
				http.StatusServiceUnavailable,
				ReasonConnectionAuditUnavailable,
				"",
			)
			return
		}
		terminal = true
		writeReason(
			writer,
			http.StatusForbidden,
			ReasonConnectionDenied,
			outcome.RuleID,
		)
		return
	}
	// A cleartext forward-proxy request carries its target in an absolute
	// request-target. It is authenticated exactly like a CONNECT, then
	// forwarded to that origin; it never enters a model pipeline and never
	// carries a provider credential.
	if cleartextForward {
		terminal = handler.serveCleartextForward(
			writer,
			request,
			active,
			audit,
			source,
			outcome.RuleID,
		)
		return
	}
	origin, host, err := connectOrigin(request.Host)
	if err != nil {
		if handler.denyConnection(
			request.Context(),
			audit,
			source,
			ReasonConnectAuthorityInvalid,
		) != nil {
			writeReason(
				writer,
				http.StatusServiceUnavailable,
				ReasonConnectionAuditUnavailable,
				"",
			)
			return
		}
		terminal = true
		writeReason(writer, http.StatusBadRequest, ReasonConnectAuthorityInvalid, "")
		return
	}
	binding, err := handler.ingress.ResolveClientOrigin(origin)
	if err != nil {
		// An authority that is not an enabled AgentEndpoint is forwarded
		// without decryption. The launcher exports the proxy to the whole
		// child process tree, so refusing these would refuse every package
		// install, update check, and MCP server an Agent touches.
		terminal = handler.serveBlindTunnel(
			writer,
			request,
			active,
			audit,
			source,
			outcome.RuleID,
			request.Host,
			host,
			port,
		)
		return
	}
	if err := audit.Decide(
		request.Context(),
		connectionevent.DecisionEvidence{
			Source:               source,
			Decision:             connectionevent.DecisionAllow,
			RuleID:               outcome.RuleID,
			RouteHost:            origin.TLSServerName(),
			EgressScope:          connectionevent.EgressScopeAccess,
			EgressSource:         connectionevent.EgressSourceAccessDefault,
			EgressPolicyRevision: uint64(binding.AccessRevision()),
			Decryption:           connectionevent.DecryptionMITM,
		},
	); err != nil {
		writeReason(
			writer,
			http.StatusServiceUnavailable,
			ReasonConnectionAuditUnavailable,
			"",
		)
		return
	}
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		writeReason(writer, http.StatusInternalServerError, ReasonMITMUnavailable, "")
		return
	}
	connection, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	counted = &countingConnection{Conn: connection}
	if !handler.attachConnection(active, counted) {
		terminalEvidence.Outcome = connectionevent.OutcomeCanceled
		terminalEvidence.ErrorClass = string(ReasonProxyStopping)
		_ = counted.Close()
		return
	}
	defer counted.Close()
	if err := writeConnectEstablished(buffered); err != nil {
		return
	}
	if err := handler.serveTLS(
		request.Context(),
		counted,
		host,
		admission,
		binding,
		audit,
	); err != nil {
		terminalEvidence.Outcome, terminalEvidence.ErrorClass =
			connectionTerminalOf(err)
		return
	}
	terminalEvidence.Outcome = connectionevent.OutcomeCompleted
	terminalEvidence.ErrorClass = ""
}

func (handler *Handler) serveTLS(
	parent context.Context,
	connection net.Conn,
	expectedHost string,
	admission captureadmission.Admission,
	binding access.IngressBinding,
	audit *connectionevent.Connection,
) error {
	handshakeContext, cancel := context.WithTimeout(parent, handler.handshake)
	defer cancel()
	observation, replay, err := transportprofile.CaptureClientHello(
		handshakeContext,
		connection,
		transportprofile.DefaultMaxClientHelloBytes,
	)
	if err != nil {
		return err
	}
	protocols, err := handler.ingress.ResolveDownstreamProtocols(binding)
	if err != nil {
		return errors.Join(
			errors.New(string(ReasonApplicationProtocolUnavailable)),
			err,
		)
	}
	nextProtos, err := downstreamNextProtos(protocols, observation.OfferedALPN())
	if err != nil {
		return err
	}
	observedSNI := ""
	config := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: nextProtos,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if hello == nil {
				return nil, errors.New(string(ReasonMITMUnavailable))
			}
			observedSNI = hello.ServerName
			if !sniMatches(expectedHost, hello.ServerName) {
				return nil, errors.New(string(ReasonConnectSNIMismatch))
			}
			san, err := certidentity.NewDNSName(hello.ServerName)
			if err != nil {
				return nil, errors.Join(
					errors.New(string(ReasonMITMUnavailable)),
					err,
				)
			}
			intent, err := access.NewLeafIssuanceIntent(
				handler.certificates.Identity().Revision(),
				binding,
				san,
				certidentity.LeafKeyAlgorithmECDSAP256,
			)
			if err != nil {
				return nil, errors.Join(
					errors.New(string(ReasonMITMUnavailable)),
					err,
				)
			}
			admission, err := handler.ingress.AdmitLeaf(intent)
			if err != nil {
				return nil, errors.Join(
					errors.New(string(ReasonMITMUnavailable)),
					err,
				)
			}
			certificate, err := handler.certificates.Issue(
				hello.Context(),
				admission,
			)
			if err != nil {
				return nil, errors.Join(
					errors.New(string(ReasonMITMUnavailable)),
					err,
				)
			}
			return &certificate, nil
		},
	}
	secured := tls.Server(replay, config)
	if err := secured.HandshakeContext(handshakeContext); err != nil {
		return err
	}
	connectionState := secured.ConnectionState()
	observation, err = observation.WithDownstreamNegotiatedALPN(
		connectionState.NegotiatedProtocol,
	)
	if err != nil {
		return err
	}
	if err := audit.Connected(
		parent,
		connectionevent.ConnectedEvidence{
			ObservedSNI: observedSNI,
			RouteHost:   binding.ClientOrigin().TLSServerName(),
		},
	); err != nil {
		return fmt.Errorf("record connected ConnectionEvent: %w", err)
	}
	listener := newSingleConnListener(secured)
	inner := &http.Server{
		Handler: http.HandlerFunc(func(
			writer http.ResponseWriter,
			request *http.Request,
		) {
			state := connectionState
			request.TLS = &state
			handler.serveInner(
				writer,
				request,
				admission,
				binding,
				observation,
				audit,
			)
		}),
		ReadHeaderTimeout: handler.handshake,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    maxInnerHeaderBytes,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	h2Server := &http2.Server{
		MaxConcurrentStreams:         32,
		MaxDecoderHeaderTableSize:    4096,
		MaxEncoderHeaderTableSize:    4096,
		MaxReadFrameSize:             1 << 20,
		MaxUploadBufferPerConnection: 1 << 20,
		MaxUploadBufferPerStream:     1 << 20,
		ReadIdleTimeout:              30 * time.Second,
		PingTimeout:                  10 * time.Second,
		WriteByteTimeout:             handler.handshake,
	}
	if connectionState.NegotiatedProtocol ==
		string(access.ApplicationProtocolHTTP2) {
		h2Server.ServeConn(secured, &http2.ServeConnOpts{
			Context:    parent,
			BaseConfig: inner,
			Handler:    inner.Handler,
		})
		return nil
	}
	err = inner.Serve(listener)
	if errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (handler *Handler) serveInner(
	writer http.ResponseWriter,
	request *http.Request,
	admission captureadmission.Admission,
	binding access.IngressBinding,
	observation transportprofile.Observation,
	audit *connectionevent.Connection,
) {
	if request == nil ||
		request.URL == nil ||
		request.URL.IsAbs() ||
		request.TLS == nil ||
		!authorityMatches(request.Host, binding.ClientOrigin()) {
		writeReason(
			writer,
			http.StatusMisdirectedRequest,
			ReasonRequestAuthorityMismatch,
			"",
		)
		return
	}
	requestLease, err := handler.accessRequests.AdmitRequest(
		request.Context(),
		binding,
	)
	if err != nil {
		writer.Header().Set("Connection", "close")
		writeReason(
			writer,
			http.StatusMisdirectedRequest,
			ReasonAgentEndpointChanged,
			"",
		)
		return
	}
	defer requestLease.Release()
	currentBinding, err := handler.ingress.ResolveClientOrigin(
		binding.ClientOrigin(),
	)
	if err != nil || binding.ValidateCurrent(currentBinding) != nil {
		writer.Header().Set("Connection", "close")
		writeReason(
			writer,
			http.StatusMisdirectedRequest,
			ReasonAgentEndpointChanged,
			"",
		)
		return
	}
	request.Header.Del("Proxy-Authorization")
	request.Header.Del("Proxy-Connection")
	capability, err := handler.paths.Classify(
		binding.ClientDialect(),
		request.Method,
		request.URL.Path,
		request.URL.RawPath,
		request.URL.RawQuery,
	)
	if err != nil {
		writeReason(
			writer,
			http.StatusUnprocessableEntity,
			ReasonPathUnsupported,
			string(pathcapability.ReasonOf(err)),
		)
		return
	}
	if capability.Kind() == pathcapability.KindUnsupported {
		if capability.Transport() ==
			access.ClientOperationTransportWebSocket &&
			isWebSocketUpgrade(request) &&
			admission.Supports(
				clientadapter.FeatureResponsesWebSocketHTTPFallback,
			) {
			writeReason(
				writer,
				http.StatusUpgradeRequired,
				ReasonResponsesWebSocketUnsupported,
				"",
			)
			return
		}
		writeReason(
			writer,
			http.StatusUnprocessableEntity,
			ReasonPathUnsupported,
			"",
		)
		return
	}
	// An upgrade the proxy cannot serve is refused explicitly. Answering it as
	// an ordinary request would tell a client it negotiated a protocol this
	// proxy never spoke, which it discovers only once it starts sending
	// frames.
	if isWebSocketUpgrade(request) &&
		capability.Transport() != access.ClientOperationTransportWebSocket {
		drainBounded(request.Body, capability.MaxBodyBytes())
		writeDialectReason(
			writer,
			binding.ClientDialect(),
			http.StatusUpgradeRequired,
			ReasonUnsupportedUpgrade,
		)
		return
	}
	// Admission is decided from the frozen payload class before any body is
	// read, so a request whose typed handling plan does not exist cannot place
	// client payload in a buffer that outlives this decision.
	if !admitsLocalDispatch(capability, request) {
		drainBounded(request.Body, capability.MaxBodyBytes())
		writeDialectReason(
			writer,
			binding.ClientDialect(),
			http.StatusUnprocessableEntity,
			ReasonProfileOperationUnsupported,
		)
		return
	}
	body, err := readBounded(request.Body, capability.MaxBodyBytes())
	if err != nil {
		writeReason(writer, http.StatusRequestEntityTooLarge, ReasonRequestBodyInvalid, "")
		return
	}
	exchangeID, err := handler.exchangeIDs.NewExchangeID(request.Context())
	if err != nil {
		writeReason(writer, http.StatusServiceUnavailable, ReasonProxyStopping, "")
		return
	}
	switch capability.Kind() {
	case pathcapability.KindSemantic:
		handler.serveSemantic(
			writer,
			request,
			admission,
			binding,
			capability,
			exchangeID,
			body,
			observation,
			audit,
		)
	case pathcapability.KindAuxiliary:
		handler.serveAgentProbe(
			writer,
			request,
			binding,
			capability,
			audit.ID(),
			exchangeID,
			body,
		)
	case pathcapability.KindOpaque:
		handler.serveOriginal(
			writer,
			request,
			binding,
			capability,
			audit.ID(),
			exchangeID,
			body,
		)
	default:
		writeReason(writer, http.StatusUnprocessableEntity, ReasonPathUnsupported, "")
	}
}

func isWebSocketUpgrade(request *http.Request) bool {
	if request == nil ||
		!strings.EqualFold(
			strings.TrimSpace(request.Header.Get("Upgrade")),
			"websocket",
		) {
		return false
	}
	for _, value := range request.Header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

func (handler *Handler) serveSemantic(
	writer http.ResponseWriter,
	request *http.Request,
	admission captureadmission.Admission,
	binding access.IngressBinding,
	capability pathcapability.Capability,
	exchangeID string,
	body []byte,
	observation transportprofile.Observation,
	audit *connectionevent.Connection,
) {
	operation, err := exchange.NewClientOperationEvidence(
		capability.OperationID(),
		capability.Revision(),
		request.Method,
		request.URL.Path,
		request.URL.RawQuery,
	)
	if err != nil {
		writeReason(writer, http.StatusBadRequest, ReasonRequestBodyInvalid, "")
		return
	}
	clientProtocol := access.ApplicationProtocolHTTP1
	if request.ProtoMajor == 2 {
		clientProtocol = access.ApplicationProtocolHTTP2
	} else if request.ProtoMajor != 1 {
		writeReason(writer, http.StatusBadRequest, ReasonRequestBodyInvalid, "")
		return
	}
	requestOptions := []exchange.ClientRequestOption{
		exchange.WithClientHelloObservation(observation),
		exchange.WithOriginalHeaders(request.Header),
		// Every identity is generated independently; association travels as
		// typed references rather than as a delimiter-joined string.
		exchange.WithIngressCorrelation(admission, audit.ID()),
	}
	if beta := strings.Join(request.Header.Values("Anthropic-Beta"), ","); beta != "" {
		requestOptions = append(
			requestOptions,
			exchange.WithAnthropicBetaHeader(beta),
		)
	}
	userAgents := request.Header.Values("User-Agent")
	if len(userAgents) > 1 || (len(userAgents) == 1 && userAgents[0] == "") {
		writeReason(writer, http.StatusBadRequest, ReasonRequestBodyInvalid, "")
		return
	}
	if len(userAgents) == 1 {
		requestOptions = append(
			requestOptions,
			exchange.WithClientUserAgent(userAgents[0]),
		)
	}
	clientRequest, err := exchange.NewClientRequest(
		exchangeID,
		binding,
		operation,
		body,
		capability.ReplayClass(),
		clientProtocol,
		requestOptions...,
	)
	if err != nil {
		writeReason(writer, http.StatusBadRequest, ReasonRequestBodyInvalid, "")
		return
	}
	downstream := newHTTPDownstream(writer)
	_, err = handler.exchanges.Execute(
		request.Context(),
		clientRequest,
		downstream,
	)
	// The per-request destination and credential decision belong to the
	// EgressAttempt the provider transport records. Writing them back onto the
	// connection would leave only the last request's answer on a persistent
	// connection carrying several.
	if err != nil && !downstream.Begun() {
		writeExchangeFailure(writer, binding.ClientDialect(), err)
	}
}

// connectionSource grades ingress attribution from the evidence the CaptureRun
// actually carries. A digest-verified compound release is the strongest
// evidence a pure proxy can obtain, so it reports `verified`; a run without one
// reports `configured`. Neither is a claim of operating-system process
// identity, which the proxy cannot establish.
// rules is the set in force for one connection. A connection that has already
// been decided is never revisited, and a connection still being decided sees
// one revision whole.
func (handler *Handler) rules() connectionpolicy.RuleSet {
	if handler.policy == nil {
		return connectionpolicy.RuleSet{}
	}
	return handler.policy.Current()
}

func connectionSource(admission captureadmission.Admission) connectionevent.Source {
	confidence := connectionevent.SourceConfidenceUnknown
	switch admission.AttributionConfidence() {
	case captureadmission.AttributionVerified:
		confidence = connectionevent.SourceConfidenceVerified
	case captureadmission.AttributionConfigured:
		confidence = connectionevent.SourceConfidenceConfigured
	}
	return connectionevent.Source{
		IngressID:  admission.IngressProfileID(),
		Label:      admission.SourceLabel(),
		Confidence: confidence,
	}
}

func (handler *Handler) denyConnection(
	ctx context.Context,
	audit *connectionevent.Connection,
	source connectionevent.Source,
	reason ReasonCode,
) error {
	return audit.Decide(ctx, connectionevent.DecisionEvidence{
		Source:     source,
		Decision:   connectionevent.DecisionDeny,
		RuleID:     string(reason),
		Decryption: connectionevent.DecryptionNone,
		ErrorClass: string(reason),
	})
}

func (handler *Handler) finishConnectionAudit(
	audit *connectionevent.Connection,
	evidence connectionevent.TerminalEvidence,
) {
	if audit == nil {
		return
	}
	ctx, cancel := context.WithTimeout(
		context.WithoutCancel(handler.ownerContext),
		2*time.Second,
	)
	defer cancel()
	_ = audit.Finish(ctx, evidence)
}

func connectionTerminalOf(err error) (connectionevent.Outcome, string) {
	switch {
	case err == nil:
		return connectionevent.OutcomeCompleted, ""
	case errors.Is(err, context.Canceled),
		errors.Is(err, net.ErrClosed):
		return connectionevent.OutcomeCanceled, "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return connectionevent.OutcomeFailed, "deadline"
	case strings.Contains(err.Error(), string(ReasonConnectSNIMismatch)):
		return connectionevent.OutcomeFailed, string(ReasonConnectSNIMismatch)
	case strings.Contains(
		err.Error(),
		string(ReasonApplicationProtocolUnavailable),
	):
		return connectionevent.OutcomeFailed,
			string(ReasonApplicationProtocolUnavailable)
	default:
		return connectionevent.OutcomeFailed, string(ReasonMITMUnavailable)
	}
}

func downstreamNextProtos(
	protocols []access.ApplicationProtocol,
	offered []string,
) ([]string, error) {
	if len(protocols) == 0 || len(protocols) > 2 {
		return nil, errors.New(string(ReasonApplicationProtocolUnavailable))
	}
	seen := make(map[access.ApplicationProtocol]struct{}, len(protocols))
	next := make([]string, 0, len(protocols))
	compatible := false
	for _, protocol := range protocols {
		if protocol != access.ApplicationProtocolHTTP1 &&
			protocol != access.ApplicationProtocolHTTP2 {
			return nil, errors.New(string(ReasonApplicationProtocolUnavailable))
		}
		if _, duplicate := seen[protocol]; duplicate {
			return nil, errors.New(string(ReasonApplicationProtocolUnavailable))
		}
		seen[protocol] = struct{}{}
		next = append(next, string(protocol))
		if len(offered) == 0 && protocol == access.ApplicationProtocolHTTP1 {
			// A TLS client that omits ALPN is an HTTP/1.1 client. It cannot
			// satisfy an H2-only Access plan merely because no token was sent.
			compatible = true
		}
		for _, candidate := range offered {
			if candidate == string(protocol) {
				compatible = true
			}
		}
	}
	if !compatible {
		return nil, errors.New(string(ReasonApplicationProtocolUnavailable))
	}
	return next, nil
}

type countingConnection struct {
	net.Conn
	bytesUp   atomic.Uint64
	bytesDown atomic.Uint64
}

func (connection *countingConnection) Read(destination []byte) (int, error) {
	count, err := connection.Conn.Read(destination)
	if count > 0 {
		connection.bytesUp.Add(uint64(count))
	}
	return count, err
}

func (connection *countingConnection) Write(source []byte) (int, error) {
	count, err := connection.Conn.Write(source)
	if count > 0 {
		connection.bytesDown.Add(uint64(count))
	}
	return count, err
}

// admitsLocalDispatch decides whether a classified request may continue to its
// dispatch arm. Semantic operations enter the model pipeline and are governed
// by the frozen Access plan. Every other arm forwards to the inbound origin
// with the client's own credentials, so it requires a payload class that
// proves no client payload travels.
func admitsLocalDispatch(
	capability pathcapability.Capability,
	request *http.Request,
) bool {
	if capability.Kind() == pathcapability.KindSemantic {
		return true
	}
	class := capability.PayloadClass()
	if class.AllowsOriginalOrigin() {
		return true
	}
	// An unclassified request without a body still reaches the inbound origin
	// today. The connection-policy Goal replaces this with an explicit
	// allow/deny/ask decision; until then the exception stays narrow and is
	// never extended to a request that can carry a body.
	return class == access.OperationPayloadUnknown && !carriesBody(request)
}

func carriesBody(request *http.Request) bool {
	if request == nil {
		return false
	}
	switch request.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	}
	if request.ContentLength != 0 {
		return true
	}
	return len(request.TransferEncoding) > 0
}

// serveAgentProbe forwards a catalogued no-payload probe to the inbound
// origin. It re-proves admission so the arm stays closed even if a future
// caller reaches it without passing the dispatch gate.
func (handler *Handler) serveAgentProbe(
	writer http.ResponseWriter,
	request *http.Request,
	binding access.IngressBinding,
	capability pathcapability.Capability,
	connectionID string,
	requestID string,
	body []byte,
) {
	if !admitsLocalDispatch(capability, request) {
		writeDialectReason(
			writer,
			binding.ClientDialect(),
			http.StatusUnprocessableEntity,
			ReasonProfileOperationUnsupported,
		)
		return
	}
	handler.forwardToOriginalOrigin(
		writer,
		request,
		binding,
		offlinehold.EgressAuxiliary,
		capability.PayloadClass(),
		connectionID,
		requestID,
		body,
	)
}

func (handler *Handler) serveOriginal(
	writer http.ResponseWriter,
	request *http.Request,
	binding access.IngressBinding,
	capability pathcapability.Capability,
	connectionID string,
	requestID string,
	body []byte,
) {
	if !admitsLocalDispatch(capability, request) {
		writeDialectReason(
			writer,
			binding.ClientDialect(),
			http.StatusUnprocessableEntity,
			ReasonProfileOperationUnsupported,
		)
		return
	}
	handler.forwardToOriginalOrigin(
		writer,
		request,
		binding,
		offlinehold.EgressOpaque,
		capability.PayloadClass(),
		connectionID,
		requestID,
		body,
	)
}

func (handler *Handler) forwardToOriginalOrigin(
	writer http.ResponseWriter,
	request *http.Request,
	binding access.IngressBinding,
	kind offlinehold.EgressKind,
	payloadClass access.OperationPayloadClass,
	connectionID string,
	requestID string,
	body []byte,
) {
	// The egress identity and the original-request identity are generated
	// independently; the association travels as typed references.
	egressID, err := handler.exchangeIDs.NewExchangeID(request.Context())
	if err != nil {
		writeReason(writer, http.StatusServiceUnavailable, ReasonProxyStopping, "")
		return
	}
	frozen, err := originaltransport.NewRequest(originaltransport.RequestOptions{
		RequestID:    egressID,
		ConnectionID: connectionID,
		ParentID:     requestID,
		Kind:         kind,
		Origin:       binding.ClientOrigin(),
		Method:       request.Method,
		Path:         request.URL.Path,
		RawQuery:     request.URL.RawQuery,
		Headers:      request.Header,
		Body:         body,
		PayloadClass: payloadClass,
	})
	if err != nil {
		writeReason(writer, http.StatusBadRequest, ReasonRequestBodyInvalid, "")
		return
	}
	response, err := handler.original.Do(request.Context(), frozen)
	if err != nil {
		writeReason(writer, http.StatusBadGateway, ReasonOriginalEgressFailed, "")
		return
	}
	defer response.Body.Close()
	copyResponseHeaders(writer.Header(), response.Header)
	writer.WriteHeader(response.StatusCode)
	_, _ = io.Copy(writer, response.Body)
}

func (handler *Handler) begin() (*operation, error) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if !handler.accepting {
		return nil, ErrProxyStopping
	}
	active := &operation{}
	handler.active[active] = struct{}{}
	handler.signalLocked()
	return active, nil
}

func (handler *Handler) finish(active *operation) {
	handler.mu.Lock()
	delete(handler.active, active)
	handler.signalLocked()
	handler.mu.Unlock()
}

func (handler *Handler) attachConnection(
	active *operation,
	connection net.Conn,
) bool {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if !handler.accepting {
		return false
	}
	if _, exists := handler.active[active]; !exists {
		return false
	}
	active.connection = connection
	return true
}

func (handler *Handler) BeginShutdown() {
	if handler == nil {
		return
	}
	handler.mu.Lock()
	if !handler.accepting {
		handler.mu.Unlock()
		return
	}
	handler.accepting = false
	connections := make([]net.Conn, 0, len(handler.active))
	for active := range handler.active {
		if active.connection != nil {
			connections = append(connections, active.connection)
		}
	}
	handler.signalLocked()
	handler.mu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}

func (handler *Handler) Drain(ctx context.Context) error {
	if handler == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("loopback proxy drain context is nil")
	}
	handler.mu.Lock()
	for len(handler.active) != 0 {
		changed := handler.changed
		handler.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return fmt.Errorf("drain loopback proxy connections: %w", ctx.Err())
		}
		handler.mu.Lock()
	}
	handler.mu.Unlock()
	return nil
}

func (handler *Handler) Shutdown(ctx context.Context) error {
	if handler == nil {
		return nil
	}
	if handler.stopOwner != nil {
		handler.stopOwner()
	}
	handler.BeginShutdown()
	return handler.Drain(ctx)
}

func (handler *Handler) signalLocked() {
	close(handler.changed)
	handler.changed = make(chan struct{})
}

func consumeProxyAuthorization(header http.Header) (string, error) {
	values := header.Values("Proxy-Authorization")
	header.Del("Proxy-Authorization")
	header.Del("Proxy-Connection")
	if len(values) != 1 {
		return "", errors.New("one proxy authorization value is required")
	}
	parts := strings.SplitN(values[0], " ", 2)
	if len(parts) != 2 ||
		!strings.EqualFold(parts[0], "Basic") ||
		parts[1] == "" ||
		strings.ContainsAny(parts[1], " \t\r\n") {
		return "", errors.New("proxy authorization scheme is invalid")
	}
	decoded, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	username, password, found := strings.Cut(string(decoded), ":")
	if !found || username != captureadmission.ProxyUsername || password == "" ||
		strings.Contains(password, ":") {
		return "", errors.New("proxy authorization credentials are invalid")
	}
	return password, nil
}

func authorizeCaptureAdmission(
	ctx context.Context,
	authorizer captureadmission.Authorizer,
	rawCredential string,
) (captureadmission.Admission, error) {
	if authorizer == nil {
		return captureadmission.Admission{},
			captureadmission.ErrCredentialRejected
	}
	credential, err := captureadmission.NewProxyCredential(rawCredential)
	if err != nil {
		return captureadmission.Admission{}, err
	}
	admission, err := authorizer.Authorize(ctx, credential)
	if err != nil {
		return captureadmission.Admission{}, err
	}
	if err := admission.Validate(); err != nil {
		return captureadmission.Admission{}, errors.Join(
			captureadmission.ErrCredentialRejected,
			err,
		)
	}
	return admission, nil
}

func connectOrigin(authority string) (access.ClientOrigin, string, error) {
	host, port, err := splitAuthority(authority)
	if err != nil {
		return access.ClientOrigin{}, "", err
	}
	origin, err := access.NewClientOrigin(
		"https://" + net.JoinHostPort(host, strconv.Itoa(int(port))),
	)
	if err != nil {
		return access.ClientOrigin{}, "", err
	}
	return origin, origin.TLSServerName(), nil
}

// splitAuthority canonicalizes rather than refuses. RFC 3986 makes a host
// case-insensitive and a trailing dot is the root form of the same name, so a
// client that sends either is asking for the same endpoint. Canonicalization
// cannot widen the match: case folding and root-dot removal map a name only
// onto itself, and no suffix, wildcard, or different host becomes equal.
func splitAuthority(authority string) (string, uint16, error) {
	host, portText, err := net.SplitHostPort(authority)
	if err != nil || host == "" || portText == "" {
		return "", 0, errors.New("CONNECT authority must contain host and port")
	}
	host = canonicalCONNECTHost(host)
	if host == "" {
		return "", 0, errors.New("CONNECT host is invalid")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return "", 0, errors.New("CONNECT port is invalid")
	}
	return host, uint16(port), nil
}

func canonicalCONNECTHost(host string) string {
	host = strings.ToLower(host)
	// A single trailing dot is the DNS root label. More than one, or a dot
	// alone, is not a name.
	if strings.HasSuffix(host, ".") {
		host = strings.TrimSuffix(host, ".")
	}
	if host == "" || strings.HasSuffix(host, ".") {
		return ""
	}
	return host
}

func authorityMatches(authority string, origin access.ClientOrigin) bool {
	host, port, err := splitAuthority(authority)
	if err != nil {
		// HTTP/1.1 Host may omit the default HTTPS port after MITM.
		host = strings.ToLower(authority)
		port = 443
	}
	return host == origin.TLSServerName() && port == origin.Port()
}

// sniMatches compares canonically: RFC 6066 server names are case-insensitive
// and may carry the DNS root label.
func sniMatches(expected, actual string) bool {
	if address := net.ParseIP(expected); address != nil {
		return actual == ""
	}
	return canonicalCONNECTHost(actual) == expected
}

func writeConnectEstablished(buffered *bufio.ReadWriter) error {
	if _, err := buffered.WriteString(
		"HTTP/1.1 200 Connection Established\r\n\r\n",
	); err != nil {
		return err
	}
	return buffered.Flush()
}

func writeReason(
	writer http.ResponseWriter,
	status int,
	reason ReasonCode,
	detail string,
) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	payload := struct {
		ReasonCode ReasonCode `json:"reasonCode"`
		DetailCode string     `json:"detailCode,omitempty"`
	}{
		ReasonCode: reason,
		DetailCode: detail,
	}
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	if reader == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("request body exceeds PathCapability limit")
	}
	return data, nil
}

func exchangeStatus(err error) int {
	switch exchange.ReasonOf(err) {
	case exchange.ReasonInvalidExchangeRequest:
		return http.StatusBadRequest
	case exchange.ReasonUnsupportedClientInput:
		return http.StatusUnprocessableEntity
	case exchange.ReasonProviderCredentialUnavailable:
		// The client reached the selected managed route, but that route cannot
		// authenticate its provider attempt. Reporting a generic gateway failure
		// asks SDKs to retry an operator configuration error and hides the action
		// the user needs to take.
		return http.StatusUnauthorized
	case exchange.ReasonAccessPlanUnavailable,
		exchange.ReasonExchangeRuntimeStopping,
		exchange.ReasonToolDecisionUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

func copyResponseHeaders(destination, source http.Header) {
	clean := source.Clone()
	for _, token := range strings.Split(clean.Get("Connection"), ",") {
		clean.Del(strings.TrimSpace(token))
	}
	for _, name := range []string{
		"Connection",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Proxy-Connection",
		"Keep-Alive",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		clean.Del(name)
	}
	for name, values := range clean {
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

type httpDownstream struct {
	writer http.ResponseWriter
	mode   exchange.ResponseMode
	begun  bool
}

func newHTTPDownstream(writer http.ResponseWriter) *httpDownstream {
	return &httpDownstream{writer: writer}
}

func (downstream *httpDownstream) Begin(
	ctx context.Context,
	envelope exchange.ResponseEnvelope,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if downstream.begun {
		return errors.New("downstream response already began")
	}
	mode := envelope.Mode()
	if mode != exchange.ResponseModeJSON &&
		mode != exchange.ResponseModeEventStream {
		return errors.New("downstream response mode is invalid")
	}
	if envelope.StatusCode() < 200 || envelope.StatusCode() > 599 {
		return errors.New("downstream status code is invalid")
	}
	downstream.mode = mode
	downstream.begun = true
	copyResponseHeaders(downstream.writer.Header(), envelope.Headers())
	downstream.writer.WriteHeader(envelope.StatusCode())
	if mode == exchange.ResponseModeEventStream {
		if flusher, ok := downstream.writer.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	return nil
}

func (downstream *httpDownstream) Write(
	ctx context.Context,
	data []byte,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !downstream.begun {
		return 0, errors.New("downstream response did not begin")
	}
	count, err := downstream.writer.Write(data)
	if downstream.mode == exchange.ResponseModeEventStream {
		if flusher, ok := downstream.writer.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	return count, err
}

func (downstream *httpDownstream) Keepalive(ctx context.Context) error {
	if !downstream.begun ||
		downstream.mode != exchange.ResponseModeEventStream {
		return errors.New("only a begun event stream accepts a keepalive")
	}
	const wire = ": keepalive\n\n"
	count, err := downstream.Write(ctx, []byte(wire))
	if err != nil {
		return err
	}
	if count != len(wire) {
		return io.ErrShortWrite
	}
	return nil
}

func (downstream *httpDownstream) Abort(
	ctx context.Context,
	notice exchange.FailureNotice,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !downstream.begun || downstream.mode != exchange.ResponseModeEventStream {
		return errors.New("only a begun event stream can abort in band")
	}
	data, err := json.Marshal(struct {
		Type           string                         `json:"type"`
		ReasonCode     exchange.ReasonCode            `json:"reasonCode"`
		ProviderStatus int                            `json:"providerStatus,omitempty"`
		ProviderField  exchange.ProviderField         `json:"providerField,omitempty"`
		ProtocolReason protocolcore.Reason            `json:"protocolReason,omitempty"`
		ResponseIssue  exchange.ProviderResponseIssue `json:"providerResponseIssue,omitempty"`
	}{
		Type:           "error",
		ReasonCode:     notice.ReasonCode,
		ProviderStatus: notice.ProviderStatus,
		ProviderField:  notice.ProviderField,
		ProtocolReason: notice.ProtocolReason,
		ResponseIssue:  notice.ResponseIssue,
	})
	if err != nil {
		return err
	}
	encoded, err := ssewire.Encode(ssewire.Event{
		Name: "error",
		Data: data,
	})
	if err != nil {
		return err
	}
	_, err = downstream.Write(ctx, encoded)
	return err
}

func (downstream *httpDownstream) Begun() bool {
	return downstream.begun
}

// serveBlindTunnel forwards a connection whose authority is not an enabled
// AgentEndpoint. It terminates no TLS, reads no request, and records only
// counts, so nothing it carries can reach a record. It reports whether the
// connection audit already reached a terminal.
func (handler *Handler) serveBlindTunnel(
	writer http.ResponseWriter,
	request *http.Request,
	active *operation,
	audit *connectionevent.Connection,
	source connectionevent.Source,
	policyRuleID string,
	authority string,
	host string,
	port uint16,
) bool {
	if handler.blindTunnels == nil {
		if handler.denyConnection(
			request.Context(),
			audit,
			source,
			ReasonAgentEndpointNotConfigured,
		) != nil {
			writeReason(
				writer,
				http.StatusServiceUnavailable,
				ReasonConnectionAuditUnavailable,
				"",
			)
			return false
		}
		writeReason(
			writer,
			http.StatusForbidden,
			ReasonAgentEndpointNotConfigured,
			"",
		)
		return true
	}
	if err := audit.Decide(
		request.Context(),
		connectionevent.DecisionEvidence{
			Source:   source,
			Decision: connectionevent.DecisionAllow,
			RuleID:   policyRuleID,
			// A blind tunnel performs no route translation, so the client's
			// requested authority is the actual destination.
			RouteHost:  host,
			Decryption: connectionevent.DecryptionBlind,
		},
	); err != nil {
		writeReason(
			writer,
			http.StatusServiceUnavailable,
			ReasonConnectionAuditUnavailable,
			"",
		)
		return false
	}
	connectionTerminal := connectionevent.TerminalEvidence{
		Outcome:    connectionevent.OutcomeFailed,
		ErrorClass: blindTunnelFailureClass,
	}
	defer func() {
		handler.finishConnectionAudit(audit, connectionTerminal)
	}()

	egressID, err := handler.exchangeIDs.NewExchangeID(request.Context())
	if err != nil {
		_, connectionTerminal.Outcome, connectionTerminal.ErrorClass =
			blindTunnelTerminal(err)
		writeReason(writer, http.StatusServiceUnavailable, ReasonProxyStopping, "")
		return true
	}
	actionLease, err := handler.blindTunnels.BeginAction(
		request.Context(),
		offlinehold.ActionRequest{ActionID: egressID},
	)
	if err != nil {
		_, connectionTerminal.Outcome, connectionTerminal.ErrorClass =
			blindTunnelTerminal(err)
		writeReason(writer, http.StatusBadGateway, ReasonBlindTunnelFailed, "")
		return true
	}
	defer actionLease.Release()

	// The attempt is durable before Dial reaches egress admission or opens a
	// socket. A failed dial is still evidence of the exact destination that was
	// attempted; an audit failure means no external connection is permitted.
	record, err := handler.beginBlindAudit(
		request.Context(),
		egressID,
		audit.ID(),
		authority,
	)
	if err != nil {
		writeReason(writer, http.StatusServiceUnavailable, ReasonBlindTunnelFailed, "")
		return true
	}
	egressOutcome := egressaudit.OutcomeFailed
	egressErrorClass := blindTunnelFailureClass
	result := blindtunnel.Result{}
	defer func() {
		handler.completeBlindAudit(
			record,
			egressOutcome,
			egressErrorClass,
			result,
		)
	}()

	upstream, lease, err := handler.blindTunnels.Dial(
		request.Context(),
		blindtunnel.DialRequest{
			RequestID: egressID,
			Action:    actionLease,
			Authority: authority,
			Host:      host,
			Port:      port,
		},
	)
	if err != nil {
		egressOutcome, connectionTerminal.Outcome, egressErrorClass =
			blindTunnelTerminal(err)
		connectionTerminal.ErrorClass = egressErrorClass
		writeReason(writer, http.StatusBadGateway, ReasonBlindTunnelFailed, "")
		return true
	}
	defer lease.Release()
	defer upstream.Close()

	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		writeReason(writer, http.StatusInternalServerError, ReasonBlindTunnelFailed, "")
		return true
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		egressOutcome, connectionTerminal.Outcome, egressErrorClass =
			blindTunnelTerminal(err)
		connectionTerminal.ErrorClass = egressErrorClass
		return true
	}
	if !handler.attachConnection(active, client) {
		egressOutcome, connectionTerminal.Outcome, egressErrorClass =
			blindTunnelTerminal(context.Canceled)
		connectionTerminal.ErrorClass = egressErrorClass
		_ = client.Close()
		return true
	}
	defer client.Close()
	if err := writeConnectEstablished(buffered); err != nil {
		egressOutcome, connectionTerminal.Outcome, egressErrorClass =
			blindTunnelTerminal(err)
		connectionTerminal.ErrorClass = egressErrorClass
		return true
	}

	result, err = blindtunnel.Copy(handler.ownerContext, client, upstream)
	egressOutcome, connectionTerminal.Outcome, egressErrorClass =
		blindTunnelTerminal(err)
	connectionTerminal.ErrorClass = egressErrorClass
	connectionTerminal.BytesUp = uint64(result.BytesOut)
	connectionTerminal.BytesDown = uint64(result.BytesIn)
	return true
}

func blindTunnelTerminal(
	err error,
) (egressaudit.Outcome, connectionevent.Outcome, string) {
	switch {
	case err == nil:
		return egressaudit.OutcomeCompleted,
			connectionevent.OutcomeCompleted, ""
	case errors.Is(err, context.Canceled), errors.Is(err, net.ErrClosed):
		return egressaudit.OutcomeCanceled,
			connectionevent.OutcomeCanceled, "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return egressaudit.OutcomeFailed,
			connectionevent.OutcomeFailed, "deadline"
	default:
		return egressaudit.OutcomeFailed,
			connectionevent.OutcomeFailed, blindTunnelFailureClass
	}
}

func (handler *Handler) beginBlindAudit(
	ctx context.Context,
	egressID string,
	connectionID string,
	authority string,
) (egressaudit.Attempt, error) {
	if handler.egressAudit == nil {
		return egressaudit.Attempt{}, nil
	}
	attempt, err := egressaudit.New(egressaudit.NewInput{
		ID:           egressID,
		ConnectionID: connectionID,
		Purpose:      egressaudit.PurposeBlindTunnel,
		PayloadClass: egressaudit.PayloadOpaqueTunnel,
		Parent: egressaudit.ParentRef{
			Kind: egressaudit.ParentBlindConnection,
			ID:   connectionID,
		},
		Caller:       egressaudit.CallerCore,
		TargetOrigin: "https://" + authority,
		Decision: egressaudit.BuiltInDirectDecision(
			egressaudit.AuthorityNetwork,
		),
		StartedAt: handler.clock(),
	})
	if err != nil {
		return egressaudit.Attempt{}, err
	}
	if _, err := handler.egressAudit.Append(ctx, attempt); err != nil {
		return egressaudit.Attempt{}, err
	}
	return attempt, nil
}

func (handler *Handler) completeBlindAudit(
	attempt egressaudit.Attempt,
	outcome egressaudit.Outcome,
	errorClass string,
	result blindtunnel.Result,
) {
	if handler.egressAudit == nil || attempt.ID() == "" {
		return
	}
	terminal, err := attempt.Finish(egressaudit.TerminalInput{
		Outcome:     outcome,
		ErrorClass:  errorClass,
		BytesOut:    result.BytesOut,
		BytesIn:     result.BytesIn,
		CompletedAt: completionTime(attempt, handler.clock()),
	})
	if err != nil {
		handler.reportTerminalFailure(fmt.Errorf(
			"construct blind-tunnel EgressAttempt terminal: %w",
			err,
		))
		return
	}
	auditTimeout := handler.auditTimeout
	if auditTimeout <= 0 {
		auditTimeout = defaultAuditCompletionLimit
	}
	ctx, cancel := context.WithTimeout(
		context.WithoutCancel(handler.ownerContext),
		auditTimeout,
	)
	defer cancel()
	_, _ = handler.egressAudit.Complete(ctx, terminal)
}

func (handler *Handler) reportTerminalFailure(err error) {
	reporter, ok := handler.egressAudit.(terminalFailureReporter)
	if ok {
		reporter.ReportTerminalFailure(err)
	}
}

func completionTime(attempt egressaudit.Attempt, completedAt time.Time) time.Time {
	if !completedAt.IsZero() && completedAt.Before(attempt.StartedAt()) {
		return attempt.StartedAt()
	}
	return completedAt
}

// serveCleartextForward forwards a cleartext proxy request to its own origin.
// A proxy necessarily sees an unencrypted request line, so the record says
// plainly that this connection was not encrypted rather than implying a
// blindness it does not have. It still records no body.
func (handler *Handler) serveCleartextForward(
	writer http.ResponseWriter,
	request *http.Request,
	active *operation,
	audit *connectionevent.Connection,
	source connectionevent.Source,
	policyRuleID string,
) bool {
	if handler.blindTunnels == nil {
		if handler.denyConnection(
			request.Context(),
			audit,
			source,
			ReasonConnectOnly,
		) != nil {
			writeReason(
				writer,
				http.StatusServiceUnavailable,
				ReasonConnectionAuditUnavailable,
				"",
			)
			return false
		}
		writeReason(writer, http.StatusMethodNotAllowed, ReasonConnectOnly, "")
		return true
	}
	if request.URL.Scheme != "http" {
		writeReason(writer, http.StatusBadRequest, ReasonConnectAuthorityInvalid, "")
		return true
	}
	host, port, err := cleartextAuthority(request.URL.Host)
	if err != nil {
		writeReason(writer, http.StatusBadRequest, ReasonConnectAuthorityInvalid, "")
		return true
	}
	authority := net.JoinHostPort(host, strconv.Itoa(int(port)))
	if err := audit.Decide(
		request.Context(),
		connectionevent.DecisionEvidence{
			Source:     source,
			Decision:   connectionevent.DecisionAllow,
			RuleID:     policyRuleID,
			RouteHost:  host,
			Decryption: connectionevent.DecryptionBlind,
		},
	); err != nil {
		writeReason(
			writer,
			http.StatusServiceUnavailable,
			ReasonConnectionAuditUnavailable,
			"",
		)
		return false
	}

	egressID, err := handler.exchangeIDs.NewExchangeID(request.Context())
	if err != nil {
		writeReason(writer, http.StatusServiceUnavailable, ReasonProxyStopping, "")
		return true
	}
	actionLease, err := handler.blindTunnels.BeginAction(
		request.Context(),
		offlinehold.ActionRequest{ActionID: egressID},
	)
	if err != nil {
		writeReason(writer, http.StatusBadGateway, ReasonBlindTunnelFailed, "")
		return true
	}
	defer actionLease.Release()
	upstream, lease, err := handler.blindTunnels.Dial(
		request.Context(),
		blindtunnel.DialRequest{
			RequestID: egressID,
			Action:    actionLease,
			Authority: authority,
			Host:      host,
			Port:      port,
		},
	)
	if err != nil {
		writeReason(writer, http.StatusBadGateway, ReasonBlindTunnelFailed, "")
		return true
	}
	defer lease.Release()
	defer upstream.Close()
	_ = active

	record, err := handler.beginBlindAudit(
		request.Context(),
		egressID,
		audit.ID(),
		authority,
	)
	if err != nil {
		writeReason(writer, http.StatusServiceUnavailable, ReasonBlindTunnelFailed, "")
		return true
	}

	// Write in origin form and without hop-by-hop or proxy headers, so the
	// origin sees an ordinary request and never a proxy credential.
	forwarded := request.Clone(request.Context())
	forwarded.RequestURI = ""
	forwarded.Header = forwardableHeaders(request.Header)
	forwarded.Host = request.URL.Host
	if err := forwarded.Write(upstream); err != nil {
		handler.completeBlindAudit(
			record,
			egressaudit.OutcomeFailed,
			"forward_write_failed",
			blindtunnel.Result{},
		)
		writeReason(writer, http.StatusBadGateway, ReasonBlindTunnelFailed, "")
		return true
	}
	response, err := http.ReadResponse(bufio.NewReader(upstream), forwarded)
	if err != nil {
		handler.completeBlindAudit(
			record,
			egressaudit.OutcomeFailed,
			"forward_read_failed",
			blindtunnel.Result{},
		)
		writeReason(writer, http.StatusBadGateway, ReasonBlindTunnelFailed, "")
		return true
	}
	defer response.Body.Close()
	copyResponseHeaders(writer.Header(), response.Header)
	writer.WriteHeader(response.StatusCode)
	copied, _ := io.Copy(writer, response.Body)
	handler.completeBlindAudit(
		record,
		egressaudit.OutcomeCompleted,
		"",
		blindtunnel.Result{BytesIn: copied},
	)
	handler.finishConnectionAudit(audit, connectionevent.TerminalEvidence{
		Outcome:   connectionevent.OutcomeCompleted,
		BytesDown: uint64(copied),
	})
	return true
}

func cleartextAuthority(hostPort string) (string, uint16, error) {
	if host, port, err := splitAuthority(hostPort); err == nil {
		return host, port, nil
	}
	host := canonicalCONNECTHost(hostPort)
	if host == "" || strings.Contains(host, ":") {
		return "", 0, errors.New("cleartext authority is invalid")
	}
	return host, 80, nil
}

// forwardableHeaders removes proxy and hop-by-hop headers so the origin sees
// an ordinary request and never this proxy's credential.
func forwardableHeaders(source http.Header) http.Header {
	headers := source.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	for _, token := range strings.Split(headers.Get("Connection"), ",") {
		headers.Del(strings.TrimSpace(token))
	}
	for _, name := range []string{
		"Connection",
		"Proxy-Authorization",
		"Proxy-Connection",
		"Keep-Alive",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		headers.Del(name)
	}
	return headers
}

// policyTarget is what the connection is asking to reach. A CONNECT names it
// in the authority; a cleartext forward names it in the absolute
// request-target.
func policyTarget(
	request *http.Request,
	cleartextForward bool,
) (string, uint16) {
	if cleartextForward {
		host, port, err := cleartextAuthority(request.URL.Host)
		if err != nil {
			return "", 0
		}
		return host, port
	}
	host, port, err := splitAuthority(request.Host)
	if err != nil {
		return "", 0
	}
	return host, port
}
