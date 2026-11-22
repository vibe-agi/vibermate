package productruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clienttarget"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

// Opt-in: VIBERMATE_CLAUDE_ACCEPTANCE=/absolute/path/to/claude go test
// ./internal/productruntime -run TestInstalledClaudeOnResponses -count=1 -v
//
// These scenarios run an installed Claude Code through the real runtime: Start,
// published routing sets, the real Exchange and proxy, and a private provider
// transport that delivers api.openai.com requests to a local fake. Claude Code
// runs in first-party mode, as `vibermate run` launches it: no base URL, the
// proxy through HTTPS_PROXY, the runtime Root through NODE_EXTRA_CA_CERTS and
// the managed placeholder token. Decryption is decided by the routing set, so
// any build at or above the release floor runs here without recognition. The
// user's own Claude configuration, keychain and accounts are never opened.
const (
	claudeAcceptanceAccount = "synthetic-openai-key"
	claudeAcceptanceReply   = "synthetic reply through ViberMate"
)

// claudeAcceptanceFloor is the first Claude Code release these contracts were
// written against. Every later release has to keep them.
var claudeAcceptanceFloor = [3]int{2, 1, 329}

// claudeAcceptanceModels is the exact mapping the routing set uses until glob
// mappings exist: the three Claude model IDs 2.1.329 sends.
var claudeAcceptanceModels = []environment.ModelMapping{
	{RequestedModel: "claude-haiku-5-5", UpstreamModel: "gpt-6-luna"},
	{RequestedModel: "claude-opus-5-5", UpstreamModel: "gpt-6.2-astra"},
	{RequestedModel: "claude-sonnet-5-5", UpstreamModel: "gpt-6.2-sol"},
}

func TestInstalledClaudeOnResponses(t *testing.T) {
	binary := claudeAcceptanceBinary(t)
	t.Run("A0_baseline_on_anthropic_to_chat", func(t *testing.T) {
		testInstalledClaudeBaselineOnChat(t, binary)
	})
	// Each scenario of design section 15.5 is unskipped by the step that makes
	// it pass; "observe, then pin" scenarios record the first green run's
	// behaviour and assert it from then on.
	for _, scenario := range []struct{ id, name, step string }{
		{"A1", "plain_text_in_stream_json_and_json", "S13"},
		{"A2", "reasoning_round_trip_with_one_tool_call", "S13"},
		{"A3", "media_tool_results_and_deferred_tools", "S13"},
		{"A4", "parallel_calls_under_review", "S13"},
		{"A5", "glob_model_rows", "S15"},
		{"A6", "context_overflow_compacts", "S13"},
		{"A7", "rate_limit_retry_after", "S13"},
		{"A8", "insufficient_quota_stops", "S13"},
		{"A9", "failure_after_text_in_band", "S13"},
		{"A10", "silent_upstream_kept_alive", "S13"},
		{"A11", "chatgpt_early_envelope", "T6"},
		{"A12", "approval_longer_than_byte_watchdog", "S13"},
		{"A13", "count_tokens_answered_locally", "S16"},
		{"A14", "auto_mode_safeguards_unsupported", "S13"},
		{"A15", "web_search_refused_visibly", "S13"},
		{"A16", "invalid_encrypted_content_heals", "S13"},
		{"A17", "invalid_image_heals", "S13"},
	} {
		t.Run(scenario.id+"_"+scenario.name, func(t *testing.T) {
			t.Skip("needs " + scenario.step)
		})
	}
}

func claudeAcceptanceBinary(t *testing.T) string {
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
	release, supported, err := claudeReleaseAtLeast(string(version), claudeAcceptanceFloor)
	if err != nil {
		t.Fatal(err)
	}
	if !supported {
		t.Skipf("Claude Code %s predates the release floor %d.%d.%d", release, claudeAcceptanceFloor[0], claudeAcceptanceFloor[1], claudeAcceptanceFloor[2])
	}
	t.Logf("Claude Code %s", release)
	return binary
}

// claudeReleaseAtLeast reads `claude --version` output ("2.1.329 (Claude
// Code)") and reports whether the release is at least floor.
func claudeReleaseAtLeast(output string, floor [3]int) (string, bool, error) {
	fields := strings.Fields(output)
	if len(fields) < 1 {
		return "", false, fmt.Errorf("unexpected Claude Code version output %q", output)
	}
	release := fields[0]
	core, _, _ := strings.Cut(release, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return release, false, fmt.Errorf("unexpected Claude Code release %q", release)
	}
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil {
			return release, false, fmt.Errorf("unexpected Claude Code release %q", release)
		}
		if number != floor[index] {
			return release, number > floor[index], nil
		}
	}
	return release, true, nil
}

