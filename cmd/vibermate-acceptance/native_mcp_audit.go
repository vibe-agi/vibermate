package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
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

// Body-free bounded evidence, not a completion inference: an open audit may
// carry a default known flag while the operation still has no byte projection.
func logNativePhaseSQL(ctx context.Context, t nativeFixtureChecks, directory string, capture captureidentity.Reference) {
	reader, err := openExchangeAuditReader(ctx, directory)
	if err != nil {
		t.Error("native phase audit unavailable")
		return
	}
	defer reader.Close()
	rows, err := reader.connection.QueryContext(ctx, `SELECT o.rpc_kind,o.state,o.terminal_reason,COALESCE(b.session_revision,0),COALESCE(b.catalog_generation,0),COALESCE(s.revision,0),COALESCE(s.catalog_generation,0),COALESCE(n.claimed_at_unix_ms,0),COALESCE(a.completed_at_unix_ms,0),COALESCE(a.outcome,''),COALESCE(a.error_class,''),COALESCE(a.byte_counts_known,-1),COALESCE(a.bytes_in,0),COALESCE(a.bytes_out,0),o.bytes_in,o.bytes_out,c.state,c.replay_barrier FROM native_tool_operations o LEFT JOIN native_tool_bindings b USING(operation_id) LEFT JOIN native_tool_sessions s USING(session_id) LEFT JOIN native_tool_once n USING(operation_id) LEFT JOIN runtime_egress_attempts a USING(attempt_id) JOIN native_tool_scopes c ON c.capture_kind=o.capture_kind AND c.capture_id=o.capture_id WHERE o.capture_kind=? AND o.capture_id=? ORDER BY o.created_at_unix_ms,o.operation_id LIMIT 129`, capture.Kind, capture.ID)
	if err != nil {
		t.Error("native phase SQL unavailable")
		return
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > 128 {
			t.Error("native phase row bound exceeded")
			return
		}
		var kind, state, reason, outcome, errorClass, scope string
		var br, bg, sr, sg, claim, end, known, bytesIn, bytesOut, barrier int64
		var opIn, opOut sql.NullInt64
		if err := rows.Scan(&kind, &state, &reason, &br, &bg, &sr, &sg, &claim, &end, &outcome, &errorClass, &known, &bytesIn, &bytesOut, &opIn, &opOut, &scope, &barrier); err != nil {
			t.Error("native phase row invalid")
			return
		}
		t.Logf("native SQL kind=%s state=%s/%s binding=%d/%d current=%d/%d claimMS=%d physicalEndMS=%d audit=%s/%s byte_counts_known=%d numericAuditBytes=%d/%d opCountsValid=%t/%t opBytes=%d/%d scope=%s barrier=%d", kind, state, reason, br, bg, sr, sg, claim, end, outcome, errorClass, known, bytesIn, bytesOut, opIn.Valid, opOut.Valid, opIn.Int64, opOut.Int64, scope, barrier)
	}
	if rows.Err() != nil {
		t.Error("native phase read failed")
	}
}

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
	modelAuxiliary       bool
	modelGuard           *nativeModelAuxiliaryGuard
	modelCaseActive      atomic.Bool
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
	if transport.modelAuxiliary && nativeModelAuxiliaryClass(request) != "" {
		response, err := transport.transport.RoundTrip(request)
		if err != nil {
			if transport.modelCaseActive.Load() && transport.modelGuard != nil {
				transport.modelGuard.deny("")
			}
			return nil, errors.New("private MODEL auxiliary TLS request failed")
		}
		if response.StatusCode != http.StatusForbidden || response.Header.Get("Location") != "" {
			if transport.modelCaseActive.Load() && transport.modelGuard != nil {
				transport.modelGuard.deny("")
			}
			_ = response.Body.Close()
			return nil, errors.New("private MODEL auxiliary did not return closed denial")
		}
		return response, nil
	}
	accountRouting := u.Path == "/backend-api/wham/accounts/check" && request.Method == "GET" && request.ContentLength == 0 && len(request.TransferEncoding) == 0 && (request.Body == nil || request.Body == http.NoBody)
	if u.Scheme != "https" || (u.Host != "chatgpt.com" && u.Host != "chatgpt.com:443") || u.User != nil || (u.Path != "/backend-api/ps/mcp" && !accountRouting) || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (request.Host != "" && request.Host != u.Host) {
		if transport.modelCaseActive.Load() && transport.modelGuard != nil {
			transport.modelGuard.deny("")
		}
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
