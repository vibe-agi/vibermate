//go:build !vibermate_native_secrets

package desktophost_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/desktophost"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

const (
	managedEnvironmentID = "managed-anthropic"
	managedEndpointID    = "target.managed.anthropic"
	managedAccountID     = "anthropic-test"
	managedSecret        = "managed-test-credential"
)

type managedProviderObservation struct {
	path          string
	authorization string
	xAPIKey       string
	apiKey        string
	cookie        string
	body          string
}

// This is the first test that drives a managed request through the real
// Desktop composition root: launcher -> authenticated proxy -> leaf TLS ->
// Environment request resolution -> Exchange -> ProviderAccount lease ->
// final AuthDriver -> provider transport. It repeats after a full Host reopen
// to prove that SQLite metadata and the private file SecretStore recover as
// one usable route, not merely as independent component records.
func TestManagedAnthropicRouteUsesOnlyFrozenAccountAcrossRestart(t *testing.T) {
	observed := make(chan managedProviderObservation, 2)
	provider := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		body, _ := io.ReadAll(request.Body)
		observed <- managedProviderObservation{
			path: request.URL.Path, authorization: request.Header.Get("Authorization"),
			xAPIKey: request.Header.Get("X-Api-Key"), apiKey: request.Header.Get("Api-Key"),
			cookie: request.Header.Get("Cookie"), body: string(body),
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{
			"id":"msg_managed","type":"message","role":"assistant","model":"claude-test",
			"content":[{"type":"text","text":"managed reached"}],
			"stop_reason":"end_turn","stop_sequence":null,
			"usage":{"input_tokens":4,"output_tokens":2}
		}`)
	}))
	defer provider.Close()

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	dataDirectory := filepath.Join(root, "data")
	secretPath := filepath.Join(root, "private", "provider-secrets.json")
	factory, err := hostsecret.NewDevelopmentFileFactory(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog := fixedSelfTestCatalog(t)

	first, _ := startManagedHost(t, paths, dataDirectory, factory, catalog)
	endpointID := createManagedEndpoint(t, first.Runtime().UpstreamEndpoints(), provider.URL)
	accountID := createManagedAccount(t, first.Runtime().ProviderAccounts(), endpointID)
	environmentID := publishManagedEnvironment(t, first, provider.URL, endpointID, accountID)
	firstDigest := resolveEnvironmentDigest(t, first, environmentID)
	runManagedChild(t, paths, environmentID, false)
	assertManagedObservation(t, <-observed)
	assertManagedEvidence(t, first, environmentID, accountID)
	shutdownHost(t, first)

	second, secondSecrets := startManagedHost(t, paths, dataDirectory, factory, catalog)
	defer shutdownHost(t, second)
	if got := resolveEnvironmentDigest(t, second, environmentID); got != firstDigest {
		t.Fatalf("recovered Environment digest = %q, want %q", got, firstDigest)
	}
	view, err := second.Runtime().ProviderAccounts().Get(context.Background(), accountID)
	if err != nil || view.Account.Revision != 1 ||
		view.Health.State != provideraccount.HealthReady ||
		view.Health.CredentialEpoch != 1 {
		t.Fatalf("recovered ProviderAccount = %+v, %v", view, err)
	}
	runManagedChild(t, paths, environmentID, false)
	assertManagedObservation(t, <-observed)
	assertManagedEvidence(t, second, environmentID, accountID)

	account, err := second.Runtime().ProviderAccounts().Get(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	if err := secondSecrets.Delete(context.Background(), account.Account.SecretRef); err != nil {
		t.Fatal(err)
	}
	runManagedChild(t, paths, environmentID, true)
	select {
	case got := <-observed:
		t.Fatalf("missing managed credential reached provider: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
	missing, err := second.Runtime().ProviderAccounts().Get(context.Background(), accountID)
	if err != nil || missing.Health.State != provideraccount.HealthMissing {
		t.Fatalf("missing managed account health = %+v, %v", missing, err)
	}
}

func TestManagedClaudeOAuthRouteUsesBearerCredential(t *testing.T) {
	observed := make(chan managedProviderObservation, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		body, _ := io.ReadAll(request.Body)
		observed <- managedProviderObservation{
			path: request.URL.Path, authorization: request.Header.Get("Authorization"),
			xAPIKey: request.Header.Get("X-Api-Key"), apiKey: request.Header.Get("Api-Key"),
			cookie: request.Header.Get("Cookie"), body: string(body),
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{
			"id":"msg_oauth","type":"message","role":"assistant","model":"claude-test",
			"content":[{"type":"text","text":"managed reached"}],
			"stop_reason":"end_turn","stop_sequence":null,
			"usage":{"input_tokens":4,"output_tokens":2}
		}`)
	}))
	defer provider.Close()

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	factory, err := hostsecret.NewDevelopmentFileFactory(
		filepath.Join(root, "private", "provider-secrets.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	host, _ := startManagedHost(
		t, paths, filepath.Join(root, "data"), factory, fixedSelfTestCatalog(t),
	)
	defer shutdownHost(t, host)
	endpointID := createManagedEndpoint(t, host.Runtime().UpstreamEndpoints(), provider.URL)
	accountID := createManagedAccountWithDriver(
		t, host.Runtime().ProviderAccounts(), endpointID, providerauth.StaticHeaderDriverRef(),
	)
	environmentID := publishManagedEnvironment(t, host, provider.URL, endpointID, accountID)
	runManagedChild(t, paths, environmentID, false)

	got := <-observed
	if got.path != "/v1/messages" || got.authorization != "Bearer "+managedSecret ||
		got.xAPIKey != "" || got.apiKey != "" || got.cookie != "" ||
		!strings.Contains(got.body, `"content":"managed route"`) {
		t.Fatalf("managed Claude OAuth provider observation = %+v", got)
	}
	assertManagedEvidence(t, host, environmentID, accountID)
}

