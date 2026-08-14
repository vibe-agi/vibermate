package providertransport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

var (
	ErrClientClosing      = errors.New("provider client is closing")
	ErrRedirectNotAllowed = errors.New("upstream redirect is not allowed")
	errSemanticTerminal   = errors.New("provider semantic terminal confirmed")
)

const (
	responseCanceledClass = "response_canceled"
	responseTimeoutClass  = "response_timeout"
	responseFailureClass  = "response_body_failed"
)

type ClientOptions struct {
	Coordinator    offlinehold.Coordinator
	Authenticator  Authenticator
	Authenticators []Authenticator
	Transport      Transport
	// Audit records one immutable attempt per real outbound.
	Audit                 egressaudit.Writer
	RawEvidence           rawevidence.Observer
	RawObservationTimeout time.Duration
	RawResponseBodyBytes  int
}

// terminalFailureReporter is implemented by the production audit boundary.
// Complete cannot return an error once an outbound response has been handed
// off, so an invalid terminal must cross this explicit durability boundary.
type terminalFailureReporter interface {
	ReportTerminalFailure(error)
}

// rawEvidenceFailureReporter is deliberately separate from the core Egress
// terminal boundary. Raw HTTP retention is best-effort: a failure must remain
// observable, but it must never alter the provider response seen by the Agent.
type rawEvidenceFailureReporter interface {
	ReportRawEvidenceFailure(error)
}

type Evidence struct {
	Credential   CredentialEvidence
	Presentation WirePresentationEvidence
	Transport    transportprofile.Evidence
}

type clientOperation struct {
	mu sync.Mutex

	cancel context.CancelCauseFunc
	lease  offlinehold.Lease
	body   *leaseBody
}

func (operation *clientOperation) setLease(lease offlinehold.Lease) {
	operation.mu.Lock()
	defer operation.mu.Unlock()
	operation.lease = lease
}

func (operation *clientOperation) setBody(body *leaseBody) {
	operation.mu.Lock()
	defer operation.mu.Unlock()
	operation.body = body
}

func (operation *clientOperation) cancelAndClose(cause error) {
	operation.mu.Lock()
	cancel := operation.cancel
	body := operation.body
	operation.mu.Unlock()
	cancel(cause)
	if body != nil {
		_ = body.Close()
	}
}

func (operation *clientOperation) takeLease() offlinehold.Lease {
	operation.mu.Lock()
	defer operation.mu.Unlock()
	lease := operation.lease
	operation.lease = nil
	return lease
}

type Client struct {
	mu sync.Mutex

	coordinator    offlinehold.Coordinator
	authenticators map[providerauth.DriverRef]Authenticator
	transport      Transport
	audit          egressaudit.Writer
	raw            rawevidence.Observer
	rawTimeout     time.Duration
	rawBodyBytes   int
	clock          func() time.Time
	operations     map[*clientOperation]struct{}
	closing        bool
	changed        chan struct{}
}

func NewClient(options ClientOptions) (*Client, error) {
	if options.Coordinator == nil || options.Transport == nil {
		return nil, errors.New("provider client dependencies are incomplete")
	}
	authenticators := make([]Authenticator, 0, len(options.Authenticators)+1)
	if options.Authenticator != nil {
		authenticators = append(authenticators, options.Authenticator)
	}
	authenticators = append(authenticators, options.Authenticators...)
	if len(authenticators) == 0 {
		return nil, errors.New("provider client has no authenticators")
	}
	rawTimeout := options.RawObservationTimeout
	rawBodyBytes := options.RawResponseBodyBytes
	if options.RawEvidence != nil {
		if rawTimeout == 0 {
			rawTimeout = rawevidence.DefaultObservationLimit
		}
		if rawBodyBytes == 0 {
			rawBodyBytes = rawevidence.DefaultMaximumBodyBytes
		}
		if rawTimeout <= 0 || rawBodyBytes <= 0 ||
			rawBodyBytes > rawevidence.DefaultMaximumBodyBytes {
			return nil, errors.New("provider raw evidence configuration is invalid")
		}
	}
	byRef := make(map[providerauth.DriverRef]Authenticator, len(authenticators))
	for _, authenticator := range authenticators {
		if authenticator == nil || authenticator.Ref().String() == "" {
			return nil, errors.New("provider client authenticator is invalid")
		}
		if _, duplicate := byRef[authenticator.Ref()]; duplicate {
			return nil, errors.New("provider client authenticator is duplicated")
		}
		byRef[authenticator.Ref()] = authenticator
	}
	return &Client{
		coordinator:    options.Coordinator,
		authenticators: byRef,
		transport:      options.Transport,
		audit:          options.Audit,
		raw:            options.RawEvidence,
		rawTimeout:     rawTimeout,
		rawBodyBytes:   rawBodyBytes,
		clock:          time.Now,
		operations:     make(map[*clientOperation]struct{}),
		changed:        make(chan struct{}),
	}, nil
}

