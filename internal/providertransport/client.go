package providertransport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
)

var (
	ErrClientClosing      = errors.New("provider client is closing")
	ErrRedirectNotAllowed = errors.New("upstream redirect is not allowed")
)

type ClientOptions struct {
	Coordinator   offlinehold.Coordinator
	Authenticator Authenticator
	Transport     Transport
	// Audit records one immutable attempt per real outbound.
	Audit egressaudit.Writer
}

type Evidence struct {
	Credential CredentialEvidence
	Transport  transportprofile.Evidence
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

	coordinator   offlinehold.Coordinator
	authenticator Authenticator
	transport     Transport
	audit         egressaudit.Writer
	clock         func() time.Time
	operations    map[*clientOperation]struct{}
	closing       bool
	changed       chan struct{}
}

func NewClient(options ClientOptions) (*Client, error) {
	if options.Coordinator == nil ||
		options.Authenticator == nil ||
		options.Transport == nil {
		return nil, errors.New("provider client dependencies are incomplete")
	}
	return &Client{
		coordinator:   options.Coordinator,
		authenticator: options.Authenticator,
		transport:     options.Transport,
		audit:         options.Audit,
		clock:         time.Now,
		operations:    make(map[*clientOperation]struct{}),
		changed:       make(chan struct{}),
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
	if frozen.authDriverRef != client.authenticator.Ref() {
		return nil, Evidence{}, errors.New("provider AuthDriver does not match the frozen plan")
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
	request.Host = frozen.target.httpAuthority
	request.Header = sanitizeProviderHeaders(frozen.headers)
	request.Header.Set("Content-Type", "application/json")
	request.ContentLength = int64(len(frozen.body))

	evidence, err := client.authenticator.Apply(
		operationContext,
		request,
		frozen.secretReference,
		frozen.target,
	)
	if err != nil {
		return nil, Evidence{}, fmt.Errorf("finalize provider authentication: %w", err)
	}
	// The record is appended before the first outbound byte, so an outbound
	// that fails or is cancelled still leaves evidence of where it was going.
	record, recordErr := client.beginAudit(operationContext, frozen)
	if recordErr != nil {
		return nil, Evidence{}, recordErr
	}
	response, transportEvidence, err := client.transport.RoundTrip(
		request,
		TransportDispatch{
			target:      frozen.target,
			plan:        frozen.transportPlan,
			clientHello: frozen.clientHello,
		},
	)
	request.Header.Del("Authorization")
	attemptEvidence := Evidence{
		Credential: evidence,
		Transport:  transportEvidence,
	}
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		client.completeAudit(
			operationContext, record, egressaudit.OutcomeFailed,
			"transport_failed", int64(len(frozen.body)), 0,
		)
		return nil, attemptEvidence, fmt.Errorf("send provider request: %w", err)
	}
	if response == nil || response.Body == nil {
		client.completeAudit(
			operationContext, record, egressaudit.OutcomeFailed,
			"incomplete_response", int64(len(frozen.body)), 0,
		)
		return nil, attemptEvidence, errors.New("provider transport returned an incomplete response")
	}
	if response.StatusCode >= 300 && response.StatusCode <= 399 {
		_ = response.Body.Close()
		client.completeAudit(
			operationContext, record, egressaudit.OutcomeFailed,
			"redirect_denied", int64(len(frozen.body)), 0,
		)
		return nil, attemptEvidence, ErrRedirectNotAllowed
	}

	counted := &countingReader{reader: response.Body}
	body := &leaseBody{
		reader: counted,
		close:  response.Body,
		finish: func() {
			client.completeAudit(
				context.WithoutCancel(operationContext),
				record,
				egressaudit.OutcomeCompleted,
				"",
				int64(len(frozen.body)),
				counted.count(),
			)
			client.finish(operation, nil)
		},
	}
	operation.setBody(body)
	response.Body = body
	handoff = true
	return response, attemptEvidence, nil
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

func sanitizeProviderHeaders(source http.Header) http.Header {
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
	stripProviderCredentialHeaders(headers)
	return headers
}

type leaseBody struct {
	once      sync.Once
	closeOnce sync.Once
	closeErr  error

	reader io.Reader
	close  io.Closer
	finish func()
}

func (body *leaseBody) Read(destination []byte) (int, error) {
	count, err := body.reader.Read(destination)
	if err != nil {
		body.finalize()
	}
	return count, err
}

func (body *leaseBody) Close() error {
	body.closeOnce.Do(func() {
		body.closeErr = body.close.Close()
	})
	body.finalize()
	return body.closeErr
}

func (body *leaseBody) finalize() {
	body.once.Do(body.finish)
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
		TargetOrigin: frozen.target.origin,
		Decision: egressaudit.BuiltInDirectDecision(
			egressaudit.AuthorityAccess,
		),
		StartedAt: client.clock(),
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
		CompletedAt: client.clock(),
	})
	if err != nil {
		return
	}
	_, _ = client.audit.Complete(ctx, terminal)
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