func TestManagedRouteUsesConfiguredAPIPrefixExactlyOnce(t *testing.T) {
	for _, test := range []struct{ base, want string }{
		{"", "/v1/messages"},
		{"/v1", "/v1/messages"},
		{"/relay", "/relay/v1/messages"},
		{"/relay/v1", "/relay/v1/messages"},
		{"/v10", "/v10/v1/messages"},
	} {
		t.Run(test.base, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.want {
					t.Errorf("inference path = %q, want %q", r.URL.Path, test.want)
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"msg_prefix","type":"message","role":"assistant","model":"claude-test",
				 "content":[{"type":"text","text":"managed reached"}],"stop_reason":"end_turn","stop_sequence":null,
				 "usage":{"input_tokens":4,"output_tokens":2}}`)
			}))
			defer provider.Close()
			root := t.TempDir()
			paths := newHostPaths(t, filepath.Join(root, "cache"))
			factory, err := hostsecret.NewDevelopmentFileFactory(filepath.Join(root, "private", "secrets.json"))
			if err != nil {
				t.Fatal(err)
			}
			host, _ := startManagedHost(t, paths, filepath.Join(root, "data"), factory, fixedSelfTestCatalog(t))
			defer shutdownHost(t, host)
			origin := provider.URL + test.base
			endpointID := createManagedEndpoint(t, host.Runtime().UpstreamEndpoints(), origin)
			accountID := createManagedAccount(t, host.Runtime().ProviderAccounts(), endpointID)
			environmentID := publishManagedEnvironment(t, host, origin, endpointID, accountID)
			runManagedChild(t, paths, environmentID, false)
		})
	}
}

func TestManagedTransformCannotOverrideRouteModel(t *testing.T) {
	observed := make(chan string, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		observed <- string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_transform","type":"message","role":"assistant","model":"route-model",
		 "content":[{"type":"text","text":"managed reached"}],"stop_reason":"end_turn","stop_sequence":null,
		 "usage":{"input_tokens":4,"output_tokens":2}}`)
	}))
	defer provider.Close()
	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	factory, err := hostsecret.NewDevelopmentFileFactory(filepath.Join(root, "private", "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	host, _ := startManagedHost(t, paths, filepath.Join(root, "data"), factory, fixedSelfTestCatalog(t))
	defer shutdownHost(t, host)
	endpointID := createManagedEndpoint(t, host.Runtime().UpstreamEndpoints(), provider.URL)
	accountID := createManagedAccount(t, host.Runtime().ProviderAccounts(), endpointID)
	environmentID := publishManagedEnvironment(t, host, provider.URL, endpointID, accountID, func(candidate *environment.Environment) {
		plan := &candidate.ClientEndpoints[0].ProtocolPlans[0]
		plan.Destination.Upstream.Routes[0].ModelPolicy = environment.ModelPolicy{
			Revision: 1, Mode: environment.ModelModeMap,
			Mappings: []environment.ModelMapping{{RequestedModel: "claude-test", UpstreamModel: "route-model"}},
		}
		plan.Transforms = []codelibrary.TransformRevision{{
			ID: "model-override", Revision: 1, CollectionID: "test", DisplayName: "Model override", PublishedAt: time.Now().UTC(),
			Policy: messagetransform.Policy{RequestJavaScript: `const payload = JSON.parse(request.body); payload.model = "script-model"; request.body = JSON.stringify(payload);`},
		}}
	})
	runManagedChild(t, paths, environmentID, true)
	select {
	case got := <-observed:
		t.Fatalf("transform escaped Model Mapping and reached the upstream: %s", got)
	default:
	}
	page, err := host.Runtime().Activities().ListExchanges(context.Background(), activity.PageRequest{Limit: 10, EnvironmentID: environmentID.String()})
	if err != nil || len(page.Items) != 1 || page.Items[0].ReasonCode != "message_transform_failed" {
		t.Fatalf("model override did not fail at the transform boundary: %+v, %v", page, err)
	}
}

func TestManagedNonStreamingRequestCanWaitForModelComputation(t *testing.T) {
	t.Parallel()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		// Use the production budget through the full proxy, not a substituted
		// transport: a non-streaming model may compute before sending headers.
		timer := time.NewTimer(31 * time.Second)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_slow","type":"message","role":"assistant","model":"claude-test",
		 "content":[{"type":"text","text":"managed reached"}],"stop_reason":"end_turn","stop_sequence":null,
		 "usage":{"input_tokens":4,"output_tokens":2}}`)
	}))
	defer provider.Close()
	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	factory, err := hostsecret.NewDevelopmentFileFactory(filepath.Join(root, "private", "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	host, _ := startManagedHost(t, paths, filepath.Join(root, "data"), factory, fixedSelfTestCatalog(t))
	defer shutdownHost(t, host)
	endpointID := createManagedEndpoint(t, host.Runtime().UpstreamEndpoints(), provider.URL)
	accountID := createManagedAccount(t, host.Runtime().ProviderAccounts(), endpointID)
	environmentID := publishManagedEnvironment(t, host, provider.URL, endpointID, accountID)
	runManagedChild(t, paths, environmentID, false)
	assertManagedEvidence(t, host, environmentID, accountID)
}

func TestManagedAnthropicRoutePreservesMultimodalHistory(t *testing.T) {
	const image = `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="}}`
	const document = `{"type":"document","source":{"type":"url","url":"https://files.example/synthetic.pdf"},"title":"Synthetic PDF","citations":{"enabled":true}}`
	for _, test := range []struct{ name, messages string }{
		{"image", `[{"role":"user","content":[{"type":"text","text":"Inspect this image"},` + image + `]}]`},
		{"document", `[{"role":"user","content":[` + document + `]}]`},
		{"tool image", `[{"role":"assistant","content":[{"type":"tool_use","id":"read_1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"read_1","content":[` + image + `]}]}]`},
		{"tool document", `[{"role":"assistant","content":[{"type":"tool_use","id":"read_1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"read_1","content":[` + document + `]}]}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			messages := test.messages
			var bodies []string
			for round := 0; round < 2; round++ {
				bodies = append(bodies, `{"model":"claude-test","max_tokens":32,"messages":`+messages+`}`)
				messages = strings.TrimSuffix(messages, "]") + `,{"role":"assistant","content":"managed reached"},{"role":"user","content":"Continue with the same attachment"}]`
			}
			assertManagedAnthropicRoundTrip(t, bodies...)
		})
	}
}

func TestManagedAnthropicRoutePreservesNestedExtensions(t *testing.T) {
	assertManagedAnthropicRoundTrip(t, `{
	 "model":"claude-test","max_tokens":2048,
	 "system":[{"type":"text","text":"System","future_system":{"enabled":true}}],
	 "messages":[
	  {"role":"user","future_message":true,"content":[{"type":"text","text":"Read","future_text":1}]},
	  {"role":"assistant","content":[{"type":"tool_use","id":"r1","name":"read","input":{},"future_call":true,"caller":{"type":"direct","future_caller":1}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"r1","future_result":true,"content":[{"type":"text","text":"done","future_result_text":1,"cache_control":{"type":"ephemeral"}}]}]}
	 ],
	 "tools":[{"name":"read","input_schema":{"type":"object"},"future_tool":true}],
	 "tool_choice":{"type":"auto","future_choice":true},
	 "thinking":{"type":"enabled","budget_tokens":1024,"future_thinking":true},
	 "output_config":{"effort":"high","future_output":true,"format":{"type":"json_schema","schema":{"type":"object"},"future_format":true},"task_budget":{"type":"tokens","total":4096,"future_budget":true}},
	 "context_management":{"edits":[{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":2,"future_keep":true},"future_edit":true}],"future_context":true},
	 "diagnostics":{"future_diagnostics":true}
	}`)
}

func assertManagedAnthropicRoundTrip(t *testing.T, bodies ...string) {
	t.Helper()
	assertManagedAnthropicResponse(t, "application/json", `{"id":"msg_native","type":"message","role":"assistant","model":"claude-test",
		 "content":[{"type":"text","text":"managed reached"}],"stop_reason":"end_turn","stop_sequence":null,
		 "usage":{"input_tokens":4,"output_tokens":2}}`, bodies...)
}

// Build native SSE from a synthetic message, including incremental JSON input
// for both client and server tools. No production codec participates here.
func managedAnthropicStream(t *testing.T, response string) string {
	t.Helper()
	var message map[string]json.RawMessage
	if err := json.Unmarshal([]byte(response), &message); err != nil {
		t.Fatal(err)
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(message["content"], &blocks); err != nil {
		t.Fatal(err)
	}
	usage, stop := message["usage"], message["stop_reason"]
	message["content"] = json.RawMessage(`[]`)
	message["stop_reason"] = json.RawMessage(`null`)
	message["usage"] = json.RawMessage(`{"input_tokens":4,"output_tokens":0}`)
	var stream strings.Builder
	emit := func(name string, event map[string]any) {
		event["type"] = name
		body, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&stream, "event: %s\ndata: %s\n\n", name, body)
	}
	emit("message_start", map[string]any{"message": message})
	for index, raw := range blocks {
		var block map[string]json.RawMessage
		if err := json.Unmarshal(raw, &block); err != nil {
			t.Fatal(err)
		}
		var kind string
		if err := json.Unmarshal(block["type"], &kind); err != nil {
			t.Fatal(err)
		}
		input := block["input"]
		text := block["text"]
		var citations []json.RawMessage
		if kind == "text" {
			block["text"] = json.RawMessage(`""`)
			if raw, present := block["citations"]; present {
				if err := json.Unmarshal(raw, &citations); err != nil {
					t.Fatal(err)
				}
				block["citations"] = json.RawMessage(`[]`)
			}
		}
		if kind == "tool_use" || kind == "server_tool_use" {
			block["input"] = json.RawMessage(`{}`)
		}
		emit("content_block_start", map[string]any{"index": index, "content_block": block})
		if kind == "text" {
			emit("content_block_delta", map[string]any{"index": index, "delta": map[string]any{
				"type": "text_delta", "text": text,
			}})
			for _, citation := range citations {
				emit("content_block_delta", map[string]any{"index": index, "delta": map[string]any{
					"type": "citations_delta", "citation": citation,
				}})
			}
		}
		if kind == "tool_use" || kind == "server_tool_use" {
			emit("content_block_delta", map[string]any{"index": index, "delta": map[string]any{
				"type": "input_json_delta", "partial_json": string(input),
			}})
		}
		emit("content_block_stop", map[string]any{"index": index})
	}
	emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": usage})
	emit("message_stop", map[string]any{})
	return stream.String()
}

func TestManagedAnthropicRouteAcceptsExplicitCustomTool(t *testing.T) {
	// Anthropic's explicit "custom" type is the same schema-based client tool
	// as an omitted type, not an OpenAI free-form tool or a server-side action.
	// https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/tool_param.py
	request := `{"model":"claude-test","max_tokens":32,"messages":[{"role":"user","content":"Read the fixture"}],
	 "tools":[{"type":"custom","name":"read","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}],
	 "tool_choice":{"type":"tool","name":"read"}}`
	response := `{"id":"msg_custom","type":"message","role":"assistant","model":"claude-test",
	 "content":[{"type":"tool_use","id":"read_1","name":"read","input":{"path":"fixture"}}],
	 "stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":2}}`
	assertManagedAnthropicResponse(t, "application/json", response, request)
	t.Run("stream", func(t *testing.T) {
		stream := managedAnthropicStream(t, response)
		assertManagedAnthropicResponse(t, "text/event-stream", stream, strings.TrimSuffix(request, "}")+`,"stream":true}`)
	})
}

func TestManagedAnthropicRouteAcceptsNativeToolDefinition(t *testing.T) {
	// Native definitions do not carry a client-provided input_schema. Their
	// configuration must stay on the native wire, not become a fake function.
	assertManagedAnthropicRoundTrip(t, `{"model":"claude-test","max_tokens":32,"messages":[{"role":"user","content":"Find documentation"}],
	 "tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2,"allowed_domains":["docs.example.test"]}],
	 "tool_choice":{"type":"tool","name":"web_search"}}`)
}

func TestManagedAnthropicRoutePreservesServerToolResponse(t *testing.T) {
	request := `{"model":"claude-test","max_tokens":32,"messages":[{"role":"user","content":"Find documentation"}],
	 "tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2}]}`
	response := `{"id":"msg_search","type":"message","role":"assistant","model":"claude-test",
	 "content":[
	  {"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"documentation"}},
	  {"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","url":"https://docs.example.test","title":"Synthetic documentation","encrypted_content":"synthetic-citation"}]},
	  {"type":"text","text":"Found it.","citations":[{"type":"web_search_result_location","url":"https://docs.example.test","title":"Synthetic documentation","encrypted_index":"synthetic-index","cited_text":"Documentation"}]}],
	 "stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":2,"server_tool_use":{"web_search_requests":1}}}`
	var received struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(response), &received); err != nil {
		t.Fatal(err)
	}
	followup := `{"model":"claude-test","max_tokens":32,"messages":[{"role":"user","content":"Find documentation"},{"role":"assistant","content":` + string(received.Content) + `},{"role":"user","content":"Continue"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2}]}`
	assertManagedAnthropicResponseWithToolPolicy(t, environment.ToolPolicyStrict, "application/json", response, request, followup)
	t.Run("stream", func(t *testing.T) {
		assertManagedAnthropicResponseWithToolPolicy(t, environment.ToolPolicyStrict, "text/event-stream", managedAnthropicStream(t, response),
			strings.TrimSuffix(request, "}")+`,"stream":true}`,
			strings.TrimSuffix(followup, "}")+`,"stream":true}`)
	})
}

func TestManagedAnthropicNativeDefinitionsDoNotBypassClientToolPolicy(t *testing.T) {
	for _, definition := range []struct{ name, wire string }{
		{"read", `{"type":"custom","name":"read","input_schema":{"type":"object"}}`},
		{"bash", `{"type":"bash_20250124","name":"bash"}`},
		{"web_search", `{"type":"web_search_20250305","name":"web_search"}`},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", definition.name, stream), func(t *testing.T) {
				request := fmt.Sprintf(`{"model":"claude-test","max_tokens":32,"stream":%t,"messages":[{"role":"user","content":"Run a tool"}],"tools":[%s]}`, stream, definition.wire)
				response := fmt.Sprintf(`{"id":"msg_client_tool","type":"message","role":"assistant","model":"claude-test","content":[{"type":"tool_use","id":"call_1","name":%q,"input":{"command":"synthetic-only"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":2}}`, definition.name)
				mediaType := "application/json"
				wantStatus := "502"
				want := `{"type":"error","error":{"type":"api_error","message":"ViberMate could not complete this request (tool_decision_rejected)."}}`
				if stream {
					mediaType, wantStatus = "text/event-stream", "200"
					response = managedAnthropicStream(t, response)
					want = strings.Split(response, "event: content_block_start")[0] + "event: error\ndata: " + want + "\n\n"
				}
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", mediaType)
					_, _ = io.WriteString(w, response)
				}))
				defer provider.Close()
				root := t.TempDir()
				paths := newHostPaths(t, filepath.Join(root, "cache"))
				factory, err := hostsecret.NewDevelopmentFileFactory(filepath.Join(root, "private", "secrets.json"))
				if err != nil {
					t.Fatal(err)
				}
				host, _ := startManagedHost(t, paths, filepath.Join(root, "data"), factory, fixedSelfTestCatalog(t))
				defer shutdownHost(t, host)
				endpointID := createManagedEndpoint(t, host.Runtime().UpstreamEndpoints(), provider.URL)
				accountID := createManagedAccount(t, host.Runtime().ProviderAccounts(), endpointID)
				environmentID := publishManagedEnvironment(t, host, provider.URL, endpointID, accountID, func(value *environment.Environment) {
					value.PolicySet = &environment.PolicySet{ToolMode: environment.ToolPolicyStrict}
				})
				runManagedChild(t, paths, environmentID, false, childManagedBody+"="+request,
					childManagedResponse+"="+want, childManagedStatus+"="+wantStatus)
				page, err := host.Runtime().Activities().ListExchanges(context.Background(), activity.PageRequest{Limit: 1, EnvironmentID: environmentID.String()})
				if err != nil || len(page.Items) != 1 || page.Items[0].ReasonCode != "tool_decision_rejected" {
					t.Fatalf("client tool missed the policy boundary: %+v, %v", page, err)
				}
			})
		}
	}
}