func NewProductionClient(
	coordinator offlinehold.Coordinator,
	authenticator Authenticator,
	timeouts TransportTimeouts,
	audit egressaudit.Writer,
) (*Client, error) {
	transport, err := newProductionTransport(timeouts)
	if err != nil {
		return nil, err
	}
	return NewClient(ClientOptions{
		Coordinator:   coordinator,
		Authenticator: authenticator,
		Transport:     transport,
		Audit:         audit,
	})
}

func NewProductionClientWithAuthenticators(
	coordinator offlinehold.Coordinator,
	authenticators []Authenticator,
	timeouts TransportTimeouts,
	audit egressaudit.Writer,
	rawEvidence rawevidence.Observer,
) (*Client, error) {
	transport, err := newProductionTransport(timeouts)
	if err != nil {
		return nil, err
	}
	return NewClient(ClientOptions{
		Coordinator:    coordinator,
		Authenticators: authenticators,
		Transport:      transport,
		Audit:          audit,
		RawEvidence:    rawEvidence,
	})
}

// Do acquires the global egress lease before secret access, authentication, or
// transport use. The lease remains active until the response body reaches EOF
// or is closed.
func (client *Client) Do(
	ctx context.Context,
	frozen Request,
) (*http.Response, Evidence, error) {
	if ctx == nil {
		return nil, Evidence{}, errors.New("provider request context is nil")
	}
	if err := frozen.target.validate(); err != nil {
		return nil, Evidence{}, err
	}
	var authenticator Authenticator
	if frozen.credentialMode == providerauth.CredentialManaged {
		var supported bool
		authenticator, supported = client.authenticators[frozen.authDriverRef]
		if !supported {
			return nil, Evidence{}, errors.New(
				"provider AuthDriver does not match the frozen plan",
			)
		}
	} else if frozen.credentialMode !=
		providerauth.CredentialClientPassthrough {
		return nil, Evidence{}, errors.New(
			"provider credential source does not match the frozen plan",
		)
	}
	operationContext, operation, err := client.begin(ctx)
	if err != nil {
		return nil, Evidence{}, err
	}
	handoff := false
	defer func() {
		if !handoff {
			client.finish(operation, nil)
		}
	}()

	lease, err := client.coordinator.Acquire(
		operationContext,
		offlinehold.AcquireRequest{
			RequestID: frozen.requestID,
			Action:    frozen.action,
			Target:    frozen.probeTarget,
			SizeBytes: int64(len(frozen.body)),
		},
	)
	if err != nil {
		return nil, Evidence{}, fmt.Errorf("acquire provider egress lease: %w", err)
	}
	operation.setLease(lease)
	if err := operationContext.Err(); err != nil {
		return nil, Evidence{}, err
	}

	request, err := http.NewRequestWithContext(
		operationContext,
		frozen.method,
		frozen.buildURL().String(),
		bytes.NewReader(frozen.body),
	)
	if err != nil {
		return nil, Evidence{}, err
	}
	request.Host = frozen.target.HTTPAuthority()
	request.Header = prepareProviderHeaders(
		frozen.headers,
		frozen.credentialMode,
	)
	if frozen.credentialMode == providerauth.CredentialManaged {
		request.Header.Set("Content-Type", "application/json")
	}
	request.ContentLength = int64(len(frozen.body))
	if err := applyUpstreamWireHeaders(
		request.Header,
		frozen.wireVariant,
		frozen.clientUserAgent,
	); err != nil {
		return nil, Evidence{}, err
	}

	evidence := CredentialEvidence{
		DriverRef:  string(frozen.credentialMode),
		SecretRead: false,
	}
	if authenticator != nil {
		evidence, err = authenticator.Apply(
			operationContext,
			request,
			frozen.secretReference,
			frozen.target,
		)
		if err != nil {
			return nil, Evidence{}, fmt.Errorf(
				"finalize provider authentication: %w",
				err,
			)
		}
	}
	if err := validateUpstreamWireHeaders(
		request.Header,
		frozen.wireVariant,
		frozen.clientUserAgent,
	); err != nil {
		stripProviderCredentialHeaders(request.Header)
		return nil, Evidence{}, err
	}
	rawContext, hasRawContext := frozen.RawEvidenceContext()
	rawEnabled := false
	if client.raw != nil {
		if !hasRawContext {
			client.reportRawEvidenceFailure(errors.New(
				"provider request has no Raw evidence context",
			))
		} else {
			rawEnabled = rawContext.Recording != rawevidence.RecordingOff
		}
	}
	// The core attempt is durable before either optional Raw evidence or the
	// first outbound byte. Raw persistence can degrade without weakening this
	// auditable routing/credential/attempt boundary.
	record, recordErr := client.beginAudit(operationContext, frozen)
	if recordErr != nil {
		stripProviderCredentialHeaders(request.Header)
		return nil, Evidence{}, recordErr
	}
	if rawEnabled {
		if _, err := client.observeRaw(operationContext, rawevidence.Observation{
			Context:         rawContext,
			Layer:           rawevidence.LayerProviderEgress,
			Method:          request.Method,
			Scheme:          request.URL.Scheme,
			Authority:       request.Host,
			Path:            request.URL.EscapedPath(),
			RawQuery:        request.URL.RawQuery,
			Headers:         request.Header.Clone(),
			Body:            frozen.body,
			Complete:        true,
			Representation:  "http_message",
			ContentType:     request.Header.Get("Content-Type"),
			ContentEncoding: request.Header.Get("Content-Encoding"),
		}); err != nil {
			client.reportRawEvidenceFailure(fmt.Errorf(
				"record provider egress Raw evidence: %w", err,
			))
			rawEnabled = false
		}
	}
	response, transportEvidence, err := client.transport.RoundTrip(
		request,
		TransportDispatch{
			target:      frozen.target,
			plan:        frozen.wireVariant.TransportFingerprintPlan(),
			clientHello: frozen.clientHello,
		},
	)
	stripProviderCredentialHeaders(request.Header)
	attemptEvidence := Evidence{
		Credential:   evidence,
		Presentation: frozen.WirePresentationEvidence(),
		Transport:    transportEvidence,
	}
	if err != nil {
		var rawErr error
		if response != nil && response.Body != nil {
			if rawEnabled {
				response.Body = client.rawResponseBody(
					rawContext,
					request,
					response,
				)
			}
			rawErr = response.Body.Close()
		} else if rawEnabled {
			_, rawErr = client.observeUnavailableResponse(
				operationContext,
				rawContext,
				request,
				"transport_failed",
			)
		}
		client.completeAudit(
			operationContext, record, egressaudit.OutcomeFailed,
			"transport_failed", int64(len(frozen.body)), 0,
		)
		client.reportRawEvidenceFailure(rawErr)
		return nil, attemptEvidence, fmt.Errorf("send provider request: %w", err)
	}
	if response == nil || response.Body == nil {
		var rawErr error
		if rawEnabled {
			_, rawErr = client.observeUnavailableResponse(
				operationContext,
				rawContext,
				request,
				"incomplete_response",
			)
		}
		client.completeAudit(
			operationContext, record, egressaudit.OutcomeFailed,
			"incomplete_response", int64(len(frozen.body)), 0,
		)
		client.reportRawEvidenceFailure(rawErr)
		return nil, attemptEvidence, errors.New(
			"provider transport returned an incomplete response",
		)
	}
	if rawEnabled {
		response.Body = client.rawResponseBody(
			rawContext,
			request,
			response,
		)
	}
	if response.StatusCode >= 300 && response.StatusCode <= 399 {
		closeErr := response.Body.Close()
		client.completeAudit(
			operationContext, record, egressaudit.OutcomeFailed,
			"redirect_denied", int64(len(frozen.body)), 0,
		)
		return nil, attemptEvidence, errors.Join(ErrRedirectNotAllowed, closeErr)
	}

	responseBody := response.Body
	counted := &countingReader{reader: responseBody}
	body := &leaseBody{
		reader: counted,
		close:  responseBody,
		finish: func(terminalErr error) {
			outcome, errorClass := responseAuditTerminal(
				operationContext,
				terminalErr,
			)
			client.completeAudit(
				context.WithoutCancel(operationContext),
				record,
				outcome,
				errorClass,
				int64(len(frozen.body)),
				counted.count(),
			)
			client.finish(operation, terminalErr)
		},
	}
	operation.setBody(body)
	response.Body = body
	handoff = true
	return response, attemptEvidence, nil
}

