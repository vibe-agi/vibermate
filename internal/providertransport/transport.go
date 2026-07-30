package providertransport

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
)

const (
	DefaultProviderDialTimeout         = 15 * time.Second
	DefaultTLSHandshakeTimeout         = 10 * time.Second
	DefaultProviderResponseHeadTimeout = 30 * time.Second
	DefaultProviderResponseIdleTimeout = 60 * time.Second
)

var ErrProviderResponseIdle = errors.New(
	"provider response exceeded its idle timeout",
)

type TransportDispatch struct {
	target      Target
	plan        access.CompiledTransportFingerprintPlan
	clientHello transportprofile.Observation
}

type contextDialer = transportprofile.ContextDialer

type Transport interface {
	RoundTrip(
		*http.Request,
		TransportDispatch,
	) (*http.Response, transportprofile.Evidence, error)
}

// TransportTimeouts keeps connection establishment, TLS negotiation, response
// headers, and response-body progress as independent budgets. The whole
// Exchange deadline remains owned by the request context.
type TransportTimeouts struct {
	Dial         time.Duration
	TLSHandshake time.Duration
	ResponseHead time.Duration
	ResponseIdle time.Duration
}

func DefaultTransportTimeouts() TransportTimeouts {
	return TransportTimeouts{
		Dial:         DefaultProviderDialTimeout,
		TLSHandshake: DefaultTLSHandshakeTimeout,
		ResponseHead: DefaultProviderResponseHeadTimeout,
		ResponseIdle: DefaultProviderResponseIdleTimeout,
	}
}

func (timeouts TransportTimeouts) validate() error {
	if timeouts.Dial <= 0 ||
		timeouts.TLSHandshake <= 0 ||
		timeouts.ResponseHead <= 0 ||
		timeouts.ResponseIdle <= 0 {
		return errors.New("provider transport timeout budgets must be positive")
	}
	return nil
}

type profileTransport struct {
	connector *transportprofile.Connector
	timeouts  TransportTimeouts
}

func newProductionStrictTransport(
	timeouts TransportTimeouts,
) (*profileTransport, error) {
	if err := timeouts.validate(); err != nil {
		return nil, err
	}
	return newStrictTransport(nil, &net.Dialer{
		Timeout:   timeouts.Dial,
		KeepAlive: 30 * time.Second,
	}, timeouts)
}

func newStrictTransport(
	roots *x509.CertPool,
	dialer transportprofile.ContextDialer,
	timeouts TransportTimeouts,
) (*profileTransport, error) {
	if err := timeouts.validate(); err != nil {
		return nil, err
	}
	connector, err := transportprofile.NewConnector(
		transportprofile.ConnectorOptions{
			Dialer:           dialer,
			RootCAs:          roots,
			HandshakeTimeout: timeouts.TLSHandshake,
		},
	)
	if err != nil {
		return nil, err
	}
	return &profileTransport{
		connector: connector,
		timeouts:  timeouts,
	}, nil
}

func (transport *profileTransport) RoundTrip(
	request *http.Request,
	dispatch TransportDispatch,
) (*http.Response, transportprofile.Evidence, error) {
	if transport == nil || transport.connector == nil {
		return nil, transportprofile.Evidence{}, errors.New(
			"provider transport is not initialized",
		)
	}
	if request == nil || request.URL == nil {
		return nil, transportprofile.Evidence{}, errors.New(
			"provider HTTP request is incomplete",
		)
	}
	if err := dispatch.target.validate(); err != nil {
		return nil, transportprofile.Evidence{}, err
	}
	if request.URL.Scheme != "https" ||
		request.URL.Host != dispatch.target.httpAuthority ||
		request.Host != dispatch.target.httpAuthority {
		return nil, transportprofile.Evidence{}, errors.New(
			"provider HTTP and transport target identities disagree",
		)
	}

	var evidenceMu sync.Mutex
	var evidence transportprofile.Evidence
	httpTransport := &http.Transport{
		Proxy:                  nil,
		ForceAttemptHTTP2:      false,
		DisableCompression:     true,
		DisableKeepAlives:      true,
		ResponseHeaderTimeout:  transport.timeouts.ResponseHead,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: 1 << 20,
		DialTLSContext: func(
			ctx context.Context,
			network string,
			address string,
		) (net.Conn, error) {
			connection, dialEvidence, err := transport.connector.Connect(
				ctx,
				transportprofile.ConnectRequest{
					Network:       network,
					Address:       address,
					TLSServerName: dispatch.target.tlsServerName,
					Plan:          dispatch.plan,
					Observation:   dispatch.clientHello,
				},
			)
			evidenceMu.Lock()
			evidence = dialEvidence
			evidenceMu.Unlock()
			if err != nil {
				return nil, err
			}
			return newIdleReadConnection(
				ctx,
				connection,
				transport.timeouts.ResponseIdle,
			), nil
		},
	}
	response, err := httpTransport.RoundTrip(request)
	evidenceMu.Lock()
	resultEvidence := evidence
	evidenceMu.Unlock()
	if err != nil {
		httpTransport.CloseIdleConnections()
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, resultEvidence, err
	}
	if response == nil || response.Body == nil {
		httpTransport.CloseIdleConnections()
		return response, resultEvidence, errors.New(
			"provider HTTP transport returned an incomplete response",
		)
	}
	response.Body = &transportBody{
		reader: response.Body,
		close:  response.Body,
		finish: httpTransport.CloseIdleConnections,
	}
	return response, resultEvidence, nil
}

