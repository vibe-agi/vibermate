package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/serverhost"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

// nativeFixtureChecks is the private assertion/cleanup boundary of the
// acceptance executable, not a product authority or dependency.
type nativeFixtureChecks interface {
	Helper()
	TempDir() string
	Cleanup(func())
	Fatal(...any)
	Fatalf(string, ...any)
	Error(...any)
	Logf(string, ...any)
}

type nativeProfileProbe struct {
	inject, argumentsRetained, resultRetained bool
	capacityDrop                              bool
	arguments                                 map[string]any
	result                                    map[string]any
	description                               string
	text, image                               string
}

func nativeProfileProbeFor(scenario string) (nativeProfileProbe, bool) {
	p := nativeProfileProbe{inject: true, argumentsRetained: true, resultRetained: true, arguments: map[string]any{"query": "synthetic"}, description: "Synthetic public tool", text: "synthetic result"}
	p.result = map[string]any{"content": []any{map[string]any{"type": "text", "text": p.text}}, "_meta": map[string]any{"proof": "synthetic-transport-private-marker"}}
	switch scenario {
	case "profile_full_text":
	case "profile_full_image":
		p.image = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
		p.result["content"] = []any{map[string]any{"type": "image", "mimeType": "image/png", "data": p.image}}
	case "profile_full_large", "profile_full_large_public":
		p.text = strings.Repeat("x", (2<<20)+4096)
		p.result["content"] = []any{map[string]any{"type": "text", "text": p.text}}
		if scenario == "profile_full_large" {
			// Preserve the original metadata-bearing wire as the bounded-loss
			// control: two conservative encoder peaks exceed the shared64MiB.
			p.resultRetained = false
			p.capacityDrop = true
		} else {
			// Same exact large public text, without metadata-removal encoding.
			delete(p.result, "_meta")
		}
	case "profile_full_unknown":
		p.inject = false
		p.argumentsRetained = false
		p.resultRetained = false
	case "profile_full_mutated":
		p.description = "Changed synthetic public tool"
		p.argumentsRetained = false
		p.resultRetained = false
	case "profile_full_proof":
		p.arguments["proof"] = "synthetic-sensitive-argument-marker"
		p.argumentsRetained = false
	case "profile_full_credential":
		p.arguments["credentialId"] = "synthetic-sensitive-argument-marker"
		p.argumentsRetained = false
	case "profile_full_result_credential":
		p.result["credentialId"] = "synthetic-sensitive-result-marker"
		p.resultRetained = false
	case "profile_full_result_structured":
		p.result["structuredContent"] = map[string]any{"credentialId": "synthetic-sensitive-result-marker"}
		p.resultRetained = false
	default:
		return nativeProfileProbe{}, false
	}
	return p, true
}

// Acceptance-only synthetic MODEL diagnostic options. Nil preserves the
// original one-text-response model_probe and every native MCP scenario.
// ObserveTerminal may only read fixture-owned state; it is not a product hook.
type nativeModelProbeOptions struct {
	Handler               http.Handler
	ThreadParameters      func(string) map[string]any
	ClientObservation     io.Writer
	ExpectedModelRequests int
	ObserveTerminal       func(context.Context, nativeModelProbeObservation)
	WorkLimit             time.Duration
	Review                bool
	ExpireReview          bool
	Profile               nativeModelProbeProfile
	BeforeTurn            func(context.Context, nativeModelProbeSetup) (func(), error)
	Auxiliary             *nativeModelAuxiliaryGuard
}

// Closed, synthetic MODEL-only configuration, never a compatibility grant.
type nativeModelProbeProfile uint8

const (
	nativeModelProbeNative nativeModelProbeProfile = iota
	nativeModelProbePluginsOn
	nativeModelProbeAPIKey
	nativeModelProbeAPIKeyMissing
	nativeModelProbeAPIKeyEmpty
)

func (profile nativeModelProbeProfile) usesAPIKey() bool {
	return profile == nativeModelProbeAPIKey || profile == nativeModelProbeAPIKeyMissing || profile == nativeModelProbeAPIKeyEmpty
}

type nativeModelProbeSetup struct {
	Runtime     *productruntime.Runtime
	Management  *nativeManagementFixture
	Environment environment.EnvironmentSnapshot
	AccountID   string
}

func modelProbeWorkLimit(scenario string, probe *nativeModelProbeOptions) (time.Duration, error) {
	if probe == nil {
		return 60 * time.Second, nil
	}
	if scenario != "model_probe" || probe.Handler == nil || probe.ThreadParameters == nil || probe.ObserveTerminal == nil || probe.ExpectedModelRequests < 0 || probe.ExpectedModelRequests > 2 || probe.Profile > nativeModelProbeAPIKeyEmpty || probe.ExpireReview && !probe.Review || probe.WorkLimit < 0 || probe.WorkLimit > 330*time.Second {
		return 0, errors.New("synthetic model diagnostic options are invalid or outside model_probe")
	}
	if probe.ExpectedModelRequests == 0 && probe.Profile != nativeModelProbeAPIKeyMissing && probe.Profile != nativeModelProbeAPIKeyEmpty {
		return 0, errors.New("zero MODEL requests requires a closed missing-key negative")
	}
	if (probe.Auxiliary != nil) != (probe.Profile == nativeModelProbePluginsOn) {
		return 0, errors.New("auxiliary guard is restricted to default-plugin MODEL profile")
	}
	if probe.WorkLimit == 0 {
		return 60 * time.Second, nil
	}
	return probe.WorkLimit, nil
}

type nativeModelProbeObservation struct {
	Runtime       *productruntime.Runtime
	Capture       captureidentity.Reference
	Assignment    captureassignment.Assignment
	AccountID     string
	Completed     bool
	Failure       string
	ModelRequests int
	DataDirectory string
}

func runNativeFixtureSlice(t nativeFixtureChecks, binary string, nodePaths ...string) {
	runNativeFixtureSliceWithModelProbe(t, binary, nil, nodePaths...)
}

