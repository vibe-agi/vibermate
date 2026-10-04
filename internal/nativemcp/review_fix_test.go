package nativemcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestNativeHopNominationsCannotStripAuthenticatedEvidence(t *testing.T) {
	for _, name := range []string{"Authorization", "Cookie", "Mcp-Session-Id", "Mcp-Protocol-Version", "Content-Type", "Accept", "Last-Event-Id"} {
		t.Run(name, func(t *testing.T) {
			f := catalogReadyFixture(t)
			c := f.captured(t, "POST", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "synthetic-session")
			c.request.Header.Set("Connection", name)
			before := f.transport.sends()
			terminal := f.executor.Serve(context.Background(), c, httptest.NewRecorder())
			if terminal.Outcome == OutcomeCompleted || f.transport.sends() != before {
				t.Fatalf("hop nomination %s reached authenticated producer %+v", name, terminal)
			}
			var catalogs int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_sessions WHERE catalog_digest IS NOT NULL`).Scan(&catalogs); err != nil || catalogs != 0 {
				t.Fatalf("unproved catalog %d %v", catalogs, err)
			}
		})
	}
}

func TestNativeTypedToolResponsesAndKnownRPCErrors(t *testing.T) {
	for _, media := range []string{"json", "sse"} {
		for _, test := range append([]struct {
			payload         string
			failed, invalid bool
		}{{`"result":{"content":false}`, false, true}, {`"result":{"content":[{"type":"text","text":null}]}`, false, true},
			{`"result":{"content":[{"type":"resource","resource":{"uri":"synthetic:resource","text":"ok","mimeType":42}}]}`, false, true},
			{`"result":{"content":[{"type":"text","text":"ok","annotations":{"audience":false}}]}`, false, true},
			{`"result":{"content":[{"type":"text","text":"ok","annotations":{"priority":"high"}}]}`, false, true},
			{`"result":{"content":[{"type":"resource","resource":{"uri":"synthetic:resource","text":"ok","mimeType":"text/plain"},"annotations":{"audience":["user","assistant"],"priority":0.5}}]}`, false, false},
			{`"error":{}`, false, true}, {`"error":{"code":-32603,"message":"Synthetic known failure"}`, true, false}, {`"result":{"content":[{"type":"text","text":"valid"}]}`, false, false}}, optionalTypeCases()...) {
			t.Run(media+"/"+test.payload, func(t *testing.T) {
				f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
				f.handshake(t)
				wire := `{"jsonrpc":"2.0","id":3,` + test.payload + `}`
				body := wire
				contentType := "application/json"
				if media == "sse" {
					body = "data: " + wire + "\n\n"
					contentType = "text/event-stream"
				}
				f.transport.handler = func(r *http.Request, b []byte) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				}
				terminal, w := f.serve(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
				op, err := f.store.NativeToolRepository().Get(context.Background(), terminal.OperationID)
				if err != nil {
					t.Fatal(err)
				}
				if test.invalid {
					if terminal.Outcome != OutcomeUncertain {
						t.Fatalf("untrusted result accepted %+v", terminal)
					}
				} else if test.failed {
					if op.State != "failed" || op.Reason != "rpc_error" || op.BytesIn == nil || *op.BytesIn != int64(len(body)) || w.Body.String() != body {
						t.Fatalf("known RPC error relabeled %+v %+v wire%s", terminal, op, w.Body.String())
					}
				} else if terminal.Outcome != OutcomeCompleted {
					t.Fatal(terminal)
				}
			})
		}
	}
}

func optionalTypeCases() []struct {
	payload         string
	failed, invalid bool
} {
	var cases []struct {
		payload         string
		failed, invalid bool
	}
	for _, fields := range []string{`"title":false`, `"description":42`, `"mimeType":false`, `"size":-1`, `"size":1.5`, `"icons":false`, `"icons":[{"src":42}]`, `"icons":[{"src":"synthetic:icon","mimeType":42}]`, `"icons":[{"src":"synthetic:icon","sizes":false}]`, `"icons":[{"src":"synthetic:icon","sizes":[42]}]`, `"icons":[{"src":"synthetic:icon","theme":"blue"}]`} {
		cases = append(cases, struct {
			payload         string
			failed, invalid bool
		}{`"result":{"content":[{"type":"resource_link","uri":"synthetic:x","name":"x",` + fields + `}]}`, false, true})
	}
	for _, fields := range []string{`"audience":["system"]`, `"priority":1.1`, `"lastModified":42`, `"lastModified":"yesterday"`} {
		cases = append(cases, struct {
			payload         string
			failed, invalid bool
		}{`"result":{"content":[{"type":"text","text":"ok","annotations":{` + fields + `}}]}`, false, true})
	}
	cases = append(cases, struct {
		payload         string
		failed, invalid bool
	}{`"result":{"content":[{"type":"resource_link","uri":"synthetic:x","name":"x","title":"Title","description":"Description","mimeType":"text/plain","size":4294967295,"icons":[{"src":"synthetic:icon","mimeType":"image/png","sizes":["48x48","any"],"theme":"dark"}],"annotations":{"audience":[],"priority":1,"lastModified":"2025-01-12T15:00:58.123Z","customExtension":false},"customExtension":42}]}`, false, false})
	cases = append(cases, struct {
		payload         string
		failed, invalid bool
	}{`"result":{"content":[{"type":"resource","resource":{"uri":"synthetic:blob","blob":"YWJj","mimeType":"application/octet-stream","_meta":{}}},{"type":"image","data":"YWJj","mimeType":"image/png","_meta":{}},{"type":"audio","data":"YWJj","mimeType":"audio/wav"}]}`, false, false})
	return cases
}

func TestNativeRequiredTypesRejectNullAndMalformedInitialize(t *testing.T) {
	if _, ok := text([]byte(`null`)); ok {
		t.Fatal("null accepted as typed string")
	}
	for _, params := range []string{`{"protocolVersion":"2025-06-18","clientInfo":42}`, `{"protocolVersion":"2025-06-18","clientInfo":{"name":null,"version":"0.160.0"}}`} {
		f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
		terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":`+params+`}`, "")
		if terminal.Outcome == OutcomeCompleted || f.transport.sends() != 0 {
			t.Fatalf("malformed initialize sent %+v", terminal)
		}
	}
}

func TestNativeExpiredOwnerAllowsFreshInitialization(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e := f.executor.(*executor)
	e.mu.Lock()
	expiry := e.scopes[f.capture.Key()].expires
	e.mu.Unlock()
	e.dependencies.Clock = func() time.Time { return expiry.Add(time.Millisecond) }
	terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, "")
	if terminal.Outcome != OutcomeCompleted {
		t.Fatalf("expired owner permanently blocked init %+v", terminal)
	}
}

