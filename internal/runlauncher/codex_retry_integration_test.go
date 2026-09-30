package runlauncher_test

import (
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

// Exercise the public launcher and a real Codex child, with only synthetic HTTP
// at the proxy boundary. Neither invocation can load the user's Codex home or
// send an inference request to a real provider.
func TestInstalledCodexRecoversTransientHTTPFailureThroughLauncher(t *testing.T) {
	binary := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if binary == "" {
		t.Skip("set VIBERMATE_CODEX_ACCEPTANCE to an absolute native Codex binary")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("native Codex binary must be absolute")
	}
	binary, err := filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, launched := range []bool{false, true} {
		name := "direct"
		if launched {
			name = "vibermate_run"
		}
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			root := httptest.NewTLSServer(http.NotFoundHandler())
			defer root.Close()
			rootPath := filepath.Join(directory, "root.pem")
			if err := os.WriteFile(rootPath, pem.EncodeToMemory(&pem.Block{
				Type: "CERTIFICATE", Bytes: root.Certificate().Raw,
			}), 0o600); err != nil {
				t.Fatal(err)
			}
			arguments := []string{"-c", `analytics.enabled=false`, "-c", `feedback.enabled=false`,
				"exec", "--json", "--skip-git-repo-check", "--ephemeral", "--model", "fixture", "hello"}
			control := &controlFixture{
				t: t, executable: binary, workspace: directory, rootPath: rootPath,
				credential: capability(0x61), proxy: capability(0x62), run: capability(0x63),
				expectedCommand: append([]string{"codex"}, arguments...),
				recipe:          clientadapter.LaunchCodexResponsesHTTP, recognition: clientadapter.RecognitionVerified,
				adapter: &capturecontrol.ClientLaunchAdapterView{
					ClientAdapterView: capturecontrol.ClientAdapterView{
						ID: "codex-cli", Revision: 1, Version: "synthetic-recipe", CatalogRevision: 7,
						Source:       capturecontrol.ClientAdapterSourcePrelaunchDigestCatalog,
						InstallShape: clientadapter.InstallNativeSingleBinary, LaunchRecipe: clientadapter.LaunchCodexResponsesHTTP,
					},
					StreamingFallbackPolicy: clientadapter.StreamingFallbackClientDefault,
				},
			}
			codec, err := openairesponses.New(openairesponses.DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			var upgrades atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/api/v1/") {
					control.ServeHTTP(w, r)
					return
				}
				if r.Method == http.MethodConnect && r.Host == "responses.fixture.invalid:80" {
					// The built-in provider in newer Codex versions negotiates a
					// WebSocket first. Match the real proxy's bounded 426 response,
					// so the native HTTP fallback is exercised rather than bypassed.
					conn, buffered, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
					_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
					_ = buffered.Flush()
					upgrade, err := http.ReadRequest(buffered.Reader)
					if err != nil {
						t.Error(err)
						return
					}
					defer upgrade.Body.Close()
					if upgrade.URL.Path != "/v1/responses" || !strings.EqualFold(upgrade.Header.Get("Upgrade"), "websocket") {
						t.Error("unexpected CONNECT request")
						return
					}
					upgrades.Add(1)
					_, _ = fmt.Fprint(buffered, "HTTP/1.1 426 Upgrade Required\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
					_ = buffered.Flush()
					return
				}
				if r.Method != http.MethodPost || r.Host != "responses.fixture.invalid" || r.URL.Path != "/v1/responses" {
					http.NotFound(w, r)
					return
				}
				if requests.Add(1) <= 6 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadGateway)
					_, _ = w.Write([]byte(`{"error":{"message":"ViberMate could not complete this request (provider_transport_failed).","type":"api_error","param":null,"code":"provider_transport_failed"}}`))
					return
				}
				body, readErr := io.ReadAll(io.LimitReader(r.Body, int64(openairesponses.DefaultOptions().MaxRequestBytes)+1))
				decoded, _, decodeErr := codec.DecodeCompatibleClientRequest(body)
				if readErr != nil || decodeErr != nil {
					t.Errorf("production codec rejected real Codex request: %v / %v", readErr, decodeErr)
					http.Error(w, "decode failed", http.StatusBadRequest)
					return
				}
				wire := []byte("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"recovered fixture\"}]}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"created_at\":1,\"status\":\"completed\",\"model\":\"fixture\",\"output\":[],\"usage\":{\"input_tokens\":5,\"input_tokens_details\":{\"cached_tokens\":2},\"output_tokens\":2,\"total_tokens\":7}}}\n\n")
				stream, streamErr := codec.NewProviderStream(decoded)
				if streamErr != nil {
					t.Error(streamErr)
					http.Error(w, "stream failed", 500)
					return
				}
				released, streamErr := stream.Feed(r.Context(), wire)
				if streamErr != nil {
					t.Error(streamErr)
					http.Error(w, "stream failed", 500)
					return
				}
				terminal, streamErr := stream.FinishDecoded(r.Context())
				if streamErr != nil {
					t.Error(streamErr)
					http.Error(w, "terminal failed", 500)
					return
				}
				usage := terminal.DecodedResponse().Usage
				if usage.InputUncached.Tokens != 3 || usage.CacheRead.Tokens != 2 || usage.Output.Tokens != 2 {
					t.Errorf("usage lost: %+v", usage)
				}
				final, streamErr := terminal.Approve()
				if streamErr != nil {
					t.Error(streamErr)
					http.Error(w, "release failed", 500)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write(append(released, final...))
			}))
			defer server.Close()
			baseEnvironment := []string{"PATH=/opt/homebrew/bin:/usr/bin:/bin", "CODEX_HOME=" + directory, "HOME=" + directory,
				"OPENAI_API_KEY=synthetic-fixture", "OPENAI_BASE_URL=http://responses.fixture.invalid/v1"}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var stdout, stderr bytes.Buffer
			if launched {
				launcher, err := runlauncher.New(runlauncher.Config{
					Discovery: fixedDiscovery{session: localdiscovery.Session{
						Schema: localdiscovery.Schema, InstanceID: capability(0x64), ProcessID: os.Getpid(),
						BaseURL: server.URL, ControlCredential: control.credential, ExpiresAt: time.Now().Add(time.Minute),
					}},
					BaseEnvironment: baseEnvironment, Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
					Getwd:    func() (string, error) { return directory, nil },
					LookPath: func(string) (string, error) { return binary, nil },
				})
				if err != nil {
					t.Fatal(err)
				}
				code, err := launcher.Run(ctx, transparentLaunch(control.expectedCommand...))
				if err != nil || code != 0 {
					t.Fatalf("Codex did not recover: requests=%d code=%d err=%v stdout=%s stderr=%s", requests.Load(), code, err, &stdout, &stderr)
				}
			} else {
				directArguments := append([]string{"-c", `openai_base_url="http://responses.fixture.invalid/v1"`,
					"-c", `features.responses_websockets=false`}, arguments...)
				command := exec.CommandContext(ctx, binary, directArguments...)
				command.Dir = directory
				command.Env = append(baseEnvironment, "HTTP_PROXY="+server.URL, "HTTPS_PROXY="+server.URL)
				command.Stdin = strings.NewReader("")
				command.WaitDelay = time.Second
				command.Stdout, command.Stderr = &stdout, &stderr
				if err := command.Run(); err != nil {
					t.Fatalf("native Codex did not recover: requests=%d err=%v stdout=%s stderr=%s", requests.Load(), err, &stdout, &stderr)
				}
			}
			if requests.Load() != 7 || !strings.Contains(stdout.String(), "recovered fixture") || !strings.Contains(stdout.String(), `"type":"turn.completed"`) {
				t.Fatalf("same prompt did not recover: requests=%d stdout=%s stderr=%s", requests.Load(), &stdout, &stderr)
			}
			if upgrades.Load() > 1 {
				t.Fatalf("WebSocket 426 did not select HTTP immediately: %d upgrades", upgrades.Load())
			}
			if strings.Contains(stdout.String(), "unrecognized configuration settings") ||
				strings.Contains(stderr.String(), "unrecognized configuration settings") {
				t.Fatalf("launcher changed the native retry configuration: stdout=%s stderr=%s", &stdout, &stderr)
			}
		})
	}
}
