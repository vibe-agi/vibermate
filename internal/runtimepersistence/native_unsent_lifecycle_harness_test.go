package runtimepersistence

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
)

type unsentLifecycleHarness struct {
	f         nativeLeaseFixture
	module    nativemcp.Executor
	held      *heldUnsentAttempt
	transport *recoveryFinishTransport
	ids       *mcpFaultIDs
	advance   atomic.Int64
	kind      string
	parent    nativetool.ID
}

func (h *unsentLifecycleHarness) now() time.Time {
	now := time.Now().UTC()
	if advanced := time.UnixMilli(h.advance.Load()).UTC(); advanced.After(now) {
		return advanced
	}
	return now
}
func newUnsentLifecycleHarness(t *testing.T, kind string) *unsentLifecycleHarness {
	t.Helper()
	h := &unsentLifecycleHarness{f: newNativeLeaseFixture(t, true, false, environment.ToolPolicyReview), ids: &mcpFaultIDs{}, transport: &recoveryFinishTransport{}, kind: kind}
	// Configure legal private fixture lifetimes before any observation/goroutine.
	h.f.store.nativeTools.streamLifetime = 10 * time.Second
	h.held = &heldUnsentAttempt{Repository: h.f.store.NativeToolRepository(), started: make(chan unsentStartObservation, 1), release: make(chan struct{})}
	offline, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err = offline.Start(context.Background(), offlinehold.RuntimeBinding{InstanceID: "unsent-lifecycle"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(offline.BeginShutdown)
	options := toolapproval.DefaultOptions(h.f.store.ToolApprovalRepository())
	options.NativeRepository = h.f.store.NativeToolRepository()
	authority, err := toolapproval.New(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authority.Shutdown(context.Background()) })
	gate, err := toolpolicy.New(authority)
	if err != nil {
		t.Fatal(err)
	}
	limits := nativemcp.DefaultLimits()
	if kind == "initialize" || kind == "initialized" {
		limits.SessionIdle = 10 * time.Second
	}
	limits.CallLifetime = 10 * time.Second
	h.module, err = nativemcp.New(nativemcp.Dependencies{OwnerContext: context.Background(), Transport: h.transport, Store: h.held, ToolDecisions: gate, Offline: offline, Clock: h.now, IDs: h.ids}, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.module.Close(context.Background()) })
	h.transport.handler = h.response
	return h
}
func (h *unsentLifecycleHarness) capture(t *testing.T, method, body, sid, cursor string) (nativemcp.CapturedRequest, nativetool.ID) {
	t.Helper()
	facts := h.f.facts
	facts.Target.Method = method
	lease, err := h.f.assignments.BeginRequest(context.Background(), h.f.capture, h.f.connectionID, facts)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := h.ids.NewNativeOperationID(context.Background())
	r := httptest.NewRequest(method, "https://native.example/backend-api/ps/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Authorization", "Bearer synthetic-original")
	if sid != "" {
		r.Header.Set("Mcp-Session-Id", sid)
		r.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	}
	if cursor != "" {
		r.Header.Set("Last-Event-Id", cursor)
	}
	c, err := nativemcp.NewCapturedRequest(lease, h.f.admission, r, id)
	if err != nil {
		lease.Release()
		t.Fatal(err)
	}
	return c, id
}
func (h *unsentLifecycleHarness) serve(t *testing.T, body, sid string) {
	t.Helper()
	c, _ := h.capture(t, "POST", body, sid, "")
	if result := h.module.Serve(context.Background(), c, httptest.NewRecorder()); result.Outcome != nativemcp.OutcomeCompleted {
		t.Fatal(result)
	}
}

