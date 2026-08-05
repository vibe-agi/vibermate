//go:build !vibermate_native_secrets

// These runs put a provider credential into a Store, so they use the
// development file backend and are built only when it is the selected one. A
// live run must not file a test secret in somebody's keychain.
package desktophost_test

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accesscredential"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/desktophost"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

const capturedProcessHelperEnvironment = "GO_WANT_VIBERMATE_CAPTURE_HELPER"

const (
	liveOriginEnvironment = "VIBERMATE_LIVE_PROVIDER_ORIGIN"
	liveKeyEnvironment    = "VIBERMATE_LIVE_PROVIDER_KEY"
	liveModelEnvironment  = "VIBERMATE_LIVE_PROVIDER_MODEL"
	liveAgentEnvironment  = "VIBERMATE_LIVE_AGENT"
	localClaudeFixture    = "claude-local-fixture"
	localResponsesFixture = "codex-local-fixture"
	currentLoginCodex     = "codex-current-login"
)

// A real agent client, launched the way the product launches one, reaching a
// real model through vibermate.
//
// Everything else proves a part. This proves the thing: an installed Claude
// Code binary that knows nothing about vibermate beyond the environment the
// launcher gave it, talking to a backend that speaks a different dialect,
// through a TLS connection vibermate terminated with its own root.
func TestARealAgentClientReachesAModelThroughVibermate(t *testing.T) {
	origin := os.Getenv(liveOriginEnvironment)
	key := os.Getenv(liveKeyEnvironment)
	model := os.Getenv(liveModelEnvironment)
	agent := os.Getenv(liveAgentEnvironment)
	if agent == localClaudeFixture {
		provider := newLocalResponsesChatProvider(t)
		defer provider.Close()
		origin = provider.URL + "/v1"
		key = "local-provider-key"
		model = "local-provider-model"
		agent = "claude"
	}
	if origin == "" || key == "" || model == "" || agent == "" {
		t.Skipf(
			"live agent run needs %s, %s, %s, and %s",
			liveOriginEnvironment,
			liveKeyEnvironment,
			liveModelEnvironment,
			liveAgentEnvironment,
		)
	}
	agentPath, err := exec.LookPath(agent)
	if err != nil {
		t.Skipf("%s is not on PATH: %v", agent, err)
	}

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	options := liveHostOptions(t, paths, filepath.Join(root, "data"))
	host := startHost(t, options)
	defer shutdownHost(t, host)
	runtime := host.Runtime()

	accessID, err := access.NewAccessID("access-live-agent")
	if err != nil {
		t.Fatal(err)
	}
	aggregate := liveAgentAccess(t, accessID, origin, model)
	if write, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{ExpectedRevision: 0, Aggregate: aggregate},
	); err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, err)
	}
	value, err := secretstore.NewValue([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Credentials().ReplaceSecret(
		context.Background(),
		accesscredential.ReplaceCommand{
			AccessID:         accessID,
			ProfileID:        aggregate.Profiles[0].ID,
			CredentialID:     aggregate.AccountBindings[0].ID,
			ExpectedRevision: 0,
			Value:            value,
		},
	); err != nil {
		t.Fatalf("store the provider credential: %v", err)
	}
	rules := runtime.ConnectionRules()
	if _, err := rules.Replace(
		context.Background(),
		rules.Current().Revision,
		[]connectionpolicy.Rule{{
			ID:       "live.allow-agent-endpoint",
			Priority: 100,
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchExactHostPort("api.anthropic.com", 443),
		}},
		rules.Current().Default,
	); err != nil {
		t.Fatal(err)
	}

	sessionFile, err := localdiscovery.NewFile(
		paths.DiscoveryPath(),
		productruntime.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery: sessionFile,
		BaseEnvironment: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + t.TempDir(),
			// The client's own credential is never what reaches the backend.
			"ANTHROPIC_API_KEY=vibermate-live-placeholder",
		},
		Stdin:              strings.NewReader(""),
		Stdout:             &output,
		Stderr:             os.Stderr,
		HeartbeatInterval:  100 * time.Millisecond,
		ControlTimeout:     10 * time.Second,
		TerminationTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelRun := context.WithTimeout(
		context.Background(),
		4*time.Minute,
	)
	defer cancelRun()
	approvalContext, cancelApproval := context.WithCancel(runContext)
	approvalResult := approveRecognizedClientRoot(
		approvalContext,
		runtime.ToolApprovals(),
	)
	exitCode, err := launcher.Run(runContext, []string{
		agentPath,
		"--print",
		"--model",
		"claude-client-alias",
		"Reply with the single word: ready",
	})
	cancelApproval()
	approval := <-approvalResult
	if approval.err != nil {
		t.Fatalf("answer the recognized-client Root question: %v", approval.err)
	}
	if approval.allowed {
		t.Log("approved the recognized client to receive the local Root for this launch")
	}
	answered := output.String()
	if err != nil || exitCode != 0 {
		// A failed live run is worth explaining, so the records that would
		// otherwise be discarded with the runtime are printed first.
		records, listErr := runtime.Activities().List(
			context.Background(),
			activity.PageRequest{Limit: 20},
		)
		if listErr == nil {
			for _, record := range records.Items {
				t.Logf("activity: %+v diagnosis: %+v", record, record.Diagnosis)
			}
		}
		t.Fatalf(
			"a captured agent client failed: exit=%d err=%v output=%s",
			exitCode,
			err,
			answered,
		)
	}
	if strings.TrimSpace(answered) == "" {
		t.Fatal("the agent client printed nothing")
	}
	t.Logf("agent client output: %q", strings.TrimSpace(answered))

	// The connection was decrypted as an agent endpoint, and a provider
	// attempt reached the real backend.
	connections, err := runtime.ConnectionEvents().List(
		context.Background(),
		connectionevent.PageRequest{Limit: 50},
	)
	if err != nil {
		t.Fatal(err)
	}
	decrypted := false
	for _, record := range connections.Items {
		if record.RequestedHost == "api.anthropic.com" &&
			record.Decryption == connectionevent.DecryptionMITM {
			decrypted = true
		}
	}
	if !decrypted {
		t.Fatalf("no decrypted connection was recorded: %+v", connections.Items)
	}
	attempts, err := runtime.EgressAttempts().List(
		context.Background(),
		egressaudit.PageRequest{Limit: 50},
	)
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	for _, record := range attempts.Items {
		if record.Attempt.Purpose() == egressaudit.PurposeProviderAttempt &&
			strings.Contains(record.Attempt.TargetOrigin(), originHost(t, origin)) {
			reached = true
		}
	}
	if !reached {
		t.Fatalf("no provider attempt reached %q: %+v", origin, attempts.Items)
	}
}

type clientRootApprovalResult struct {
	allowed bool
	err     error
}

// approveRecognizedClientRoot plays the one explicit user decision made in the
// real Approval Center. It is deliberately test-only: a client known only by
// its publisher must never receive the Root through an automatic production
// bypass. A fully verified release does not ask, so cancellation without a
// pending question is a valid no-op.
func approveRecognizedClientRoot(
	ctx context.Context,
	approvals toolapproval.Controller,
) <-chan clientRootApprovalResult {
	result := make(chan clientRootApprovalResult, 1)
	go func() {
		defer close(result)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			page, err := approvals.ListApprovals(
				ctx,
				toolapproval.PageRequest{
					State: toolapproval.StatePending,
					Limit: 20,
				},
			)
			if err != nil {
				if ctx.Err() != nil {
					result <- clientRootApprovalResult{}
					return
				}
				result <- clientRootApprovalResult{err: err}
				return
			}
			for _, pending := range page.Items {
				if pending.Kind != string(toolapproval.KindClientRootAsk) {
					continue
				}
				_, err = approvals.DecideApproval(
					ctx,
					toolapproval.DecisionCommand{
						ApprovalID:       pending.ID,
						ExpectedRevision: pending.Revision,
						IdempotencyKey:   "live-agent-root-approval-0001",
						Decision:         toolapproval.DecisionAllowOnce,
						Scope:            toolapproval.ScopeRequest,
					},
				)
				result <- clientRootApprovalResult{
					allowed: err == nil,
					err:     err,
				}
				return
			}
			select {
			case <-ctx.Done():
				result <- clientRootApprovalResult{}
				return
			case <-ticker.C:
			}
		}
	}()
	return result
}

