package runlauncher_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
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
	"github.com/vibe-agi/vibermate/internal/protocolcore"
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
	// decoded is the production same-dialect decode of the body; it is the
	// zero value when the codec refused the request (codecErr says why).
	decoded protocolcore.Request
	// stream, tools and messagesDigest are read from the body as sent:
	// whether it asked for a stream, how many tools it defined, and a digest
	// of its messages, which a non-streaming resend of the same turn repeats.
	stream         bool
	tools          int
	messagesDigest string
	// failed marks the request the fixture failed after answering it.
	failed bool
}

// claudeGrantShape is the launch grant the fixture's control plane issues.
type claudeGrantShape int

const (
	// The signer grant a publisher-recognised build receives: no release
	// evidence, so the launcher decides the fallback from the signer view.
	grantRecognized claudeGrantShape = iota
	// An exact release's grant whose adapter leaves the fallback to the client.
	grantVerifiedClientDefault
	// An exact release's grant whose adapter leaves it to ViberMate.
	grantVerifiedCoreOwned
)

// claudeStreamFailure is how the fixture fails the first streaming
// /v1/messages request that defines tools, after it has started answering.
type claudeStreamFailure string

const (
	streamCompletes claudeStreamFailure = ""
	// The connection drops after a delta, as when a provider stream breaks.
	streamCut claudeStreamFailure = "cut"
	// The stream ends with the error event the proxy writes when an Upstream
	// stream fails after ViberMate has answered.
	streamInBandError claudeStreamFailure = "in_band_error"
)

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
	// environment is the launcher's base environment; a test may add to it
	// before it launches.
	environment []string
	mu          sync.Mutex
	failure     claudeStreamFailure
	failureTail []byte
	messages    []claudeNativeRequest
	other       []string
	refused     []string
	tunnels     int
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

func newClaudeNativeFixture(t *testing.T, binary, home string, command []string, fill byte, managed bool, shape claudeGrantShape) *claudeNativeFixture {
	t.Helper()
	rootPath, leaf := nativeFixtureCertificates(t, "api.anthropic.com")
	f := &claudeNativeFixture{t: t, managed: managed, reply: "synthetic reply through ViberMate", environment: claudeNativeEnvironment(home)}
	f.control = &controlFixture{t: t, executable: binary, workspace: home, rootPath: rootPath,
		credential: capability(fill), proxy: capability(fill + 1), run: capability(fill + 2),
		expectedCommand: command, expectedEnvironment: "work", authorities: []string{"api.anthropic.com:443"},
		recipe: clientadapter.LaunchNodeEnvProxy,
	}
	switch shape {
	case grantRecognized:
		f.control.recognition = clientadapter.RecognitionRecognized
		f.control.signer = &capturecontrol.ClientSignerView{
			ID: "claude-code", Revision: 1, CatalogRevision: 7,
			InstallShape: clientadapter.InstallNativeSingleBinary, LaunchRecipe: clientadapter.LaunchNodeEnvProxy,
			SignedPath: binary,
		}
	case grantVerifiedClientDefault, grantVerifiedCoreOwned:
		policy := clientadapter.StreamingFallbackClientDefault
		if shape == grantVerifiedCoreOwned {
			policy = clientadapter.StreamingFallbackCoreOwned
		}
		f.control.recognition = clientadapter.RecognitionVerified
		f.control.adapter = &capturecontrol.ClientLaunchAdapterView{ClientAdapterView: capturecontrol.ClientAdapterView{
			ID: "claude-code", Revision: 1, Version: "synthetic-recipe", CatalogRevision: 7,
			Source: capturecontrol.ClientAdapterSourcePrelaunchDigestCatalog, InstallShape: clientadapter.InstallNativeSingleBinary,
			LaunchRecipe: clientadapter.LaunchNodeEnvProxy,
		}, StreamingFallbackPolicy: policy}
	default:
		t.Fatalf("unknown grant shape %d", shape)
	}
	f.upstream = httptest.NewUnstartedServer(http.HandlerFunc(f.serveAnthropic))
	f.upstream.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}, MinVersion: tls.VersionTLS12}
	f.upstream.StartTLS()
	t.Cleanup(f.upstream.Close)
	f.server = httptest.NewServer(http.HandlerFunc(f.serveProxy))
	t.Cleanup(f.server.Close)
	return f
}

