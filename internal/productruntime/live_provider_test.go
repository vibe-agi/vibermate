package productruntime

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accesscredential"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

// Environment names for the opt-in live provider run. Without them this test
// skips loudly rather than passing: a live test that quietly does nothing when
// the service is absent is worse than no live test, because it reads as
// evidence in a report.
const (
	liveOriginEnvironment = "VIBERMATE_LIVE_PROVIDER_ORIGIN"
	liveKeyEnvironment    = "VIBERMATE_LIVE_PROVIDER_KEY"
	liveModelEnvironment  = "VIBERMATE_LIVE_PROVIDER_MODEL"
)

type liveProvider struct {
	origin string
	key    string
	model  string
}

func liveProviderFromEnvironment(t *testing.T) liveProvider {
	t.Helper()

	provider := liveProvider{
		origin: os.Getenv(liveOriginEnvironment),
		key:    os.Getenv(liveKeyEnvironment),
		model:  os.Getenv(liveModelEnvironment),
	}
	if provider.origin == "" || provider.key == "" || provider.model == "" {
		t.Skipf(
			"live provider run needs %s, %s, and %s",
			liveOriginEnvironment,
			liveKeyEnvironment,
			liveModelEnvironment,
		)
	}
	parsed, err := url.Parse(provider.origin)
	if err != nil || parsed.Host == "" {
		t.Fatalf("%s is not an origin: %q", liveOriginEnvironment, provider.origin)
	}
	return provider
}

// A client that speaks Anthropic Messages reaches a backend that speaks
// OpenAI chat completions, and gets a real answer back.
//
// Everything between those two sentences is the product: the frozen Access
// plan, the model policy, the credential resolved from a SecretRef and
// injected at the boundary, the dialect translation in both directions, and
// the outbound attempt recorded on the way. A fake provider proves the wiring;
// only a real one proves the wire.
func TestALiveProviderAnswersThroughTheWholePipeline(t *testing.T) {
	provider := liveProviderFromEnvironment(t)

	accessID, err := access.NewAccessID("access-live-provider")
	if err != nil {
		t.Fatal(err)
	}
	// The development file store is what the user asked to keep for now, in
	// place of the platform keychain. It is the same Store interface the
	// release backend implements, so what this test exercises above the
	// boundary is unchanged.
	// A real Offline Hold gate, because a live request has to acquire a real
	// egress lease before its first byte leaves.
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions(t, hostcontract.Desktop(), gate)
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
	options.Secrets = secrets
	runtime, err := Start(context.Background(), options)
	if err != nil {
		t.Fatalf("start ProductRuntime: %v", err)
	}
	defer shutdownRuntime(t, runtime)

	aggregate := liveAccessAggregate(t, accessID, provider)
	if write, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{ExpectedRevision: 0, Aggregate: aggregate},
	); err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, err)
	}

	// The key never appears in the plan, only its reference. This is where a
	// value enters, and it enters through the same door the window uses.
	value, err := secretstore.NewValue([]byte(provider.key))
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

	activePlan, err := runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve active Access plan: %v", err)
	}
	request, err := exchange.NewClientRequest(
		"exchange-live-provider",
		activePlan.IngressBinding(),
		runtimeAnthropicOperationEvidence(t),
		[]byte(`{
			"model":"claude-client-alias",
			"max_tokens":64,
			"messages":[{"role":"user","content":"Reply with the single word: ready"}]
		}`),
		exchange.ReplayGenerationCostOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	downstream := &runtimeDownstream{}
	result, err := runtime.ExchangeExecutor().Execute(ctx, request, downstream)
	if err != nil {
		t.Fatalf("execute a live Exchange: %v", err)
	}
	if result.Outcome != exchange.AttemptSucceeded ||
		!result.Ledger.DownstreamTerminal {
		t.Fatalf("live Exchange result = %+v", result)
	}

	// The client asked in Anthropic Messages, so the answer must come back in
	// Anthropic Messages no matter what the backend speaks.
	var answer struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(downstream.body.Bytes(), &answer); err != nil {
		t.Fatalf("decode the answer: %v body=%s", err, downstream.body.Bytes())
	}
	if answer.Type != "message" || answer.Role != "assistant" {
		t.Fatalf("answer shape = %+v", answer)
	}
	if len(answer.Content) == 0 || strings.TrimSpace(answer.Content[0].Text) == "" {
		t.Fatalf("the model said nothing: %s", downstream.body.Bytes())
	}
	if answer.Usage.InputTokens == 0 || answer.Usage.OutputTokens == 0 {
		t.Fatalf("usage was not carried back: %+v", answer.Usage)
	}
	t.Logf("live answer: %q", strings.TrimSpace(answer.Content[0].Text))

	// The outbound is on the record, with its real destination.
	attempts, err := runtime.EgressAttempts().List(
		context.Background(),
		egressaudit.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	for _, record := range attempts.Items {
		if record.Attempt.Purpose() == egressaudit.PurposeProviderAttempt &&
			strings.Contains(record.Attempt.TargetOrigin(), provider.origin) {
			reached = true
			if !record.Attempt.Terminal() ||
				record.Attempt.Outcome() != egressaudit.OutcomeCompleted {
				t.Fatalf("the recorded attempt did not complete: %+v", record)
			}
			if record.Attempt.BytesIn() == 0 {
				t.Fatalf("the recorded attempt received nothing: %+v", record)
			}
		}
	}
	if !reached {
		t.Fatalf("no outbound to %q was recorded: %+v", provider.origin, attempts.Items)
	}

	// Nothing that went out is allowed to come back in a record.
	encoded, err := json.Marshal(egressaudit.PageViewOf(attempts))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(provider.key)) {
		t.Fatal("the provider credential reached an audit record")
	}
}

