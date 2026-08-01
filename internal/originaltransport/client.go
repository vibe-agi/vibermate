package originaltransport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

var (
	ErrClientClosing      = errors.New("original-origin client is closing")
	ErrRedirectNotAllowed = errors.New("original-origin redirect is not allowed")
)

type Options struct {
	Coordinator offlinehold.Coordinator
	Transport   http.RoundTripper
	// Audit records one immutable attempt per real outbound. It is optional
	// only so existing callers can be migrated; a wired runtime supplies it.
	Audit egressaudit.Writer
}

type operation struct {
	mu sync.Mutex

	cancel context.CancelCauseFunc
	action *offlinehold.ActionLease
	lease  offlinehold.Lease
	body   *leaseBody
}

func (active *operation) setLease(lease offlinehold.Lease) {
	active.mu.Lock()
	active.lease = lease
	active.mu.Unlock()
}

func (active *operation) setAction(action *offlinehold.ActionLease) {
	active.mu.Lock()
	active.action = action
	active.mu.Unlock()
}

func (active *operation) actionLease() *offlinehold.ActionLease {
	active.mu.Lock()
	defer active.mu.Unlock()
	return active.action
}

func (active *operation) setBody(body *leaseBody) {
	active.mu.Lock()
	active.body = body
	active.mu.Unlock()
}

func (active *operation) cancelAndClose(cause error) {
	active.mu.Lock()
	body := active.body
	cancel := active.cancel
	active.mu.Unlock()
	cancel(cause)
	if body != nil {
		_ = body.Close()
	}
}

func (active *operation) takeLease() offlinehold.Lease {
	active.mu.Lock()
	defer active.mu.Unlock()
	lease := active.lease
	active.lease = nil
	return lease
}

func (active *operation) takeAction() *offlinehold.ActionLease {
	active.mu.Lock()
	defer active.mu.Unlock()
	action := active.action
	active.action = nil
	return action
}

type Client struct {
	mu sync.Mutex

	coordinator offlinehold.Coordinator
	transport   http.RoundTripper
	audit       egressaudit.Writer
	clock       func() time.Time
	operations  map[*operation]struct{}
	closing     bool
	changed     chan struct{}
}

func New(options Options) (*Client, error) {
	if options.Coordinator == nil || options.Transport == nil {
		return nil, errors.New("original-origin transport dependencies are incomplete")
	}
	return &Client{
		coordinator: options.Coordinator,
		transport:   options.Transport,
		audit:       options.Audit,
		clock:       time.Now,
		operations:  make(map[*operation]struct{}),
		changed:     make(chan struct{}),
	}, nil
}

func NewProduction(
	coordinator offlinehold.Coordinator,
	audit egressaudit.Writer,
) (*Client, error) {
	return New(Options{
		Coordinator: coordinator,
		Transport:   newProductionStrictTransport(),
		Audit:       audit,
	})
}

// Do acquires an offline-hold lease before any original-origin byte can leave
// the process. Proxy credentials are absent from the frozen request.
func (client *Client) Do(
	ctx context.Context,
	frozen Request,
) (*http.Response, error) {
	if ctx == nil {
		return nil, errors.New("original-origin context is nil")
	}
	activeContext, active, err := client.begin(ctx, frozen.requestID)
	if err != nil {
		return nil, err
	}
	handoff := false
	defer func() {
		if !handoff {
			client.finish(active)
		}
	}()
	target := frozen.probeTarget()
	if err := target.Validate(); err != nil {
		return nil, fmt.Errorf("freeze original-origin probe target: %w", err)
	}
	lease, err := client.coordinator.Acquire(
		activeContext,
		offlinehold.AcquireRequest{
			RequestID: frozen.requestID,
			Action:    active.actionLease(),
			Target:    target,
			SizeBytes: int64(len(frozen.body)),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("acquire original-origin egress lease: %w", err)
	}
	active.setLease(lease)
	if err := activeContext.Err(); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(
		activeContext,
		frozen.method,
		frozen.targetURL().String(),
		bytes.NewReader(frozen.body),
	)
	if err != nil {
		return nil, err
	}
	request.Host = frozen.origin.HTTPAuthority()
	request.Header = frozen.headers.Clone()
	request.ContentLength = int64(len(frozen.body))
	// The record is appended before the first outbound byte, so an outbound
	// that fails or is cancelled still leaves evidence of where it was going.
	record, recordErr := client.beginAudit(activeContext, frozen)
	if recordErr != nil {
		return nil, recordErr
	}
	response, err := client.transport.RoundTrip(request)
	request.Header.Del("Authorization")
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		client.completeAudit(
			activeContext,
			record,
			egressaudit.OutcomeFailed,
			"transport_failed",
			int64(len(frozen.body)),
			0,
		)
		return nil, fmt.Errorf("send original-origin request: %w", err)
	}
	if response == nil || response.Body == nil {
		client.completeAudit(
			activeContext,
			record,
			egressaudit.OutcomeFailed,
			"incomplete_response",
			int64(len(frozen.body)),
			0,
		)
		return nil, errors.New("original-origin transport returned an incomplete response")
	}
	if response.StatusCode >= 300 && response.StatusCode <= 399 {
		_ = response.Body.Close()
		client.completeAudit(
			activeContext,
			record,
			egressaudit.OutcomeFailed,
			"redirect_denied",
			int64(len(frozen.body)),
			0,
		)
		return nil, ErrRedirectNotAllowed
	}
	counted := &countingReader{reader: response.Body}
	body := &leaseBody{
		reader: counted,
		close:  response.Body,
		finish: func() {
			client.completeAudit(
				context.WithoutCancel(activeContext),
				record,
				egressaudit.OutcomeCompleted,
				"",
				int64(len(frozen.body)),
				counted.count(),
			)
			client.finish(active)
		},
	}
	active.setBody(body)
	response.Body = body
	handoff = true
	return response, nil
}