// failStream makes the fixture fail the first streaming request that defines
// tools. An in-band failure ends with the exact bytes the proxy writes, which
// api/samples/anthropic-stream-failure.sse pins to the production encoder.
func (f *claudeNativeFixture) failStream(failure claudeStreamFailure) {
	f.t.Helper()
	var tail []byte
	if failure == streamInBandError {
		var err error
		tail, err = os.ReadFile(filepath.Join("..", "..", "api", "samples", "anthropic-stream-failure.sse"))
		if err != nil || len(tail) == 0 {
			f.t.Fatalf("read the Anthropic stream failure sample: %v", err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failure, f.failureTail = failure, tail
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
		if decoded, _, err := codec.DecodeCompatibleClientRequest(body); err != nil {
			problems = append(problems, "Anthropic route: "+err.Error())
		} else {
			request.decoded = decoded
		}
		if _, _, err := codec.DecodeClientRequest(body); err != nil {
			problems = append(problems, "translated route: "+err.Error())
		}
		request.codecErr = strings.Join(problems, "; ")
	}
	var shape struct {
		Model    string            `json:"model"`
		Stream   bool              `json:"stream"`
		Messages json.RawMessage   `json:"messages"`
		Tools    []json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(body, &shape)
	if shape.Model == "" {
		shape.Model = "fixture"
	}
	request.stream, request.tools = shape.Stream, len(shape.Tools)
	var messages bytes.Buffer
	if json.Compact(&messages, shape.Messages) != nil {
		messages.Reset()
	}
	digest := sha256.Sum256(messages.Bytes())
	request.messagesDigest = hex.EncodeToString(digest[:])
	f.mu.Lock()
	failure, tail := f.failure, f.failureTail
	if failure != streamCompletes && shape.Stream && request.tools > 0 {
		request.failed = true
		f.failure = streamCompletes // only the first such request fails
	}
	f.messages = append(f.messages, request)
	f.mu.Unlock()
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
	if request.failed {
		// The stream breaks after ViberMate has answered: message_start, the
		// block and one delta reach the client, then the stream fails.
		for _, event := range events[:3] {
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, event.data)
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		if failure == streamCut {
			panic(http.ErrAbortHandler)
		}
		_, _ = w.Write(tail)
		return
	}
	for _, event := range events {
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, event.data)
	}
}

func (f *claudeNativeFixture) launcher(binary, home string, stdout, stderr io.Writer) *runlauncher.Launcher {
	f.t.Helper()
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery:       fixedDiscovery{session: localdiscovery.Session{BaseURL: f.server.URL, ControlCredential: f.control.credential}},
		BaseEnvironment: f.environment, Stdin: strings.NewReader(""), Stdout: stdout, Stderr: stderr,
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
	// stream-json is how Claude Code talks to an editor or SDK, and it asks
	// for the same thinking display ("updates") as an interactive session,
	// which the json output mode does not. Every new release is launched with
	// the signer grant a publisher-recognised build receives, and once with an
	// exact release's grant, so both launch paths keep installed coverage.
	for index, test := range []struct {
		name       string
		shape      claudeGrantShape
		managed    bool
		streamJSON bool
	}{
		{name: "recognized/own_credential", shape: grantRecognized},
		{name: "recognized/managed_auth", shape: grantRecognized, managed: true},
		{name: "recognized/stream_json", shape: grantRecognized, streamJSON: true},
		{name: "verified/own_credential", shape: grantVerifiedClientDefault},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			command := []string{"claude", "-p", "Reply with the fixture text", "--output-format", "json"}
			if test.streamJSON {
				command = []string{"claude", "-p", "Reply with the fixture text", "--output-format", "stream-json", "--verbose"}
			}
			f := newClaudeNativeFixture(t, binary, home, command, byte(0x41+index*4), test.managed, test.shape)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			var stdout, stderr strings.Builder
			code, err := f.launcher(binary, home, &stdout, &stderr).Run(ctx, runlauncher.LaunchRequest{EnvironmentID: "work", ManagedAuth: test.managed, Command: command})
			result, parsed := claudeNativeResult(stdout.String(), test.streamJSON)
			if err != nil || code != 0 || !parsed || result.IsError || !strings.Contains(result.Result, f.reply) {
				t.Fatalf("Claude Code turn failed: code=%d err=%v stdout=%s stderr=%s", code, err, stdout.String(), stderr.String())
			}
			f.assertWire(t)
		})
	}
}

type claudeNativeTurnResult struct {
	Type    string `json:"type"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
}

// claudeNativeResult reads the turn's result: the single JSON object of the
// json output mode, or the "result" event among stream-json's lines.
func claudeNativeResult(stdout string, streamJSON bool) (claudeNativeTurnResult, bool) {
	var result claudeNativeTurnResult
	if !streamJSON {
		return result, json.Unmarshal([]byte(stdout), &result) == nil
	}
	for _, line := range strings.Split(stdout, "\n") {
		var event claudeNativeTurnResult
		if json.Unmarshal([]byte(line), &event) == nil && event.Type == "result" {
			return event, true
		}
	}
	return result, false
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