func runNativeFixtureSliceWithModelProbe(t nativeFixtureChecks, binary string, modelProbe *nativeModelProbeOptions, nodePaths ...string) {
	directChoice, directErr := egressprofile.Explicit(egressprofile.Direct())
	if directErr != nil {
		t.Fatal(directErr)
	}
	scenario := "observe"
	if source, ok := t.(interface{ NativeScenario() string }); ok {
		scenario = source.NativeScenario()
	}
	workLimit, limitErr := modelProbeWorkLimit(scenario, modelProbe)
	if limitErr != nil {
		t.Fatal(limitErr)
	}
	streamObserve := scenario == "sse_observe"
	if streamObserve {
		scenario = "sse_reconnect"
	}
	knownDeny := scenario == "review_deny_known"
	sensitiveDeny := scenario == "review_sensitive_proof" || scenario == "review_sensitive_credential"
	sensitiveArgument := "proof"
	if scenario == "review_sensitive_credential" {
		sensitiveArgument = "credentialId"
	}
	if knownDeny || sensitiveDeny {
		scenario = "review_reject"
	}
	profileProbe, probingProfile := nativeProfileProbeFor(scenario)
	if !probingProfile && scenario != "observe" && scenario != "off" && scenario != "strict_external" && scenario != "review_reject" && scenario != "review_allow_once" && scenario != "session404_once" && scenario != "sse_reconnect" && scenario != "model_probe" {
		t.Fatal("native scenario not implemented")
	}
	parent := context.Background()
	if source, ok := t.(interface{ NativeContext() context.Context }); ok {
		parent = source.NativeContext()
	}
	if parent == nil {
		t.Fatal("native fixture parent context is nil")
	}
	// Default60s or explicitly bounded MODEL330s; callers reserve30s teardown.
	ctx, cancel := context.WithTimeout(parent, workLimit)
	defer cancel()
	var nativeCount, modelCount atomic.Int64
	var streams atomic.Int64
	resumed := make(chan struct{}, 1)
	freshReply, resumedReply := make(chan struct{}), make(chan struct{})
	var callID json.RawMessage
	callbackReplies := map[string]int{}
	var initializeCapabilities json.RawMessage
	defer func() {
		if observer, ok := t.(interface{ NativeCounters(int, int) }); ok {
			observer.NativeCounters(int(nativeCount.Load()), int(modelCount.Load()))
		}
	}()
	expectedVersion := ""
	if binary != "" {
		verifier, err := clientadapter.NewReleaseVerifier(clientadapter.BuiltInCatalog())
		if err != nil {
			t.Fatal(err)
		}
		detection, err := verifier.Verify(ctx, clientadapter.Request{Command: []string{"codex"}, ExecutablePath: binary, CWD: filepath.Dir(binary)})
		if err != nil || detection.Status != clientadapter.StatusVerified || detection.Evidence == nil {
			t.Fatal("exact compound package is not verified")
		}
		expectedVersion = detection.Evidence.Version
		if expected, ok := t.(interface{ NativeExpectedVersion() string }); ok && expected.NativeExpectedVersion() != expectedVersion {
			t.Fatal("verified client does not match case version")
		}
		entry := detection.CanonicalPath
		native := filepath.Join(filepath.Dir(entry), "../../codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex")
		if err := checkNativeWrapperResolution(entry, native); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(native)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.New()
		_, err = io.Copy(digest, file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("actual compound client=%s nativeSHA256=%x", expectedVersion, digest.Sum(nil))
	}
	directory := t.TempDir()
	var mu sync.Mutex
	var methods []string
	defer func() {
		if scenario == "review_allow_once" || probingProfile {
			mu.Lock()
			defer mu.Unlock()
			gets := streams.Load()
			if strings.Join(methods, ",") != "initialize,notifications/initialized,tools/list,tools/call" || gets < 0 || gets > 1 || nativeCount.Load() != 4+gets {
				t.Error("profile trace differs from four native POSTs plus optional fresh GET")
			}
			t.Logf("profile trace handshakePOST=3 toolsCallPOST=1 freshGET=%d native=%d model=%d", gets, nativeCount.Load(), modelCount.Load())
		}
		if scenario == "review_reject" {
			mu.Lock()
			defer mu.Unlock()
			gets := streams.Load()
			if strings.Join(methods, ",") != "initialize,notifications/initialized,tools/list" || gets < 0 || gets > 1 || nativeCount.Load() != 3+gets {
				t.Error("Deny trace differs from three handshake POSTs plus optional fresh GET")
			}
			t.Logf("Deny trace handshakePOST=3 toolsCallPOST=0 freshGET=%d native=%d model=%d", gets, nativeCount.Load(), modelCount.Load())
		}
		if scenario == "sse_reconnect" {
			mu.Lock()
			defer mu.Unlock()
			calls := 0
			for _, method := range methods {
				if method == "tools/call" {
					calls++
				}
			}
			t.Logf("stream trace client=%s capabilities=%s toolsCallPOST=%d GET=%d freshReplies=%d postReplies=%d resumedReplies=%d native=%d model=%d", expectedVersion, initializeCapabilities, calls, streams.Load(), callbackReplies["fresh-ping"], callbackReplies["post-ping"], callbackReplies["resumed-ping"], nativeCount.Load(), modelCount.Load())
		}
	}()
	upstream, roots := nativeTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelProbe != nil && modelProbe.Profile == nativeModelProbePluginsOn && r.URL.Path != "/backend-api/wham/accounts/check" {
			clone := r.Clone(r.Context())
			u := *r.URL
			u.Scheme = "https"
			u.Host = r.Host
			clone.URL = &u
			class := nativeModelAuxiliaryClass(clone)
			if r.TLS == nil || r.TLS.ServerName != "chatgpt.com" || class == "" || !modelProbe.Auxiliary.deny(class) {
				t.Error("default-plugin fixture unexpected or over-cap request")
				w.WriteHeader(400)
				return
			}
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-original-login" {
			t.Error("original authentication lost")
			w.WriteHeader(401)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/backend-api/wham/accounts/check" {
			// Exact pinned app-server auth_fixtures.rs routing response; this
			// is original-client control metadata, not a native tool outbound.
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"accounts":[{"id":"synthetic-account","workspace_backend_origin":"https://chatgpt.com","account_routing_override":"NO_CONSTRAINT"}]}`)
			return
		}
		nativeCount.Add(1)
		if r.Method == "GET" {
			streams.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			if scenario == "sse_reconnect" {
				if r.Header.Get("Last-Event-Id") == "" {
					// This no-ID callback belongs to the fresh listening GET.
					_, _ = io.WriteString(w, ": fresh stream\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"fresh-ping\",\"method\":\"ping\"}\n\n")
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				if r.Header.Get("Last-Event-Id") != "post-e1" || r.Header.Get("Mcp-Session-Id") != "private-session" {
					t.Error("SSE resumed without the actually observed event ID")
					return
				}
				select {
				case resumed <- struct{}{}:
				default:
				}
				_, _ = io.WriteString(w, "id: post-e2\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"resumed-ping\",\"method\":\"ping\"}\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-resumedReply:
				case <-r.Context().Done():
					return
				}
				mu.Lock()
				id := bytes.Clone(callID)
				mu.Unlock()
				_, _ = fmt.Fprintf(w, "id: post-e3\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"synthetic result\"}]}}\n\n", id)
				return
			}
			<-r.Context().Done()
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		methods = append(methods, request.Method)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch request.Method {
		case "initialize":
			var params struct {
				Capabilities json.RawMessage `json:"capabilities"`
				ClientInfo   struct {
					Version string `json:"version"`
				} `json:"clientInfo"`
			}
			if json.Unmarshal(request.Params, &params) != nil || params.ClientInfo.Version != expectedVersion {
				t.Error("actual initialize version differs from verified native package")
				w.WriteHeader(400)
				return
			}
			if scenario == "sse_reconnect" {
				// These launches declare experimentalApi, not any host MCP
				// extensions. Observe the actual client negotiation, not the
				// broader capabilities in the module's synthetic pin fixtures.
				var caps map[string]map[string]json.RawMessage
				if json.Unmarshal(params.Capabilities, &caps) != nil || len(caps) != 1 || len(caps["elicitation"]) != 2 || string(caps["elicitation"]["form"]) != "{}" || string(caps["elicitation"]["url"]) != "{}" {
					t.Error("actual initialized callback capabilities changed")
					w.WriteHeader(400)
					return
				}
			}
			w.Header().Set("Mcp-Session-Id", "private-session")
			mu.Lock()
			initializeCapabilities = bytes.Clone(params.Capabilities)
			mu.Unlock()
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "synthetic", "version": "1"}}
			if scenario == "sse_reconnect" {
				result.(map[string]any)["capabilities"] = map[string]any{"tools": map[string]any{"listChanged": true}}
			}
		case "notifications/initialized":
			w.WriteHeader(202)
			return
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "synthetic_read", "description": "Synthetic public tool", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}}}
			if probingProfile {
				result.(map[string]any)["tools"].([]any)[0].(map[string]any)["description"] = profileProbe.description
			}
		case "tools/call":
			if scenario == "sse_reconnect" {
				mu.Lock()
				callID = bytes.Clone(request.ID)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "id: post-e1\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"post-ping\",\"method\":\"ping\"}\n\n")
				w.(http.Flusher).Flush()
				return
			}
			if scenario == "session404_once" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "synthetic result"}}}
			if probingProfile {
				result = profileProbe.result
			}
		case "":
			var id string
			if scenario != "sse_reconnect" || json.Unmarshal(request.ID, &id) != nil || (id != "fresh-ping" && id != "post-ping" && id != "resumed-ping") || string(request.Result) != "{}" {
				t.Error("unexpected callback response")
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			callbackReplies[id]++
			count := callbackReplies[id]
			mu.Unlock()
			if count != 1 {
				t.Error("callback replied more than once")
			} else if id == "fresh-ping" {
				close(freshReply)
			} else if id == "resumed-ping" {
				close(resumedReply)
			}
			w.WriteHeader(202)
			return
		default:
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	transport, err := newNativeFixtureTransport(upstream.Listener.Addr().String(), roots)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.transport.CloseIdleConnections()
	if modelProbe != nil && modelProbe.Profile == nativeModelProbePluginsOn {
		transport.modelAuxiliary = true
		transport.modelGuard = modelProbe.Auxiliary
		defer func() {
			_, _, failed := modelProbe.Auxiliary.snapshot()
			if failed {
				t.Error("default-plugin auxiliary guard failed")
			}
		}()
	}
	paths, err := productruntime.NewRuntimePaths(filepath.Join(directory, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	factory, err := hostsecret.NewServerFileFactory(paths.DataDirectory())
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := factory.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	blind := &nativeDeniedBlindDialer{gate: gate}
	options := serverhost.DefaultOptions(productruntime.Options{BlindTunnels: blind, OriginalTransport: transport, Paths: paths, Host: hostcontract.Server(), OfflineHold: gate, Secrets: secrets, Approvals: toolapproval.DefaultConfig(), ExchangeHold: exchange.DefaultHoldPolicy(), Clock: productruntime.SystemClock{}, InstanceIDs: productruntime.NewCryptographicInstanceIDSource(), SecurityRandom: rand.Reader, Lifecycle: productruntime.DefaultLifecycleOptions()})
	if modelProbe != nil && modelProbe.ExpireReview {
		options.Runtime.Approvals.DecisionTimeout = 25 * time.Second
	}
	if scenario == "review_allow_once" || scenario == "sse_reconnect" && !streamObserve || knownDeny || sensitiveDeny || probingProfile && profileProbe.inject {
		profiles, available := nativeAcceptanceContentProfiles()
		if !available {
			t.Fatal("synthetic_content_profiles_required")
		}
		options.Runtime.ContentProfiles = profiles
	}
	options.ListenAddress = "127.0.0.1:0"
	host, err := serverhost.Start(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		shutdown, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		if err := host.Shutdown(shutdown); err != nil {
			t.Error(err)
		}
	}()
	if _, err = host.Runtime().ConnectionRules().Replace(ctx, host.Runtime().ConnectionRules().Current().Revision, []connectionpolicy.Rule{{ID: "synthetic.native", Priority: 1, Decision: connectionpolicy.DecisionAllow, Match: connectionpolicy.MatchExactHostPort("chatgpt.com", 443)}}, connectionpolicy.ModeDenyUnknown); err != nil {
		t.Fatal(err)
	}
	origin, _ := originidentity.ParseProviderOrigin("https://chatgpt.com")
	e := environment.Environment{ID: "native.acceptance", Name: "Private native acceptance", State: environment.StateActive, Revision: 1, ContentRecording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, PolicySet: &environment.PolicySet{ToolMode: environment.ToolPolicyObserve}, ClientToolAccess: &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{ID: "native.acceptance.route", Revision: 1, TargetOrigin: origin, Path: environment.ClientToolMCPPath, CredentialOwner: environment.ClientToolCredentialOriginal, Contract: environment.ClientToolContractCodexLegacy, EgressProfile: directChoice}}}
	if probingProfile || modelProbe != nil {
		e.ContentRecording = environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1}
	}
	if scenario == "off" {
		e.ClientToolAccess = nil
	}
	if scenario == "strict_external" {
		e.PolicySet.ToolMode = environment.ToolPolicyStrict
	}
	if scenario == "review_reject" || scenario == "review_allow_once" || scenario == "sse_reconnect" && !streamObserve {
		e.PolicySet.ToolMode = environment.ToolPolicyReview
	}
	if modelProbe != nil && modelProbe.Review {
		e.PolicySet.ToolMode = environment.ToolPolicyReview
	}
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCount.Add(1)
		if scenario == "model_probe" {
			if r.Method != "POST" || r.ProtoMajor != 1 || r.TLS != nil || r.Header.Get("Authorization") != "Bearer synthetic-managed-model" {
				t.Error("model request did not use selected managed account")
				w.WriteHeader(400)
				return
			}
			if modelProbe != nil {
				modelProbe.Handler.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"synthetic model result\"}]}}\n\n"+
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"created_at\":1,\"status\":\"completed\",\"model\":\"gpt-5.4\",\"output\":[],\"usage\":{\"input_tokens\":3,\"input_tokens_details\":{\"cached_tokens\":0},\"output_tokens\":2,\"total_tokens\":5}}}\n\n")
			return
		}
		t.Error("direct tool-call slice unexpectedly requested a model")
		w.WriteHeader(503)
	}))
	defer modelServer.Close()
	modelOrigin, err := originidentity.ParseProviderOrigin(modelServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	capabilities := []protocolspec.ProviderCapability{protocolspec.ProviderCapabilityMessages, protocolspec.ProviderCapabilityStreaming, protocolspec.ProviderCapabilityToolCalls}
	endpoint, err := host.Runtime().UpstreamEndpoints().Create(ctx, upstreamendpoint.CreateCommand{ID: "native.synthetic.model", DisplayName: "Private model", Origin: modelOrigin, RealmID: "native.synthetic.model", BackendProtocols: []string{string(environment.ClientProtocolOpenAIResponses)}, Capabilities: capabilities, Drivers: []providerauth.DriverRef{providerauth.StaticHeaderDriverRef()}})
	if err != nil {
		t.Fatal(err)
	}
	material, err := providerauth.NewMaterial("synthetic-managed-model", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	encoded, err := material.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	secret, err := secretstore.NewValue(encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	account, err := host.Runtime().ProviderAccounts().Create(ctx, provideraccount.CreateCommand{ID: "native.synthetic.account", DisplayName: "Private managed model", UpstreamEndpointID: endpoint.ID, Driver: providerauth.StaticHeaderDriverRef(), Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	clientOrigin, _ := originidentity.ParseClientOrigin("https://chatgpt.com")
	e.ClientEndpoints = []environment.ClientEndpoint{{ID: "native.synthetic.client", Revision: 1, ClientOrigin: clientOrigin, ProtocolPlans: []environment.ClientProtocolPlan{{ID: "native.synthetic.protocol", Revision: 1, ClientProtocol: environment.ClientProtocolOpenAIResponses, ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: "native.synthetic.adapter", Revision: 1}, EgressProfile: directChoice, Destination: environment.DestinationPlan{Kind: environment.DestinationKindUpstream, Upstream: &environment.UpstreamPlan{DefaultRouteID: "native.synthetic.route", RouteSet: environment.RouteSet{ID: "native.synthetic.routes", Revision: 1, CandidateRouteIDs: []environment.UpstreamRouteID{"native.synthetic.route"}}, Routes: []environment.UpstreamRoute{{ID: "native.synthetic.route", Revision: 1, ProviderTarget: environment.ProviderTarget{ID: endpoint.ID.String(), Revision: environment.Revision(endpoint.Revision), Origin: endpoint.Origin, RealmID: endpoint.RealmID, Capabilities: endpoint.Capabilities}, BackendProtocol: string(environment.ClientProtocolOpenAIResponses), AccountPolicy: environment.RouteAccountPolicy{Revision: 1, Mode: environment.AccountSelectionFixed, FixedAccountID: account.Account.ID.String(), Accounts: []environment.RouteAccountReference{{ID: account.Account.ID.String(), Revision: environment.Revision(account.Account.Revision), DisplayName: account.Account.DisplayName}}}, ModelPolicy: environment.ModelPolicy{Revision: 1, Mode: environment.ModelModePassthrough}, WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue}}}}}}}}
	if modelProbe != nil && modelProbe.Profile.usesAPIKey() {
		// Canonical flow is an existing protocol identity, not a dial grant.
		// The actual prelaunch target remains private chatgpt.com/v1.
		encoded, _ := json.Marshal(e.ClientEndpoints[0])
		var generic environment.ClientEndpoint
		if err := json.Unmarshal(encoded, &generic); err != nil {
			t.Fatal("synthetic generic flow copy failed")
		}
		generic.ID = "native.synthetic.api.client"
		generic.ClientOrigin, _ = originidentity.ParseClientOrigin("https://api.openai.com")
		plan := &generic.ProtocolPlans[0]
		plan.ID = "native.synthetic.api.protocol"
		plan.ClientAdapterPolicy.ID = "native.synthetic.api.adapter"
		plan.Destination.Upstream.DefaultRouteID = "native.synthetic.api.route"
		plan.Destination.Upstream.RouteSet.ID = "native.synthetic.api.routes"
		plan.Destination.Upstream.RouteSet.CandidateRouteIDs = []environment.UpstreamRouteID{"native.synthetic.api.route"}
		plan.Destination.Upstream.Routes[0].ID = "native.synthetic.api.route"
		e.ClientEndpoints = append(e.ClientEndpoints, generic)
	}
	draft, err := host.Runtime().Environments().SaveDraft(ctx, environment.DraftCommand{Candidate: e})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := host.Runtime().Environments().Preview(ctx, e.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Runtime().Environments().Publish(ctx, preview); err != nil {
		t.Fatal(err)
	}
	// Exercise the real authenticated proxy before allowing a native process.
	proxyServer := httptest.NewServer(host.Runtime().ProxyHandler())
	defer proxyServer.Close()
	management := newNativeManagementFixture(ctx, t, host)
	proxyFor := func(id environment.EnvironmentID) *http.Client {
		t.Helper()
		grant := management.createContainmentCapture(ctx, id)
		proxyURL, _ := url.Parse(proxyServer.URL)
		proxyURL.User = url.UserPassword(grant.ProxyUsername, grant.ProxyPassword)
		tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: x509.NewCertPool()}}
		t.Cleanup(tr.CloseIdleConnections)
		return &http.Client{Transport: tr, Timeout: 3 * time.Second}
	}
	privateClient := proxyFor(e.ID)
	deniedTargets := []string{"https://auth.openai.com/", "https://unlisted.invalid/", "https://chatgpt.com:444/"}
	if modelProbe != nil && modelProbe.Profile.usesAPIKey() {
		deniedTargets = append(deniedTargets, "https://api.openai.com/")
	}
	for _, target := range deniedTargets {
		response, requestErr := privateClient.Get(target)
		if response != nil {
			_ = response.Body.Close()
		}
		if requestErr == nil {
			t.Fatal("unlisted CONNECT allowed")
		}
		if blind.attempts.Load() != 0 {
			t.Fatal("unlisted CONNECT reached blind dial")
		}
	}
	// An untrusted client cannot turn a failed native MITM into a blind retry.
	response, requestErr := privateClient.Get("https://chatgpt.com/backend-api/ps/mcp")
	if response != nil {
		_ = response.Body.Close()
	}
	if requestErr == nil || blind.attempts.Load() != 0 {
		t.Fatal("missing-root path escaped native interception")
	}
	blindClient := proxyFor(environment.SystemTransparentID)
	if _, err = host.Runtime().ConnectionRules().Replace(ctx, host.Runtime().ConnectionRules().Current().Revision, []connectionpolicy.Rule{{ID: "synthetic.blind.probe", Priority: 1, Decision: connectionpolicy.DecisionAllow, Match: connectionpolicy.MatchExactHostPort("blind.fixture.invalid", 443)}}, connectionpolicy.ModeDenyUnknown); err != nil {
		t.Fatal(err)
	}
	response, requestErr = blindClient.Get("https://blind.fixture.invalid/")
	if response != nil {
		_ = response.Body.Close()
	}
	if requestErr == nil || blind.attempts.Load() != 1 {
		t.Fatal("deliberate blind fallback did not hit fail-closed boundary")
	}
	if _, err = host.Runtime().ConnectionRules().Replace(ctx, host.Runtime().ConnectionRules().Current().Revision, nil, connectionpolicy.ModeDenyUnknown); err != nil {
		t.Fatal(err)
	}
	response, requestErr = blindClient.Get("https://chatgpt.com/")
	if response != nil {
		_ = response.Body.Close()
	}
	if requestErr == nil || blind.attempts.Load() != 1 {
		t.Fatal("OFF containment reached blind dial")
	}
	mu.Lock()
	before := len(methods)
	mu.Unlock()
	if before != 0 {
		t.Fatal("containment probes reached native upstream")
	}
	if binary == "" {
		return
	}
	var clientRules []connectionpolicy.Rule
	if scenario != "off" {
		clientRules = []connectionpolicy.Rule{{ID: "synthetic.native", Priority: 1, Decision: connectionpolicy.DecisionAllow, Match: connectionpolicy.MatchExactHostPort("chatgpt.com", 443)}}
	}
	if _, err = host.Runtime().ConnectionRules().Replace(ctx, host.Runtime().ConnectionRules().Current().Revision, clientRules, connectionpolicy.ModeDenyUnknown); err != nil {
		t.Fatal(err)
	}
	user, err := host.Runtime().RuntimeUsers().Create(ctx, runtimeuser.CreateCommand{Username: "synthetic", Password: []byte("synthetic-private-password")})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := runtimeuser.NewPolicy(true, nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Runtime().RuntimeUsers().SetPolicy(ctx, user.ID, policy); err != nil {
		t.Fatal(err)
	}
	target, err := serverconnection.ParseTarget("https://" + host.Status().ListenAddress)
	if err != nil {
		t.Fatal(err)
	}
	remote := runlauncher.RemoteConfig{Target: target, StateDirectory: filepath.Join(directory, "remote"), DisplayName: "Private acceptance", Clock: productruntime.SystemClock{}, Random: rand.Reader}
	if _, err = runlauncher.LoginRemote(ctx, runlauncher.RemoteLoginRequest{Config: remote, Username: "synthetic", Password: []byte("synthetic-private-password")}); err != nil {
		t.Fatal(err)
	}
	clientHome := filepath.Join(directory, "client")
	if err = os.Mkdir(clientHome, 0700); err != nil {
		t.Fatal(err)
	}
	jwt := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_plan_type":"plus","chatgpt_user_id":"synthetic-user","chatgpt_account_id":"synthetic-account"}}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte("signature"))
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "tokens": map[string]any{"id_token": jwt, "access_token": "synthetic-original-login", "refresh_token": "synthetic-never-refresh", "account_id": "synthetic-account"}, "last_refresh": time.Now().UTC()})
	if err = os.WriteFile(filepath.Join(clientHome, "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	clientConfig := "cli_auth_credentials_store = \"file\"\n[features]\napps = true\n[analytics]\nenabled = false\n[feedback]\nenabled = false\n"
	if modelProbe != nil {
		clientConfig = "cli_auth_credentials_store = \"file\"\n[features]\napps = false\nplugins = false\n[analytics]\nenabled = false\n[feedback]\nenabled = false\n"
		if modelProbe.Profile == nativeModelProbePluginsOn {
			clientConfig = "cli_auth_credentials_store = \"file\"\n[features]\napps = false\nplugins = true\nremote_plugin = true\n[analytics]\nenabled = false\n[feedback]\nenabled = false\n"
		}
	}
	if err = os.WriteFile(filepath.Join(clientHome, "config.toml"), []byte(clientConfig), 0600); err != nil {
		t.Fatal(err)
	}
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	defer ownNativeFixturePipes(ctx, inputReader, inputWriter, outputReader, outputWriter)()
	defer inputWriter.Close()
	defer outputReader.Close()
	stderr := newBoundedBuffer(64 << 10)
	defer func() {
		if _, overflow := stderr.snapshot(); overflow {
			t.Error("client diagnostics exceeded fixture bound")
		}
	}()
	nodePath := os.Getenv("VIBERMATE_PINNED_NODE")
	if len(nodePaths) > 0 {
		nodePath = nodePaths[0]
	}
	if nodePath == "" {
		nodePath, _ = exec.LookPath("node")
	}
	if !filepath.IsAbs(nodePath) {
		t.Fatal("absolute pinned Node required")
	}
	baseEnvironment := []string{"PATH=" + filepath.Dir(nodePath) + ":/usr/bin:/bin", "HOME=" + clientHome, "CODEX_HOME=" + clientHome}
	if modelProbe != nil && modelProbe.Profile.usesAPIKey() {
		baseEnvironment = append(baseEnvironment, "OPENAI_BASE_URL=https://chatgpt.com/v1")
	}
	if modelProbe != nil && modelProbe.Profile == nativeModelProbeAPIKey {
		baseEnvironment = append(baseEnvironment, "VIBERMATE_SYNTHETIC_MODEL_API_KEY=synthetic-original-model-api-key")
	}
	if modelProbe != nil && modelProbe.Profile == nativeModelProbeAPIKeyEmpty {
		baseEnvironment = append(baseEnvironment, "VIBERMATE_SYNTHETIC_MODEL_API_KEY=")
	}
	launcher, err := runlauncher.New(runlauncher.Config{Remote: &remote, BaseEnvironment: baseEnvironment, Stdin: inputReader, Stdout: outputWriter, Stderr: stderr, Getwd: func() (string, error) { return clientHome, nil }, LookPath: func(string) (string, error) { return binary, nil }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	if modelProbe != nil && modelProbe.Profile == nativeModelProbePluginsOn {
		transport.modelCaseActive.Store(true)
	}
	go func() {
		code, err := launcher.Run(ctx, runlauncher.LaunchRequest{EnvironmentID: e.ID, Command: []string{"codex", "app-server", "--listen", "stdio://"}})
		if err == nil && code != 0 {
			err = fmt.Errorf("client exit %d", code)
		}
		done <- err
		_ = outputWriter.Close()
		_ = inputReader.Close()
	}()
	defer func() {
		cancel()
		_ = inputWriter.Close()
		_ = outputReader.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("launcher cleanup timeout")
		}
	}()
	var observedOutput io.Reader = outputReader
	if modelProbe != nil && modelProbe.ClientObservation != nil {
		observedOutput = io.TeeReader(outputReader, modelProbe.ClientObservation)
	}
	frames, frameFinished := nativeClientFrames(ctx, observedOutput)
	defer func() {
		cancel()
		_ = outputReader.Close()
		if err := <-frameFinished; err != nil {
			t.Error("bounded client output failed")
		}
	}()
	send := func(id int, method string, params any) {
		t.Helper()
		if err := json.NewEncoder(inputWriter).Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			select {
			case launchErr := <-done:
				done <- launchErr
				t.Fatalf("launcher failed before client input: %v", launchErr)
			default:
			}
			t.Fatalf("launcher input failed: %v", err)
		}
	}
	read := func(id int, expectedDenial ...bool) map[string]json.RawMessage {
		t.Helper()
		for value := range frames {
			if string(value["id"]) == fmt.Sprint(id) {
				if value["error"] != nil && !(len(expectedDenial) > 0 && expectedDenial[0]) {
					page, _ := host.Runtime().CaptureRunReader().ListRuns(ctx, capturerun.PageRequest{Limit: 10})
					for _, run := range page.Items {
						t.Logf("capture recognition=%s adapterPresent=%t", run.Recognition, run.Adapter != nil)
					}
					connections, _ := host.Runtime().ConnectionEvents().List(ctx, connectionevent.PageRequest{Limit: 20, LatestPerConnection: true})
					for _, event := range connections.Items {
						t.Logf("connection host=%s phase=%s outcome=%s error=%s decryption=%s", event.RequestedHost, event.Phase, event.Outcome, event.ErrorClass, event.Decryption)
					}
					t.Fatalf("client rejected request%d: %s", id, value["error"])
				}
				return value
			}
		}
		t.Fatal("launcher output ended before correlated response")
		return nil
	}
	send(1, "initialize", map[string]any{"clientInfo": map[string]any{"name": "native_acceptance", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}})
	read(1)
	threadParameters := map[string]any{"cwd": clientHome, "model": "gpt-5.4", "approvalPolicy": "never", "sandbox": "read-only"}
	if modelProbe != nil {
		send(4, "config/read", map[string]any{"includeLayers": false, "cwd": clientHome})
		var config struct {
			Config struct{ Features map[string]bool }
		}
		if json.Unmarshal(read(4)["result"], &config) != nil {
			t.Fatal("synthetic effective config unavailable")
		}
		wantPlugins := modelProbe.Profile == nativeModelProbePluginsOn
		if value, exists := config.Config.Features["plugins"]; !exists || value != wantPlugins {
			t.Fatal("synthetic startup plugin profile not confirmed")
		}
		if wantPlugins && !config.Config.Features["remote_plugin"] {
			t.Fatal("default remote-plugin guard missing")
		}
		threadParameters = modelProbe.ThreadParameters(clientHome)
	}
	send(2, "thread/start", threadParameters)
	var thread struct {
		ModelProvider string `json:"modelProvider"`
		Thread        struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(read(2)["result"], &thread)
	if modelProbe != nil && (thread.ModelProvider != threadParameters["modelProvider"] || thread.Thread.ID == "") {
		t.Fatal("actual synthetic model provider differs")
	}
	if scenario == "model_probe" {
		if modelProbe != nil && modelProbe.BeforeTurn != nil {
			snapshot, err := host.Runtime().EnvironmentResolver().Resolve(e.ID)
			if err != nil {
				t.Fatal("published MODEL environment unavailable")
			}
			cleanup, err := modelProbe.BeforeTurn(ctx, nativeModelProbeSetup{Runtime: host.Runtime(), Management: management, Environment: snapshot, AccountID: account.Account.ID.String()})
			if cleanup != nil {
				defer cleanup()
			}
			if err != nil {
				t.Fatal("MODEL before-turn setup failed")
			}
		}
		send(3, "turn/start", map[string]any{"threadId": thread.Thread.ID, "input": []any{map[string]any{"type": "text", "text": "Reply with the synthetic fixture result.", "text_elements": []any{}}}})
		read(3)
		completed := false
		turnFailure := ""
		for frame := range frames {
			var method string
			_ = json.Unmarshal(frame["method"], &method)
			if method == "turn/completed" {
				var params struct {
					Turn struct {
						Status string `json:"status"`
						Error  *struct {
							Code json.RawMessage `json:"codexErrorInfo"`
						} `json:"error"`
					} `json:"turn"`
				}
				_ = json.Unmarshal(frame["params"], &params)
				completed = params.Turn.Status == "completed"
				if params.Turn.Error != nil {
					var codes map[string]struct {
						HTTPStatus *uint16 `json:"httpStatusCode"`
					}
					if json.Unmarshal(params.Turn.Error.Code, &codes) == nil {
						for _, kind := range []string{"httpConnectionFailed", "responseStreamConnectionFailed", "responseStreamDisconnected", "responseTooManyFailedAttempts"} {
							if code, ok := codes[kind]; ok {
								turnFailure = kind
								if code.HTTPStatus != nil {
									turnFailure += fmt.Sprintf(" status%d", *code.HTTPStatus)
								}
							}
						}
					} else {
						var kind string
						_ = json.Unmarshal(params.Turn.Error.Code, &kind)
						switch kind {
						case "unauthorized", "badRequest", "other", "sandboxError", "internalServerError":
							turnFailure = kind
						default:
							turnFailure = "unclassified_error"
						}
					}
				}
				break
			}
		}
		if modelProbe != nil {
			runs, err := host.Runtime().CaptureRunReader().ListRuns(ctx, capturerun.PageRequest{Limit: 10})
			if err != nil || len(runs.Items) != 1 {
				t.Fatal("synthetic model Capture missing")
			}
			capture := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: runs.Items[0].ID}
			assignment, err := host.Runtime().CaptureAssignments().Resolve(ctx, capture)
			if err != nil || assignment.Source != captureassignment.SourceLaunch {
				t.Fatal("synthetic model launch assignment missing")
			}
			modelProbe.ObserveTerminal(ctx, nativeModelProbeObservation{Runtime: host.Runtime(), Capture: capture, Assignment: assignment, AccountID: account.Account.ID.String(), Completed: completed, Failure: turnFailure, ModelRequests: int(modelCount.Load()), DataDirectory: paths.DataDirectory()})
			if int(modelCount.Load()) != modelProbe.ExpectedModelRequests {
				t.Fatal("synthetic model request count differs")
			}
			return
		}
		if !completed {
			failed, _ := host.Runtime().Activities().ListExchanges(ctx, activity.PageRequest{Limit: 10})
			reasons := []string{turnFailure}
			for _, record := range failed.Items {
				if record.ReasonCode != "" {
					reasons = append(reasons, record.ReasonCode)
					if record.Diagnosis != nil {
						reasons = append(reasons, record.Diagnosis.ClientField, record.Diagnosis.ClientPath)
					}
				}
			}
			connections, _ := host.Runtime().ConnectionEvents().List(ctx, connectionevent.PageRequest{Limit: 10, LatestPerConnection: true})
			for _, event := range connections.Items {
				if event.ErrorClass != "" {
					reasons = append(reasons, event.ErrorClass)
				}
			}
			t.Fatal("actual model turn did not complete: " + strings.Join(reasons, ",") + fmt.Sprintf(" routingProbeDenied=%d", transport.deniedAccountRouting.Load()))
		}
		runs, err := host.Runtime().CaptureRunReader().ListRuns(ctx, capturerun.PageRequest{Limit: 10})
		if err != nil || len(runs.Items) != 1 {
			t.Fatal("model turn Capture missing")
		}
		capture := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: runs.Items[0].ID}
		assignment, err := host.Runtime().CaptureAssignments().Resolve(ctx, capture)
		if err != nil {
			t.Fatal("model assignment missing")
		}
		var records activity.Page
		// Content and terminal observation each have the existing two-second
		// runtime budget; wait past both without changing those product limits.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			records, err = host.Runtime().Activities().ListExchanges(ctx, activity.PageRequest{CaptureRunID: capture.ID, Limit: 20})
			if err != nil {
				t.Fatal("model Activity read failed")
			}
			if len(records.Items) == 1 && records.Items[0].Kind == activity.KindExchangeCompleted {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if len(records.Items) != 1 || records.Items[0].Kind != activity.KindExchangeCompleted || modelCount.Load() != 1 {
			status := host.Runtime().Status()
			failure := "none"
			if status.RecordingFailure != nil {
				failure = status.RecordingFailure.Operation + ":" + status.RecordingFailure.Reason
			}
			if status.StorageFailure != nil {
				failure = status.StorageFailure.Operation + ":" + status.StorageFailure.Reason
			}
			t.Fatal("model turn did not create one terminal Exchange; recording=" + failure)
		}
		record := records.Items[0]
		if record.Status != activity.StatusSucceeded || record.Transport == nil || record.Transport.Presentation == nil || record.Transport.Presentation.ClientProtocol != "h2" || record.Transport.Presentation.UpstreamProtocol != "http/1.1" || record.Transport.Presentation.ProtocolAdjustment != "cleartext_http1" || record.Transport.Requested != nil || record.Transport.Effective != nil || host.Runtime().Status().RecordingFailure != nil {
			t.Fatal("real model transport adjustment or healthy terminal recording missing")
		}
		if record.CaptureRunID != capture.ID || record.EnvironmentID != assignment.EnvironmentID.String() || record.EnvironmentRevision != uint64(assignment.EnvironmentRevision) || record.EnvironmentDigest != assignment.EnvironmentDigest.String() || record.AccountID != account.Account.ID.String() {
			t.Fatal(fmt.Sprintf("actual model ownership: capture=%t env=%t revision=%t digest=%t account=%t kind=%s status=%s", record.CaptureRunID == capture.ID, record.EnvironmentID == assignment.EnvironmentID.String(), record.EnvironmentRevision == uint64(assignment.EnvironmentRevision), record.EnvironmentDigest == assignment.EnvironmentDigest.String(), record.AccountID == account.Account.ID.String(), record.Kind, record.Status))
		}
		if observer, ok := t.(interface{ NativeOwnership(bool, bool, bool) }); ok {
			observer.NativeOwnership(true, true, true)
		}
		return
	}
	var streamCapture captureidentity.Reference
	streamApprovals := map[string]bool{}
	allowStream := func() {
		if streamObserve {
			return
		}
		id := management.allowStreamPending(ctx, streamCapture)
		if streamApprovals[id] {
			t.Fatal("stream reused approval")
		}
		streamApprovals[id] = true
	}
	if scenario == "sse_reconnect" {
		runs, err := host.Runtime().CaptureRunReader().ListRuns(ctx, capturerun.PageRequest{Limit: 10})
		if err != nil || len(runs.Items) != 1 {
			t.Fatal("stream Capture missing")
		}
		streamCapture = captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: runs.Items[0].ID}
		allowStream()
		select {
		case <-freshReply:
		case <-ctx.Done():
			t.Fatal("fresh GET callback reply missing")
		}
	}
	callArguments := map[string]any{"query": "synthetic"}
	if sensitiveDeny {
		callArguments[sensitiveArgument] = "synthetic-sensitive-argument-marker"
	}
	if probingProfile {
		callArguments = profileProbe.arguments
	}
	if probingProfile && len(profileProbe.text) > 2<<20 {
		// Positive persistence assertions require an initialized observation role
		// and the real launched Capture mirror before measured client traffic.
		runs, err := host.Runtime().CaptureRunReader().ListRuns(ctx, capturerun.PageRequest{Limit: 10})
		if err != nil || len(runs.Items) != 1 {
			t.Fatal("profile Capture missing before traffic")
		}
		profileCapture := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: runs.Items[0].ID}
		readyDeadline := time.Now().Add(6 * time.Second)
		for {
			_, readyErr := host.Runtime().NativeInspection().Page(ctx, nativetool.InspectionPageRequest{Capture: profileCapture, Limit: 1})
			if readyErr == nil && host.Runtime().Status().Recording.State == productruntime.RecordingAvailable {
				break
			}
			if time.Now().After(readyDeadline) || ctx.Err() != nil {
				t.Fatal("positive profile observation role did not become ready before traffic")
			}
			time.Sleep(time.Millisecond)
		}
	}
	priorRecording := host.Runtime().Status().Recording
	send(3, "mcpServer/tool/call", map[string]any{"threadId": thread.Thread.ID, "server": "codex_apps", "tool": "synthetic_read", "arguments": callArguments})
	if scenario == "review_allow_once" {
		runs, err := host.Runtime().CaptureRunReader().ListRuns(ctx, capturerun.PageRequest{Limit: 10})
		if err != nil || len(runs.Items) != 1 {
			t.Fatal("ordinary Review Capture missing")
		}
		management.allowSyntheticPending(ctx, captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: runs.Items[0].ID})
	}
	if scenario == "review_reject" {
		runs, err := host.Runtime().CaptureRunReader().ListRuns(ctx, capturerun.PageRequest{Limit: 10})
		if err != nil || len(runs.Items) != 1 {
			t.Fatal("pending call Capture missing")
		}
		pendingCapture := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: runs.Items[0].ID}
		decided := false
		defer func() {
			if decided {
				return
			}
			select {
			case frame := <-frames:
				var rpcError struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				}
				_ = json.Unmarshal(frame["error"], &rpcError)
				classification := "unclassified"
				for _, candidate := range []string{"session_invalid", "protocol_invalid", "execution_uncertain", "not found", "initializ"} {
					if strings.Contains(rpcError.Message, candidate) {
						classification = candidate
						break
					}
				}
				t.Logf("client response before missing approval: hasError=%t code=%d class=%s hasResult=%t", len(frame["error"]) > 0, rpcError.Code, classification, len(frame["result"]) > 0)
			default:
				t.Logf("no buffered client response before missing approval")
			}
			page, err := host.Runtime().NativeInspection().Page(ctx, nativetool.InspectionPageRequest{Capture: pendingCapture, Limit: 100})
			if err == nil {
				seen := map[nativetool.ID]bool{}
				for _, observed := range page.Items {
					op := observed.Operation
					if seen[op.ID] {
						continue
					}
					seen[op.ID] = true
					t.Logf("pending diagnostic kind=%s state=%s reason=%s", op.Kind, op.State, op.Reason)
				}
			}
			logNativePhaseSQL(ctx, t, paths.DataDirectory(), pendingCapture)
		}()
		management.rejectPending(ctx, pendingCapture, knownDeny)
		decided = true
	}
	if scenario == "sse_reconnect" {
		allowStream() // original tools/call
		allowStream() // original POST-stream ping response
		select {
		case <-resumed:
		case <-time.After(10 * time.Second):
			t.Fatal("actual client did not resume original POST stream")
		case <-ctx.Done():
			t.Fatal("SSE resume canceled")
		}
		allowStream() // new callback belongs to the resumed original stream
	}
	toolResponse := read(3, scenario != "observe" && scenario != "sse_reconnect")
	mu.Lock()
	observed := append([]string{}, methods...)
	mu.Unlock()
	wantMethods := "initialize,notifications/initialized,tools/list,tools/call"
	if scenario == "strict_external" || scenario == "review_reject" {
		wantMethods = "initialize,notifications/initialized,tools/list"
	}
	if scenario == "off" {
		wantMethods = ""
	}
	if scenario == "sse_reconnect" {
		calls := 0
		for _, method := range observed {
			if method == "tools/call" {
				calls++
			}
		}
		mu.Lock()
		validReplies := len(callbackReplies) == 3 && callbackReplies["fresh-ping"] == 1 && callbackReplies["post-ping"] == 1 && callbackReplies["resumed-ping"] == 1
		capabilities := string(initializeCapabilities)
		mu.Unlock()
		t.Logf("stream client=%s capabilities=%s originalPOST=%d GET=%d callbackRepliesValid=%t distinctApprovals=%d", expectedVersion, capabilities, calls, streams.Load(), validReplies, len(streamApprovals))
		if calls != 1 || len(observed) != 7 || streams.Load() != 2 || !validReplies || !streamObserve && len(streamApprovals) != 4 || capabilities == "" {
			t.Fatal("SSE reconnection repeated a tool call or omitted the stream")
		}
	} else if strings.Join(observed, ",") != wantMethods {
		t.Fatalf("native methods=%v", observed)
	}
	runs, err := host.Runtime().CaptureRunReader().ListRuns(ctx, capturerun.PageRequest{Limit: 10})
	if err != nil || len(runs.Items) != 1 {
		t.Fatal("actual launcher Capture missing")
	}
	capture := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: runs.Items[0].ID}
	assignment, err := host.Runtime().CaptureAssignments().Resolve(ctx, capture)
	if err != nil || assignment.Source != captureassignment.SourceLaunch {
		t.Fatal("actual launch assignment missing")
	}
	expectedSnapshot, err := host.Runtime().EnvironmentResolver().Resolve(e.ID)
	if err != nil || assignment.EnvironmentID != expectedSnapshot.ID() || assignment.EnvironmentRevision != expectedSnapshot.Revision() || assignment.EnvironmentDigest != expectedSnapshot.Digest() {
		t.Fatal("launcher did not retain the selected published profile")
	}
	if scenario == "off" {
		page, err := host.Runtime().NativeInspection().Page(ctx, nativetool.InspectionPageRequest{Capture: capture, Limit: 100})
		if err != nil || len(page.Items) != 0 || nativeCount.Load() != 0 || toolResponse["error"] == nil {
			t.Fatal("OFF did not prevent native registration and call")
		}
	} else {
		var call nativetool.Operation
		var observedCall nativetool.InspectionOperation
		wantState := "completed"
		if scenario == "session404_once" {
			wantState = "uncertain"
		}
		if scenario == "strict_external" || scenario == "review_reject" {
			wantState = "denied"
		}
		callID, identityErr := nativeToolCallID(ctx, paths.DataDirectory(), capture)
		if identityErr != nil {
			t.Fatal(identityErr)
		}
		// Actual client response/terminal has returned. Recheck required authority
		// independently, then settle only the selected optional inspection record.
		required, requiredErr := host.Runtime().NativeToolRepository().Get(ctx, callID)
		if requiredErr != nil || required.State != wantState || required.Capture != capture || required.EnvironmentDigest != assignment.EnvironmentDigest {
			t.Fatalf("required native terminal/ownership differs: err=%v state=%s want=%s captureMatched=%t environmentMatched=%t", requiredErr, required.State, wantState, required.Capture == capture, required.EnvironmentDigest == assignment.EnvironmentDigest)
		}
		settleDeadline := time.Now().Add(6 * time.Second)
		for {
			observed, readErr := host.Runtime().NativeInspection().Get(ctx, capture, callID)
			ready := readErr == nil && observed.Operation.Revision == required.Revision
			if ready && probingProfile {
				ready = (observed.Arguments != nil) == profileProbe.argumentsRetained && (observed.Result != nil) == profileProbe.resultRetained
			}
			if ready {
				observedCall = observed
				break
			}
			if time.Now().After(settleDeadline) || ctx.Err() != nil {
				t.Logf("optional settlement read=%v requiredRevision=%d observedRevision=%d arguments=%t result=%t recording=%+v", readErr, required.Revision, observed.Operation.Revision, observed.Arguments != nil, observed.Result != nil, host.Runtime().Status().RecordingFailure)
				t.Fatal("actual native optional inspection did not settle after producer completion")
			}
			time.Sleep(time.Millisecond)
		}
		call = required
		if call.State != wantState || call.Capture != capture || call.EnvironmentID != assignment.EnvironmentID || call.EnvironmentRevision != assignment.EnvironmentRevision || call.EnvironmentDigest != assignment.EnvironmentDigest || call.BindingDigest == nil || call.CredentialOwner != "original_client" || !probingProfile && (observedCall.Arguments != nil || observedCall.Result != nil) {
			t.Fatal("native ownership/terminal/OFF recording differs from actual frozen assignment")
		}
		audit, err := nativeDeliveryAuditPage(ctx, paths.DataDirectory(), call.ID)
		if err != nil {
			t.Fatal("native policy decision is not authoritative")
		}
		if scenario == "review_reject" {
			if call.DecisionSource != "user" || call.ApprovalID == nil || call.Reason != "approval_denied" || len(audit.Items) != 0 || (call.BytesOut != nil && *call.BytesOut != 0) {
				t.Fatal("Review reject did not consume the actual user decision before send")
			}
		} else if scenario == "review_allow_once" {
			if call.DecisionSource != "user" || call.ApprovalID == nil || len(audit.Items) != 1 {
				t.Fatal("ordinary Review lacks one consumed Allow send")
			}
			approval, err := host.Runtime().NativeToolRepository().GetApproval(ctx, *call.ApprovalID)
			if err != nil || approval.State != "allowed" || approval.ConsumedAt == nil {
				t.Fatal("ordinary Review Allow not consumed")
			}
			reader, err := openControlAuditReader(ctx, paths.DataDirectory())
			if err != nil {
				t.Fatal(err)
			}
			var once, consumed int
			err = reader.connection.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?),(SELECT COUNT(*) FROM native_tool_approvals WHERE operation_id=? AND consumed_at_unix_ms IS NOT NULL)`, call.ID, call.ID).Scan(&once, &consumed)
			_ = reader.Close()
			if err != nil || once != 1 || consumed != 1 {
				t.Fatal("synthetic Allow replay changed once/consumption")
			}
			t.Logf("synthetic Review SQL once=%d consumed=%d audit=%d", once, consumed, len(audit.Items))
		} else if scenario == "sse_reconnect" && !streamObserve {
			if call.DecisionSource != "user" || call.ApprovalID == nil || !streamApprovals[call.ApprovalID.String()] {
				t.Fatal("stream original call lacks its own Allow")
			}
		} else if call.DecisionSource != "policy" || call.ApprovalID != nil {
			t.Fatal("native policy decision was replaced")
		}
		if scenario == "strict_external" {
			if call.Reason != "policy_strict" || len(audit.Items) != 0 || (call.BytesOut != nil && *call.BytesOut != 0) {
				t.Fatal("Strict external action was not denied before send")
			}
		} else if scenario == "observe" {
			if call.BytesOut == nil || *call.BytesOut <= 0 || len(audit.Items) != 1 || audit.Items[0].Attempt.Purpose() != egressaudit.PurposeClientToolService || !bytes.Contains(toolResponse["result"], []byte("synthetic result")) {
				t.Fatal("Observe lacks a completed matching native send and result")
			}
		}
		if scenario == "sse_reconnect" && (len(audit.Items) != 1 || !bytes.Contains(toolResponse["result"], []byte("synthetic result"))) {
			t.Fatal("recovered original call lacks its single audit/result")
		}
		if scenario == "session404_once" {
			if call.Reason != "execution_uncertain" || call.BytesIn != nil || call.BytesOut != nil || len(audit.Items) != 1 || audit.Items[0].Attempt.ByteCountsKnown() || audit.Items[0].Attempt.Purpose() != egressaudit.PurposeClientToolService {
				t.Fatal("404 did not retain one truthful uncertain attempt")
			}
			scope, err := host.Runtime().NativeToolRepository().GetScope(ctx, capture)
			if err != nil || !scope.ReplayBarrier {
				t.Fatal("404 did not persist a replay barrier")
			}
			// A different app-server RPC ID and the same parameters cannot
			// supply new intent or fresh native send authority.
			send(4, "mcpServer/tool/call", map[string]any{"threadId": thread.Thread.ID, "server": "codex_apps", "tool": "synthetic_read", "arguments": map[string]any{"query": "synthetic"}})
			_ = read(4, true)
			mu.Lock()
			calls := 0
			for _, method := range methods {
				if method == "tools/call" {
					calls++
				}
			}
			mu.Unlock()
			if calls != 1 {
				t.Fatal("uncertain native call replayed under a new client request ID")
			}
			scope, err = host.Runtime().NativeToolRepository().GetScope(ctx, capture)
			if err != nil || !scope.ReplayBarrier {
				t.Fatal("automatic request cleared persisted uncertainty")
			}
		}
		if probingProfile && len(profileProbe.text) > 2<<20 {
			if toolResponse["error"] != nil || bytes.Count(toolResponse["result"], []byte(profileProbe.text)) != 1 || len(audit.Items) != 1 || audit.Items[0].Attempt.Outcome() != egressaudit.OutcomeCompleted {
				t.Fatal("large native client result or single actual delivery changed")
			}
			if profileProbe.capacityDrop {
				status := host.Runtime().Status()
				t.Logf("large metadata loss droppedBefore=%d droppedAfter=%d state=%s writerFailure=%t", priorRecording.Dropped, status.Recording.Dropped, status.Recording.State, status.RecordingFailure != nil)
				if status.Recording.Dropped <= priorRecording.Dropped || status.Recording.State != productruntime.RecordingDegraded || status.RecordingFailure != nil || observedCall.Result != nil {
					t.Fatal("large metadata bounded admission loss is not truthfully reported")
				}
			}
		}
		if probingProfile {
			management.verifySyntheticFull(ctx, observedCall, profileProbe)
			verifySyntheticSQLPrivacy(ctx, t, paths.DataDirectory())
		} else {
			management.verifyMetadata(ctx, call)
		}
		if sensitiveDeny {
			verifySyntheticSQLPrivacy(ctx, t, paths.DataDirectory())
		}
		if scenario == "sse_reconnect" {
			verifyNativeStreamSQL(ctx, t, paths.DataDirectory(), capture, !streamObserve)
			page, err := host.Runtime().NativeInspection().Page(ctx, nativetool.InspectionPageRequest{Capture: capture, Limit: 100})
			if err != nil {
				t.Fatal("stream reply page unavailable")
			}
			seen := map[nativetool.ID]bool{}
			for _, observed := range page.Items {
				op := observed.Operation
				if op.Kind == nativetool.RequestServerReply && !seen[op.ID] {
					seen[op.ID] = true
					management.verifyMetadata(ctx, op)
				}
			}
			if len(seen) != 3 {
				t.Fatal("stream reply API count differs")
			}
		}
	}
	activities, err := host.Runtime().Activities().ListExchanges(ctx, activity.PageRequest{CaptureRunID: capture.ID, Limit: 100})
	if err != nil || len(activities.Items) != 0 {
		t.Fatal("native direct call fabricated a model Exchange")
	}
	if blind.attempts.Load() != 1 {
		t.Fatal("actual client attempted blind egress after preflight")
	}
	mu.Lock()
	t.Logf("actual native startup client=%s initializedCapabilities=%s freshGET=%d", expectedVersion, initializeCapabilities, streams.Load())
	mu.Unlock()
	logNativePhaseSQL(ctx, t, paths.DataDirectory(), capture)
	if observer, ok := t.(interface{ NativeOwnership(bool, bool, bool) }); ok {
		observer.NativeOwnership(true, true, false)
	}
}