func TestNativeSessionContinuousActivityAndLateInitialization(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e := f.executor.(*executor)
	now := e.dependencies.Clock().UTC()
	e.dependencies.Clock = func() time.Time { return now }
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		if strings.Contains(string(body), `"method":"initialize"`) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}, "Mcp-Session-Id": []string{"late-session"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"synthetic","version":"1"}}}`))}, nil
		}
		rpc, _ := parseRequest(body, e.limits)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":` + string(rpc.id) + `,"result":{}}`))}, nil
	}
	for i := 0; i < 4; i++ {
		now = now.Add(10 * time.Minute)
		terminal, _ := f.serve(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"ping"}`, 44+i), "synthetic-session")
		if terminal.Outcome != OutcomeCompleted {
			t.Fatalf("continuous activity at %d: %+v", i, terminal)
		}
	}
	terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, "")
	if terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.scopes[f.capture.Key()].sessions {
		if s.wire == "late-session" && !s.timeLimit.Equal(now.Truncate(time.Millisecond).Add(e.limits.SessionIdle)) {
			t.Fatalf("late session shortened: %v", s.timeLimit)
		}
	}
}

func TestNativeCatalogPageAdmissionActuallySerializes(t *testing.T) {
	f := catalogReadyFixture(t)
	e := f.executor.(*executor)
	first := f.captured(t, "POST", `{"jsonrpc":"2.0","id":100,"method":"tools/list"}`, "synthetic-session")
	defer first.lease.Release()
	r, _ := parseRequest([]byte(`{"jsonrpc":"2.0","id":100,"method":"tools/list"}`), e.limits)
	plan, _ := first.lease.Plan().ClientToolPlan()
	s, err := e.admitSession(context.Background(), first, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	release, err := e.acquireRPC(s, first.id, r)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	r.id = []byte("101")
	if other, err := e.acquireRPC(s, "ffffffffffffffffffffffffffffffff", r); err == nil {
		other()
		t.Fatal("same cursor admitted concurrently")
	}
}

func TestNativeInitializeKnownRPCErrorWithoutSession(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	wire := `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Synthetic initialization failed"}}`
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) { return wireResponse(wire, false), nil }
	terminal, w := f.serve(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, "")
	op, err := f.store.NativeToolRepository().Get(context.Background(), terminal.OperationID)
	if err != nil || op.State != "failed" || op.Reason != "rpc_error" || op.BytesIn == nil || w.Body.String() != wire {
		t.Fatalf("known initialize error %+v %+v %v", terminal, op, err)
	}
}

func TestNativeExpiredSessionClearsKeysAndResourcesBeforeReplacement(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e := f.executor.(*executor)
	var old *session
	e.mu.Lock()
	for _, s := range e.scopes[f.capture.Key()].sessions {
		old = s
	}
	e.mu.Unlock()
	expired := old.timeLimit.Add(time.Millisecond)
	e.dependencies.Clock = func() time.Time { return expired }
	terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":44,"method":"ping"}`, "synthetic-session")
	if terminal.Outcome == OutcomeCompleted {
		t.Fatal("expired session sent")
	}
	old.mu.Lock()
	if !old.closed || old.authorization != "" || old.cookie != "" || old.retained != 0 || old.catalog != nil {
		t.Fatal("expired session retained proof/resources")
	}
	old.mu.Unlock()
	terminal, _ = f.serve(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, "")
	if terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
}