func (client *Client) rawResponseBody(
	rawContext rawevidence.Context,
	request *http.Request,
	response *http.Response,
) io.ReadCloser {
	maximumBodyBytes := client.rawBodyBytes
	if rawContext.Recording != rawevidence.RecordingFull {
		maximumBodyBytes = 0
	}
	return newRawResponseBody(rawResponseBodyOptions{
		Source:           response.Body,
		Observer:         client.raw,
		Timeout:          client.rawTimeout,
		MaximumBodyBytes: maximumBodyBytes,
		Context:          rawContext,
		StatusCode:       response.StatusCode,
		Scheme:           request.URL.Scheme,
		Authority:        request.Host,
		Path:             request.URL.EscapedPath(),
		RawQuery:         request.URL.RawQuery,
		Headers:          response.Header.Clone(),
		Trailers:         func() http.Header { return response.Trailer.Clone() },
		ReportFailure:    client.reportRawEvidenceFailure,
	})
}

func (client *Client) observeUnavailableResponse(
	ctx context.Context,
	rawContext rawevidence.Context,
	request *http.Request,
	reason string,
) (rawevidence.Watermark, error) {
	return client.observeRaw(ctx, rawevidence.Observation{
		Context:          rawContext,
		Layer:            rawevidence.LayerProviderResponse,
		Scheme:           request.URL.Scheme,
		Authority:        request.Host,
		Path:             request.URL.EscapedPath(),
		RawQuery:         request.URL.RawQuery,
		Unavailable:      true,
		Complete:         false,
		IncompleteReason: reason,
		Representation:   "http_message",
	})
}