// TestACapturedProcessStreamsThroughTheCompleteLocalDataPlane keeps every
// network dependency local while exercising the same CaptureRun, proxy, TLS,
// Access, credential, codec, transport, stream and audit composition used by a
// real terminal client. It is intentionally separate from the credentialed
// live-provider tests above.
func TestACapturedProcessStreamsThroughTheCompleteLocalDataPlane(t *testing.T) {
	providerRequests := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		defer request.Body.Close()
		if request.Method != http.MethodPost ||
			request.URL.Path != "/v1/chat/completions" {
			t.Errorf("provider request = %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if request.Header.Get("Authorization") != "Bearer local-provider-key" ||
			request.Header.Get("x-api-key") != "" {
			t.Errorf("provider authorization was not replaced")
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			Model         string `json:"model"`
			MaxTokens     int    `json:"max_tokens"`
			Stream        bool   `json:"stream"`
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil ||
			body.Model != "local-provider-model" ||
			body.MaxTokens != 32 ||
			!body.Stream ||
			!body.StreamOptions.IncludeUsage ||
			len(body.Messages) != 1 ||
			body.Messages[0].Role != "user" ||
			body.Messages[0].Content != "stream locally" {
			t.Errorf("provider body = %+v, err=%v", body, err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		providerRequests <- struct{}{}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Error("fixture response writer cannot flush")
			return
		}
		for _, event := range []string{
			`{"id":"chatcmpl-local","object":"chat.completion.chunk","created":1,"model":"local-provider-model","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello "},"finish_reason":null}]}`,
			`{"id":"chatcmpl-local","object":"chat.completion.chunk","created":1,"model":"local-provider-model","choices":[{"index":0,"delta":{"content":"world"},"finish_reason":"stop"}]}`,
			`{"id":"chatcmpl-local","object":"chat.completion.chunk","created":1,"model":"local-provider-model","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		} {
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", event)
			flusher.Flush()
			time.Sleep(15 * time.Millisecond)
		}
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer provider.Close()

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	options := liveHostOptions(t, paths, filepath.Join(root, "data"))
	host := startHost(t, options)
	defer shutdownHost(t, host)
	runtime := host.Runtime()

	accessID, err := access.NewAccessID("access-local-captured-process")
	if err != nil {
		t.Fatal(err)
	}
	aggregate := liveAgentAccess(
		t,
		accessID,
		provider.URL+"/v1",
		"local-provider-model",
	)
	if write, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{ExpectedRevision: 0, Aggregate: aggregate},
	); err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, err)
	}
	value, err := secretstore.NewValue([]byte("local-provider-key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Credentials().ReplaceSecret(
		context.Background(),
		accesscredential.ReplaceCommand{
			AccessID:         accessID,
			ProfileID:        aggregate.Profiles[0].ID,
			CredentialID:     aggregate.AccountBindings[0].ID,
			ExpectedRevision: 0,
			Value:            value,
		},
	); err != nil {
		t.Fatalf("store provider credential: %v", err)
	}
	rules := runtime.ConnectionRules()
	if _, err := rules.Replace(
		context.Background(),
		rules.Current().Revision,
		[]connectionpolicy.Rule{{
			ID:       "local.allow-agent-endpoint",
			Priority: 100,
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchExactHostPort("api.anthropic.com", 443),
		}},
		rules.Current().Default,
	); err != nil {
		t.Fatal(err)
	}

	discovery, err := localdiscovery.NewFile(
		paths.DiscoveryPath(),
		productruntime.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery: discovery,
		BaseEnvironment: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + t.TempDir(),
			capturedProcessHelperEnvironment + "=1",
		},
		Stdin:              strings.NewReader(""),
		Stdout:             &output,
		Stderr:             &output,
		HeartbeatInterval:  50 * time.Millisecond,
		ControlTimeout:     5 * time.Second,
		TerminationTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	exitCode, err := launcher.Run(runContext, []string{
		executable,
		"-test.run=^TestCapturedProcessHelper$",
		"-test.count=1",
	})
	if err != nil || exitCode != 0 {
		t.Fatalf("captured helper exit=%d err=%v output=%s", exitCode, err, output.String())
	}
	select {
	case <-providerRequests:
	default:
		t.Fatal("captured process never reached the local provider")
	}
	for _, expected := range []string{
		`"type":"message_start"`,
		`"type":"content_block_delta"`,
		`"text":"Hello "`,
		`"text":"world"`,
		`"type":"message_stop"`,
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("captured stream omitted %s: %s", expected, output.String())
		}
	}

	connections, err := runtime.ConnectionEvents().List(
		context.Background(),
		connectionevent.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	decrypted := false
	for _, record := range connections.Items {
		if record.RequestedHost == "api.anthropic.com" &&
			record.Decryption == connectionevent.DecryptionMITM {
			decrypted = true
		}
	}
	if !decrypted {
		t.Fatalf("captured connection was not recorded as MITM: %+v", connections.Items)
	}
	attempts, err := runtime.EgressAttempts().List(
		context.Background(),
		egressaudit.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	providerAttempt := false
	for _, record := range attempts.Items {
		if record.Attempt.Purpose() == egressaudit.PurposeProviderAttempt &&
			record.Attempt.Terminal() &&
			record.Attempt.BytesOut() > 0 &&
			record.Attempt.BytesIn() > 0 {
			providerAttempt = true
		}
	}
	if !providerAttempt {
		t.Fatalf("captured provider attempt is incomplete: %+v", attempts.Items)
	}
}

func TestCapturedProcessHelper(t *testing.T) {
	if os.Getenv(capturedProcessHelperEnvironment) != "1" {
		return
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	// The fixture provider is an HTTP/1.1-only loopback service. Model that
	// client capability explicitly: production never downgrades a downstream
	// HTTP/2 request to make an H1-only local target appear compatible.
	transport.ForceAttemptHTTP2 = false
	// This synthetic process deliberately trusts only the local test proxy by
	// skipping verification. Production clients receive the scoped VibeMate Root
	// through their verified or explicitly recognized launch recipe.
	transport.TLSClientConfig = &tls.Config{ //nolint:gosec -- test-only local proxy
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // #nosec G402 -- test-only local proxy
		NextProtos:         []string{string(access.ApplicationProtocolHTTP1)},
	}
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	body := `{"model":"client-alias","max_tokens":32,"stream":true,` +
		`"messages":[{"role":"user","content":"stream locally"}]}`
	request, err := http.NewRequest(
		http.MethodPost,
		"https://api.anthropic.com/v1/messages",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set("accept", "text/event-stream")
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("x-api-key", "client-key-must-not-reach-provider")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		answered, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
		t.Fatalf("status=%d body=%s", response.StatusCode, answered)
	}
	if _, err := io.Copy(os.Stdout, io.LimitReader(response.Body, 1<<20)); err != nil {
		t.Fatal(err)
	}
}

func originHost(t *testing.T, origin string) string {
	t.Helper()

	parsed, err := url.Parse(origin)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Host
}

func liveHostOptions(
	t *testing.T,
	paths desktophost.Paths,
	dataDirectory string,
) desktophost.Options {
	t.Helper()

	runtimePaths, err := productruntime.NewRuntimePaths(dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	factory, err := hostsecret.NewDevelopmentFileFactory(
		filepath.Join(t.TempDir(), "secrets", "store.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	options := desktophost.DefaultOptions(paths, productruntime.Options{
		Paths:          runtimePaths,
		Host:           hostcontract.Desktop(),
		OfflineHold:    gate,
		Secrets:        secrets,
		Approvals:      toolapproval.DefaultConfig(),
		ExchangeHold:   exchange.DefaultHoldPolicy(),
		Clock:          productruntime.SystemClock{},
		InstanceIDs:    productruntime.NewCryptographicInstanceIDSource(),
		SecurityRandom: rand.Reader,
		Lifecycle:      productruntime.DefaultLifecycleOptions(),
	})
	options.CLIControlDiscoveryTTL = 10 * time.Minute
	options.AppSessionTTL = time.Hour
	options.CaptureRunLifetime = 5 * time.Minute
	options.ShutdownTimeout = 20 * time.Second
	return options
}

// The other agent client, and the other client dialect. Codex speaks OpenAI
// Responses; the backend here speaks OpenAI chat completions, so this is the
// translation the Anthropic runs never touch.
func TestARealResponsesClientReachesAModelThroughVibermate(t *testing.T) {
	origin := os.Getenv(liveOriginEnvironment)
	key := os.Getenv(liveKeyEnvironment)
	model := os.Getenv(liveModelEnvironment)
	if os.Getenv(liveAgentEnvironment) == localResponsesFixture {
		provider := newLocalResponsesChatProvider(t)
		defer provider.Close()
		origin = provider.URL + "/v1"
		key = "local-provider-key"
		model = "local-provider-model"
	}
	if origin == "" || key == "" || model == "" {
		t.Skipf(
			"live agent run needs %s, %s, and %s",
			liveOriginEnvironment,
			liveKeyEnvironment,
			liveModelEnvironment,
		)
	}
	codexPath, err := exec.LookPath("codex")
	if err != nil {
		t.Skipf("codex is not on PATH: %v", err)
	}
	if version, versionErr := exec.Command(codexPath, "--version").Output(); versionErr == nil {
		t.Logf("Responses client version: %s", strings.TrimSpace(string(version)))
	}

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	host := startHost(t, liveHostOptions(t, paths, filepath.Join(root, "data")))
	defer shutdownHost(t, host)
	runtime := host.Runtime()

	accessID, err := access.NewAccessID("access-live-responses")
	if err != nil {
		t.Fatal(err)
	}
	aggregate := liveAgentAccess(t, accessID, origin, model)
	// The client origin is OpenAI's, and the client speaks Responses.
	clientOrigin, err := access.NewClientOrigin("https://api.openai.com:443")
	if err != nil {
		t.Fatal(err)
	}
	aggregate.AgentEndpoint.ClientOrigin = clientOrigin
	aggregate.AgentEndpoint.ClientDialect = access.DialectOpenAIResponses
	aggregate, err = access.RefreshOriginalPassthrough(aggregate)
	if err != nil {
		t.Fatalf("refresh Core original passthrough profile: %v", err)
	}
	if write, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{ExpectedRevision: 0, Aggregate: aggregate},
	); err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, err)
	}
	value, err := secretstore.NewValue([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Credentials().ReplaceSecret(
		context.Background(),
		accesscredential.ReplaceCommand{
			AccessID:         accessID,
			ProfileID:        aggregate.Profiles[0].ID,
			CredentialID:     aggregate.AccountBindings[0].ID,
			ExpectedRevision: 0,
			Value:            value,
		},
	); err != nil {
		t.Fatalf("store the provider credential: %v", err)
	}
	rules := runtime.ConnectionRules()
	if _, err := rules.Replace(
		context.Background(),
		rules.Current().Revision,
		[]connectionpolicy.Rule{{
			ID:       "live.allow-responses-endpoint",
			Priority: 100,
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchExactHostPort("api.openai.com", 443),
		}},
		rules.Current().Default,
	); err != nil {
		t.Fatal(err)
	}

	sessionFile, err := localdiscovery.NewFile(
		paths.DiscoveryPath(),
		productruntime.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery: sessionFile,
		BaseEnvironment: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + t.TempDir(),
			"CODEX_API_KEY=vibermate-live-placeholder",
		},
		Stdin:              strings.NewReader("Reply with the single word: ready"),
		Stdout:             &output,
		Stderr:             os.Stderr,
		HeartbeatInterval:  100 * time.Millisecond,
		ControlTimeout:     10 * time.Second,
		TerminationTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelRun := context.WithTimeout(
		context.Background(),
		4*time.Minute,
	)
	defer cancelRun()
	approvalContext, cancelApproval := context.WithCancel(runContext)
	approvalResult := approveRecognizedClientRoot(
		approvalContext,
		runtime.ToolApprovals(),
	)
	exitCode, err := launcher.Run(runContext, []string{
		codexPath,
		"-c",
		`model_provider="vibermate-live"`,
		"-c",
		`model_providers.vibermate-live={name="VibeMate Live",` +
			`base_url="https://api.openai.com/v1",env_key="CODEX_API_KEY",` +
			`wire_api="responses",requires_openai_auth=false,` +
			`supports_websockets=false}`,
		"-a",
		"never",
		"-s",
		"read-only",
		"exec",
		"--skip-git-repo-check",
		"--ignore-user-config",
		"--color",
		"never",
		"--model",
		"gpt-client-alias",
		"-",
	})
	cancelApproval()
	approval := <-approvalResult
	if approval.err != nil {
		t.Fatalf("answer the recognized-client Root question: %v", approval.err)
	}
	if approval.allowed {
		t.Log("approved the recognized client to receive the local Root for this launch")
	}
	answered := output.String()
	if err != nil || exitCode != 0 {
		records, listErr := runtime.Activities().List(
			context.Background(),
			activity.PageRequest{Limit: 20},
		)
		if listErr == nil {
			for _, record := range records.Items {
				t.Logf("activity: %+v diagnosis: %+v", record, record.Diagnosis)
			}
		}
		t.Fatalf(
			"a captured Responses client failed: exit=%d err=%v output=%s",
			exitCode,
			err,
			answered,
		)
	}
	t.Logf("Responses client output: %q", strings.TrimSpace(answered))

	attempts, err := runtime.EgressAttempts().List(
		context.Background(),
		egressaudit.PageRequest{Limit: 50},
	)
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	for _, record := range attempts.Items {
		if record.Attempt.Purpose() == egressaudit.PurposeProviderAttempt &&
			strings.Contains(record.Attempt.TargetOrigin(), originHost(t, origin)) {
			reached = true
		}
	}
	if !reached {
		t.Fatalf("no provider attempt reached %q: %+v", origin, attempts.Items)
	}
}

// A current-login run is deliberately separate from the managed-provider
// fixture above. It preserves the Codex login already owned by the child and
// proves the zero-account original_passthrough Access against the exact client
// origin. The explicit opt-in may consume a small amount of the operator's
// existing Codex allowance and may contact only the first-party origin.
func TestARealResponsesClientUsesItsOwnLoginThroughOriginalRoute(t *testing.T) {
	if os.Getenv(liveAgentEnvironment) != currentLoginCodex {
		t.Skipf("current-login run needs %s=%s", liveAgentEnvironment, currentLoginCodex)
	}
	codexPath, err := exec.LookPath("codex")
	if err != nil {
		t.Skipf("codex is not on PATH: %v", err)
	}
	if statusErr := exec.Command(codexPath, "login", "status").Run(); statusErr != nil {
		t.Skipf("Codex has no usable current login: %v", statusErr)
	}
	if version, versionErr := exec.Command(codexPath, "--version").Output(); versionErr == nil {
		t.Logf("current-login Responses client version: %s", strings.TrimSpace(string(version)))
	}

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	host := startHost(t, liveHostOptions(t, paths, filepath.Join(root, "data")))
	defer shutdownHost(t, host)
	runtime := host.Runtime()

	accessID, err := access.NewAccessID("access-current-login-responses")
	if err != nil {
		t.Fatal(err)
	}
	aggregate := liveOriginalAccess(
		t,
		accessID,
		"https://chatgpt.com:443",
		access.DialectOpenAIResponses,
	)
	if len(aggregate.AccountBindings) != 0 ||
		len(aggregate.Profiles) != 1 ||
		aggregate.Profiles[0].ID != access.OriginalPassthroughProfileID() {
		t.Fatalf("current-login Access unexpectedly owns managed state: %+v", aggregate)
	}
	if write, writeErr := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{ExpectedRevision: 0, Aggregate: aggregate},
	); writeErr != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, writeErr)
	}
	rules := runtime.ConnectionRules()
	if _, err := rules.Replace(
		context.Background(),
		rules.Current().Revision,
		[]connectionpolicy.Rule{{
			ID:       "live.allow-current-login-responses-endpoint",
			Priority: 100,
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchExactHostPort("chatgpt.com", 443),
		}},
		rules.Current().Default,
	); err != nil {
		t.Fatal(err)
	}

	sessionFile, err := localdiscovery.NewFile(
		paths.DiscoveryPath(),
		productruntime.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	baseEnvironment := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		baseEnvironment = append(baseEnvironment, "CODEX_HOME="+codexHome)
	}
	var output strings.Builder
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery:          sessionFile,
		BaseEnvironment:    baseEnvironment,
		Stdin:              strings.NewReader("Reply with the single word: ready"),
		Stdout:             &output,
		Stderr:             os.Stderr,
		HeartbeatInterval:  100 * time.Millisecond,
		ControlTimeout:     10 * time.Second,
		TerminationTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelRun := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancelRun()
	approvalContext, cancelApproval := context.WithCancel(runContext)
	approvalResult := approveRecognizedClientRoot(
		approvalContext,
		runtime.ToolApprovals(),
	)
	exitCode, runErr := launcher.Run(runContext, []string{
		codexPath,
		"-a", "never",
		"-s", "read-only",
		"exec",
		"--skip-git-repo-check",
		"--ignore-user-config",
		"--color", "never",
		"-",
	})
	cancelApproval()
	approval := <-approvalResult
	if approval.err != nil {
		t.Fatalf("answer the recognized-client Root question: %v", approval.err)
	}
	if approval.allowed {
		t.Log("approved the recognized client to receive the local Root for this launch")
	}
	answered := strings.TrimSpace(output.String())
	if runErr != nil || exitCode != 0 {
		t.Fatalf(
			"a current-login Responses client failed: exit=%d err=%v output=%s",
			exitCode,
			runErr,
			answered,
		)
	}
	if !strings.Contains(strings.ToLower(answered), "ready") {
		t.Fatalf("current-login Responses output did not contain the proof marker: %q", answered)
	}
	t.Logf("current-login Responses client output: %q", answered)

	attempts, err := runtime.EgressAttempts().List(
		context.Background(),
		egressaudit.PageRequest{Limit: 50},
	)
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	for _, record := range attempts.Items {
		if record.Attempt.Purpose() == egressaudit.PurposeProviderAttempt &&
			record.Attempt.TargetOrigin() == "https://chatgpt.com:443" &&
			record.Attempt.Outcome() == egressaudit.OutcomeCompleted {
			reached = true
		}
	}
	if !reached {
		t.Fatalf("no successful original-origin attempt reached the exact client origin: %+v", attempts.Items)
	}
}

func newLocalResponsesChatProvider(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost ||
			request.URL.Path != "/v1/chat/completions" {
			http.NotFound(writer, request)
			return
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, (16<<20)+1))
		if err != nil || len(body) == 0 || len(body) > 16<<20 {
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Cache-Control", "no-cache")
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Error("fixture response writer cannot flush")
			return
		}
		for _, event := range []string{
			`{"id":"chatcmpl-local","object":"chat.completion.chunk","created":1,"model":"local-provider-model","choices":[{"index":0,"delta":{"role":"assistant","content":"ready"},"finish_reason":null}]}`,
			`{"id":"chatcmpl-local","object":"chat.completion.chunk","created":1,"model":"local-provider-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
		} {
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", event)
			flusher.Flush()
		}
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}