func TestManagedAnthropicRoutePreservesLegalTerminalsAndUsage(t *testing.T) {
	for _, reason := range []string{"refusal", "pause_turn", "model_context_window_exceeded", "max_tokens"} {
		for _, stream := range []bool{false, true} {
			name := reason + "/json"
			if stream {
				name = reason + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				request := `{"model":"claude-test","max_tokens":32,"messages":[{"role":"user","content":"hello"}]`
				block := `{"type":"text","text":"managed reached"}`
				if reason == "max_tokens" {
					request += `,"tools":[{"name":"read","input_schema":{"type":"object"}}]`
					block = `{"type":"tool_use","id":"r1","name":"read","input":{}}`
				}
				mediaType := "application/json"
				response := `{"id":"msg_terminal","type":"message","role":"assistant","model":"claude-test","content":[` + block + `],"stop_reason":"` + reason + `","stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":2}}`
				if stream {
					request += `,"stream":true`
					mediaType = "text/event-stream"
					response = "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_terminal","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":0}}}` + "\n\n" +
						"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":` + block + `}` + "\n\n" +
						"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\n" +
						"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"` + reason + `","stop_sequence":null},"usage":{"output_tokens":2}}` + "\n\n" +
						"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"
				}
				assertManagedAnthropicResponse(t, mediaType, response, request+`}`)
			})
		}
	}
}

