package loopbackproxy_test

import (
	"encoding/json"
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