func (*profileTransport) CloseIdleConnections() {}

type transportBody struct {
	once      sync.Once
	closeOnce sync.Once
	closeErr  error

	reader io.Reader
	close  io.Closer
	finish func()
}

func (body *transportBody) Read(destination []byte) (int, error) {
	count, err := body.reader.Read(destination)
	if err != nil {
		body.finalize()
	}
	return count, err
}

func (body *transportBody) Close() error {
	body.closeOnce.Do(func() {
		body.closeErr = body.close.Close()
	})
	body.finalize()
	return body.closeErr
}

func (body *transportBody) finalize() {
	body.once.Do(body.finish)
}

type idleReadConnection struct {
	net.Conn
	ctx     context.Context
	timeout time.Duration

	mu                sync.Mutex
	explicitReadLimit time.Time
}

func newIdleReadConnection(
	ctx context.Context,
	connection net.Conn,
	timeout time.Duration,
) net.Conn {
	return &idleReadConnection{
		Conn:    connection,
		ctx:     ctx,
		timeout: timeout,
	}
}

func (connection *idleReadConnection) SetReadDeadline(deadline time.Time) error {
	connection.mu.Lock()
	connection.explicitReadLimit = deadline
	connection.mu.Unlock()
	return connection.Conn.SetReadDeadline(deadline)
}

func (connection *idleReadConnection) Read(destination []byte) (int, error) {
	if err := connection.ctx.Err(); err != nil {
		return 0, context.Cause(connection.ctx)
	}
	deadline := time.Now().Add(connection.timeout)
	connection.mu.Lock()
	explicit := connection.explicitReadLimit
	connection.mu.Unlock()
	if !explicit.IsZero() && explicit.Before(deadline) {
		deadline = explicit
	}
	if contextDeadline, ok := connection.ctx.Deadline(); ok &&
		contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.Conn.SetReadDeadline(deadline); err != nil {
		return 0, err
	}
	cancelReadDone := make(chan struct{})
	stopCancelRead := context.AfterFunc(connection.ctx, func() {
		_ = connection.Conn.SetReadDeadline(time.Now())
		close(cancelReadDone)
	})
	defer func() {
		if !stopCancelRead() {
			<-cancelReadDone
		}
	}()
	count, err := connection.Conn.Read(destination)
	if err == nil || !isTimeout(err) {
		return count, err
	}
	if connection.ctx.Err() != nil {
		return count, context.Cause(connection.ctx)
	}
	if !explicit.IsZero() && !deadline.After(explicit) {
		return count, err
	}
	return count, &providerIdleError{cause: err}
}

type providerIdleError struct {
	cause error
}

func (failure *providerIdleError) Error() string {
	return fmt.Sprintf("%s: %v", ErrProviderResponseIdle, failure.cause)
}

func (failure *providerIdleError) Unwrap() error {
	return errors.Join(ErrProviderResponseIdle, failure.cause)
}

func (*providerIdleError) Timeout() bool {
	return true
}

func (*providerIdleError) Temporary() bool {
	return true
}

func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}