func TestManagedAnthropicRoutePreservesUpstreamErrors(t *testing.T) {
	for _, test := range []struct {
		status        int
		kind, message string
	}{
		{400, "invalid_request_error", "prompt is too long: 200001 tokens > 200000 maximum"},
		{429, "rate_limit_error", "synthetic quota window is exhausted"},
		{200, "overloaded_error", "synthetic upstream is overloaded"},
		{200, "api_error", "synthetic stream failed after partial output"},
		{200, "permission_error", "synthetic stream failed after tool start"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			body := `{"type":"error","error":{"type":"` + test.kind + `","message":"` + test.message + `","native_detail":{"preserve":true}},"request_id":"synthetic-request-id"}`
			mediaType := "application/json"
			request := `{"model":"claude-test","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`
			wantBody := body
			if test.status == http.StatusOK {
				mediaType = "text/event-stream"
				body = "event: error\ndata: " + body + "\n\n"
				wantBody = body
				request = `{"model":"claude-test","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hello"}]}`
				if test.kind == "api_error" {
					body = "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_before_error","type":"message","role":"assistant","model":"claude-test","usage":{"input_tokens":4,"output_tokens":0}}}` + "\n\n" +
						"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"visible before failure"}}` + "\n\n" + body
					wantBody = body
				}
				if test.kind == "permission_error" {
					start := "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_tool_error","type":"message","role":"assistant","model":"claude-test","usage":{"input_tokens":4,"output_tokens":0}}}` + "\n\n"
					wantBody = start + body
					body = start + "event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"unapproved-tool","name":"read","input":{}}}` + "\n\n" + body
					request = `{"model":"claude-test","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"read","input_schema":{"type":"object"}}]}`
				}
			}
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Api-Key") != managedSecret {
					t.Error("rejection fixture did not receive the managed credential")
				}
				w.Header().Set("Content-Type", mediaType)
				w.Header().Set("Retry-After", "60")
				w.Header().Set("Retry-After-Ms", "60000")
				w.Header().Set("Request-Id", "synthetic-request-id")
				w.Header().Set("X-Should-Retry", "false")
				w.Header().Set("Set-Cookie", "private-cookie")
				w.Header().Set("Authorization", "Bearer private-provider-header")
				w.Header().Set("X-Codex-Turn-State", "unbound-state")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, body)
			}))
			defer provider.Close()
			root := t.TempDir()
			paths := newHostPaths(t, filepath.Join(root, "cache"))
			factory, err := hostsecret.NewDevelopmentFileFactory(filepath.Join(root, "private", "secrets.json"))
			if err != nil {
				t.Fatal(err)
			}
			host, _ := startManagedHost(t, paths, filepath.Join(root, "data"), factory, fixedSelfTestCatalog(t))
			defer shutdownHost(t, host)
			endpointID := createManagedEndpoint(t, host.Runtime().UpstreamEndpoints(), provider.URL)
			accountID := createManagedAccount(t, host.Runtime().ProviderAccounts(), endpointID)
			environmentID := publishManagedEnvironment(t, host, provider.URL, endpointID, accountID)
			runManagedChild(t, paths, environmentID, false,
				childManagedBody+"="+request, childManagedResponse+"="+wantBody, childManagedStatus+"="+strconv.Itoa(test.status),
				childManagedHeaders+`={"Retry-After":"60","Retry-After-Ms":"60000","Request-Id":"synthetic-request-id","X-Should-Retry":"false","Set-Cookie":"","Authorization":"","X-Codex-Turn-State":""}`)
		})
	}
}

