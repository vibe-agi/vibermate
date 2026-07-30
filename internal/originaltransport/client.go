package originaltransport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

var (
	ErrClientClosing      = errors.New("original-origin client is closing")
	ErrRedirectNotAllowed = errors.New("original-origin redirect is not allowed")
)

type Options struct {
	Coordinator offlinehold.Coordinator
	Transport   http.RoundTripper
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
		operations:  make(map[*operation]struct{}),
		changed:     make(chan struct{}),
	}, nil
}

func NewProduction(coordinator offlinehold.Coordinator) (*Client, error) {
	return New(Options{
		Coordinator: coordinator,
		Transport:   newProductionStrictTransport(),
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
	response, err := client.transport.RoundTrip(request)
	request.Header.Del("Authorization")
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, fmt.Errorf("send original-origin request: %w", err)
	}
	if response == nil || response.Body == nil {
		return nil, errors.New("original-origin transport returned an incomplete response")
	}
	if response.StatusCode >= 300 && response.StatusCode <= 399 {
		_ = response.Body.Close()
		return nil, ErrRedirectNotAllowed
	}
	body := &leaseBody{
		reader: response.Body,
		close:  response.Body,
		finish: func() {
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
