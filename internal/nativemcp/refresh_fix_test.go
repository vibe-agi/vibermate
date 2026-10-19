package nativemcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

type refreshProfiles struct{}

func (refreshProfiles) Resolve(c environment.ClientToolContract, d nativetool.CatalogDefinition) (ContentProfile, bool) {
	p := ContentProfile{contract: c, definition: d.Digest()}
	if d.Definition().Description == "Synthetic public tool" {
		p.result = []string{"/credentialId"}
	}
	return p, d.Definition().Name == "read_file"
}

func wireResponse(body string, sse bool) *http.Response {
	media := "application/json"
	if sse {
		media = "text/event-stream"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{media}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestNativeFullInflightRefreshUsesFrozenProfileAndKnownCompletion(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1})
	f.executor.(*executor).dependencies.ContentProfiles = refreshProfiles{}
	f.handshake(t)
	entered, release := make(chan struct{}), make(chan struct{})
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		switch {
		case strings.Contains(string(body), `"method":"tools/call"`):
			close(entered)
			<-release
			return wireResponse(`{"jsonrpc":"2.0","id":3,"result":{"content":[],"credentialId":"frozen-sensitive-result"}}`, false), nil
		case strings.Contains(string(body), `"method":"ping"`):
			return wireResponse("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"params\":{}}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":44,\"result\":{}}\n\n", true), nil
		default:
			return wireResponse(`{"jsonrpc":"2.0","id":5,"result":{"tools":[{"name":"read_file","description":"Replacement synthetic tool","inputSchema":{"type":"object"}}]}}`, false), nil
		}
	}
	c := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	results := make(chan Terminal, 1)
	go func() { results <- f.executor.Serve(context.Background(), c, httptest.NewRecorder()) }()
	<-entered
	for _, body := range []string{`{"jsonrpc":"2.0","id":44,"method":"ping"}`, `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`} {
		terminal, _ := f.serve(t, body, "synthetic-session")
		if terminal.Outcome != OutcomeCompleted {
			t.Fatal(terminal)
		}
	}
	close(release)
	terminal := <-results
	op, err := f.store.NativeToolRepository().Get(context.Background(), c.id)
	if err != nil || terminal.Outcome != OutcomeCompleted || op.State != "completed" || op.BytesIn == nil || op.Result != nil {
		t.Fatalf("known effect/privacy %+v %+v %v", terminal, op, err)
	}
	scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	if err != nil || scope.ReplayBarrier {
		t.Fatalf("known effect marked uncertain %+v %v", scope, err)
	}
	f.requireNoContent(t, c.id, nativetool.ContentToolResult)
	assertNoSensitiveRows(t, f, []string{"frozen-sensitive-result"})
}

func TestNativePartialCatalogInvalidationDiscardsLatePage(t *testing.T) {
	f := catalogReadyFixture(t)
	e := f.executor.(*executor)
	entered, release := make(chan struct{}), make(chan struct{})
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		switch {
		case strings.Contains(string(body), `"id":2`):
			return wireResponse(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"old","inputSchema":{"type":"object"}}],"nextCursor":"old-next"}}`, false), nil
		case strings.Contains(string(body), `"id":3`):
			close(entered)
			<-release
			return wireResponse(`{"jsonrpc":"2.0","id":3,"result":{"tools":[{"name":"late","inputSchema":{"type":"object"}}]}}`, false), nil
		case strings.Contains(string(body), `"method":"ping"`):
			return wireResponse("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"params\":{}}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":44,\"result\":{}}\n\n", true), nil
		default:
			return wireResponse(`{"jsonrpc":"2.0","id":5,"result":{"tools":[{"name":"fresh","inputSchema":{"type":"object"}}]}}`, false), nil
		}
	}
	terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "synthetic-session")
	if terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	results := make(chan Terminal, 1)
	c := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"cursor":"old-next"}}`, "synthetic-session")
	go func() { results <- f.executor.Serve(context.Background(), c, httptest.NewRecorder()) }()
	<-entered
	terminal, _ = f.serve(t, `{"jsonrpc":"2.0","id":44,"method":"ping"}`, "synthetic-session")
	if terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	close(release)
	if terminal = <-results; terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	terminal, _ = f.serve(t, `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`, "synthetic-session")
	if terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.scopes[f.capture.Key()].sessions {
		s.mu.Lock()
		_, fresh := s.catalog.Lookup("fresh")
		_, old := s.catalog.Lookup("old")
		_, late := s.catalog.Lookup("late")
		if !fresh || old || late || len(s.pages) != 0 || s.catalogBytes != 0 || s.catalogBusy {
			t.Fatalf("obsolete collection survived fresh=%v old=%v late=%v pages%d bytes%d busy%v", fresh, old, late, len(s.pages), s.catalogBytes, s.catalogBusy)
		}
		s.mu.Unlock()
	}
}