func (client *Client) observeRaw(
	ctx context.Context,
	observation rawevidence.Observation,
) (rawevidence.Watermark, error) {
	operation, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		client.rawTimeout,
	)
	defer cancel()
	return client.raw.Observe(operation, observation)
}

func (client *Client) reportRawEvidenceFailure(err error) {
	if err == nil {
		return
	}
	reporter, ok := client.audit.(rawEvidenceFailureReporter)
	if ok {
		reporter.ReportRawEvidenceFailure(err)
	}
}

type rawResponseBodyOptions struct {
	Source           io.ReadCloser
	Observer         rawevidence.Observer
	Timeout          time.Duration
	MaximumBodyBytes int
	Context          rawevidence.Context
	StatusCode       int
	Scheme           string
	Authority        string
	Path             string
	RawQuery         string
	Headers          http.Header
	Trailers         func() http.Header
	ReportFailure    func(error)
}

type rawResponseBody struct {
	options rawResponseBodyOptions
	hash    hash.Hash

	mu          sync.Mutex
	condition   *sync.Cond
	body        []byte
	total       int64
	activeReads int
	closed      bool
	reachedEOF  bool

	once       sync.Once
	observeErr error
}

func newRawResponseBody(options rawResponseBodyOptions) *rawResponseBody {
	body := &rawResponseBody{
		options: options,
		hash:    sha256.New(),
		body:    make([]byte, 0, min(options.MaximumBodyBytes, 32<<10)),
	}
	body.condition = sync.NewCond(&body.mu)
	return body
}

func (body *rawResponseBody) Read(destination []byte) (int, error) {
	body.mu.Lock()
	body.activeReads++
	body.mu.Unlock()
	count, readErr := body.options.Source.Read(destination)
	body.mu.Lock()
	if count > 0 {
		_, _ = body.hash.Write(destination[:count])
		body.total += int64(count)
		remaining := body.options.MaximumBodyBytes - len(body.body)
		if remaining > 0 {
			retained := min(remaining, count)
			body.body = append(body.body, destination[:retained]...)
		}
	}
	if errors.Is(readErr, io.EOF) {
		body.reachedEOF = true
	}
	body.activeReads--
	body.condition.Broadcast()
	body.mu.Unlock()
	if readErr != nil {
		body.finalize(readErr)
	}
	return count, readErr
}