const unsentInit = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"elicitation":{"form":{},"url":{}}}}}`

func (h *unsentLifecycleHarness) prepare(t *testing.T) (nativemcp.CapturedRequest, nativetool.ID) {
	t.Helper()
	if h.kind == "initialize" {
		return h.capture(t, "POST", unsentInit, "", "")
	}
	h.serve(t, unsentInit, "")
	if h.kind == "initialized" {
		return h.capture(t, "POST", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "session", "")
	}
	h.serve(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "session")
	if h.kind == "recovery" {
		h.serve(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "session")
		c, id := h.capture(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{},"_meta":{"progressToken":"p"}}}`, "session", "")
		h.parent = id
		done := make(chan nativemcp.Terminal, 1)
		go func() { done <- h.module.Serve(context.Background(), c, httptest.NewRecorder()) }()
		approveUnsentLifecycle(t, h.f.store.NativeToolRepository(), id)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("original stream did not detach")
		}
		op, err := h.f.store.NativeToolRepository().Get(context.Background(), id)
		if err != nil || op.State != "started" {
			t.Fatal("original logical RPC missing")
		}
		return h.capture(t, "GET", "", "session", "e1")
	}
	parent, id := h.capture(t, "GET", "", "session", "")
	h.parent = id
	if result := h.module.Serve(context.Background(), parent, httptest.NewRecorder()); result.Outcome != nativemcp.OutcomeCompleted {
		t.Fatal(result)
	}
	reply := `{"jsonrpc":"2.0","id":17,"result":{"action":"decline"}}`
	if h.kind == "url" {
		reply = `{"jsonrpc":"2.0","id":17,"result":{"action":"accept"}}`
	}
	return h.capture(t, "POST", reply, "session", "")
}
func (h *unsentLifecycleHarness) invalidate(t *testing.T, id nativetool.ID) {
	t.Helper()
	switch h.kind {
	case "initialize", "initialized":
		var expires int64
		if err := h.f.store.database.QueryRow(`SELECT s.expires_at_unix_ms FROM native_tool_sessions s JOIN native_tool_bindings b USING(session_id) WHERE b.operation_id=?`, id).Scan(&expires); err != nil {
			t.Fatal(err)
		}
		h.advance.Store(expires - 9000)
		h.serve(t, strings.Replace(unsentInit, `"id":1`, `"id":91`, 1), "")
		h.serve(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "sibling")
		h.advance.Store(expires + 1)
	case "form", "url":
		h.f.store.nativeTools.issuerMu.Lock()
		var expires time.Time
		for _, s := range h.f.store.nativeTools.streams {
			if s.receipt.OperationID() == h.parent {
				expires = s.expires
			}
		}
		h.f.store.nativeTools.issuerMu.Unlock()
		if expires.IsZero() {
			t.Fatal("actual callback authority absent")
		}
		h.advance.Store(expires.Add(time.Millisecond).UnixMilli())
	case "recovery":
		h.serve(t, `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`, "session")
	}
}
func (h *unsentLifecycleHarness) response(r *http.Request, body []byte) (*http.Response, error) {
	if r.Method == "GET" {
		callback := `{"jsonrpc":"2.0","id":17,"method":"elicitation/create","params":{"message":"synthetic form","requestedSchema":{"type":"object","properties":{"answer":{"type":"string"}}}}}`
		if h.kind == "url" {
			callback = `{"jsonrpc":"2.0","id":17,"method":"elicitation/create","params":{"mode":"url","message":"synthetic consent","url":"https://example.invalid/consent","elicitationId":"fault-url"}}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + callback + "\n\n"))}, nil
	}
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	header := http.Header{"Content-Type": []string{"application/json"}}
	result := `{}`
	switch request.Method {
	case "initialize":
		header.Set("Mcp-Session-Id", "session")
		if string(request.ID) == "91" {
			header.Set("Mcp-Session-Id", "sibling")
		}
		result = `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}`
	case "notifications/initialized", "":
		return &http.Response{StatusCode: 202, Header: header, Body: http.NoBody}, nil
	case "tools/list":
		result = `{"tools":[{"name":"read_file","inputSchema":{"type":"object"}}]}`
	case "tools/call":
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("id: e1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"p\",\"progress\":1}}\n\n"))}, nil
	}
	return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":` + result + `}`))}, nil
}
func approveUnsentLifecycle(t *testing.T, r nativetool.Repository, id nativetool.ID) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		op, err := r.Get(context.Background(), id)
		if err == nil && op.ApprovalID != nil {
			nativeAllow(t, r, id, time.Now().UTC())
			return
		}
		select {
		case <-deadline:
			t.Fatal("actual approval absent")
		case <-time.After(time.Millisecond):
		}
	}
}