func verifySyntheticSQLPrivacy(ctx context.Context, t nativeFixtureChecks, directory string) {
	for _, open := range []func(context.Context, string) (*exchangeAuditReader, error){openControlAuditReader, openExchangeAuditReader} {
		verifySyntheticSQLPrivacyRole(ctx, t, directory, open)
	}
}
func verifySyntheticSQLPrivacyRole(ctx context.Context, t nativeFixtureChecks, directory string, open func(context.Context, string) (*exchangeAuditReader, error)) {
	reader, err := open(ctx, directory)
	if err != nil {
		t.Fatal("private SQL audit unavailable")
	}
	defer reader.Close()
	rows, err := reader.connection.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			t.Fatal("private schema row invalid")
		}
		tables = append(tables, name)
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	_ = rows.Close()
	for _, table := range tables {
		rows, err := reader.connection.QueryContext(ctx, `SELECT * FROM "`+strings.ReplaceAll(table, `"`, `""`)+`"`)
		if err != nil {
			t.Fatal("private table unavailable")
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if rows.Scan(destinations...) != nil {
				t.Fatal("private row unavailable")
			}
			for _, value := range values {
				var raw string
				switch value := value.(type) {
				case []byte:
					raw = string(value)
				case string:
					raw = value
				default:
					continue
				}
				for _, marker := range []string{"synthetic-original-login", "synthetic-managed-model", "private-session", "synthetic-transport-private-marker", "synthetic-sensitive-argument-marker", "synthetic-sensitive-result-marker"} {
					if strings.Contains(raw, marker) {
						t.Fatal("synthetic sensitive value reached SQL")
					}
				}
			}
		}
		if rows.Err() != nil {
			t.Fatal(rows.Err())
		}
		_ = rows.Close()
	}
}