func (body *rawResponseBody) Close() error {
	closeErr := body.options.Source.Close()
	body.mu.Lock()
	body.closed = true
	for body.activeReads != 0 {
		body.condition.Wait()
	}
	reachedEOF := body.reachedEOF
	body.mu.Unlock()
	terminalErr := closeErr
	if terminalErr == nil && !reachedEOF {
		terminalErr = io.ErrClosedPipe
	}
	body.finalize(terminalErr)
	return closeErr
}

func (body *rawResponseBody) finalize(terminalErr error) error {
	body.once.Do(func() {
		body.mu.Lock()
		retained := slices.Clone(body.body)
		total := body.total
		reachedEOF := body.reachedEOF
		digestBytes := body.hash.Sum(nil)
		body.mu.Unlock()
		var digest [sha256.Size]byte
		copy(digest[:], digestBytes)
		complete := reachedEOF && total == int64(len(retained))
		reason := ""
		switch {
		case reachedEOF && !complete:
			reason = "response_payload_limit"
		case errors.Is(terminalErr, io.ErrClosedPipe):
			reason = "response_closed_before_eof"
		case !reachedEOF:
			reason = "response_read_failed"
		}
		trailers := make(http.Header)
		if body.options.Trailers != nil {
			trailers = body.options.Trailers()
		}
		operation, cancel := context.WithTimeout(
			context.Background(),
			body.options.Timeout,
		)
		defer cancel()
		_, body.observeErr = body.options.Observer.Observe(
			operation,
			rawevidence.Observation{
				Context:             body.options.Context,
				Layer:               rawevidence.LayerProviderResponse,
				StatusCode:          body.options.StatusCode,
				Scheme:              body.options.Scheme,
				Authority:           body.options.Authority,
				Path:                body.options.Path,
				RawQuery:            body.options.RawQuery,
				Headers:             body.options.Headers.Clone(),
				Trailers:            trailers,
				Body:                retained,
				TotalBodyBytes:      total,
				BodySHA256:          digest,
				DigestAvailable:     true,
				FullDigestAvailable: reachedEOF,
				Complete:            complete,
				IncompleteReason:    reason,
				Representation:      "http_message",
				ContentType:         body.options.Headers.Get("Content-Type"),
				ContentEncoding:     body.options.Headers.Get("Content-Encoding"),
			},
		)
		if body.observeErr != nil && body.options.ReportFailure != nil {
			body.options.ReportFailure(fmt.Errorf(
				"record provider response Raw evidence: %w", body.observeErr,
			))
		}
	})
	return body.observeErr
}

func (client *Client) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("provider client shutdown context is nil")
	}
	client.mu.Lock()
	client.closing = true
	operations := make([]*clientOperation, 0, len(client.operations))
	for operation := range client.operations {
		operations = append(operations, operation)
	}
	client.signalLocked()
	client.mu.Unlock()

	for _, operation := range operations {
		operation.cancelAndClose(ErrClientClosing)
	}

	client.mu.Lock()
	for len(client.operations) != 0 {
		changed := client.changed
		client.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
		client.mu.Lock()
	}
	client.mu.Unlock()
	if transport, ok := client.transport.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
	return nil
}

func (client *Client) begin(
	ctx context.Context,
) (context.Context, *clientOperation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing {
		return nil, nil, ErrClientClosing
	}
	operationContext, cancel := context.WithCancelCause(ctx)
	operation := &clientOperation{cancel: cancel}
	client.operations[operation] = struct{}{}
	client.signalLocked()
	return operationContext, operation, nil
}

func (client *Client) finish(operation *clientOperation, _ error) {
	client.mu.Lock()
	if _, exists := client.operations[operation]; !exists {
		client.mu.Unlock()
		return
	}
	delete(client.operations, operation)
	client.signalLocked()
	client.mu.Unlock()
	if lease := operation.takeLease(); lease != nil {
		lease.Release()
	}
	operation.cancel(errors.New("provider operation finished"))
}

func (client *Client) signalLocked() {
	close(client.changed)
	client.changed = make(chan struct{})
}

