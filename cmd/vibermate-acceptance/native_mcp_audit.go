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
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

func verifyNativeStreamSQL(ctx context.Context, t nativeFixtureChecks, directory string, capture captureidentity.Reference, reviewed bool) {
	reader, err := openExchangeAuditReader(ctx, directory)
	if err != nil {
		t.Fatal("private stream read-only audit unavailable")
	}
	defer reader.Close()
	for _, check := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM native_tool_operations WHERE capture_kind=? AND capture_id=? AND rpc_kind='tools_call' AND state='completed'`, 1},
		{`SELECT COUNT(*) FROM native_tool_operations o JOIN native_tool_once n USING(operation_id) WHERE o.capture_kind=? AND o.capture_id=? AND o.rpc_kind IN('tools_call','server_reply')`, 4},
		{`SELECT COUNT(*) FROM native_tool_operations o JOIN native_tool_bindings b USING(operation_id) JOIN native_tool_operations p ON p.operation_id=b.parent_operation_id WHERE o.capture_kind=? AND o.capture_id=? AND o.rpc_kind='server_reply' AND o.state='completed' AND p.rpc_kind='get_stream'`, 1},
		{`SELECT COUNT(*) FROM native_tool_operations o JOIN native_tool_bindings b USING(operation_id) JOIN native_tool_operations p ON p.operation_id=b.parent_operation_id WHERE o.capture_kind=? AND o.capture_id=? AND o.rpc_kind='server_reply' AND o.state='completed' AND p.rpc_kind='tools_call'`, 2},
		{`SELECT COUNT(*) FROM native_tool_operations o JOIN native_tool_once n USING(operation_id) JOIN runtime_egress_attempts a USING(attempt_id) WHERE o.capture_kind=? AND o.capture_id=? AND o.rpc_kind='tools_call' AND a.outcome='failed' AND a.error_class='transport_failed' AND a.purpose='client_tool_service' AND a.account_id='' AND a.parent_kind='client_operation'`, 1},
		{`SELECT COUNT(*) FROM native_tool_operations o JOIN native_tool_once n USING(operation_id) JOIN runtime_egress_attempts a USING(attempt_id) WHERE o.capture_kind=? AND o.capture_id=? AND o.rpc_kind='tools_call' AND CASE WHEN a.byte_counts_known=1 THEN o.bytes_in IS NOT a.bytes_in OR o.bytes_out IS NOT a.bytes_out ELSE o.bytes_in IS NOT NULL OR o.bytes_out IS NOT NULL END`, 0},
		{`SELECT COUNT(*) FROM native_tool_operations o JOIN native_tool_once n USING(operation_id) JOIN runtime_egress_attempts a USING(attempt_id) WHERE o.capture_kind=? AND o.capture_id=? AND o.rpc_kind='get_stream' AND o.state='completed' AND a.outcome='completed'`, 1},
		{`SELECT COUNT(*) FROM native_tool_contents c JOIN native_tool_operations o USING(operation_id) WHERE o.capture_kind=? AND o.capture_id=?`, 0},
	} {
		var got int
		if err := reader.connection.QueryRowContext(ctx, check.query, capture.Kind, capture.ID).Scan(&got); err != nil || got != check.want {
			t.Fatalf("stream SQL ownership/once/audit check failed got=%d want=%d error=%v", got, check.want, err)
		}
	}
	want := 0
	if reviewed {
		want = 4
	}
	var consumed int
	if err := reader.connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_approvals a JOIN native_tool_operations o USING(operation_id) WHERE o.capture_kind=? AND o.capture_id=? AND a.state='allowed' AND a.consumed_at_unix_ms IS NOT NULL`, capture.Kind, capture.ID).Scan(&consumed); err != nil || consumed != want {
		t.Fatal("stream approval consumption differs from actual policy")
	}
	t.Logf("stream SQL: one original call/once/audit, freshGET parent1 originalPOST parents2, completed recoveryGET1, consumedApprovals=%d", consumed)
}

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