func TestClaudeReleaseFloor(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		output    string
		supported bool
	}{
		{"2.1.329 (Claude Code)\n", true},
		{"2.1.330 (Claude Code)", true},
		{"2.2.0 (Claude Code)", true},
		{"3.0.0", true},
		{"2.1.328 (Claude Code)", false},
		{"2.1.220 (Claude Code)", false},
		{"1.9.999 (Claude Code)", false},
	} {
		_, supported, err := claudeReleaseAtLeast(test.output, claudeAcceptanceFloor)
		if err != nil || supported != test.supported {
			t.Fatalf("claudeReleaseAtLeast(%q) = %v, %v", test.output, supported, err)
		}
	}
	for _, malformed := range []string{"", "Claude Code", "2.1 (Claude Code)", "2.x.329"} {
		if _, _, err := claudeReleaseAtLeast(malformed, claudeAcceptanceFloor); err == nil {
			t.Fatalf("claudeReleaseAtLeast(%q) accepted malformed output", malformed)
		}
	}
}

// claudeAcceptanceUpstream is the fake provider behind the private transport.
type claudeAcceptanceUpstream struct {
	t        *testing.T
	mu       sync.Mutex
	requests []claudeUpstreamRequest
	other    []string
}

type claudeUpstreamRequest struct {
	path          string
	authorization string
	headers       http.Header
	body          []byte
}

// serveChat answers OpenAI Chat completions. The built-in OpenAI API origin
// carries no base path, so the Chat codec path can arrive with or without
// the /v1 prefix; both are accepted.
func (upstream *claudeAcceptanceUpstream) serveChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || (r.URL.Path != "/chat/completions" && r.URL.Path != "/v1/chat/completions") {
		upstream.mu.Lock()
		upstream.other = append(upstream.other, r.Method+" "+r.URL.Path)
		upstream.mu.Unlock()
		http.Error(w, "fake Chat upstream serves only chat completions", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		upstream.t.Errorf("fake Chat upstream body: %v", err)
		return
	}
	upstream.mu.Lock()
	upstream.requests = append(upstream.requests, claudeUpstreamRequest{path: r.URL.Path, authorization: r.Header.Get("Authorization"), headers: r.Header.Clone(), body: body})
	upstream.mu.Unlock()
	var shape struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &shape); err != nil {
		upstream.t.Errorf("fake Chat upstream request: %v", err)
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	reply, err := json.Marshal(claudeAcceptanceReply)
	if err != nil {
		upstream.t.Error(err)
		return
	}
	if !shape.Stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"chatcmpl-acceptance","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16}}`, shape.Model, reply)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	chunk := func(delta, finish string) string {
		return fmt.Sprintf(`{"id":"chatcmpl-acceptance","object":"chat.completion.chunk","created":1,"model":%q,"choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`, shape.Model, delta, finish)
	}
	for _, payload := range []string{
		chunk(`{"role":"assistant","content":""}`, "null"),
		chunk(`{"content":`+string(reply)+`}`, "null"),
		chunk(`{}`, `"stop"`),
		fmt.Sprintf(`{"id":"chatcmpl-acceptance","object":"chat.completion.chunk","created":1,"model":%q,"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16}}`, shape.Model),
		"[DONE]",
	} {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	}
}

// claudeAcceptanceRun is one prepared runtime, routing set and capture.
type claudeAcceptanceRun struct {
	fixture  *task7Fixture
	upstream *claudeAcceptanceUpstream
	proxy    *url.URL
	home     string
	rootPEM  string
	grant    capturerun.LaunchGrant
}

func newClaudeAcceptanceRun(t *testing.T, ctx context.Context, binary string, backend environment.ClientProtocol, serve http.HandlerFunc, upstream *claudeAcceptanceUpstream) *claudeAcceptanceRun {
	t.Helper()
	directory := t.TempDir()
	f := task7Open(t, ctx, filepath.Join(directory, "data"))
	environmentID := environment.EnvironmentID("claude-openai")
	f.configureFor(task7Config{
		AccountID: claudeAcceptanceAccount, AccountName: "OpenAI API key", ProviderToken: claudeAcceptanceAccount,
		EndpointID: upstreamendpoint.OpenAIPlatformID,
		Environments: []task7EnvironmentConfig{{
			ID: environmentID, Name: "Claude Code · OpenAI", Mode: environment.ContentRecordingFull,
			ClientProtocol: environment.ClientProtocolAnthropicMessages, ClientOrigin: "https://api.anthropic.com",
			BackendProtocol: backend,
			Customize: func(aggregate *environment.Environment) {
				route := &aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
				route.ModelPolicy = environment.ModelPolicy{Revision: 1, Mode: environment.ModelModeMap, Mappings: slices.Clone(claudeAcceptanceModels)}
			},
		}},
	})
	proxy := f.bindWith(serve, task7BindOptions{
		ProviderHost:    "api.openai.com",
		ClientHostPorts: []task7HostPort{{RuleID: "claude.anthropic", Host: "api.anthropic.com", Port: 443}},
	})
	home := filepath.Join(directory, "client")
	task7Must(t, os.Mkdir(home, 0o700))
	rootPEM := filepath.Join(directory, "runtime-root.pem")
	task7Must(t, os.WriteFile(rootPEM, f.runtime.LocalRootCertificate().CertificatePEM(), 0o600))
	grant, err := f.runtime.captureRuns.Create(ctx, capturerun.CreateCommand{CWD: home, CanonicalExecutablePath: binary, ExecutableLabel: "claude", Lifetime: 5 * time.Minute, CatalogRevision: 1})
	task7Must(t, err)
	capture, err := captureidentity.New(captureidentity.KindManagedRun, grant.Run.ID)
	task7Must(t, err)
	profile, err := clienttarget.NewProfile("claude-code", clienttarget.EnvironmentFacts{})
	task7Must(t, err)
	_, _, err = f.runtime.assignments.CreateForLaunch(ctx, captureassignment.CreateCommand{Capture: capture, EnvironmentID: environmentID, Source: captureassignment.SourceLaunch, ClientProfile: profile})
	task7Must(t, err)
	proxy.User = url.UserPassword("capture", grant.ProxyCapability.Value())
	return &claudeAcceptanceRun{fixture: f, upstream: upstream, proxy: proxy, home: home, rootPEM: rootPEM, grant: grant}
}

