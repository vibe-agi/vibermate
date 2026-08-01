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

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/certidentity"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
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
	proxyUsername         = "capture"
	maxInnerHeaderBytes   = 1 << 20
	defaultHandshakeLimit = 10 * time.Second
	exchangeIDBytes       = 20
)

type ReasonCode string

const (
	ReasonProxyAuthenticationRequired   ReasonCode = "proxy_authentication_required"
	ReasonCaptureRunRejected            ReasonCode = "capture_run_rejected"
	ReasonAgentEndpointNotConfigured    ReasonCode = "agent_endpoint_not_configured"
	ReasonConnectAuthorityInvalid       ReasonCode = "connect_authority_invalid"
	ReasonConnectSNIMismatch            ReasonCode = "connect_sni_mismatch"
	ReasonMITMUnavailable               ReasonCode = "mitm_unavailable"
	ReasonRequestAuthorityMismatch      ReasonCode = "request_authority_mismatch"
	ReasonAgentEndpointChanged          ReasonCode = "agent_endpoint_changed"
	ReasonPathUnsupported               ReasonCode = "path_unsupported"
	ReasonResponsesWebSocketUnsupported ReasonCode = "responses_websocket_unsupported"
	ReasonRequestBodyInvalid            ReasonCode = "request_body_invalid"
	ReasonExchangeFailed                ReasonCode = "exchange_failed"
	ReasonOriginalEgressFailed          ReasonCode = "original_egress_failed"
	ReasonProxyStopping                 ReasonCode = "proxy_stopping"
	ReasonConnectOnly                   ReasonCode = "connect_only"
	ReasonConnectionAuditUnavailable    ReasonCode = "connection_audit_unavailable"
	ReasonProfileOperationUnsupported   ReasonCode = "profile_operation_unsupported"
)

var ErrProxyStopping = errors.New("loopback proxy is stopping")

type RunAuthorizer interface {
	AuthorizeProxy(
		context.Context,
		capturerun.ProxyCapability,
	) (capturerun.Evidence, error)
}

type CertificateAuthority interface {
	Identity() localca.RootIdentity
	Issue(context.Context, access.LeafIssuanceAdmission) (tls.Certificate, error)
}

type IngressAuthority interface {
	access.IngressResolver
	access.LeafIssuanceAdmitter
}

type OriginalClient interface {
	Do(context.Context, originaltransport.Request) (*http.Response, error)
}

type ExchangeIDSource interface {
	NewExchangeID(context.Context) (string, error)
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
	OwnerContext     context.Context
	Runs             RunAuthorizer
	Ingress          IngressAuthority
	Paths            *pathcapability.Catalog
	Exchanges        exchange.Executor
	Original         OriginalClient
	Certificates     CertificateAuthority
	Connections      ConnectionJournal
	ExchangeIDs      ExchangeIDSource
	HandshakeTimeout time.Duration
}

type operation struct {
	connection net.Conn
}

type Handler struct {
	runs         RunAuthorizer
	ingress      IngressAuthority
	paths        *pathcapability.Catalog
	exchanges    exchange.Executor
	original     OriginalClient
	certificates CertificateAuthority
	connections  ConnectionJournal
	exchangeIDs  ExchangeIDSource
	handshake    time.Duration
	ownerContext context.Context

	mu        sync.Mutex
	accepting bool
	active    map[*operation]struct{}
	changed   chan struct{}
	stopOwner func() bool
}

