package originaltransport

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
)

type egressDialerBuilder interface {
	Dialer(egressnetwork.Policy) (egressnetwork.ContextDialer, error)
}

type strictTransport struct {
	dialers egressDialerBuilder
}

func newProductionStrictTransport() (*strictTransport, error) {
	dialers, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{
		BaseDialer: &net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		},
	})
	if err != nil {
		return nil, err
	}
	return newStrictTransport(dialers)
}

func newStrictTransport(dialers egressDialerBuilder) (*strictTransport, error) {
	if dialers == nil {
		return nil, errors.New("original-origin traffic egress dialer builder is nil")
	}
	return &strictTransport{dialers: dialers}, nil
}

func (transport *strictTransport) RoundTrip(
	request *http.Request,
	policy egressnetwork.Policy,
) (*http.Response, error) {
	if transport == nil || transport.dialers == nil || request == nil {
		return nil, errors.New("original-origin transport is not initialized")
	}
	dialer, err := transport.dialers.Dialer(policy)
	if err != nil {
		return nil, err
	}
	httpTransport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      true,
		DisableCompression:     true,
		DisableKeepAlives:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  30 * time.Second,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: 1 << 20,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	response, err := httpTransport.RoundTrip(request)
	if err != nil {
		httpTransport.CloseIdleConnections()
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	if response == nil || response.Body == nil {
		httpTransport.CloseIdleConnections()
		return response, errors.New("original-origin transport returned an incomplete response")
	}
	response.Body = &closingBody{
		reader: response.Body,
		finish: httpTransport.CloseIdleConnections,
	}
	return response, nil
}

func (*strictTransport) CloseIdleConnections() {}

type closingBody struct {
	once   sync.Once
	reader io.ReadCloser
	finish func()
	err    error
}

func (body *closingBody) Read(destination []byte) (int, error) {
	count, err := body.reader.Read(destination)
	if err != nil {
		_ = body.Close()
	}
	return count, err
}

func (body *closingBody) Close() error {
	body.once.Do(func() {
		body.err = body.reader.Close()
		body.finish()
	})
	return body.err
}

var _ Transport = (*strictTransport)(nil)
