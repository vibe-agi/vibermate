package runlauncher_test

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

// These tests run an installed Claude Code through the real launcher, with
// the launch grant a recognised build receives. They answer what a new Claude
// release needs to keep working: that it trusts the Root the recipe delivers,
// that the credential boundary holds at the wire, and that ViberMate's own
// Anthropic codec reads every request it sends, natively and for translation.
// Which builds are recognised is the issuer's decision and is not tested here.

const claudeNativeUserKey = "sk-ant-api03-synthetic-user-key-not-real"

type claudeNativeRequest struct {
	path, authorization, apiKey, proxyAuthorization, encoding, codecErr string
	body                                                                []byte
}

// The proxy authenticates every request, terminates nothing itself and relays
// a tunnel only to api.anthropic.com, where a local TLS server holding a leaf
// from the fixture root stands in for the provider. No request leaves the
// machine.
type claudeNativeFixture struct {
	t        *testing.T
	control  *controlFixture
	server   *httptest.Server
	upstream *httptest.Server
	managed  bool
	reply    string
	mu       sync.Mutex
	messages []claudeNativeRequest
	other    []string
	refused  []string
	tunnels  int
}

func claudeNativeBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("VIBERMATE_CLAUDE_ACCEPTANCE")
	if binary == "" {
		t.Skip("requires an explicitly selected local Claude Code binary")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("VIBERMATE_CLAUDE_ACCEPTANCE must be an absolute path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "--version")
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "DISABLE_AUTOUPDATER=1"}
	version, err := command.Output()
	if err != nil {
		t.Fatalf("Claude Code --version: %v", err)
	}
	t.Log(strings.TrimSpace(string(version)))
	return binary
}

func newClaudeNativeFixture(t *testing.T, binary, home string, command []string, fill byte, managed bool) *claudeNativeFixture {
	t.Helper()
	rootPath, leaf := nativeFixtureCertificates(t, "api.anthropic.com")
	f := &claudeNativeFixture{t: t, managed: managed, reply: "synthetic reply through ViberMate"}
	f.control = &controlFixture{t: t, executable: binary, workspace: home, rootPath: rootPath,
		credential: capability(fill), proxy: capability(fill + 1), run: capability(fill + 2),
		expectedCommand: command, expectedEnvironment: "work", authorities: []string{"api.anthropic.com:443"},
		recipe: clientadapter.LaunchNodeEnvProxy, recognition: clientadapter.RecognitionVerified,
		adapter: &capturecontrol.ClientLaunchAdapterView{ClientAdapterView: capturecontrol.ClientAdapterView{
			ID: "claude-code", Revision: 1, Version: "synthetic-recipe", CatalogRevision: 7,
			Source: capturecontrol.ClientAdapterSourcePrelaunchDigestCatalog, InstallShape: clientadapter.InstallNativeSingleBinary,
			LaunchRecipe: clientadapter.LaunchNodeEnvProxy,
		}, StreamingFallbackPolicy: clientadapter.StreamingFallbackClientDefault},
	}
	f.upstream = httptest.NewUnstartedServer(http.HandlerFunc(f.serveAnthropic))
	f.upstream.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}, MinVersion: tls.VersionTLS12}
	f.upstream.StartTLS()
	t.Cleanup(f.upstream.Close)
	f.server = httptest.NewServer(http.HandlerFunc(f.serveProxy))
	t.Cleanup(f.server.Close)
	return f
}

