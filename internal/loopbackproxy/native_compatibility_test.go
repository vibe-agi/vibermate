package loopbackproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func nativeCodexFixtureCommand(t *testing.T, endpoint string, options ...string) *exec.Cmd {
	t.Helper()
	binary := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if binary == "" {
		t.Skip("requires an explicitly selected local Codex binary")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("Codex binary must be absolute")
	}
	directory := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	requireNoNativeCodexPluginSync(t, directory)
	args := []string{"exec", "--skip-git-repo-check", "--ephemeral", "--model", "fixture"}
	settings := []string{`model_provider="fixture"`, `model_providers.fixture.name="fixture"`,
		"model_providers.fixture.base_url=" + strconv.Quote(endpoint),
		`model_providers.fixture.wire_api="responses"`, `model_providers.fixture.requires_openai_auth=false`,
		`model_providers.fixture.stream_max_retries=1`, `model_providers.fixture.request_max_retries=0`,
		`analytics.enabled=false`, `feedback.enabled=false`,
		// These fixtures exercise model HTTP/SSE, excluding unrelated catalog
		// bootstrap whose detached Git can outlive native exec. Codex 0.159.2
		// supports this stable feature gate in codex-rs/features/src/lib.rs.
		`features.plugins=false`}
	for _, value := range append(settings, options...) {
		args = append(args, "-c", value)
	}
	args = append(args, "hello")
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = directory
	command.Env = []string{"PATH=/opt/homebrew/bin:/usr/bin:/bin", "CODEX_HOME=" + directory}
	return command
}

func requireNoNativeCodexPluginSync(t *testing.T, directory string) {
	t.Helper()
	t.Cleanup(func() {
		// In Codex 0.159.2, startup_sync.rs creates this lock before any
		// curated plugin Git/HTTP sync. Check before testing removes the home.
		path := filepath.Join(directory, ".tmp", "plugins.sync.lock")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("native model fixture started unrelated plugin catalog sync: stat %s: %v", path, err)
		}
	})
}

func nativeResponsesFixture(t *testing.T) (*openairesponses.Codec, protocolcore.Request) {
	t.Helper()
	codec, err := openairesponses.New(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := codec.DecodeCompatibleClientRequest([]byte(`{"model":"fixture","input":[{"type":"message","role":"user","content":"hello"}],"stream":true,"store":false}`))
	if err != nil {
		t.Fatal(err)
	}
	return codec, request
}

func TestInstalledCodexNativeFatalErrorsDoNotBecomeRetries(t *testing.T) {
	for _, code := range []string{"cyber_policy", "credit_balance_exhausted", "usage_not_included"} {
		for _, managed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/managed=%t", code, managed), func(t *testing.T) {
				var count atomic.Int32
				codec, request := nativeResponsesFixture(t)
				errorObject, _ := json.Marshal(map[string]string{"code": code, "message": "synthetic native fixture"})
				wire := []byte(fmt.Sprintf("data: {\"type\":\"response.failed\",\"response\":{\"error\":%s}}\n\n", errorObject))
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
						http.NotFound(w, r)
						return
					}
					count.Add(1)
					if !managed {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write(wire)
						return
					}
					stream, err := codec.NewProviderStream(request)
					if err != nil {
						t.Error(err)
						return
					}
					_, err = stream.Feed(r.Context(), wire)
					d := newHTTPDownstream(w, httpDownstreamOptions{ClientDialect: protocolspec.DialectOpenAIResponses})
					envelope, _ := exchange.NewResponseEnvelope(exchange.ResponseModeEventStream, 200, http.Header{"Content-Type": {"text/event-stream"}})
					if beginErr := d.Begin(r.Context(), envelope); beginErr != nil {
						t.Error(beginErr)
						return
					}
					if abortErr := d.Abort(r.Context(), exchange.FailureNotice{
						ReasonCode: exchange.ReasonProviderResponseFailed, ProviderErrorCode: protocolcore.ProviderErrorCodeOf(err),
						NativeError: protocolcore.NativeProviderErrorOf(err),
					}); abortErr != nil {
						t.Error(abortErr)
					}
				}))
				defer server.Close()
				output, err := nativeCodexFixtureCommand(t, server.URL).CombinedOutput()
				if err == nil || count.Load() != 1 || strings.Contains(string(output), "stream disconnected") {
					t.Fatalf("fatal error changed retry semantics: requests=%d err=%v output=%s", count.Load(), err, output)
				}
			})
		}
	}
}

