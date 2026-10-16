package originaltransport

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

type egressDialerBuilder interface {
	Dialer(egressnetwork.Policy) (egressnetwork.ContextDialer, error)
}

type strictTransport struct {
	dialers egressDialerBuilder
}

// NewProductionTransport verifies HTTPS and constrains HTTP to a direct private
// peer. Original and native consumers retain their own admission and audit.
func NewProductionTransport() (Transport, error) { return newProductionStrictTransport() }

// NewProductionTransportWithBuilder accepts only trusted Runtime composition.
func NewProductionTransportWithBuilder(builder *egressnetwork.Builder) (Transport, error) {
	return newStrictTransport(builder)
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
	if transport == nil || transport.dialers == nil || request == nil || request.URL == nil {
		return nil, errors.New("original-origin transport is not initialized")
	}
	origin, err := originidentity.ParseProviderOrigin(request.URL.Scheme + "://" + request.URL.Host)
	if err != nil {
		return nil, err
	}
	normalized, err := policy.Normalize()
	if err != nil {
		return nil, err
	}
	if origin.Scheme() == "http" && normalized.Proxy.Kind != egressnetwork.ProxyDirect {
		return nil, errors.New("cleartext original traffic cannot use a secondary proxy")
	}
	dialer, err := transport.dialers.Dialer(policy)
	if err != nil {
		return nil, err
	}
	httpTransport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != origin.EndpointAuthority() {
				return nil, errors.New("original dial authority changed")
			}
			connection, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			if origin.Scheme() == "http" {
				if err := validateCleartextPeer(connection.RemoteAddr(), origin); err != nil {
					_ = connection.Close()
					return nil, err
				}
			}
			return connection, nil
		},
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

// Validate the connected peer before any credential or HTTP byte is written.
// The egress dialer separately enforces the request context's target access.
func validateCleartextPeer(remote net.Addr, origin originidentity.ProviderOrigin) error {
	tcp, ok := remote.(*net.TCPAddr)
	if !ok || tcp == nil {
		return errors.New("cleartext original peer is not TCP")
	}
	peer, ok := netip.AddrFromSlice(tcp.IP)
	if !ok {
		return errors.New("cleartext original peer IP is invalid")
	}
	peer = peer.Unmap()
	if !originidentity.IsPrivateCleartextAddress(peer) || tcp.Zone != "" || tcp.Port != int(origin.Port()) {
		return errors.New("cleartext original peer is not local/private")
	}
	if expected, err := netip.ParseAddr(origin.Host()); err == nil && peer != expected.Unmap() {
		return errors.New("cleartext original literal peer identity changed")
	}
	return nil
}
