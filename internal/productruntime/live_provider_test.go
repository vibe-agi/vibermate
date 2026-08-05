//go:build !vibermate_native_secrets

// These runs put a provider credential into a Store, so they use the
// development file backend and are built only when it is the selected one. A
// live run must not file a test secret in somebody's keychain.
package productruntime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
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
	"github.com/vibe-agi/vibermate/internal/activity"
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
		access.ApplicationProtocolHTTP1,
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
type liveProxyFixture struct {
	runtime *Runtime
	client  *http.Client
}

// newLiveProxyFixture is a runtime a real client can reach: a proxy on a real
// socket, a CaptureRun capability to authorize with, the local root in the
// client's trust store, and a rule that decides the endpoint the client is
// about to ask for.
func newLiveProxyFixture(
	t *testing.T,
	provider liveProvider,
	name string,
) liveProxyFixture {
	t.Helper()

	accessID, err := access.NewAccessID(name)
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
	t.Cleanup(func() { shutdownRuntime(t, runtime) })

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
	t.Cleanup(func() { _ = server.Close() })

	captureDirectory := t.TempDir()
	workspace, err := runtime.WorkspaceIdentity().ResolveLocal(
		context.Background(),
		captureDirectory,
	)
	if err != nil {
		t.Fatalf("resolve workspace identity: %v", err)
	}
	grant, err := runtime.CaptureRuns().Create(
		context.Background(),
		capturerun.CreateCommand{
			CWD:             captureDirectory,
			ExecutablePath:  "/usr/bin/true",
			Lifetime:        time.Minute,
			CatalogRevision: 1,
			Workspace:       workspace,
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
		User:   url.UserPassword("capture", grant.ProxyCapability.Value()),
	}
	return liveProxyFixture{
		runtime: runtime,
		client: &http.Client{
			Timeout: 90 * time.Second,
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
				TLSClientConfig: &tls.Config{
					RootCAs:    roots,
					MinVersion: tls.VersionTLS12,
				},
			},
		},
	}
}

