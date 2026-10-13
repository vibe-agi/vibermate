//go:build darwin

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
)

const modelIdleArguments = `{"plan":[{"step":"Check synthetic progress","status":"completed"}]}`

type modelIdleTrace struct {
	mu                     sync.Mutex
	requests, events, acks int
	pluginDenied           int
	installedDenied        int
	modelsDenied           int
	settingsDenied         int
	comments               int
	first, last            time.Time
	failure                string
	failureDetails         []string
	terminal               chan struct{}
	terminalOnce           sync.Once
	terminalAt             time.Time
}

func (trace *modelIdleTrace) event() {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	now := time.Now()
	if trace.first.IsZero() {
		trace.first = now
	}
	trace.last = now
	trace.events++
}
func (trace *modelIdleTrace) fail(reason string) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.failure == "" {
		trace.failure = reason
	}
	if len(trace.failureDetails) < 8 {
		trace.failureDetails = append(trace.failureDetails, reason)
	} else {
		trace.failure = "failure_diagnostics_overflow"
	}
}

func modelIdleWrite(w http.ResponseWriter, trace *modelIdleTrace, kind string, value any) bool {
	body, err := json.Marshal(value)
	if err != nil {
		return false
	}
	if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, body); err != nil {
		return false
	}
	w.(http.Flusher).Flush()
	trace.event()
	return true
}
func modelIdleItem(args, status string) map[string]any {
	return map[string]any{"id": "fc_plan", "type": "function_call", "call_id": "plan_call", "name": "update_plan", "arguments": args, "status": status}
}
func modelIdleComplete(w http.ResponseWriter, trace *modelIdleTrace, items []any) bool {
	ok := modelIdleWrite(w, trace, "response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_idle", "created_at": 1, "model": "gpt-5.4", "status": "completed", "output": items, "usage": map[string]any{"input_tokens": 3, "output_tokens": 2, "total_tokens": 5}}})
	if ok && trace.terminal != nil {
		trace.terminalOnce.Do(func() { trace.mu.Lock(); trace.terminalAt = time.Now(); trace.mu.Unlock(); close(trace.terminal) })
	}
	return ok
}