func (f *claudeNativeFixture) serveProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect && strings.HasPrefix(r.URL.Path, "/api/v1/") {
		if r.URL.Path == "/api/v1/capture-runs" && !f.managed {
			recorder := httptest.NewRecorder()
			f.control.ServeHTTP(recorder, r)
			var grant capturecontrol.LaunchGrant
			if err := json.Unmarshal(recorder.Body.Bytes(), &grant); err != nil {
				f.t.Error(err)
				return
			}
			grant.ManagedCredentialAuthorities = []string{}
			writeControlJSON(w, recorder.Code, grant)
			return
		}
		f.control.ServeHTTP(w, r)
		return
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("capture:"+f.control.proxy))
	if r.Header.Get("Proxy-Authorization") != want {
		w.WriteHeader(http.StatusProxyAuthRequired)
		return
	}
	if r.Method != http.MethodConnect || r.Host != f.control.authorities[0] {
		f.mu.Lock()
		f.refused = append(f.refused, r.Method+" "+r.Host)
		f.mu.Unlock()
		http.Error(w, "fixture has no upstream transport", http.StatusBadGateway)
		return
	}
	upstream, err := net.DialTimeout("tcp", f.upstream.Listener.Addr().String(), 5*time.Second)
	if err != nil {
		f.t.Errorf("fixture upstream: %v", err)
		http.Error(w, "fixture upstream unavailable", http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	conn, buffered, err := w.(http.Hijacker).Hijack()
	if err != nil {
		f.t.Error(err)
		return
	}
	defer conn.Close()
	f.mu.Lock()
	f.tunnels++
	f.mu.Unlock()
	deadline := time.Now().Add(90 * time.Second)
	_ = conn.SetDeadline(deadline)
	_ = upstream.SetDeadline(deadline)
	_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = buffered.Flush()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(upstream, buffered)
		if tcp, ok := upstream.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	_, _ = io.Copy(conn, upstream)
	_ = conn.Close()
	<-done
}

func (f *claudeNativeFixture) serveAnthropic(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		f.t.Errorf("fixture upstream body: %v", err)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/messages/count_tokens":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input_tokens":10}`)
		return
	case r.Method != http.MethodPost || r.URL.Path != "/v1/messages":
		f.mu.Lock()
		f.other = append(f.other, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		// Answer as ViberMate does for an operation it does not catalogue.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"reasonCode":"path_unsupported"}`+"\n")
		return
	}
	request := claudeNativeRequest{path: r.URL.Path, authorization: r.Header.Get("Authorization"), apiKey: r.Header.Get("X-Api-Key"),
		proxyAuthorization: r.Header.Get("Proxy-Authorization"), encoding: r.Header.Get("Content-Encoding"), body: body}
	if request.encoding == "" {
		codec, err := anthropicchat.New(anthropicchat.DefaultOptions())
		if err != nil {
			f.t.Error(err)
			return
		}
		var problems []string
		if _, _, err := codec.DecodeCompatibleClientRequest(body); err != nil {
			problems = append(problems, "Anthropic route: "+err.Error())
		}
		if _, _, err := codec.DecodeClientRequest(body); err != nil {
			problems = append(problems, "translated route: "+err.Error())
		}
		request.codecErr = strings.Join(problems, "; ")
	}
	f.mu.Lock()
	f.messages = append(f.messages, request)
	f.mu.Unlock()
	var shape struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &shape)
	if shape.Model == "" {
		shape.Model = "fixture"
	}
	w.Header().Set("Request-Id", "req_fixture")
	if !shape.Stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"msg_fixture","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":%q}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":2}}`, shape.Model, f.reply)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	events := []struct {
		name string
		data string
	}{
		{"message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_fixture","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":1}}}`, shape.Model)},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, f.reply)},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
	for _, event := range events {
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, event.data)
	}
}

func (f *claudeNativeFixture) launcher(binary, home string, stdout, stderr io.Writer) *runlauncher.Launcher {
	f.t.Helper()
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery:       fixedDiscovery{session: localdiscovery.Session{BaseURL: f.server.URL, ControlCredential: f.control.credential}},
		BaseEnvironment: claudeNativeEnvironment(home), Stdin: strings.NewReader(""), Stdout: stdout, Stderr: stderr,
		Getwd: func() (string, error) { return home, nil }, LookPath: func(string) (string, error) { return binary, nil },
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return launcher
}

// The user's own key and a NO_PROXY naming the provider are both present, so
// the test sees the launcher keep the user's credential or replace it, and
// keep the provider inside the capture either way.
func claudeNativeEnvironment(home string) []string {
	return []string{"PATH=/usr/bin:/bin", "TERM=dumb", "HOME=" + home, "DISABLE_AUTOUPDATER=1",
		"ANTHROPIC_API_KEY=" + claudeNativeUserKey,
		"NO_PROXY=api.anthropic.com,localhost"}
}

func TestInstalledClaudeCompletesATurnThroughLauncher(t *testing.T) {
	binary := claudeNativeBinary(t)
	for index, managed := range []bool{false, true} {
		name := "own_credential"
		if managed {
			name = "managed_auth"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			command := []string{"claude", "-p", "Reply with the fixture text", "--output-format", "json"}
			f := newClaudeNativeFixture(t, binary, home, command, byte(0x41+index*4), managed)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			var stdout, stderr strings.Builder
			code, err := f.launcher(binary, home, &stdout, &stderr).Run(ctx, runlauncher.LaunchRequest{EnvironmentID: "work", ManagedAuth: managed, Command: command})
			var result struct {
				IsError bool   `json:"is_error"`
				Result  string `json:"result"`
			}
			if err != nil || code != 0 || json.Unmarshal([]byte(stdout.String()), &result) != nil || result.IsError || !strings.Contains(result.Result, f.reply) {
				t.Fatalf("Claude Code turn failed: code=%d err=%v stdout=%s stderr=%s", code, err, stdout.String(), stderr.String())
			}
			f.assertWire(t)
		})
	}
}

func (f *claudeNativeFixture) assertWire(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Logf("tunnels=%d messages=%d other=%v refused=%v", f.tunnels, len(f.messages), f.other, f.refused)
	if len(f.messages) == 0 {
		t.Fatal("no /v1/messages request reached the provider")
	}
	for index, request := range f.messages {
		if request.encoding != "" {
			t.Errorf("request %d is %s-encoded; decode it here before checking it against the codec", index, request.encoding)
		}
		if request.codecErr != "" {
			t.Errorf("ViberMate's Anthropic codec refused request %d: %s", index, request.codecErr)
		}
		if request.proxyAuthorization != "" {
			t.Errorf("request %d carried the proxy credential to the provider", index)
		}
		for _, secret := range []string{f.control.credential, f.control.proxy, f.control.run} {
			if strings.Contains(request.authorization+request.apiKey+string(request.body), secret) {
				t.Errorf("a Capture secret reached the provider in request %d", index)
			}
		}
		if f.managed {
			if request.authorization != "Bearer vibermate-local-proxy" || request.apiKey != "" {
				t.Errorf("managed request %d did not carry only the placeholder: authorization=%q api key present=%v", index, request.authorization, request.apiKey != "")
			}
		} else if request.apiKey != claudeNativeUserKey || request.authorization != "" {
			t.Errorf("request %d did not carry the user's own key unchanged", index)
		}
	}
	f.control.mu.Lock()
	defer f.control.mu.Unlock()
	if f.control.createCalls != 1 || f.control.attachCalls != 1 || f.control.finishCalls != 1 {
		t.Fatalf("Capture lifecycle create=%d attach=%d finish=%d", f.control.createCalls, f.control.attachCalls, f.control.finishCalls)
	}
}