func prepareProviderHeaders(
	source http.Header,
	credentialMode providerauth.CredentialMode,
) http.Header {
	headers := source.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	connectionTokens := strings.Split(headers.Get("Connection"), ",")
	for _, token := range connectionTokens {
		headers.Del(strings.TrimSpace(token))
	}
	for _, name := range []string{
		"Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
		"Forwarded",
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Proto",
		"X-Original-Host",
		"Host",
		"Content-Length",
	} {
		headers.Del(name)
	}
	switch credentialMode {
	case providerauth.CredentialManaged:
		stripProviderCredentialHeaders(headers)
	case providerauth.CredentialClientPassthrough:
		// User-Agent is carried as a separately validated presentation field.
		// Removing it here prevents the raw header bag from becoming a second
		// authority while preserving the exact observed value below.
		headers.Del("User-Agent")
	default:
		stripProviderCredentialHeaders(headers)
	}
	return headers
}

func applyUpstreamWireHeaders(
	headers http.Header,
	variant wireprofile.CompiledUpstreamWireVariant,
	clientUserAgent string,
) error {
	if headers == nil {
		return errors.New("provider headers are nil")
	}
	values, keys := headerValuesFold(headers, "User-Agent")
	if keys != 0 {
		return errors.New("provider codec conflicts with the upstream User-Agent policy")
	}
	switch variant.UserAgentPolicy() {
	case wireprofile.UserAgentPolicyOmit:
		// net/http otherwise synthesizes its own default value when the key is
		// absent. A present nil slice is the explicit wire-level omission.
		headers["User-Agent"] = nil
	case wireprofile.UserAgentPolicyFollowClient:
		if clientUserAgent == "" {
			headers["User-Agent"] = nil
		} else {
			headers["User-Agent"] = []string{clientUserAgent}
		}
	case wireprofile.UserAgentPolicyConstant:
		if variant.SemanticUserAgent() == "" {
			return errors.New("upstream User-Agent profile is incomplete")
		}
		headers["User-Agent"] = []string{variant.SemanticUserAgent()}
	default:
		return errors.New("upstream User-Agent policy is unsupported")
	}
	_ = values
	return nil
}

func validateUpstreamWireHeaders(
	headers http.Header,
	variant wireprofile.CompiledUpstreamWireVariant,
	clientUserAgent string,
) error {
	values, keys := headerValuesFold(headers, "User-Agent")
	if keys != 1 {
		return errors.New("AuthDriver changed the upstream User-Agent identity")
	}
	switch variant.UserAgentPolicy() {
	case wireprofile.UserAgentPolicyOmit:
		if len(values) != 0 {
			return errors.New("AuthDriver changed the upstream User-Agent identity")
		}
	case wireprofile.UserAgentPolicyFollowClient:
		if clientUserAgent == "" {
			if len(values) != 0 {
				return errors.New("AuthDriver changed the upstream User-Agent identity")
			}
		} else if len(values) != 1 || values[0] != clientUserAgent {
			return errors.New("AuthDriver changed the upstream User-Agent identity")
		}
	case wireprofile.UserAgentPolicyConstant:
		if len(values) != 1 || values[0] != variant.SemanticUserAgent() {
			return errors.New("AuthDriver changed the upstream User-Agent identity")
		}
	default:
		return errors.New("upstream User-Agent policy is unsupported")
	}
	return nil
}

func headerValuesFold(headers http.Header, name string) ([]string, int) {
	var values []string
	keys := 0
	for key, candidate := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		keys++
		values = append(values, candidate...)
	}
	return values, keys
}

type leaseBody struct {
	once      sync.Once
	closeOnce sync.Once
	closeErr  error

	reader io.Reader
	close  io.Closer
	finish func(error)
}

func (body *leaseBody) Read(destination []byte) (int, error) {
	count, err := body.reader.Read(destination)
	// A streaming client can cancel its request context immediately after it has
	// consumed the provider terminal event. Leave that cancellation pending until
	// Close unless the protocol layer explicitly confirms the semantic terminal.
	if err != nil && !errors.Is(err, context.Canceled) {
		body.finalize(err)
	}
	return count, err
}

// ConfirmSemanticTerminal changes only the EgressAttempt terminal
// classification after a bounded protocol decoder has proven the terminal
// event. It never changes or reconstructs response bytes.
func (body *leaseBody) ConfirmSemanticTerminal() {
	body.finalize(errSemanticTerminal)
}

func (body *leaseBody) Close() error {
	body.closeOnce.Do(func() {
		body.closeErr = body.close.Close()
	})
	body.finalize(body.closeErr)
	return body.closeErr
}

