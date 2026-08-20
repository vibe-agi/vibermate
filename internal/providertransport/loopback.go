package providertransport

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
)

type cleartextTransport struct {
	dialer   contextDialer
	timeouts TransportTimeouts
}

func newProductionCleartextTransport(
	timeouts TransportTimeouts,
) (*cleartextTransport, error) {
	return newCleartextTransport(
		&net.Dialer{Timeout: timeouts.Dial},
		timeouts,
	)
}

func newCleartextTransport(
	dialer contextDialer,
	timeouts TransportTimeouts,
) (*cleartextTransport, error) {
	if dialer == nil {
		return nil, errors.New("cleartext provider dialer is nil")
	}
	if err := timeouts.validate(); err != nil {
		return nil, err
	}
	return &cleartextTransport{
		dialer:   dialer,
		timeouts: timeouts,
	}, nil
}

func (transport *cleartextTransport) RoundTrip(
	request *http.Request,
	dispatch TransportDispatch,
) (*http.Response, transportprofile.Evidence, error) {
	if transport == nil || transport.dialer == nil {
		return nil, transportprofile.Evidence{}, errors.New(
			"cleartext provider transport is not initialized",
		)
	}
	if !isCleartextProviderTransport(dispatch.target.TransportKind()) {
		return nil, transportprofile.Evidence{}, errors.New(
			"cleartext provider transport received a TLS target",
		)
	}
	if err := dispatch.target.validateRequestIdentity(request); err != nil {
		return nil, transportprofile.Evidence{}, err
	}
	endpointAuthority, err := dispatch.target.endpointAuthority()
	if err != nil {
		return nil, transportprofile.Evidence{}, err
	}
	httpTransport := &http.Transport{
		Proxy:                  nil,
		ForceAttemptHTTP2:      false,
		DisableCompression:     true,
		DisableKeepAlives:      true,
		ResponseHeaderTimeout:  transport.timeouts.ResponseHead,
		ExpectContinueTimeout:  0,
		MaxResponseHeaderBytes: 1 << 20,
		DialContext: func(
			ctx context.Context,
			network string,
			address string,
		) (net.Conn, error) {
			if network != "tcp" &&
				network != "tcp4" &&
				network != "tcp6" {
				return nil, errors.New(
					"cleartext provider transport network is unsupported",
				)
			}
			if address != endpointAuthority {
				return nil, errors.New(
					"cleartext provider dial authority changed",
				)
			}
			connection, dialErr := transport.dialer.DialContext(
				ctx,
				network,
				endpointAuthority,
			)
			if dialErr != nil {
				return nil, dialErr
			}
			if peerErr := validateCleartextPeer(
				connection.RemoteAddr(),
				dispatch.target,
			); peerErr != nil {
				_ = connection.Close()
				return nil, peerErr
			}
			return newIdleReadConnection(
				ctx,
				connection,
				transport.timeouts.ResponseIdle,
			), nil
		},
	}
	response, err := httpTransport.RoundTrip(request)
	if err != nil {
		httpTransport.CloseIdleConnections()
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, transportprofile.Evidence{}, err
	}
	if response == nil || response.Body == nil {
		httpTransport.CloseIdleConnections()
		return response, transportprofile.Evidence{}, errors.New(
			"cleartext provider HTTP transport returned an incomplete response",
		)
	}
	response.Body = &transportBody{
		reader: response.Body,
		close:  response.Body,
		finish: httpTransport.CloseIdleConnections,
	}
	return response, transportprofile.Evidence{}, nil
}

func (*cleartextTransport) CloseIdleConnections() {}

func isCleartextProviderTransport(kind originidentity.ProviderTransport) bool {
	return kind == originidentity.ProviderTransportLoopbackCleartext ||
		kind == originidentity.ProviderTransportPrivateCleartext
}

func validateCleartextPeer(
	remote net.Addr,
	target Target,
) error {
	tcp, ok := remote.(*net.TCPAddr)
	if !ok || tcp == nil {
		return errors.New("cleartext provider peer address is not TCP")
	}
	peer, available := netip.AddrFromSlice(tcp.IP)
	if !available {
		return errors.New("cleartext provider peer IP is invalid")
	}
	peer = peer.Unmap()
	if !originidentity.IsPrivateCleartextAddress(peer) || tcp.Zone != "" ||
		tcp.Port != int(target.origin.Port()) {
		return errors.New("cleartext provider peer is not local/private")
	}
	if expected, err := netip.ParseAddr(target.NetworkHost()); err == nil &&
		peer != expected.Unmap() {
		return errors.New("cleartext provider literal peer identity changed")
	}
	return nil
}