func TestInstalledCodexProgressSurvivesNativeStream(t *testing.T) {
	for _, test := range []struct {
		managed bool
		event   string
	}{{false, "response.in_progress"}, {true, "response.in_progress"}, {false, "keepalive"}, {true, "keepalive"}} {
		managed := test.managed
		t.Run(fmt.Sprintf("managed=%t/event=%s", managed, test.event), func(t *testing.T) {
			var requests atomic.Int32
			codec, request := nativeResponsesFixture(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
					http.NotFound(w, r)
					return
				}
				requests.Add(1)
				stream, _ := codec.NewProviderStream(request)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				send := func(wire string) {
					data := []byte(wire)
					if managed {
						var err error
						data, err = stream.Feed(r.Context(), data)
						if err != nil {
							t.Error(err)
							return
						}
					}
					_, _ = w.Write(data)
					w.(http.Flusher).Flush()
				}
				for index := range 8 {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(50 * time.Millisecond):
					}
					if test.event == "keepalive" {
						send(fmt.Sprintf("event: keepalive\ndata: {\"type\":\"keepalive\",\"sequence_number\":%d}\n\n", index))
					} else {
						send("data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_fixture\"}}\n\n")
					}
				}
				send("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}}\n\n")
				send("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"created_at\":1,\"status\":\"completed\",\"model\":\"fixture\",\"output\":[]}}\n\n")
				if managed {
					terminal, err := stream.FinishDecoded(r.Context())
					if err != nil {
						t.Error(err)
						return
					}
					data, err := terminal.Approve()
					if err != nil {
						t.Error(err)
						return
					}
					_, _ = w.Write(data)
				}
			}))
			defer server.Close()
			output, err := nativeCodexFixtureCommand(t, server.URL, `model_providers.fixture.stream_idle_timeout_ms=180`, `model_providers.fixture.stream_max_retries=0`).CombinedOutput()
			if err != nil || requests.Load() != 1 {
				t.Fatalf("progressing stream disconnected: requests=%d err=%v output=%s", requests.Load(), err, output)
			}
		})
	}
}

func TestInstalledCodexErrorNotificationDoesNotCloseNativeStream(t *testing.T) {
	for _, test := range []struct{ managed, completed bool }{{false, true}, {true, true}, {true, false}} {
		t.Run(fmt.Sprintf("managed=%t/completed=%t", test.managed, test.completed), func(t *testing.T) {
			codec, request := nativeResponsesFixture(t)
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
					http.NotFound(w, r)
					return
				}
				count.Add(1)
				stream, _ := codec.NewProviderStream(request)
				downstream := newHTTPDownstream(w, httpDownstreamOptions{ClientDialect: protocolspec.DialectOpenAIResponses})
				envelope, _ := exchange.NewResponseEnvelope(exchange.ResponseModeEventStream, http.StatusOK, http.Header{"Content-Type": {"text/event-stream"}})
				if err := downstream.Begin(r.Context(), envelope); err != nil {
					t.Error(err)
					return
				}
				abort := func(err error) {
					if err := downstream.Abort(r.Context(), exchange.FailureNotice{
						ReasonCode:  exchange.ReasonProviderResponseFailed,
						NativeError: protocolcore.NativeProviderErrorOf(err),
					}); err != nil {
						t.Error(err)
					}
				}
				events := []string{
					`{"type":"error","code":"notification_fixture","message":"synthetic recoverable notification"}`,
					`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_fixture","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello"}]}}`,
					`{"type":"response.completed","response":{"id":"resp_fixture","created_at":1,"status":"completed","model":"fixture","output":[]}}`,
				}
				if !test.completed {
					events = events[:1]
				}
				for _, event := range events {
					wire := []byte("data: " + event + "\n\n")
					if test.managed {
						var err error
						wire, err = stream.Feed(r.Context(), wire)
						if err != nil {
							abort(err)
							return
						}
					}
					if _, err := downstream.Write(r.Context(), wire); err != nil {
						t.Error(err)
						return
					}
				}
				if test.managed {
					terminal, err := stream.FinishDecoded(r.Context())
					if err != nil {
						abort(err)
						return
					}
					release, err := terminal.Approve()
					if err != nil {
						t.Error(err)
						return
					}
					if _, err := downstream.Write(r.Context(), release); err != nil {
						t.Error(err)
					}
				}
			}))
			defer server.Close()
			output, err := nativeCodexFixtureCommand(t, server.URL, `model_providers.fixture.stream_max_retries=0`).CombinedOutput()
			if (err == nil) != test.completed || count.Load() != 1 || strings.Contains(string(output), "stream closed before response.completed") {
				t.Fatalf("error notification changed terminal semantics: requests=%d err=%v output=%s", count.Load(), err, output)
			}
			if !test.completed && !strings.Contains(string(output), "synthetic recoverable notification") {
				t.Fatalf("EOF lost the native error: %s", output)
			}
		})
	}
}
