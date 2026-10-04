package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/vibe-agi/vibermate/internal/blindtunnel"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

type nativeDeniedBlindDialer struct {
	gate     offlinehold.Coordinator
	attempts atomic.Uint64
}

func (dialer *nativeDeniedBlindDialer) BeginAction(ctx context.Context, request offlinehold.ActionRequest) (*offlinehold.ActionLease, error) {
	return dialer.gate.BeginAction(ctx, request)
}
func (dialer *nativeDeniedBlindDialer) Dial(context.Context, blindtunnel.DialRequest) (net.Conn, offlinehold.Lease, error) {
	dialer.attempts.Add(1)
	return nil, nil, errors.New("private fixture forbids blind dialing")
}

// nativeFixtureTransport is private to this executable. It cannot dial any
// address other than its immutable literal-loopback synthetic TLS server.
type nativeFixtureTransport struct {
	transport            *http.Transport
	deniedAccountRouting atomic.Uint64
}

func newNativeFixtureTransport(address string, roots *x509.CertPool) (*nativeFixtureTransport, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || roots == nil {
		return nil, errors.New("invalid private TLS fixture")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &nativeFixtureTransport{transport: &http.Transport{
		Proxy: nil, DisableCompression: true, ResponseHeaderTimeout: 10 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
			if network != "tcp" || target != "chatgpt.com:443" {
				return nil, errors.New("fixture destination denied")
			}
			return dialer.DialContext(ctx, "tcp", address)
		},
	}}, nil
}

func (transport *nativeFixtureTransport) RoundTrip(request *http.Request, policy egressnetwork.Policy) (*http.Response, error) {
	if transport == nil || transport.transport == nil || request == nil || request.URL == nil || policy != egressnetwork.DefaultPolicy() {
		return nil, errors.New("fixture policy denied")
	}
	u := request.URL
	accountRouting := u.Path == "/backend-api/wham/accounts/check" && request.Method == "GET" && request.ContentLength == 0 && len(request.TransferEncoding) == 0 && (request.Body == nil || request.Body == http.NoBody)
	if u.Scheme != "https" || (u.Host != "chatgpt.com" && u.Host != "chatgpt.com:443") || u.User != nil || (u.Path != "/backend-api/ps/mcp" && !accountRouting) || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (request.Host != "" && request.Host != u.Host) {
		if u.Path == "/backend-api/wham/accounts/check" {
			transport.deniedAccountRouting.Add(1)
		}
		return nil, errors.New("fixture destination denied")
	}
	if request.Method != "POST" && request.Method != "GET" && request.Method != "DELETE" {
		return nil, errors.New("fixture method denied")
	}
	response, err := transport.transport.RoundTrip(request)
	if err != nil {
		return nil, errors.New("fixture TLS request failed")
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		_ = response.Body.Close()
		return nil, errors.New("fixture redirect denied")
	}
	return response, nil
}