func liveAccessAggregate(
	t *testing.T,
	accessID access.AccessID,
	provider liveProvider,
) access.Aggregate {
	t.Helper()

	aggregate := runtimeAccessAggregate(t, accessID, 1, "Live Provider")
	origin, err := access.NewProviderOrigin(provider.origin)
	if err != nil {
		t.Fatalf("construct a live ProviderOrigin: %v", err)
	}
	model, err := access.NewModelName(provider.model)
	if err != nil {
		t.Fatalf("construct a live model name: %v", err)
	}
	aggregate.ProviderTargets[0].Origin = origin
	aggregate.Profiles[0].DefaultModelPolicy.FixedModel = model
	return aggregate
}

// The same live backend, reached the way a real client reaches it: an HTTPS
// request to the agent endpoint's own origin, through the CONNECT proxy, with
// TLS terminated by the local root.
//
// This is the path the product actually ships. The Exchange test above proves
// the pipeline; this proves the pipeline is reachable from a client that knows
// nothing about vibermate beyond a proxy address and a trusted root.
func TestALiveProviderAnswersThroughTheProxy(t *testing.T) {
	provider := liveProviderFromEnvironment(t)

	accessID, err := access.NewAccessID("access-live-proxy")
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions(t, hostcontract.Desktop(), gate)
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
	options.Secrets = secrets
	runtime, err := Start(context.Background(), options)
	if err != nil {
		t.Fatalf("start ProductRuntime: %v", err)
	}
	defer shutdownRuntime(t, runtime)

	aggregate := liveAccessAggregate(t, accessID, provider)
	if write, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{ExpectedRevision: 0, Aggregate: aggregate},
	); err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, err)
	}
	value, err := secretstore.NewValue([]byte(provider.key))
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

	// The released default asks about an undecided host, so the endpoint this
	// client is about to reach has to be decided first.
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

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: runtime.ProxyHandler()}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()

	grant, err := runtime.CaptureRuns().Create(
		context.Background(),
		capturerun.CreateCommand{
			CWD:             t.TempDir(),
			ExecutablePath:  "/usr/bin/true",
			Lifetime:        time.Minute,
			CatalogRevision: 1,
		},
	)
	if err != nil {
		t.Fatalf("create CaptureRun: %v", err)
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(runtime.LocalRootCertificate().CertificatePEM()) {
		t.Fatal("the local root did not parse")
	}
	proxyURL := &url.URL{
		Scheme: "http",
		Host:   listener.Addr().String(),
		User: url.UserPassword(
			"capture",
			grant.ProxyCapability.Value(),
		),
	}
	client := &http.Client{
		Timeout: 90 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		},
	}
	body := `{"model":"claude-client-alias","max_tokens":64,` +
		`"messages":[{"role":"user","content":"Reply with the single word: ready"}]}`
	clientRequest, err := http.NewRequest(
		http.MethodPost,
		"https://api.anthropic.com/v1/messages",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	clientRequest.Header.Set("content-type", "application/json")
	clientRequest.Header.Set("anthropic-version", "2023-06-01")
	clientRequest.Header.Set("x-api-key", "client-key-never-forwarded")
	response, err := client.Do(clientRequest)
	if err != nil {
		t.Fatalf("a client request through the proxy failed: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	answered, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.StatusCode, answered)
	}
	var answer struct {
		Type    string `json:"type"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(answered, &answer); err != nil {
		t.Fatalf("decode the answer: %v body=%s", err, answered)
	}
	if answer.Type != "message" ||
		len(answer.Content) == 0 ||
		strings.TrimSpace(answer.Content[0].Text) == "" {
		t.Fatalf("answer = %s", answered)
	}
	t.Logf("live answer through the proxy: %q",
		strings.TrimSpace(answer.Content[0].Text))

	// The connection was decrypted as an agent endpoint, and it is on the
	// record as that rather than as an opaque tunnel.
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
		t.Fatalf("no decrypted connection was recorded: %+v", connections.Items)
	}
}