func (client *Client) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("original-origin shutdown context is nil")
	}
	client.mu.Lock()
	client.closing = true
	operations := make([]*operation, 0, len(client.operations))
	for active := range client.operations {
		operations = append(operations, active)
	}
	client.signalLocked()
	client.mu.Unlock()
	for _, active := range operations {
		active.cancelAndClose(ErrClientClosing)
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
	actionID string,
) (context.Context, *operation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing {
		return nil, nil, ErrClientClosing
	}
	activeContext, cancel := context.WithCancelCause(ctx)
	active := &operation{cancel: cancel}
	action, err := client.coordinator.BeginAction(
		activeContext,
		offlinehold.ActionRequest{ActionID: actionID},
	)
	if err != nil {
		cancel(err)
		return nil, nil, fmt.Errorf(
			"begin original-origin data-plane action: %w",
			err,
		)
	}
	active.setAction(action)
	client.operations[active] = struct{}{}
	client.signalLocked()
	return activeContext, active, nil
}

func (client *Client) finish(active *operation) {
	client.mu.Lock()
	if _, exists := client.operations[active]; !exists {
		client.mu.Unlock()
		return
	}
	delete(client.operations, active)
	client.signalLocked()
	client.mu.Unlock()
	if lease := active.takeLease(); lease != nil {
		lease.Release()
	}
	if action := active.takeAction(); action != nil {
		action.Release()
	}
	active.cancel(errors.New("original-origin operation finished"))
}

func (client *Client) signalLocked() {
	close(client.changed)
	client.changed = make(chan struct{})
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

// beginAudit records the attempt before the first outbound byte. A record that
// cannot be constructed is a defect in the caller's evidence, not something to
// proceed past silently, so it fails the outbound.
func (client *Client) beginAudit(
	ctx context.Context,
	frozen Request,
) (egressaudit.Attempt, error) {
	if client.audit == nil {
		return egressaudit.Attempt{}, nil
	}
	purpose, err := egressaudit.PurposeForEgressKind(string(frozen.kind))
	if err != nil {
		return egressaudit.Attempt{}, err
	}
	authority, err := egressaudit.AuthorityForPurpose(purpose)
	if err != nil {
		return egressaudit.Attempt{}, err
	}
	attempt, err := egressaudit.New(egressaudit.NewInput{
		ID:           frozen.requestID,
		ConnectionID: frozen.connectionID,
		Purpose:      purpose,
		PayloadClass: egressaudit.PayloadClass(frozen.payloadClass),
		Parent: egressaudit.ParentRef{
			Kind: egressaudit.ParentOriginalRequest,
			ID:   frozen.parentID,
		},
		Caller:       egressaudit.CallerCore,
		TargetOrigin: frozen.origin.String(),
		Decision:     egressaudit.BuiltInDirectDecision(authority),
		StartedAt:    client.clock(),
	})
	if err != nil {
		return egressaudit.Attempt{}, fmt.Errorf(
			"construct original-origin EgressAttempt: %w",
			err,
		)
	}
	if _, err := client.audit.Append(ctx, attempt); err != nil {
		return egressaudit.Attempt{}, fmt.Errorf(
			"record original-origin EgressAttempt: %w",
			err,
		)
	}
	return attempt, nil
}

// completeAudit records the terminal. A terminal that cannot be written is
// reported through the audit boundary rather than failing an outbound that
// already happened.
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