// environment is the child environment of design section 15.5. No
// ANTHROPIC_BASE_URL: this is first-party mode, with its byte watchdog and
// first-byte deadline.
func (run *claudeAcceptanceRun) environment(extra ...string) []string {
	return append([]string{
		"PATH=/usr/bin:/bin", "HOME=" + run.home, "DISABLE_AUTOUPDATER=1",
		"HTTPS_PROXY=" + run.proxy.String(), "NODE_USE_ENV_PROXY=1",
		"NODE_EXTRA_CA_CERTS=" + run.rootPEM,
		"ANTHROPIC_AUTH_TOKEN=vibermate-local-proxy",
		"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=1",
	}, extra...)
}

func (run *claudeAcceptanceRun) claude(t *testing.T, ctx context.Context, binary string, arguments []string, extra ...string) (string, string, error) {
	t.Helper()
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Dir = run.home
	command.Env = run.environment(extra...)
	command.Stdin = strings.NewReader("")
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return stdout.String(), stderr.String(), err
}

// A0 proves the harness on the existing Anthropic to Chat pair before any new
// code: the proxy, Root delivery without recognition, the placeholder token,
// the managed account and the private transport.
func testInstalledClaudeBaselineOnChat(t *testing.T, binary string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	upstream := &claudeAcceptanceUpstream{t: t}
	run := newClaudeAcceptanceRun(t, ctx, binary, environment.ClientProtocolOpenAIChat, upstream.serveChat, upstream)
	// --output-format json sends display "omitted", which every release of
	// the Anthropic decoder accepts; stream-json waits for the display fix.
	stdout, stderr, err := run.claude(t, ctx, binary, []string{"-p", "Reply with the fixture text", "--output-format", "json"})
	var result struct {
		IsError    bool                       `json:"is_error"`
		Result     string                     `json:"result"`
		ModelUsage map[string]json.RawMessage `json:"modelUsage"`
	}
	if err != nil || json.Unmarshal([]byte(stdout), &result) != nil || result.IsError || !strings.Contains(result.Result, claudeAcceptanceReply) {
		t.Fatalf("Claude Code turn failed: err=%v stdout=%s stderr=%s", err, stdout, stderr)
	}
	for model := range result.ModelUsage {
		if !strings.HasPrefix(model, "claude-") {
			t.Errorf("Claude Code reported upstream model %q instead of the Claude model it asked for", model)
		}
	}
	task7Must(t, run.fixture.pipeline.Drain(ctx))
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	paths := make([]string, 0, len(upstream.requests))
	for _, request := range upstream.requests {
		paths = append(paths, request.path)
	}
	t.Logf("upstream requests=%v other=%v", paths, upstream.other)
	if len(upstream.requests) == 0 {
		t.Fatal("no Chat request reached the fake upstream")
	}
	mapped := map[string]bool{}
	for _, mapping := range claudeAcceptanceModels {
		mapped[mapping.UpstreamModel] = true
	}
	for index, request := range upstream.requests {
		if request.authorization != "Bearer "+claudeAcceptanceAccount {
			t.Errorf("request %d did not carry the managed account's key", index)
		}
		if request.headers.Get("Proxy-Authorization") != "" || request.headers.Get("X-Api-Key") != "" {
			t.Errorf("request %d carried a client credential header", index)
		}
		if strings.Contains(string(request.body), "vibermate-local-proxy") || strings.Contains(string(request.body), run.grant.ProxyCapability.Value()) {
			t.Errorf("request %d carried a capture secret", index)
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(request.body, &body); err != nil || !mapped[body.Model] {
			t.Errorf("request %d asked for model %q, not a mapped one (%v)", index, body.Model, err)
		}
	}
}
