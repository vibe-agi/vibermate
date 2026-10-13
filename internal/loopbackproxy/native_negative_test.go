package loopbackproxy_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
)

func TestNativeHandlerRejectsUnprovenTargetsWithoutObservationLeaks(t *testing.T) {
	const sentinel = "private-path-query-body-token-sentinel"
	for _, tc := range []struct {
		name, method, path, host, auth, body string
		status                               int
		chunked, off, missing                bool
	}{
		{name: "wrong_authority", method: "POST", path: environment.ClientToolMCPPath, host: "wrong.example:443", status: 421},
		{name: "wrong_path", method: "POST", path: "/private/" + sentinel, status: 422},
		{name: "query", method: "POST", path: environment.ClientToolMCPPath + "?private=" + sentinel, status: 422},
		{name: "force_query", method: "POST", path: environment.ClientToolMCPPath + "?", status: 422},
		{name: "raw_path", method: "POST", path: "/backend-api/ps/%6dcp", status: 422},
		{name: "method", method: "PUT", path: environment.ClientToolMCPPath, status: 422},
		{name: "malformed", method: "POST", path: environment.ClientToolMCPPath, body: `{"jsonrpc":"2.0","id":1,` + sentinel, status: 400},
		{name: "oversize", method: "POST", path: environment.ClientToolMCPPath, body: strings.Repeat("x", 1<<20+1), status: 413},
		{name: "chunked_oversize", method: "POST", path: environment.ClientToolMCPPath, body: strings.Repeat("x", 1<<20+1), status: 413, chunked: true},
		{name: "off", method: "POST", path: environment.ClientToolMCPPath, status: 403, off: true},
		{name: "missing_executor", method: "POST", path: environment.ClientToolMCPPath, status: 403, missing: true},
		{name: "missing_login", method: "POST", path: environment.ClientToolMCPPath, auth: "none", status: 403},
		{name: "model_placeholder", method: "POST", path: environment.ClientToolMCPPath, auth: "Bearer vibermate-local-proxy", status: 403},
		{name: "model_placeholder_lower", method: "POST", path: environment.ClientToolMCPPath, auth: "bearer vibermate-local-proxy", body: nativePlaceholderInitialize, status: 403},
		{name: "model_placeholder_upper", method: "POST", path: environment.ClientToolMCPPath, auth: "BEARER vibermate-local-proxy", body: nativePlaceholderInitialize, status: 403},
		{name: "model_placeholder_mixed", method: "POST", path: environment.ClientToolMCPPath, auth: "bEaReR vibermate-local-proxy", body: nativePlaceholderInitialize, status: 403},
		{name: "model_placeholder_spaces", method: "POST", path: environment.ClientToolMCPPath, auth: "Bearer  vibermate-local-proxy", body: nativePlaceholderInitialize, status: 403},
		{name: "model_placeholder_tab", method: "POST", path: environment.ClientToolMCPPath, auth: "Bearer\tvibermate-local-proxy", body: nativePlaceholderInitialize, status: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ring := loopbackproxy.NewTargetObservationRing(time.Now)
			f := newNativeFixtureMode(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, &nativeHandlerSettings{shared: tc.off, off: tc.off, configure: func(o *loopbackproxy.Options) {
				o.TargetObserver = ring
				if tc.missing {
					o.NativeTools = nil
				}
			}})
			secured := f.proxy.ConnectTLS(t, f.credential, "native.example:443", "native.example")
			defer secured.Close()
			host := tc.host
			if host == "" {
				host = "native.example:443"
			}
			auth := tc.auth
			if auth == "" {
				auth = "Bearer " + sentinel
			}
			if auth == "none" {
				auth = ""
			}
			body := tc.body
			if body == "" {
				body = sentinel
			}
			request := &http.Request{Method: tc.method, URL: mustURL(t, tc.path), Host: host, Header: http.Header{"Authorization": {auth}, "Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
			if tc.chunked {
				request.ContentLength = -1
				request.TransferEncoding = []string{"chunked"}
			}
			response := writeInnerRequest(t, secured, request)
			data, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode != tc.status {
				t.Fatalf("status%d want%d body%s", response.StatusCode, tc.status, data)
			}
			if f.transport.sends() != 0 {
				t.Fatal("rejected request reached outbound")
			}
			if strings.HasPrefix(tc.name, "model_placeholder") && f.ids.value.Load() != 0 {
				t.Fatal("known model placeholder allocated a native operation ID")
			}
			deadline := time.Now().Add(time.Second)
			for len(ring.Observations()) == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			observations := ring.Observations()
			if len(observations) != 1 {
				t.Fatalf("observations%d", len(observations))
			}
			encoded, _ := json.Marshal(observations)
			if strings.Contains(string(encoded), sentinel) {
				t.Fatal("observation leaked path/query/body/header")
			}
			if tc.name == "wrong_path" && (observations[0].KnownPath != "unrecognized" || observations[0].PathSegments != 2) {
				t.Fatalf("unrecognized path %+v", observations[0])
			}
			if observations[0].StatusCode != tc.status {
				t.Fatalf("observed status %+v", observations[0])
			}
			for _, table := range []string{"runtime_activities", "runtime_exchange_contents", "native_tool_contents", "native_tool_operations"} {
				var count int
				if err := f.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("rejected payload retained %s %d %v", table, count, err)
				}
			}
		})
	}
}

const nativePlaceholderInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"codex-mcp-client","version":"0.160.0"}}}`

func TestNativePlaceholderDeniedBeforeBodyReadAndIDAllocation(t *testing.T) {
	for _, auth := range []string{"bearer vibermate-local-proxy", "BEARER vibermate-local-proxy", "bEaReR  vibermate-local-proxy", "Bearer\tvibermate-local-proxy"} {
		t.Run(auth, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			secured := f.proxy.ConnectTLS(t, f.credential, "native.example:443", "native.example")
			defer secured.Close()
			_ = secured.SetDeadline(time.Now().Add(time.Second))
			// Send headers only. A native body read would emit100 Continue before
			// this unsent valid initialize body, making the boundary observable.
			if _, err := fmt.Fprintf(secured, "POST /backend-api/ps/mcp HTTP/1.1\r\nHost: native.example:443\r\nAuthorization: %s\r\nContent-Type: application/json\r\nAccept: application/json, text/event-stream\r\nContent-Length: %d\r\nExpect: 100-continue\r\n\r\n", auth, len(nativePlaceholderInitialize)); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(secured), &http.Request{Method: "POST"})
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != 403 || f.ids.value.Load() != 0 || f.transport.sends() != 0 {
				t.Fatalf("denial status%d nativeIDs%d sends%d", response.StatusCode, f.ids.value.Load(), f.transport.sends())
			}
		})
	}
}

func TestNativeOriginalBearerCredentialComparisonIsExact(t *testing.T) {
	for _, auth := range []string{"Bearer VIBERMATE-LOCAL-PROXY", "Bearer vibermate-local-proxy-legitimate", "bEaReR synthetic-original-login"} {
		t.Run(auth, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			f.transport.before = func(request *http.Request) {
				if request.Header.Get("Authorization") != auth {
					t.Error("original credential was rewritten")
				}
			}
			secured := f.proxy.ConnectTLS(t, f.credential, "native.example:443", "native.example")
			defer secured.Close()
			response := writeInnerRequest(t, secured, &http.Request{Method: "POST", URL: mustURL(t, environment.ClientToolMCPPath), Host: "native.example:443", Header: http.Header{"Authorization": {auth}, "Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"}}, Body: io.NopCloser(strings.NewReader(nativePlaceholderInitialize)), ContentLength: int64(len(nativePlaceholderInitialize))})
			_, _ = io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode != 200 || f.transport.sends() != 1 {
				t.Fatalf("original auth status%d sends%d", response.StatusCode, f.transport.sends())
			}
		})
	}
}

func TestNativeTargetObservationRingBoundsAndExpiry(t *testing.T) {
	now := time.Now()
	ring := loopbackproxy.NewTargetObservationRing(func() time.Time { return now })
	for i := 0; i < 65; i++ {
		ring.Observe(loopbackproxy.TargetObservation{Method: "POST", Scheme: "https", Authority: "native.example", KnownPath: "/private-secret", PathSegments: i, StatusCode: 422, Reason: "arbitrary-secret-error"})
	}
	got := ring.Observations()
	if len(got) != 64 || got[0].PathSegments != 1 || got[0].KnownPath != "unrecognized" || got[0].Reason != "rejected" {
		t.Fatal("ring retention/redaction failed")
	}
	got[0].KnownPath = "mutated"
	if ring.Observations()[0].KnownPath == "mutated" {
		t.Fatal("ring exposed mutable storage")
	}
	now = now.Add(5 * time.Minute)
	if len(ring.Observations()) != 0 {
		t.Fatal("expired observation survived")
	}
}