func New(options Options) (*Handler, error) {
	if options.OwnerContext == nil ||
		options.Runs == nil ||
		options.Ingress == nil ||
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
		runs:         options.Runs,
		ingress:      options.Ingress,
		paths:        options.Paths,
		exchanges:    options.Exchanges,
		original:     options.Original,
		certificates: options.Certificates,
		connections:  options.Connections,
		exchangeIDs:  options.ExchangeIDs,
		handshake:    options.HandshakeTimeout,
		ownerContext: options.OwnerContext,
		accepting:    true,
		active:       make(map[*operation]struct{}),
		changed:      make(chan struct{}),
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
	if request == nil || request.Method != http.MethodConnect {
		writeReason(writer, http.StatusMethodNotAllowed, ReasonConnectOnly, "")
		return
	}
	port := uint16(0)
	if _, parsedPort, splitErr := splitAuthority(request.Host); splitErr == nil {
		port = parsedPort
	}
	audit, err := handler.connections.Start(
		request.Context(),
		connectionevent.Attempt{
			Source: connectionevent.Source{
				Confidence: connectionevent.SourceConfidenceUnknown,
			},
			RequestedHost: request.Host,
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
	capability, err := capturerun.NewProxyCapability(rawCapability)
	if err != nil {
		if handler.denyConnection(
			request.Context(),
			audit,
			connectionevent.Source{
				Confidence: connectionevent.SourceConfidenceUnknown,
			},
			ReasonCaptureRunRejected,
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
		writeReason(writer, http.StatusForbidden, ReasonCaptureRunRejected, "")
		return
	}
	evidence, err := handler.runs.AuthorizeProxy(request.Context(), capability)
	if err != nil {
		if handler.denyConnection(
			request.Context(),
			audit,
			connectionevent.Source{
				Confidence: connectionevent.SourceConfidenceUnknown,
			},
			ReasonCaptureRunRejected,
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
		writeReason(writer, http.StatusForbidden, ReasonCaptureRunRejected, "")
		return
	}
	source := connectionSource(evidence)
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
			return
		}
		terminal = true
		writeReason(writer, http.StatusForbidden, ReasonAgentEndpointNotConfigured, "")
		return
	}
	if err := audit.Decide(
		request.Context(),
		connectionevent.DecisionEvidence{
			Source:               source,
			Decision:             connectionevent.DecisionAllow,
			RuleID:               "m0.agent_endpoint_exact",
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
		evidence,
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
	run capturerun.Evidence,
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
	observedSNI := ""
	config := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{string(access.ApplicationProtocolHTTP1)},
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
				run,
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
	err = inner.Serve(listener)
	if errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (handler *Handler) serveInner(
	writer http.ResponseWriter,
	request *http.Request,
	run capturerun.Evidence,
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
			run.Adapter != nil &&
			run.Adapter.Supports(
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
			run,
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
			exchangeID,
			body,
		)
	case pathcapability.KindOpaque:
		handler.serveOriginal(
			writer,
			request,
			binding,
			capability,
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
	run capturerun.Evidence,
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
	clientRequest, err := exchange.NewClientRequest(
		exchangeID,
		binding,
		operation,
		body,
		capability.ReplayClass(),
		exchange.WithClientHelloObservation(observation),
		// Every identity is generated independently; association travels as
		// typed references rather than as a delimiter-joined string.
		exchange.WithIngressCorrelation(run.RunID, audit.ID()),
	)
	if err != nil {
		writeReason(writer, http.StatusBadRequest, ReasonRequestBodyInvalid, "")
		return
	}
	downstream := newHTTPDownstream(writer)
	result, err := handler.exchanges.Execute(
		request.Context(),
		clientRequest,
		downstream,
	)
	if result.RouteHost != "" {
		_ = audit.Connected(
			request.Context(),
			connectionevent.ConnectedEvidence{
				RouteHost:           result.RouteHost,
				CredentialBindingID: result.CredentialBindingID,
			},
		)
	}
	if err != nil && !downstream.Begun() {
		writeReason(
			writer,
			exchangeStatus(err),
			ReasonExchangeFailed,
			string(exchange.ReasonOf(err)),
		)
	}
}

func connectionSource(run capturerun.Evidence) connectionevent.Source {
	return connectionevent.Source{
		IngressID:  run.RunID,
		Label:      run.ExecutableLabel,
		Confidence: connectionevent.SourceConfidenceConfigured,
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
	default:
		return connectionevent.OutcomeFailed, string(ReasonMITMUnavailable)
	}
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
		requestID,
		body,
	)
}

func (handler *Handler) serveOriginal(
	writer http.ResponseWriter,
	request *http.Request,
	binding access.IngressBinding,
	capability pathcapability.Capability,
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
	requestID string,
	body []byte,
) {
	frozen, err := originaltransport.NewRequest(originaltransport.RequestOptions{
		RequestID:    requestID,
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
	if !found || username != proxyUsername || password == "" ||
		strings.Contains(password, ":") {
		return "", errors.New("proxy authorization credentials are invalid")
	}
	return password, nil
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

func splitAuthority(authority string) (string, uint16, error) {
	host, portText, err := net.SplitHostPort(authority)
	if err != nil || host == "" || portText == "" {
		return "", 0, errors.New("CONNECT authority must contain host and port")
	}
	if strings.ToLower(host) != host {
		return "", 0, errors.New("CONNECT host is not canonical")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return "", 0, errors.New("CONNECT port is invalid")
	}
	return host, uint16(port), nil
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

func sniMatches(expected, actual string) bool {
	if address := net.ParseIP(expected); address != nil {
		return actual == ""
	}
	return actual == expected
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
	mode exchange.ResponseMode,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if downstream.begun {
		return errors.New("downstream response already began")
	}
	downstream.mode = mode
	downstream.begun = true
	downstream.writer.Header().Set("Cache-Control", "no-store")
	switch mode {
	case exchange.ResponseModeJSON:
		downstream.writer.Header().Set("Content-Type", "application/json")
	case exchange.ResponseModeEventStream:
		downstream.writer.Header().Set("Content-Type", "text/event-stream")
	default:
		return errors.New("downstream response mode is invalid")
	}
	downstream.writer.WriteHeader(http.StatusOK)
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