func TestALiveProviderAnswersThroughTheProxy(t *testing.T) {
	provider := liveProviderFromEnvironment(t)

	fixture := newLiveProxyFixture(t, provider, "access-live-proxy")
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
	response, err := fixture.client.Do(clientRequest)
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
	connections, err := fixture.runtime.ConnectionEvents().List(
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

// Every agent client streams. A streamed answer is a different path through
// the whole product: a different response mode, a different event grammar on
// the wire, and a ledger that has to stay honest about what the client
// actually received.
func TestALiveProviderStreamsThroughTheProxy(t *testing.T) {
	provider := liveProviderFromEnvironment(t)

	fixture := newLiveProxyFixture(t, provider, "access-live-stream")
	body := `{"model":"claude-client-alias","max_tokens":64,"stream":true,` +
		`"messages":[{"role":"user","content":"Count: one two three"}]}`
	request, err := http.NewRequest(
		http.MethodPost,
		"https://api.anthropic.com/v1/messages",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("accept", "text/event-stream")
	response, err := fixture.client.Do(request)
	if err != nil {
		t.Fatalf("a streamed request through the proxy failed: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		answered, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
		t.Fatalf("status = %d body = %s", response.StatusCode, answered)
	}
	if mediaType := response.Header.Get("Content-Type"); !strings.HasPrefix(
		mediaType,
		"text/event-stream",
	) {
		t.Fatalf("a stream came back as %q", mediaType)
	}

	// The client asked in Anthropic Messages, so the events must be Anthropic
	// Messages events. A whole answer relabelled as a stream is not a stream.
	events, text, usage := readAnthropicStream(t, response.Body)
	for _, required := range []string{
		"message_start",
		"content_block_delta",
		"message_delta",
		"message_stop",
	} {
		if !events[required] {
			t.Fatalf("the stream had no %s: %v", required, events)
		}
	}
	if strings.TrimSpace(text) == "" {
		t.Fatal("the stream carried no text")
	}
	// Usage has to reach the client. A streamed answer that drops the token
	// counts makes every cost view downstream of it wrong.
	if usage.input == 0 || usage.output == 0 {
		t.Fatalf("usage did not reach the client: %+v", usage)
	}
	t.Logf("streamed answer: %q usage=%+v", strings.TrimSpace(text), usage)

	// The outbound reached a terminal with the bytes it actually carried.
	attempts, err := fixture.runtime.EgressAttempts().List(
		context.Background(),
		egressaudit.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	terminal := false
	for _, record := range attempts.Items {
		if record.Attempt.Purpose() != egressaudit.PurposeProviderAttempt {
			continue
		}
		terminal = true
		if !record.Attempt.Terminal() {
			t.Fatalf("a streamed outbound never reached a terminal: %+v", record)
		}
		if record.Attempt.BytesIn() == 0 {
			t.Fatalf("a streamed outbound recorded no bytes: %+v", record)
		}
	}
	if !terminal {
		t.Fatal("no provider attempt was recorded for the stream")
	}
}

type streamUsage struct {
	input  int
	output int
}

// readAnthropicStream reads the events a client would read.
func readAnthropicStream(
	t *testing.T,
	reader io.Reader,
) (map[string]bool, string, streamUsage) {
	t.Helper()

	events := map[string]bool{}
	var text strings.Builder
	var usage streamUsage
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			events[strings.TrimPrefix(line, "event: ")] = true
		case strings.HasPrefix(line, "data: "):
			var frame struct {
				Type  string `json:"type"`
				Delta struct {
					Text string `json:"text"`
				} `json:"delta"`
				Message struct {
					Usage struct {
						InputTokens  int `json:"input_tokens"`
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				} `json:"message"`
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			payload := strings.TrimPrefix(line, "data: ")
			if err := json.Unmarshal([]byte(payload), &frame); err != nil {
				t.Fatalf("a stream frame did not parse: %v frame=%s", err, payload)
			}
			text.WriteString(frame.Delta.Text)
			if frame.Message.Usage.InputTokens != 0 {
				usage.input = frame.Message.Usage.InputTokens
			}
			if frame.Usage.InputTokens != 0 {
				usage.input = frame.Usage.InputTokens
			}
			if frame.Message.Usage.OutputTokens != 0 {
				usage.output = frame.Message.Usage.OutputTokens
			}
			if frame.Usage.OutputTokens != 0 {
				usage.output = frame.Usage.OutputTokens
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read the stream: %v", err)
	}
	return events, text.String(), usage
}

// leavingDownstream is a client that goes away partway through a stream. It
// writes what it received and then refuses the rest, the way a closed socket
// does.
type leavingDownstream struct {
	mode        exchange.ResponseMode
	writesAllow int
	writes      int
	received    bytes.Buffer
	aborted     bool
}

func (downstream *leavingDownstream) Begin(
	_ context.Context,
	envelope exchange.ResponseEnvelope,
) error {
	downstream.mode = envelope.Mode()
	return nil
}

func (downstream *leavingDownstream) Write(
	_ context.Context,
	body []byte,
) (int, error) {
	if downstream.writes >= downstream.writesAllow {
		return 0, errors.New("client went away")
	}
	downstream.writes++
	return downstream.received.Write(body)
}

func (*leavingDownstream) Keepalive(context.Context) error { return nil }

func (downstream *leavingDownstream) Abort(
	context.Context,
	exchange.FailureNotice,
) error {
	downstream.aborted = true
	return nil
}

// A stream that ends early is where the ledger's two axes are easiest to
// confuse. Design 02 §10 keeps them apart on purpose: what the upstream was
// asked for and answered is a billing fact, and what the client actually
// understood is a different one. Charging for an answer nobody received is
// wrong; so is pretending an answer was received because it was paid for.
func TestALiveStreamThatEndsEarlyKeepsTheLedgerAxesApart(t *testing.T) {
	provider := liveProviderFromEnvironment(t)

	accessID, err := access.NewAccessID("access-live-early-end")
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

	activePlan, err := runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatal(err)
	}
	request, err := exchange.NewClientRequest(
		"exchange-live-early-end",
		activePlan.IngressBinding(),
		runtimeAnthropicOperationEvidence(t),
		[]byte(`{
			"model":"claude-client-alias",
			"max_tokens":256,
			"stream":true,
			"messages":[{"role":"user","content":"Count slowly from one to twenty."}]
		}`),
		exchange.ReplayGenerationCostOnly,
		access.ApplicationProtocolHTTP1,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// One write through is enough to have received something and not enough to
	// have received an answer.
	downstream := &leavingDownstream{writesAllow: 1}
	result, execErr := runtime.ExchangeExecutor().Execute(ctx, request, downstream)

	// The upstream axis says the provider was asked and answered. That is
	// true whatever the client did afterwards, and it is what a bill is
	// computed from.
	if result.Ledger.UpstreamSends == 0 {
		t.Fatalf("the upstream axis lost the send: %+v", result.Ledger)
	}
	if result.Ledger.UpstreamResponses == 0 {
		t.Fatalf("the upstream axis lost the answer: %+v", result.Ledger)
	}
	// The downstream axis says the client never received a whole answer.
	// Reporting a terminal here would tell a person they got something they
	// did not.
	if result.Ledger.DownstreamTerminal {
		t.Fatalf(
			"a client that went away was recorded as having received the answer: %+v",
			result.Ledger,
		)
	}
	if downstream.received.Len() == 0 {
		t.Fatal("the client received nothing at all, so nothing was interrupted")
	}
	if execErr == nil {
		t.Fatal("an interrupted stream reported success")
	}
	t.Logf(
		"ledger=%+v received=%d bytes reason=%q",
		result.Ledger,
		downstream.received.Len(),
		exchange.ReasonOf(execErr),
	)

	// The outbound still reaches a terminal. An attempt left open would leave
	// the audit unable to say what happened to bytes that did cross.
	attempts, err := runtime.EgressAttempts().List(
		context.Background(),
		egressaudit.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range attempts.Items {
		if record.Attempt.Purpose() != egressaudit.PurposeProviderAttempt {
			continue
		}
		found = true
		if !record.Attempt.Terminal() {
			t.Fatalf("an interrupted outbound stayed open: %+v", record)
		}
	}
	if !found {
		t.Fatal("no provider attempt was recorded")
	}
}

// The other client dialect, against a real model. Codex speaks OpenAI
// Responses; this is the translation the Anthropic runs never touch, and the
// only main translation path with no live evidence behind it.
//
// It goes through the Exchange rather than the proxy because the Codex binary
// on this machine is a release the catalog has no evidence for. The
// translation is what is under test here, not the launch.
func TestALiveProviderAnswersAResponsesClient(t *testing.T) {
	provider := liveProviderFromEnvironment(t)

	accessID, err := access.NewAccessID("access-live-responses")
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
	clientOrigin, err := access.NewClientOrigin("https://api.openai.com:443")
	if err != nil {
		t.Fatal(err)
	}
	aggregate.AgentEndpoint.ClientOrigin = clientOrigin
	aggregate.AgentEndpoint.ClientDialect = access.DialectOpenAIResponses
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
	activePlan, err := runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatal(err)
	}
	request, err := exchange.NewClientRequest(
		"exchange-live-responses",
		activePlan.IngressBinding(),
		runtimeResponsesOperationEvidence(t),
		[]byte(`{
			"model":"codex-client-alias",
			"input":[{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"Reply with the single word: ready"}]
			}],
			"store":false,
			"stream":false
		}`),
		exchange.ReplayGenerationCostOnly,
		access.ApplicationProtocolHTTP1,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	downstream := &runtimeDownstream{}
	result, err := runtime.ExchangeExecutor().Execute(ctx, request, downstream)
	if err != nil {
		t.Fatalf("execute a live Responses Exchange: %v", err)
	}
	if result.Outcome != exchange.AttemptSucceeded ||
		!result.Ledger.DownstreamTerminal {
		t.Fatalf("live Responses result = %+v", result)
	}

	// The client asked in Responses, so the answer comes back as a Responses
	// object with output items, not as a chat completion relabelled.
	var answer struct {
		Object string `json:"object"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(downstream.body.Bytes(), &answer); err != nil {
		t.Fatalf("decode the answer: %v body=%s", err, downstream.body.Bytes())
	}
	if answer.Object != "response" || answer.Status != "completed" {
		t.Fatalf("answer shape = %+v body=%s", answer, downstream.body.Bytes())
	}
	text := ""
	for _, item := range answer.Output {
		if item.Type != "message" || item.Role != "assistant" {
			continue
		}
		for _, block := range item.Content {
			if block.Type == "output_text" {
				text += block.Text
			}
		}
	}
	if strings.TrimSpace(text) == "" {
		t.Fatalf("the model said nothing: %s", downstream.body.Bytes())
	}
	if answer.Usage.InputTokens == 0 || answer.Usage.OutputTokens == 0 {
		t.Fatalf("usage was not carried back: %+v", answer.Usage)
	}
	t.Logf("live Responses answer: %q", strings.TrimSpace(text))
}

// A person pressing Ctrl-C mid-answer is the most ordinary way a stream ends.
// The proxy has to notice, stop reading from the provider, reach a terminal on
// the outbound, and record what actually happened — and it has to do all of
// that without leaving the runtime unable to shut down.
func TestALiveStreamAbandonedByItsClientIsRecordedAndDrained(t *testing.T) {
	provider := liveProviderFromEnvironment(t)

	fixture := newLiveProxyFixture(t, provider, "access-live-abandoned")
	body := `{"model":"claude-client-alias","max_tokens":512,"stream":true,` +
		`"messages":[{"role":"user","content":"Count slowly from one to fifty."}]}`
	request, err := http.NewRequest(
		http.MethodPost,
		"https://api.anthropic.com/v1/messages",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("accept", "text/event-stream")
	response, err := fixture.client.Do(request)
	if err != nil {
		t.Fatalf("a streamed request through the proxy failed: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		t.Fatalf("status = %d", response.StatusCode)
	}

	// Read enough to be sure the stream started, then hang up the way a
	// terminal does.
	buffer := make([]byte, 1)
	if _, err := io.ReadFull(response.Body, buffer); err != nil {
		t.Fatalf("the stream carried nothing before it was abandoned: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close the abandoned stream: %v", err)
	}

	// The outbound reaches a terminal. An attempt left open would leave the
	// audit unable to say what happened to bytes that did cross.
	deadline := time.Now().Add(30 * time.Second)
	for {
		attempts, err := fixture.runtime.EgressAttempts().List(
			context.Background(),
			egressaudit.PageRequest{Limit: 20},
		)
		if err != nil {
			t.Fatal(err)
		}
		settled := len(attempts.Items) > 0
		for _, record := range attempts.Items {
			if record.Attempt.Purpose() != egressaudit.PurposeProviderAttempt {
				continue
			}
			if !record.Attempt.Terminal() {
				settled = false
			}
		}
		if settled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("an abandoned stream left an outbound open: %+v", attempts.Items)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The Exchange is recorded as what it was. A stream nobody received must
	// not be reported as completed.
	records, err := fixture.runtime.Activities().List(
		context.Background(),
		activity.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Items) == 0 {
		t.Fatal("an abandoned stream left no Activity")
	}
	for _, record := range records.Items {
		if record.Kind != activity.KindExchangeCompleted {
			continue
		}
		if record.Status == activity.StatusSucceeded {
			t.Fatalf("an abandoned stream was recorded as succeeded: %+v", record)
		}
	}
	t.Logf("abandoned stream activity: %+v", records.Items[0])

	// And the runtime still drains. A hijacked connection that outlived its
	// client would make shutdown wait for a person who is gone.
	shutdownContext, cancel := context.WithTimeout(
		context.Background(),
		20*time.Second,
	)
	defer cancel()
	if err := fixture.runtime.Shutdown(shutdownContext); err != nil {
		t.Fatalf("shutdown after an abandoned stream: %v", err)
	}
}