// Keep draining startup notifications while the control client reviews a
// question. A synchronous stdout pipe must not hold the app-server's startup
// task behind that review. Only bounded correlated responses/turn terminals
// are retained; this is fixture I/O, not a substitute client implementation.
func ownNativeFixturePipes(ctx context.Context, inputReader *io.PipeReader, inputWriter *io.PipeWriter, outputReader *io.PipeReader, outputWriter *io.PipeWriter) func() {
	done := make(chan struct{})
	var closeOnce, cleanupOnce sync.Once
	closeOwned := func() {
		closeOnce.Do(func() {
			_ = inputReader.Close()
			_ = inputWriter.Close()
			_ = outputReader.Close()
			_ = outputWriter.Close()
			close(done)
		})
	}
	stop := context.AfterFunc(ctx, closeOwned)
	return func() {
		cleanupOnce.Do(func() {
			if stop() {
				closeOwned()
			}
			<-done
		})
	}
}

func nativeClientFrames(ctx context.Context, reader io.Reader) (<-chan map[string]json.RawMessage, <-chan error) {
	frames := make(chan map[string]json.RawMessage, 8)
	finished := make(chan error, 1)
	go func() {
		defer close(frames)
		defer close(finished)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			var frame map[string]json.RawMessage
			if json.Unmarshal(scanner.Bytes(), &frame) != nil {
				finished <- fmt.Errorf("invalid client frame")
				return
			}
			if frame["id"] == nil && string(frame["method"]) != `"turn/completed"` {
				continue
			}
			select {
			case frames <- frame:
			case <-ctx.Done():
				return
			default:
				finished <- fmt.Errorf("client frame queue exceeded bound")
				return
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			finished <- err
		}
	}()
	return frames, finished
}

func nativeTLSFixture(t nativeFixtureChecks, handler http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"chatgpt.com"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, roots
}
