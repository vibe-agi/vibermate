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

func runNativeFixtureSlice(t nativeFixtureChecks, binary string, nodePaths ...string) {
	scenario := "observe"
	if source, ok := t.(interface{ NativeScenario() string }); ok {
		scenario = source.NativeScenario()
	}
	if scenario != "observe" && scenario != "off" && scenario != "strict_external" && scenario != "review_reject" && scenario != "session404_once" && scenario != "sse_reconnect" && scenario != "model_probe" {
		t.Fatal("native scenario not implemented")
	}
	parent := context.Background()
	if source, ok := t.(interface{ NativeContext() context.Context }); ok {
		parent = source.NativeContext()
	}
	// Reserve thirty seconds of the ninety-second case bound for teardown.
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	var nativeCount, modelCount atomic.Int64
	var streams atomic.Int64
	releaseReconnect := make(chan struct{})
	resumed := make(chan struct{}, 1)
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
	upstream, roots := nativeTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			stream := streams.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			if scenario == "sse_reconnect" {
				if stream == 1 {
					select {
					case <-releaseReconnect:
					case <-r.Context().Done():
						return
					}
					_, _ = io.WriteString(w, "event: message\nid: native-result-event-1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
					w.(http.Flusher).Flush()
					return
				}
				if r.Header.Get("Last-Event-Id") != "native-result-event-1" {
					t.Error("SSE resumed without the actually observed event ID")
				}
				select {
				case resumed <- struct{}{}:
				default:
				}
			}
			<-r.Context().Done()
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
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
				ClientInfo struct {
					Version string `json:"version"`
				} `json:"clientInfo"`
			}
			if json.Unmarshal(request.Params, &params) != nil || params.ClientInfo.Version != expectedVersion {
				t.Error("actual initialize version differs from verified native package")
				w.WriteHeader(400)
				return
			}
			w.Header().Set("Mcp-Session-Id", "private-session")
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "synthetic", "version": "1"}}
			if scenario == "sse_reconnect" {
				result.(map[string]any)["capabilities"] = map[string]any{"tools": map[string]any{"listChanged": true}}
			}
		case "notifications/initialized":
			w.WriteHeader(202)
			return
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "synthetic_read", "description": "Synthetic public tool", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}}}
		case "tools/call":
			if scenario == "session404_once" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "synthetic result"}}}
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
	e := environment.Environment{ID: "native.acceptance", Name: "Private native acceptance", State: environment.StateActive, Revision: 1, ContentRecording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, PolicySet: &environment.PolicySet{ToolMode: environment.ToolPolicyObserve}, ClientToolAccess: &environment.ClientToolAccessPolicy{Enabled: true, Route: &environment.ClientToolRoute{ID: "native.acceptance.route", Revision: 1, TargetOrigin: origin, Path: environment.ClientToolMCPPath, CredentialOwner: environment.ClientToolCredentialOriginal, Contract: environment.ClientToolContractCodexLegacy, EgressProfile: egressprofile.Direct()}}}
	if scenario == "off" {
		e.ClientToolAccess = nil
	}
	if scenario == "strict_external" {
		e.PolicySet.ToolMode = environment.ToolPolicyStrict
	}
	if scenario == "review_reject" {
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
	e.ClientEndpoints = []environment.ClientEndpoint{{ID: "native.synthetic.client", Revision: 1, ClientOrigin: clientOrigin, ProtocolPlans: []environment.ClientProtocolPlan{{ID: "native.synthetic.protocol", Revision: 1, ClientProtocol: environment.ClientProtocolOpenAIResponses, ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: "native.synthetic.adapter", Revision: 1}, EgressProfile: egressprofile.Direct(), Destination: environment.DestinationPlan{Kind: environment.DestinationKindUpstream, Upstream: &environment.UpstreamPlan{DefaultRouteID: "native.synthetic.route", RouteSet: environment.RouteSet{ID: "native.synthetic.routes", Revision: 1, CandidateRouteIDs: []environment.UpstreamRouteID{"native.synthetic.route"}}, Routes: []environment.UpstreamRoute{{ID: "native.synthetic.route", Revision: 1, ProviderTarget: environment.ProviderTarget{ID: endpoint.ID.String(), Revision: environment.Revision(endpoint.Revision), Origin: endpoint.Origin, RealmID: endpoint.RealmID, Capabilities: endpoint.Capabilities}, BackendProtocol: string(environment.ClientProtocolOpenAIResponses), AccountPolicy: environment.RouteAccountPolicy{Revision: 1, Mode: environment.AccountSelectionFixed, FixedAccountID: account.Account.ID.String(), Accounts: []environment.RouteAccountReference{{ID: account.Account.ID.String(), Revision: environment.Revision(account.Account.Revision), DisplayName: account.Account.DisplayName}}}, ModelPolicy: environment.ModelPolicy{Revision: 1, Mode: environment.ModelModePassthrough}, WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue}}}}}}}}
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
	for _, target := range []string{"https://auth.openai.com/", "https://unlisted.invalid/", "https://chatgpt.com:444/"} {
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
	if err = os.WriteFile(filepath.Join(clientHome, "config.toml"), []byte("cli_auth_credentials_store = \"file\"\n[features]\napps = true\n[analytics]\nenabled = false\n[feedback]\nenabled = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
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
	launcher, err := runlauncher.New(runlauncher.Config{Remote: &remote, BaseEnvironment: []string{"PATH=" + filepath.Dir(nodePath) + ":/usr/bin:/bin", "HOME=" + clientHome, "CODEX_HOME=" + clientHome}, Stdin: inputReader, Stdout: outputWriter, Stderr: stderr, Getwd: func() (string, error) { return clientHome, nil }, LookPath: func(string) (string, error) { return binary, nil }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
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
	scanner := bufio.NewScanner(outputReader)
	scanner.Buffer(make([]byte, 4096), 4<<20)
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
		for scanner.Scan() {
			var value map[string]json.RawMessage
			if json.Unmarshal(scanner.Bytes(), &value) == nil && string(value["id"]) == fmt.Sprint(id) {
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
	send(2, "thread/start", map[string]any{"cwd": clientHome, "model": "gpt-5.4", "approvalPolicy": "never", "sandbox": "read-only"})
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(read(2)["result"], &thread)
	if scenario == "model_probe" {
		send(3, "turn/start", map[string]any{"threadId": thread.Thread.ID, "input": []any{map[string]any{"type": "text", "text": "Reply with the synthetic fixture result.", "text_elements": []any{}}}})
		read(3)
		completed := false
		turnFailure := ""
		for scanner.Scan() {
			var frame map[string]json.RawMessage
			if json.Unmarshal(scanner.Bytes(), &frame) != nil {
				continue
			}
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
	send(3, "mcpServer/tool/call", map[string]any{"threadId": thread.Thread.ID, "server": "codex_apps", "tool": "synthetic_read", "arguments": map[string]any{"query": "synthetic"}})
	if scenario == "review_reject" {
		runs, err := host.Runtime().CaptureRunReader().ListRuns(ctx, capturerun.PageRequest{Limit: 10})
		if err != nil || len(runs.Items) != 1 {
			t.Fatal("pending call Capture missing")
		}
		management.rejectPending(ctx, captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: runs.Items[0].ID})
	}
	toolResponse := read(3, scenario != "observe")
	if scenario == "sse_reconnect" {
		close(releaseReconnect)
		select {
		case <-resumed:
		case <-time.After(10 * time.Second):
			t.Fatal("actual client did not resume GET after SSE closure")
		case <-ctx.Done():
			t.Fatal("SSE resume canceled")
		}
	}
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
		if calls != 1 || streams.Load() < 2 {
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
		page, err := host.Runtime().NativeToolRepository().Page(ctx, nativetool.PageRequest{Capture: capture, Limit: 100})
		if err != nil || len(page.Items) != 0 || nativeCount.Load() != 0 || toolResponse["error"] == nil {
			t.Fatal("OFF did not prevent native registration and call")
		}
	} else {
		var call nativetool.Operation
		wantState := "completed"
		if scenario == "session404_once" {
			wantState = "uncertain"
		}
		if scenario == "strict_external" || scenario == "review_reject" {
			wantState = "denied"
		}
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			page, err := host.Runtime().NativeToolRepository().Page(ctx, nativetool.PageRequest{Capture: capture, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			unique := map[nativetool.ID]nativetool.Operation{}
			for _, operation := range page.Items {
				if operation.Kind == nativetool.RequestToolsCall {
					unique[operation.ID] = operation
				}
			}
			if len(unique) > 1 {
				t.Fatal("actual client tool call replayed")
			}
			for _, operation := range unique {
				call = operation
			}
			if call.State == wantState {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if call.State != wantState || call.Capture != capture || call.EnvironmentID != assignment.EnvironmentID || call.EnvironmentRevision != assignment.EnvironmentRevision || call.EnvironmentDigest != assignment.EnvironmentDigest || call.BindingDigest == nil || call.CredentialOwner != "original_client" || call.Arguments != nil || call.Result != nil {
			t.Fatal("native ownership/terminal/OFF recording differs from actual frozen assignment")
		}
		audit, err := host.Runtime().EgressAttempts().List(ctx, egressaudit.PageRequest{ParentKind: egressaudit.ParentClientOperation, ParentID: string(call.ID), Limit: 10})
		if err != nil {
			t.Fatal("native policy decision is not authoritative")
		}
		if scenario == "review_reject" {
			if call.DecisionSource != "user" || call.ApprovalID == nil || call.Reason != "approval_denied" || len(audit.Items) != 0 || (call.BytesOut != nil && *call.BytesOut != 0) {
				t.Fatal("Review reject did not consume the actual user decision before send")
			}
		} else if call.DecisionSource != "policy" || call.ApprovalID != nil {
			t.Fatal("native policy decision was replaced")
		}
		if scenario == "strict_external" {
			if call.Reason != "policy_strict" || len(audit.Items) != 0 || (call.BytesOut != nil && *call.BytesOut != 0) {
				t.Fatal("Strict external action was not denied before send")
			}
		} else if scenario == "observe" || scenario == "sse_reconnect" {
			if call.BytesOut == nil || *call.BytesOut <= 0 || len(audit.Items) != 1 || audit.Items[0].Attempt.Purpose() != egressaudit.PurposeClientToolService || !bytes.Contains(toolResponse["result"], []byte("synthetic result")) {
				t.Fatal("Observe lacks a completed matching native send and result")
			}
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
		management.verifyMetadata(ctx, call)
	}
	activities, err := host.Runtime().Activities().ListExchanges(ctx, activity.PageRequest{CaptureRunID: capture.ID, Limit: 100})
	if err != nil || len(activities.Items) != 0 {
		t.Fatal("native direct call fabricated a model Exchange")
	}
	if blind.attempts.Load() != 1 {
		t.Fatal("actual client attempted blind egress after preflight")
	}
	if observer, ok := t.(interface{ NativeOwnership(bool, bool, bool) }); ok {
		observer.NativeOwnership(true, true, false)
	}
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