func assertManagedAnthropicResponse(t *testing.T, mediaType, response string, bodies ...string) {
	t.Helper()
	assertManagedAnthropicResponseWithToolPolicy(t, environment.ToolPolicyObserve, mediaType, response, bodies...)
}

func assertManagedAnthropicResponseWithToolPolicy(t *testing.T, mode environment.ToolPolicyMode, mediaType, response string, bodies ...string) {
	t.Helper()
	observed := make(chan string, len(bodies))
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		observed <- string(body)
		if r.URL.Path != "/v1/messages" || r.Header.Get("X-Api-Key") != managedSecret || r.Header.Get("Authorization") != "" {
			t.Error("request escaped the frozen managed route/credential")
		}
		w.Header().Set("Content-Type", mediaType)
		_, _ = io.WriteString(w, response)
	}))
	defer provider.Close()
	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	factory, err := hostsecret.NewDevelopmentFileFactory(filepath.Join(root, "private", "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	host, _ := startManagedHost(t, paths, filepath.Join(root, "data"), factory, fixedSelfTestCatalog(t))
	defer shutdownHost(t, host)
	endpointID := createManagedEndpoint(t, host.Runtime().UpstreamEndpoints(), provider.URL)
	accountID := createManagedAccount(t, host.Runtime().ProviderAccounts(), endpointID)
	environmentID := publishManagedEnvironment(t, host, provider.URL, endpointID, accountID, func(value *environment.Environment) {
		value.PolicySet = &environment.PolicySet{ToolMode: mode}
	})
	usage := host.Runtime().UsageRepository()
	policy, err := usage.UsagePolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled = true
	if _, err := usage.SetUsagePolicy(context.Background(), policy, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for round, body := range bodies {
		runManagedChild(t, paths, environmentID, false, childManagedBody+"="+body, childManagedResponse+"="+response)
		var got, want any
		if err := json.Unmarshal([]byte(<-observed), &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(body), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d changed native request: got %#v, want %#v", round, got, want)
		}
	}
	assertManagedEvidence(t, host, environmentID, accountID)
	page, err := host.Runtime().Activities().ListExchanges(context.Background(), activity.PageRequest{Limit: 1, EnvironmentID: environmentID.String()})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("native exchange record = %+v, %v", page, err)
	}
	content, err := host.Runtime().ExchangeContents().Get(context.Background(), page.Items[0].SubjectID)
	if err != nil || content.Response == nil || host.Runtime().Status().RecordingFailure != nil {
		t.Fatalf("native response evidence was not recorded: response=%+v, err=%v, warning=%+v", content.Response, err, host.Runtime().Status().RecordingFailure)
	}
	now := time.Now().UTC()
	period, err := runtimeusage.NewQuery(now.AddDate(0, 0, -1).Format("2006-01-02"), now.AddDate(0, 0, 1).Format("2006-01-02"), "UTC")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = usage.ScanUsage(context.Background(), runtimeusage.AggregationQuery{Period: period}, "", now, func(bucket runtimeusage.UsageBucket) error {
		if bucket.Status != activity.StatusSucceeded || !bucket.Usage.InputUncached.Known || bucket.Usage.InputUncached.Tokens != 4 ||
			!bucket.Usage.Output.Known || bucket.Usage.Output.Tokens != 2 {
			t.Errorf("native terminal lost its status or usage: %+v", bucket)
		}
		calls += bucket.Calls
		return nil
	})
	if err != nil || calls != len(bodies) {
		t.Fatalf("native usage calls = %d, want %d: %v", calls, len(bodies), err)
	}
}

func startManagedHost(
	t *testing.T,
	paths desktophost.Paths,
	dataDirectory string,
	factory hostsecret.DevelopmentFileFactory,
	catalog clientadapter.Catalog,
) (*desktophost.Host, secretstore.Store) {
	t.Helper()
	secrets, err := factory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	options := hostOptions(t, paths, dataDirectory)
	options.Runtime.Secrets = secrets
	options.ClientCatalog = catalog
	return startHost(t, options), secrets
}

func fixedSelfTestCatalog(t *testing.T) clientadapter.Catalog {
	t.Helper()
	executable, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	catalog, err := clientadapter.NewCatalog(1, []clientadapter.Release{{
		ID: "claude-code", Revision: 1, Version: "test",
		OperatingSystem: runtime.GOOS, Architecture: runtime.GOARCH,
		InstallShape:    clientadapter.InstallNativeSingleBinary,
		InvocationLabel: filepath.Base(executable), ArtifactRoot: ".",
		Artifacts: []clientadapter.Artifact{{
			Role: clientadapter.ArtifactEntrypoint, SHA256: hex.EncodeToString(digest[:]),
		}},
		LaunchRecipe: clientadapter.LaunchNodeEnvProxy,
		Features:     clientadapter.FeatureCoreOwnedStreamingFallback,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func createManagedAccount(
	t *testing.T,
	accounts provideraccount.Controller,
	endpointID upstreamendpoint.ID,
) provideraccount.ID {
	t.Helper()
	return createManagedAccountWithDriver(
		t, accounts, endpointID, providerauth.AnthropicAPIKeyDriverRef(),
	)
}

func createManagedAccountWithDriver(
	t *testing.T,
	accounts provideraccount.Controller,
	endpointID upstreamendpoint.ID,
	driver providerauth.DriverRef,
) provideraccount.ID {
	t.Helper()
	id, err := provideraccount.NewID(managedAccountID)
	if err != nil {
		t.Fatal(err)
	}
	material, err := providerauth.NewMaterial(managedSecret, nil, nil)
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
	view, err := accounts.Create(context.Background(), provideraccount.CreateCommand{
		ID: id, DisplayName: "Anthropic test", UpstreamEndpointID: endpointID,
		Driver: driver, Secret: secret,
	})
	if err != nil || view.Account.Revision != 1 ||
		view.Health.CredentialEpoch != 1 {
		t.Fatalf("create managed account = %+v, %v", view, err)
	}
	return id
}

func createManagedEndpoint(
	t *testing.T,
	endpoints upstreamendpoint.Controller,
	providerURL string,
) upstreamendpoint.ID {
	t.Helper()
	id, err := upstreamendpoint.NewID(managedEndpointID)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := originidentity.ParseProviderOrigin(providerURL)
	if err != nil {
		t.Fatal(err)
	}
	view, err := endpoints.Create(context.Background(), upstreamendpoint.CreateCommand{
		ID: id, DisplayName: "Anthropic test relay", Origin: origin,
		RealmID: "anthropic.official", BackendProtocols: []string{"anthropic_messages"},
		Capabilities: []protocolspec.ProviderCapability{
			protocolspec.ProviderCapabilityMessages,
			protocolspec.ProviderCapabilityStreaming,
			protocolspec.ProviderCapabilityToolCalls,
		},
		Drivers: []providerauth.DriverRef{
			providerauth.AnthropicAPIKeyDriverRef(), providerauth.StaticHeaderDriverRef(),
		},
	})
	if err != nil || view.Revision != 1 {
		t.Fatalf("create managed UpstreamEndpoint = %+v, %v", view, err)
	}
	return id
}

func publishManagedEnvironment(
	t *testing.T,
	host *desktophost.Host,
	providerURL string,
	upstreamEndpointID upstreamendpoint.ID,
	accountID provideraccount.ID,
	configure ...func(*environment.Environment),
) environment.EnvironmentID {
	t.Helper()
	id, err := environment.NewEnvironmentID(managedEnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	clientOrigin, err := originidentity.ParseClientOrigin("https://api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	providerOrigin, err := originidentity.ParseProviderOrigin(providerURL)
	if err != nil {
		t.Fatal(err)
	}
	endpointID, err := environment.NewClientEndpointID("endpoint.anthropic")
	if err != nil {
		t.Fatal(err)
	}
	planID, err := environment.NewClientProtocolPlanID("plan.anthropic")
	if err != nil {
		t.Fatal(err)
	}
	routeID, err := environment.NewUpstreamRouteID("route.anthropic")
	if err != nil {
		t.Fatal(err)
	}
	aggregate := environment.Environment{
		ID: id, Name: "Managed Anthropic", State: environment.StateActive, Revision: 1,
		ContentRecording: environment.DefaultContentRecordingPolicy(),
		ClientEndpoints: []environment.ClientEndpoint{{
			ID: endpointID, Revision: 1, ClientOrigin: clientOrigin,
			ProtocolPlans: []environment.ClientProtocolPlan{{
				ID: planID, Revision: 1,
				ClientProtocol: environment.ClientProtocolAnthropicMessages,
				ClientAdapterPolicy: environment.ClientAdapterPolicy{
					ID: "adapter.claude", Revision: 1,
				},
				EgressProfile: egressprofile.Direct(),
				Destination: environment.DestinationPlan{
					Kind: environment.DestinationKindUpstream,
					Upstream: &environment.UpstreamPlan{
						DefaultRouteID: routeID,
						RouteSet: environment.RouteSet{
							ID: "routes.anthropic", Revision: 1,
							CandidateRouteIDs: []environment.UpstreamRouteID{routeID},
						},
						Routes: []environment.UpstreamRoute{{
							ID: routeID, Revision: 1,
							ProviderTarget: environment.ProviderTarget{
								ID: upstreamEndpointID.String(), Revision: 1,
								Origin: providerOrigin, RealmID: "anthropic.official",
								Capabilities: []protocolspec.ProviderCapability{
									protocolspec.ProviderCapabilityMessages,
									protocolspec.ProviderCapabilityStreaming,
									protocolspec.ProviderCapabilityToolCalls,
								},
							},
							BackendProtocol: string(environment.ClientProtocolAnthropicMessages),
							AccountPolicy: environment.RouteAccountPolicy{
								Revision: 1, Mode: environment.AccountSelectionFixed,
								FixedAccountID: accountID.String(),
								Accounts: []environment.RouteAccountReference{{
									ID: accountID.String(), Revision: 1, DisplayName: "Anthropic test",
								}},
							},
							ModelPolicy:    environment.ModelPolicy{Revision: 1, Mode: "passthrough"},
							WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue,
						}},
					},
				},
			}},
		}},
	}
	for _, apply := range configure {
		apply(&aggregate)
	}
	draft, err := host.Runtime().Environments().SaveDraft(
		context.Background(),
		environment.DraftCommand{Candidate: aggregate},
	)
	if err != nil {
		t.Fatalf("save managed Environment: %v", err)
	}
	preview, err := host.Runtime().Environments().Preview(
		context.Background(), id, draft.Revision,
	)
	if err != nil {
		t.Fatalf("preview managed Environment: %v", err)
	}
	result, err := host.Runtime().Environments().Publish(context.Background(), preview)
	if err != nil || result.Outcome != environment.CommitOutcomeCommitted {
		t.Fatalf("publish managed Environment = %+v, %v", result, err)
	}
	return id
}

func resolveEnvironmentDigest(
	t *testing.T,
	host *desktophost.Host,
	id environment.EnvironmentID,
) string {
	t.Helper()
	snapshot, err := host.Runtime().EnvironmentResolver().Resolve(id)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Digest().String()
}

func runManagedChild(
	t *testing.T,
	paths desktophost.Paths,
	environmentID environment.EnvironmentID,
	expectFailure bool,
	extraEnvironment ...string,
) {
	t.Helper()
	discovery, err := localdiscovery.NewFile(
		paths.DiscoveryPath(),
		hostClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	baseEnvironment := []string{
		"PATH=/usr/bin:/bin",
		childManagedEnvironment + "=1",
		"ANTHROPIC_API_KEY=client-ambient-secret",
		"ANTHROPIC_AUTH_TOKEN=client-ambient-auth-token",
		"CLAUDE_CODE_OAUTH_TOKEN=client-oauth-secret",
	}
	if expectFailure {
		baseEnvironment = append(baseEnvironment, childManagedFailure+"=1")
	}
	baseEnvironment = append(baseEnvironment, extraEnvironment...)
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery:       discovery,
		BaseEnvironment: baseEnvironment,
		Stdin:           strings.NewReader(""), Stdout: io.Discard, Stderr: &stderr,
		HeartbeatInterval: 10 * time.Millisecond,
		ControlTimeout:    2 * time.Second, CreateTimeout: 5 * time.Second,
		TerminationTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	exitCode, err := launcher.Run(ctx, runlauncher.LaunchRequest{
		EnvironmentID: environmentID,
		Command:       []string{os.Args[0]},
	})
	if err != nil || exitCode != 0 {
		t.Fatalf("managed child exit=%d err=%v stderr=%s", exitCode, err, stderr.String())
	}
}

type hostClock struct{}

func (hostClock) Now() time.Time { return time.Now().UTC() }

func assertManagedObservation(t *testing.T, got managedProviderObservation) {
	t.Helper()
	if got.path != "/v1/messages" || got.xAPIKey != managedSecret ||
		got.authorization != "" || got.apiKey != "" || got.cookie != "" ||
		!strings.Contains(got.body, `"content":"managed route"`) {
		t.Fatalf("managed provider observation = %+v", got)
	}
}

func assertManagedEvidence(
	t *testing.T,
	host *desktophost.Host,
	environmentID environment.EnvironmentID,
	accountID provideraccount.ID,
) {
	t.Helper()
	page, err := host.Runtime().Activities().ListExchanges(
		context.Background(),
		activity.PageRequest{Limit: 20, EnvironmentID: environmentID.String()},
	)
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("managed Activity = %+v, %v", page, err)
	}
	record := page.Items[0]
	if record.Status != activity.StatusSucceeded ||
		record.AccountID != accountID.String() || record.AccountRevision != 1 ||
		record.CredentialEpoch != 1 || record.RouteID != "route.anthropic" {
		t.Fatalf("managed Activity record = %+v", record)
	}
	egress, err := host.Runtime().EgressAttempts().List(
		context.Background(),
		egressaudit.PageRequest{
			Limit: 20, ExchangeID: record.SubjectID,
			Purpose: egressaudit.PurposeProviderAttempt,
		},
	)
	if err != nil || len(egress.Items) != 1 {
		t.Fatalf("managed EgressAttempt = %+v, %v", egress, err)
	}
	attempt := egress.Items[0].Attempt
	if !attempt.Terminal() || attempt.Outcome() != egressaudit.OutcomeCompleted ||
		attempt.TargetOrigin() == "https://api.anthropic.com" ||
		attempt.BytesOut() == 0 || attempt.BytesIn() == 0 {
		t.Fatalf("managed EgressAttempt = %+v", egressaudit.ViewOf(egress.Items[0]))
	}
}