func modelIdleProvider(trace *modelIdleTrace, modes ...string) http.Handler {
	mode := "arguments"
	if len(modes) != 0 {
		mode = modes[0]
	}
	managed := strings.HasPrefix(mode, "managed_")
	mode = strings.TrimPrefix(mode, "managed_")
	delay := 20 * time.Millisecond
	if strings.HasPrefix(mode, "long_") {
		mode = strings.TrimPrefix(mode, "long_")
		delay = 1750 * time.Millisecond
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if managed {
			if r.Method != "POST" || r.ProtoMajor != 1 || r.TLS != nil || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer synthetic-managed-model" {
				trace.fail("managed_fixture_transport_or_account_mismatch")
				w.WriteHeader(400)
				return
			}
		} else {
			if modelIdleExpectedSettingsDenial(r) {
				trace.mu.Lock()
				trace.settingsDenied++
				trace.mu.Unlock()
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if modelIdleExpectedModelsDenial(r) {
				trace.mu.Lock()
				trace.modelsDenied++
				trace.mu.Unlock()
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if modelIdleExpectedPluginDenial(r) {
				trace.mu.Lock()
				if r.URL.Path == "/backend-api/ps/plugins/list" {
					trace.pluginDenied++
				} else {
					trace.installedDenied++
				}
				trace.mu.Unlock()
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer synthetic-original-login" {
				keys := make([]string, 0, len(r.URL.Query()))
				for key := range r.URL.Query() {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				route := "unlisted"
				switch r.URL.Path {
				case "/backend-api/codex/responses":
					route = "responses"
				case "/backend-api/wham/accounts/check":
					route = "accounts_check"
				case "/backend-api/codex/models":
					route = "models"
				case "/backend-api/models":
					route = "backend_models"
				case "/backend-api/wham/config/bundle":
					route = "config_bundle"
				case "/backend-api/codex/config/bundle":
					route = "codex_config_bundle"
				}
				trace.fail(fmt.Sprintf("route=%s method=%s path=%s queryKeys=%v bodyPresent=%t contentLength=%d transferEncoding=%t authPresent=%t syntheticAuth=%t", route, r.Method, r.URL.EscapedPath(), keys, r.Body != nil && r.Body != http.NoBody, r.ContentLength, len(r.TransferEncoding) != 0, r.Header.Get("Authorization") != "", r.Header.Get("Authorization") == "Bearer synthetic-original-login"))
				w.WriteHeader(400)
				return
			}
			if r.Method == "GET" && r.URL.Path == "/backend-api/wham/accounts/check" && r.ContentLength == 0 && len(r.TransferEncoding) == 0 && r.TLS != nil && r.TLS.ServerName == "chatgpt.com" && r.Host == "chatgpt.com" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"accounts":[{"id":"synthetic-account","workspace_backend_origin":"https://chatgpt.com","account_routing_override":"NO_CONSTRAINT"}]}`)
				return
			}
			if r.Method != "POST" || r.URL.Path != "/backend-api/codex/responses" || r.Host != "chatgpt.com" || r.TLS == nil || r.TLS.ServerName != "chatgpt.com" {
				trace.fail(fmt.Sprintf("unlisted method=%s path=%s queryKeys=[] bodyPresent=%t contentLength=%d transferEncoding=%t canonicalHost=%t canonicalSNI=%t", r.Method, r.URL.EscapedPath(), r.Body != nil && r.Body != http.NoBody, r.ContentLength, len(r.TransferEncoding) != 0, r.Host == "chatgpt.com", r.TLS != nil && r.TLS.ServerName == "chatgpt.com"))
				w.WriteHeader(400)
				return
			}
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, (4<<20)+1))
		if err != nil || len(body) > 4<<20 {
			trace.fail("request_bound")
			w.WriteHeader(400)
			return
		}
		var request struct {
			Tools []struct {
				Type, Name string
				Parameters json.RawMessage
				Tools      []struct {
					Type, Name string
					Parameters json.RawMessage
				}
			}
		}
		// Actual request fields, never an assumed client tool catalog.
		var raw map[string]json.RawMessage
		if json.Unmarshal(body, &raw) != nil || json.Unmarshal(body, &request) != nil {
			trace.fail("request_json_or_encoding")
			w.WriteHeader(400)
			return
		}
		trace.mu.Lock()
		trace.requests++
		ordinal := trace.requests
		trace.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if ordinal == 1 {
			declared := false
			for _, tool := range request.Tools {
				if tool.Type == "function" && tool.Name == "update_plan" && len(tool.Parameters) > 0 {
					declared = true
				}
				if tool.Type == "namespace" && tool.Name == "functions" {
					for _, nested := range tool.Tools {
						if nested.Type == "function" && nested.Name == "update_plan" && len(nested.Parameters) > 0 {
							declared = true
						}
					}
				}
			}
			if !declared {
				trace.fail("update_plan_not_declared")
				w.WriteHeader(400)
				return
			}
			codec, err := openairesponses.New(openairesponses.DefaultOptions())
			if err != nil {
				trace.fail("codec_construction")
				return
			}
			if _, _, err = codec.DecodeCompatibleClientRequest(body); err != nil {
				trace.fail("actual_declared_request_invalid")
				w.WriteHeader(400)
				return
			}
			modelIdleWrite(w, trace, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_idle", "created_at": 1, "model": "gpt-5.4", "status": "in_progress", "output": []any{}}})
			if mode == "text" {
				text := "synthetic idle complete"
				part := map[string]any{"type": "output_text", "text": ""}
				item := map[string]any{"id": "msg_idle", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}
				modelIdleWrite(w, trace, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
				modelIdleWrite(w, trace, "response.content_part.added", map[string]any{"type": "response.content_part.added", "output_index": 0, "item_id": "msg_idle", "content_index": 0, "part": part})
				for index := 0; index < 20; index++ {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(20 * time.Millisecond):
					}
					if !modelIdleWrite(w, trace, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "output_index": 0, "item_id": "msg_idle", "content_index": 0, "delta": text[len(text)*index/20 : len(text)*(index+1)/20]}) {
						return
					}
				}
				part["text"] = text
				item["content"], item["status"] = []any{part}, "completed"
				modelIdleWrite(w, trace, "response.output_text.done", map[string]any{"type": "response.output_text.done", "output_index": 0, "item_id": "msg_idle", "content_index": 0, "text": text})
				modelIdleWrite(w, trace, "response.content_part.done", map[string]any{"type": "response.content_part.done", "output_index": 0, "item_id": "msg_idle", "content_index": 0, "part": part})
				modelIdleWrite(w, trace, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
				modelIdleComplete(w, trace, []any{item})
				return
			}
			modelIdleWrite(w, trace, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": modelIdleItem("", "in_progress")})
			for index := 0; index < 20; index++ {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(delay):
				}
				if mode == "comments" {
					if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
						return
					}
					w.(http.Flusher).Flush()
					trace.mu.Lock()
					trace.comments++
					trace.mu.Unlock()
					continue
				}
				if mode == "silence" {
					continue
				}
				if mode == "keepalive" {
					if !modelIdleWrite(w, trace, "keepalive", map[string]any{"type": "keepalive", "sequence_number": index}) {
						return
					}
					continue
				}
				if mode == "mixed" && index%4 == 0 {
					if !modelIdleWrite(w, trace, "keepalive", map[string]any{"type": "keepalive"}) {
						return
					}
				}
				delta := modelIdleArguments[len(modelIdleArguments)*index/20 : len(modelIdleArguments)*(index+1)/20]
				if !modelIdleWrite(w, trace, "response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "item_id": "fc_plan", "delta": delta}) {
					return
				}
			}
			if mode != "arguments" && mode != "mixed" {
				modelIdleWrite(w, trace, "response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "item_id": "fc_plan", "delta": modelIdleArguments})
			}
			modelIdleWrite(w, trace, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": modelIdleItem(modelIdleArguments, "completed")})
			modelIdleComplete(w, trace, []any{modelIdleItem(modelIdleArguments, "completed")})
			return
		}
		if ordinal != 2 {
			trace.fail("unexpected_model_retry")
			w.WriteHeader(400)
			return
		}
		var inputs []map[string]json.RawMessage
		if json.Unmarshal(raw["input"], &inputs) != nil {
			trace.fail("followup_input_shape")
			w.WriteHeader(400)
			return
		}
		ack := false
		for _, item := range inputs {
			var kind, call, output string
			_ = json.Unmarshal(item["type"], &kind)
			_ = json.Unmarshal(item["call_id"], &call)
			_ = json.Unmarshal(item["output"], &output)
			if kind == "function_call_output" && call == "plan_call" {
				ack = output == "Plan updated"
			}
		}
		if !ack {
			trace.fail("successful_plan_ack_missing")
			w.WriteHeader(400)
			return
		}
		trace.mu.Lock()
		trace.acks++
		trace.mu.Unlock()
		item := map[string]any{"id": "msg_idle", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "synthetic idle complete"}}}
		modelIdleWrite(w, trace, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		modelIdleComplete(w, trace, []any{item})
	})
}

func modelIdleExpectedPluginDenial(r *http.Request) bool {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return false
	}
	list := r.URL.Path == "/backend-api/ps/plugins/list" && len(query) == 2 && len(query["scope"]) == 1 && query.Get("scope") == "GLOBAL"
	installed := r.URL.Path == "/backend-api/ps/plugins/installed" && len(query) == 1
	return (list || installed) && r.Method == http.MethodGet && r.Host == "chatgpt.com" && r.TLS != nil && r.TLS.ServerName == "chatgpt.com" &&
		(r.Body == nil || r.Body == http.NoBody) && r.ContentLength == 0 && len(r.TransferEncoding) == 0 && r.Header.Get("Authorization") == "Bearer synthetic-original-login" &&
		len(query["limit"]) == 1 && query.Get("limit") == "200"
}

func modelIdleExpectedModelsDenial(r *http.Request) bool {
	query, err := url.ParseQuery(r.URL.RawQuery)
	return err == nil && r.Method == "GET" && r.URL.Path == "/backend-api/codex/models" && r.Host == "chatgpt.com" && r.TLS != nil && r.TLS.ServerName == "chatgpt.com" && (r.Body == nil || r.Body == http.NoBody) && r.ContentLength == 0 && len(r.TransferEncoding) == 0 && r.Header.Get("Authorization") == "Bearer synthetic-original-login" && len(query) == 1 && len(query["client_version"]) == 1 && query.Get("client_version") == "0.160.0"
}

func modelIdleExpectedSettingsDenial(r *http.Request) bool {
	return r.Method == "GET" && r.URL.Path == "/backend-api/wham/settings/user" && r.URL.RawQuery == "" && r.Host == "chatgpt.com" && r.TLS != nil && r.TLS.ServerName == "chatgpt.com" && (r.Body == nil || r.Body == http.NoBody) && r.ContentLength == 0 && len(r.TransferEncoding) == 0 && r.Header.Get("Authorization") == "Bearer synthetic-original-login"
}

func TestModelSSEIdleSettingsDenialIsExact(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"exact", func(*http.Request) {}},
		{"query", func(r *http.Request) { r.URL.RawQuery = "extra=1" }},
		{"empty_query_pair", func(r *http.Request) { r.URL.RawQuery = "&" }},
		{"body", func(r *http.Request) { r.Body = io.NopCloser(bytes.NewBufferString("x")); r.ContentLength = 1 }},
		{"transfer_encoding", func(r *http.Request) { r.TransferEncoding = []string{"chunked"} }},
		{"auth", func(r *http.Request) { r.Header.Del("Authorization") }},
		{"path", func(r *http.Request) { r.URL.Path += "/other" }},
		{"method", func(r *http.Request) { r.Method = "POST" }},
		{"host", func(r *http.Request) { r.Host = "other.invalid" }},
		{"sni", func(r *http.Request) { r.TLS.ServerName = "other.invalid" }},
		{"plaintext", func(r *http.Request) { r.TLS = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://chatgpt.com/backend-api/wham/settings/user", nil)
			r.TLS = &tls.ConnectionState{ServerName: "chatgpt.com"}
			r.Header.Set("Authorization", "Bearer synthetic-original-login")
			test.mutate(r)
			trace := &modelIdleTrace{}
			response := httptest.NewRecorder()
			modelIdleProvider(trace).ServeHTTP(response, r)
			if test.name == "exact" {
				if response.Code != 403 || trace.settingsDenied != 1 || trace.requests != 0 || trace.failure != "" {
					t.Fatal("exact settings rejection differs")
				}
			} else if trace.settingsDenied != 0 || trace.requests != 0 || trace.failure == "" {
				t.Fatal("altered settings request escaped")
			}
		})
	}
}

func TestModelSSEIdleModelsDenialIsExact(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"exact", func(*http.Request) {}},
		{"extra", func(r *http.Request) { r.URL.RawQuery += "&extra=1" }},
		{"duplicate", func(r *http.Request) { r.URL.RawQuery += "&client_version=0.160.0" }},
		{"wrong_pin", func(r *http.Request) { r.URL.RawQuery = "client_version=0.159.2" }},
		{"malformed", func(r *http.Request) { r.URL.RawQuery += "&bad=%zz" }},
		{"body", func(r *http.Request) { r.Body = io.NopCloser(bytes.NewBufferString("x")); r.ContentLength = 1 }},
		{"transfer_encoding", func(r *http.Request) { r.TransferEncoding = []string{"chunked"} }},
		{"auth", func(r *http.Request) { r.Header.Del("Authorization") }},
		{"path", func(r *http.Request) { r.URL.Path += "/other" }},
		{"method", func(r *http.Request) { r.Method = "POST" }},
		{"host", func(r *http.Request) { r.Host = "other.invalid" }},
		{"sni", func(r *http.Request) { r.TLS.ServerName = "other.invalid" }},
		{"plaintext", func(r *http.Request) { r.TLS = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://chatgpt.com/backend-api/codex/models?client_version=0.160.0", nil)
			r.TLS = &tls.ConnectionState{ServerName: "chatgpt.com"}
			r.Header.Set("Authorization", "Bearer synthetic-original-login")
			test.mutate(r)
			trace := &modelIdleTrace{}
			response := httptest.NewRecorder()
			modelIdleProvider(trace).ServeHTTP(response, r)
			if test.name == "exact" {
				if response.Code != 403 || trace.modelsDenied != 1 || trace.requests != 0 || trace.failure != "" {
					t.Fatal("exact models rejection differs")
				}
			} else if trace.modelsDenied != 0 || trace.requests != 0 || trace.failure == "" {
				t.Fatal("altered models request escaped")
			}
		})
	}
}

func TestModelSSEIdlePluginDenialIsExact(t *testing.T) {
	for _, endpoint := range []string{"list", "installed"} {
		t.Run(endpoint, func(t *testing.T) {
			for _, test := range []struct {
				name   string
				mutate func(*http.Request)
			}{
				{"exact", func(*http.Request) {}},
				{"extra_query", func(r *http.Request) { r.URL.RawQuery += "&extra=1" }},
				{"duplicate_query", func(r *http.Request) { r.URL.RawQuery += "&limit=200" }},
				{"download_urls", func(r *http.Request) { r.URL.RawQuery += "&includeDownloadUrls=true" }},
				{"page_token", func(r *http.Request) { r.URL.RawQuery += "&pageToken=x" }},
				{"malformed_query", func(r *http.Request) { r.URL.RawQuery += "&bad=%zz" }},
				{"wrong_scope", func(r *http.Request) { r.URL.RawQuery = "scope=USER&limit=200" }},
				{"wrong_limit", func(r *http.Request) { r.URL.RawQuery = "scope=GLOBAL&limit=201" }},
				{"body", func(r *http.Request) { r.Body = io.NopCloser(bytes.NewBufferString("x")); r.ContentLength = 1 }},
				{"transfer_encoding", func(r *http.Request) { r.TransferEncoding = []string{"chunked"} }},
				{"auth", func(r *http.Request) { r.Header.Del("Authorization") }},
				{"path", func(r *http.Request) { r.URL.Path += "/other" }},
				{"method", func(r *http.Request) { r.Method = "POST" }},
				{"host", func(r *http.Request) { r.Host = "other.invalid" }},
				{"sni", func(r *http.Request) { r.TLS.ServerName = "other.invalid" }},
				{"plaintext", func(r *http.Request) { r.TLS = nil }},
			} {
				t.Run(test.name, func(t *testing.T) {
					query := "limit=200"
					if endpoint == "list" {
						query += "&scope=GLOBAL"
					}
					r := httptest.NewRequest("GET", "https://chatgpt.com/backend-api/ps/plugins/"+endpoint+"?"+query, nil)
					r.TLS = &tls.ConnectionState{ServerName: "chatgpt.com"}
					r.Header.Set("Authorization", "Bearer synthetic-original-login")
					test.mutate(r)
					trace := &modelIdleTrace{}
					response := httptest.NewRecorder()
					modelIdleProvider(trace).ServeHTTP(response, r)
					if test.name == "exact" {
						if response.Code != 403 || trace.pluginDenied+trace.installedDenied != 1 || trace.failure != "" || trace.requests != 0 {
							t.Fatal("exact plugin request was not separately denied")
						}
					} else if trace.pluginDenied+trace.installedDenied != 0 || trace.failure == "" || trace.requests != 0 {
						t.Fatal("changed plugin request escaped strict unexpected classification")
					}
				})
			}
		})
	}
}

// Test-only CONNECT bridge. There is no DNS lookup, alternate address or blind
// fallback: the only destination is this immutable literal-loopback TLS server.
func modelIdleProxy(t *testing.T, address string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("private literal fixture required")
	}
	var mu sync.Mutex
	closed := false
	connections := map[net.Conn]bool{}
	var workers sync.WaitGroup
	denied := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != "chatgpt.com:443" || r.URL.RawQuery != "" {
			denied.Add(1)
			w.WriteHeader(403)
			return
		}
		mu.Lock()
		if closed {
			mu.Unlock()
			w.WriteHeader(503)
			return
		}
		workers.Add(1)
		mu.Unlock()
		defer workers.Done()
		upstream, err := (&net.Dialer{Timeout: time.Second}).DialContext(r.Context(), "tcp", address)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		client, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		mu.Lock()
		connections[upstream] = true
		connections[client] = true
		closing := closed
		mu.Unlock()
		defer func() {
			client.Close()
			upstream.Close()
			mu.Lock()
			delete(connections, client)
			delete(connections, upstream)
			mu.Unlock()
		}()
		if closing {
			return
		}
		_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffer.Flush()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(upstream, buffer); upstream.Close(); client.Close(); close(done) }()
		_, _ = io.Copy(client, upstream)
		client.Close()
		upstream.Close()
		<-done
	}))
	t.Cleanup(func() {
		mu.Lock()
		closed = true
		for c := range connections {
			c.Close()
		}
		mu.Unlock()
		server.Close()
		done := make(chan struct{})
		go func() { workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("private CONNECT bridge did not drain")
		}
	})
	return server, denied
}

func modelIdleVerifiedClient(t *testing.T, ctx context.Context, dir string, versions ...string) (string, string) {
	t.Helper()
	version := "0.160.0"
	if len(versions) > 0 {
		version = versions[0]
	}
	if version != "0.145.0" && version != "0.159.2" && version != "0.160.0" {
		t.Fatal("unsupported fixed MODEL client pin")
	}
	binary, node := os.Getenv("VIBERMATE_NATIVE_CODEX"), os.Getenv("VIBERMATE_PINNED_NODE")
	if binary == "" || node == "" {
		t.Skip("requires explicit exact160/Node; no diagnostic evidence")
	}
	actualNode, err := nativePinnedNode(ctx, dir)
	if err != nil || actualNode != node {
		t.Fatal("pinned Node identity unavailable")
	}
	entry, err := filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	native := filepath.Clean(filepath.Join(filepath.Dir(entry), "../../codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex"))
	if err = checkNativeWrapperResolution(entry, native); err != nil {
		t.Fatal(err)
	}
	verifier, err := clientadapter.NewReleaseVerifier(clientadapter.BuiltInCatalog())
	if err != nil {
		t.Fatal(err)
	}
	detection, err := verifier.Verify(ctx, clientadapter.Request{Command: []string{"codex"}, ExecutablePath: binary, CWD: dir})
	if err != nil || detection.Status != clientadapter.StatusVerified || detection.Evidence == nil || detection.Evidence.Version != version {
		t.Fatal("exact compound MODEL client verification failed")
	}
	if err = verifyNativeClientVersions(ctx, []nativePinnedClient{{nativeMCPClientReport: nativeMCPClientReport{Version: version}, invocation: binary}}, node, dir); err != nil {
		t.Fatal(err)
	}
	return binary, node
}

type modelIdleClientObservation struct {
	mu                                                      sync.Mutex
	partial                                                 []byte
	plans                                                   int
	finalText, idle                                         bool
	missingEnv, authenticationFailure                       bool
	pathUnsupported, targetUnproven, operationNotCatalogued bool
	errorClass                                              string
	threadID, turnID, planThread, planTurn                  string
}

func (observer *modelIdleClientObservation) Write(body []byte) (int, error) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.partial = append(observer.partial, body...)
	if len(observer.partial) > 4<<20 {
		return 0, fmt.Errorf("client observation frame exceeded bound")
	}
	for {
		index := bytes.IndexByte(observer.partial, '\n')
		if index < 0 {
			break
		}
		frame := observer.partial[:index]
		var value struct{ Method string }
		if json.Unmarshal(frame, &value) != nil {
			return 0, fmt.Errorf("client observation invalid JSON")
		}
		var identity struct {
			ID     int
			Result struct {
				Thread struct{ ID string }
				Turn   struct{ ID string }
			}
			Params struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
				Turn     struct{ ID string }
				Plan     []struct{ Step, Status string }
			}
		}
		if json.Unmarshal(frame, &identity) != nil {
			return 0, fmt.Errorf("client identity frame invalid")
		}
		if identity.ID == 2 && identity.Result.Thread.ID != "" {
			observer.threadID = identity.Result.Thread.ID
		}
		if identity.ID == 3 && identity.Result.Turn.ID != "" {
			observer.turnID = identity.Result.Turn.ID
		}
		if value.Method == "turn/started" && identity.Params.Turn.ID != "" {
			observer.turnID = identity.Params.Turn.ID
		}
		if value.Method == "turn/plan/updated" {
			if len(identity.Params.Plan) != 1 || identity.Params.Plan[0].Step != "Check synthetic progress" || identity.Params.Plan[0].Status != "completed" || identity.Params.ThreadID == "" || identity.Params.TurnID == "" {
				return 0, fmt.Errorf("actual plan notification did not match the synthetic tool result")
			}
			observer.planThread, observer.planTurn = identity.Params.ThreadID, identity.Params.TurnID
			observer.plans++
		}
		if value.Method == "turn/completed" && observer.plans > 0 && (observer.threadID == "" || observer.turnID == "" || observer.planThread != observer.threadID || observer.planTurn != observer.turnID || identity.Params.ThreadID != observer.threadID || identity.Params.Turn.ID != observer.turnID) {
			return 0, fmt.Errorf("plan notification was not correlated to actual thread and turn")
		}
		observer.finalText = observer.finalText || bytes.Contains(frame, []byte("synthetic idle complete"))
		observer.idle = observer.idle || bytes.Contains(frame, []byte("idle timeout waiting for SSE"))
		observer.missingEnv = observer.missingEnv || bytes.Contains(frame, []byte("Missing environment variable")) || bytes.Contains(frame, []byte("VIBERMATE_SYNTHETIC_MODEL_API_KEY"))
		observer.authenticationFailure = observer.authenticationFailure || bytes.Contains(frame, []byte("401 Unauthorized")) || bytes.Contains(frame, []byte("not authenticated"))
		observer.pathUnsupported = observer.pathUnsupported || bytes.Contains(frame, []byte("path_unsupported"))
		observer.targetUnproven = observer.targetUnproven || bytes.Contains(frame, []byte("target_unproven"))
		observer.operationNotCatalogued = observer.operationNotCatalogued || bytes.Contains(frame, []byte("operation_not_catalogued"))
		if value.Method == "turn/completed" {
			var terminal struct {
				Params struct {
					Turn struct {
						Error *struct {
							Code json.RawMessage `json:"codexErrorInfo"`
						} `json:"error"`
					}
				}
			}
			if json.Unmarshal(frame, &terminal) == nil && terminal.Params.Turn.Error != nil {
				var code string
				if json.Unmarshal(terminal.Params.Turn.Error.Code, &code) == nil {
					switch code {
					case "other", "badRequest", "unauthorized", "internalServerError":
						observer.errorClass = code
					default:
						observer.errorClass = "unclassified"
					}
				} else {
					observer.errorClass = "structured_transport"
				}
			}
		}
		observer.partial = observer.partial[index+1:]
	}
	return len(body), nil
}
func modelIdleThreadParams(dir string, idle int) map[string]any {
	return map[string]any{"cwd": dir, "model": "gpt-5.4", "modelProvider": "synthetic_idle", "approvalPolicy": "never", "sandbox": "read-only", "config": map[string]any{
		"tools.update_plan.enabled": true, "features.apps": false,
		"model_providers.synthetic_idle": map[string]any{"name": "Synthetic idle fixture", "base_url": "https://chatgpt.com/backend-api/codex", "wire_api": "responses", "requires_openai_auth": true, "supports_websockets": false, "stream_idle_timeout_ms": idle, "stream_max_retries": 0, "request_max_retries": 0},
	}}
}

func TestModelSSEIdleDirectHarmlessPlanBaseline(t *testing.T) {
	runModelIdleDirect(t, "arguments")
}

func TestModelSSEIdleDirectEventControls(t *testing.T) {
	for _, mode := range []string{"keepalive", "comments", "silence", "text"} {
		t.Run(mode, func(t *testing.T) { runModelIdleDirect(t, mode) })
	}
}

func TestModelSSEIdleDirectRuntimeRatioControls(t *testing.T) {
	for _, mode := range []string{"long_arguments", "long_silence"} {
		t.Run(mode, func(t *testing.T) { runModelIdleDirect(t, mode) })
	}
}

func runModelIdleDirect(t *testing.T, mode string) {
	dir := t.TempDir()
	budget, idleMS := 15*time.Second, 180
	if strings.HasPrefix(mode, "long_") {
		budget, idleMS = 60*time.Second, 20000
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	binary, node := modelIdleVerifiedClient(t, ctx, dir)
	trace := &modelIdleTrace{}
	provider, roots := nativeTLSFixture(t, modelIdleProvider(trace, mode))
	proxy, denied := modelIdleProxy(t, provider.Listener.Addr().String())
	proxyURL, _ := url.Parse(proxy.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	for _, target := range []string{"https://unlisted.invalid/", "https://auth.openai.com/", "https://chatgpt.com:444/"} {
		response, err := client.Get(target)
		if response != nil {
			response.Body.Close()
		}
		if err == nil {
			t.Fatal("containment negative escaped")
		}
	}
	if denied.Load() != 3 {
		t.Fatal("containment preflight count differs")
	}
	rootPath := filepath.Join(dir, "private-ca.pem")
	if err := os.WriteFile(rootPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.TLS.Certificates[0].Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	jwt := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_plan_type":"plus","chatgpt_user_id":"synthetic-user","chatgpt_account_id":"synthetic-account"}}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte("signature"))
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "tokens": map[string]any{"id_token": jwt, "access_token": "synthetic-original-login", "refresh_token": "synthetic-never-refresh", "account_id": "synthetic-account"}, "last_refresh": time.Now().UTC()})
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("cli_auth_credentials_store = \"file\"\n[features]\napps = false\nplugins = false\n[analytics]\nenabled = false\n[feedback]\nenabled = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://")
	command.Dir = dir
	command.Env = []string{"PATH=" + filepath.Dir(node) + ":/usr/bin:/bin", "HOME=" + dir, "CODEX_HOME=" + dir, "CODEX_CA_CERTIFICATE=" + rootPath, "SSL_CERT_FILE=" + rootPath, "HTTP_PROXY=" + proxy.URL, "HTTPS_PROXY=" + proxy.URL, "ALL_PROXY=" + proxy.URL, "NO_PROXY="}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGTERM) }
	command.WaitDelay = time.Second
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	observer := &modelIdleClientObservation{}
	defer func() {
		trace.mu.Lock()
		defer trace.mu.Unlock()
		observer.mu.Lock()
		defer observer.mu.Unlock()
		t.Logf("body-free final trace: requests=%d flushedEvents=%d acknowledgements=%d planNotifications=%d idle=%t expectedPluginListDenied=%d installedDenied=%d modelsDenied=%d settingsDenied=%d failure=%s", trace.requests, trace.events, trace.acks, observer.plans, observer.idle, trace.pluginDenied, trace.installedDenied, trace.modelsDenied, trace.settingsDenied, trace.failure)
		t.Logf("bounded failures=%v", trace.failureDetails)
	}()
	frames, frameFinished := nativeClientFrames(ctx, io.TeeReader(outputReader, observer))
	stderr := newBoundedBuffer(64 << 10)
	command.Stdin = inputReader
	command.Stdout = outputWriter
	command.Stderr = stderr
	started := time.Now()
	done := make(chan error, 1)
	go func() { err := command.Run(); outputWriter.Close(); inputReader.Close(); done <- err }()
	defer func() {
		cancel()
		inputWriter.Close()
		outputReader.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("direct client did not drain")
		}
		if err := <-frameFinished; err != nil {
			t.Error(err)
		}
	}()
	send := func(id int, method string, params any) {
		t.Helper()
		if err := json.NewEncoder(inputWriter).Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			t.Fatal("client stdin failed")
		}
	}
	read := func(id int) map[string]json.RawMessage {
		t.Helper()
		for {
			select {
			case frame, ok := <-frames:
				if !ok {
					t.Fatal("client ended before RPC response")
				}
				if string(frame["id"]) == fmt.Sprint(id) {
					if frame["error"] != nil {
						t.Fatalf("client setup RPC%d rejected", id)
					}
					return frame
				}
			case <-ctx.Done():
				t.Fatal("bounded direct client deadline")
			}
		}
	}
	send(1, "initialize", map[string]any{"clientInfo": map[string]any{"name": "model_idle_diagnostic", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}})
	read(1)
	send(4, "config/read", map[string]any{"includeLayers": false, "cwd": dir})
	var configuration struct {
		Config struct {
			Features map[string]bool `json:"features"`
		} `json:"config"`
	}
	if json.Unmarshal(read(4)["result"], &configuration) != nil {
		t.Fatal("private effective config read failed")
	}
	if plugins, exists := configuration.Config.Features["plugins"]; !exists || plugins {
		t.Fatal("private startup plugins-off not confirmed")
	}
	t.Log("supported config/read confirms plugins=false")
	send(2, "thread/start", modelIdleThreadParams(dir, idleMS))
	var thread struct {
		ModelProvider string `json:"modelProvider"`
		Thread        struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if json.Unmarshal(read(2)["result"], &thread) != nil || thread.ModelProvider != "synthetic_idle" || thread.Thread.ID == "" {
		t.Fatal("actual active provider not confirmed")
	}
	send(3, "turn/start", map[string]any{"threadId": thread.Thread.ID, "input": []any{map[string]any{"type": "text", "text": "Complete the synthetic request.", "text_elements": []any{}}}})
	read(3)
	completed, terminal := false, false
	for !terminal {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatal("client ended before terminal")
			}
			if string(frame["method"]) == `"turn/completed"` {
				var result struct {
					Turn struct {
						Status string
						Error  json.RawMessage
					}
				}
				if json.Unmarshal(frame["params"], &result) != nil {
					t.Fatal("invalid terminal frame")
				}
				completed = result.Turn.Status == "completed" && (len(result.Turn.Error) == 0 || string(result.Turn.Error) == "null")
				terminal = true
			}
		case <-ctx.Done():
			t.Fatal("bounded direct model deadline")
		}
	}
	elapsed := time.Since(started)
	diagnostics, stderrOverflow := stderr.snapshot()
	trace.mu.Lock()
	requests, events, acks, failure, duration, pluginDenied := trace.requests, trace.events, trace.acks, trace.failure, trace.last.Sub(trace.first), trace.pluginDenied
	comments, clientInterval := trace.comments, time.Since(trace.first)
	installedDenied := trace.installedDenied
	modelsDenied := trace.modelsDenied
	settingsDenied := trace.settingsDenied
	trace.mu.Unlock()
	observer.mu.Lock()
	plans, finalText, observedIdle := observer.plans, observer.finalText, observer.idle
	observer.mu.Unlock()
	idle := bytes.Contains(diagnostics, []byte("idle timeout waiting for SSE")) || observedIdle
	t.Logf("direct160 mode=%s provider=%s requests=%d flushedDataEvents=%d flushedComments=%d ack=%d planNotifications=%d streamMS=%d clientIntervalMS=%d processMS=%d idle=%t denied=%d failure=%s completed=%t", mode, thread.ModelProvider, requests, events, comments, acks, plans, duration.Milliseconds(), clientInterval.Milliseconds(), elapsed.Milliseconds(), idle, denied.Load(), failure, completed)
	if stderrOverflow || failure != "" || denied.Load() != 3 || pluginDenied != 0 || installedDenied != 0 || modelsDenied < 1 || modelsDenied > 2 || settingsDenied != 1 {
		t.Fatal("direct fixture evidence invalid")
	}
	if mode == "comments" || mode == "silence" || mode == "long_silence" {
		minimum, maximum := 150*time.Millisecond, time.Second
		if mode == "long_silence" {
			minimum, maximum = 19*time.Second, 22*time.Second
		}
		if completed || !idle || requests != 1 || events != 2 || acks != 0 || plans != 0 || finalText || clientInterval < minimum || clientInterval > maximum || mode == "comments" && comments < 1 || (mode == "silence" || mode == "long_silence") && comments != 0 {
			t.Fatal("effective idle/retries0 control not established")
		}
		return
	}
	if mode == "text" {
		if requests != 1 || events != 27 || acks != 0 || plans != 0 || !finalText || idle || !completed {
			t.Fatal("safe text progress control not established")
		}
		return
	}
	expectedEvents := 26
	if mode == "keepalive" {
		expectedEvents = 27
	}
	if requests != 2 || events != expectedEvents || acks != 1 || plans != 1 || !finalText || idle || !completed {
		t.Fatalf("harmless direct baseline not established: idle=%t failure=%s", idle, failure)
	}
}