func (body *leaseBody) finalize(err error) {
	body.once.Do(func() {
		body.finish(err)
	})
}

func responseAuditTerminal(
	operationContext context.Context,
	terminalErr error,
) (egressaudit.Outcome, string) {
	cause := context.Cause(operationContext)
	switch {
	case errors.Is(terminalErr, errSemanticTerminal):
		return egressaudit.OutcomeCompleted, ""
	case errors.Is(terminalErr, io.EOF):
		// EOF is transport-level proof that the complete response body was
		// consumed. Some Agent clients cancel their request context immediately
		// after receiving the terminal SSE event; that concurrent cancellation
		// must not overwrite the stronger EOF evidence.
		return egressaudit.OutcomeCompleted, ""
	case errors.Is(cause, context.DeadlineExceeded):
		return egressaudit.OutcomeFailed, responseTimeoutClass
	case cause != nil:
		return egressaudit.OutcomeCanceled, responseCanceledClass
	case terminalErr == nil:
		return egressaudit.OutcomeCompleted, ""
	case errors.Is(terminalErr, context.Canceled),
		errors.Is(terminalErr, ErrClientClosing):
		return egressaudit.OutcomeCanceled, responseCanceledClass
	case errors.Is(terminalErr, context.DeadlineExceeded),
		isTimeout(terminalErr):
		return egressaudit.OutcomeFailed, responseTimeoutClass
	default:
		return egressaudit.OutcomeFailed, responseFailureClass
	}
}

func (client *Client) beginAudit(
	ctx context.Context,
	frozen Request,
) (egressaudit.Attempt, error) {
	if client.audit == nil {
		return egressaudit.Attempt{}, nil
	}
	attempt, err := egressaudit.New(egressaudit.NewInput{
		ID:           frozen.egressAttemptID,
		ConnectionID: frozen.connectionID,
		Purpose:      egressaudit.PurposeProviderAttempt,
		PayloadClass: egressaudit.PayloadClientSemantic,
		Parent: egressaudit.ParentRef{
			Kind:       egressaudit.ParentUpstreamAttempt,
			ID:         frozen.parentAttemptID,
			ExchangeID: frozen.exchangeID,
		},
		Caller:       egressaudit.CallerCore,
		TargetOrigin: frozen.target.origin.String(),
		Decision:     providerEgressDecision(),
		StartedAt:    client.clock(),
	})
	if err != nil {
		return egressaudit.Attempt{}, fmt.Errorf(
			"construct provider EgressAttempt: %w",
			err,
		)
	}
	if _, err := client.audit.Append(ctx, attempt); err != nil {
		return egressaudit.Attempt{}, fmt.Errorf(
			"record provider EgressAttempt: %w",
			err,
		)
	}
	return attempt, nil
}

func providerEgressDecision() egressaudit.DecisionRef {
	authority, err := egressaudit.AuthorityForPurpose(egressaudit.PurposeProviderAttempt)
	if err != nil {
		return egressaudit.DecisionRef{}
	}
	return egressaudit.BuiltInDirectDecision(authority)
}

func (client *Client) completeAudit(
	ctx context.Context,
	attempt egressaudit.Attempt,
	outcome egressaudit.Outcome,
	errorClass string,
	bytesOut int64,
	bytesIn int64,
) {
	if client.audit == nil || attempt.ID() == "" {
		return
	}
	terminal, err := attempt.Finish(egressaudit.TerminalInput{
		Outcome:     outcome,
		ErrorClass:  errorClass,
		BytesOut:    bytesOut,
		BytesIn:     bytesIn,
		CompletedAt: completionTime(attempt, client.clock()),
	})
	if err != nil {
		client.reportTerminalFailure(fmt.Errorf(
			"construct provider EgressAttempt terminal: %w",
			err,
		))
		return
	}
	_, _ = client.audit.Complete(ctx, terminal)
}

func (client *Client) reportTerminalFailure(err error) {
	reporter, ok := client.audit.(terminalFailureReporter)
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

type countingReader struct {
	reader io.Reader
	mu     sync.Mutex
	total  int64
}

func (reader *countingReader) Read(destination []byte) (int, error) {
	count, err := reader.reader.Read(destination)
	if count > 0 {
		reader.mu.Lock()
		reader.total += int64(count)
		reader.mu.Unlock()
	}
	return count, err
}

func (reader *countingReader) count() int64 {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.total
}
