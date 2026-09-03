package desktopcontrol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

func TestMessageTransformSampleRunsOneBoundedTurn(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(),
		Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(),
		Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(),
		Offline: runtime, Clock: desktopcontrol.SystemClock{},
		ManualCaptures: runtime.ManualCaptures(),
	})
	if err != nil {
		t.Fatal(err)
	}

	body := []byte(`{
		"wireProtocol":"anthropic_messages",
		"policy":{
			"requestJavaScript":"context.requested = JSON.parse(request.body).model; request.headers['x-sample'] = 'request';",
			"responseJavaScript":"const value = JSON.parse(response.body); value.model = context.requested; response.body = JSON.stringify(value); response.headers['x-context'] = context.requested;"
		}
	}`)
	response := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/message-transforms/actions/test",
		0,
		"",
		body,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("test transform status=%d body=%s", response.Code, response.Body.Bytes())
	}
	var result desktopcontrol.MessageTransformTestResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.RequestAfter.Method != http.MethodPost || result.RequestAfter.Path != "/v1/messages" ||
		result.RequestAfter.Headers.Get("X-Sample") != "request" ||
		result.ResponseAfter.StatusCode != http.StatusOK ||
		result.ResponseAfter.Headers.Get("X-Context") != "claude-sample" ||
		!strings.Contains(result.ResponseAfter.Body, `"model":"claude-sample"`) {
		t.Fatalf("transform sample result = %+v", result)
	}

	const privateError = "PRIVATE-SAMPLE-BODY-MUST-NOT-ECHO"
	failing := []byte(`{
		"wireProtocol":"anthropic_messages",
		"policy":{
			"requestJavaScript":"throw new Error('` + privateError + `');",
			"responseJavaScript":""
		}
	}`)
	failure := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/message-transforms/actions/test",
		0,
		"",
		failing,
	)
	if failure.Code != http.StatusUnprocessableEntity ||
		!bytes.Contains(failure.Body.Bytes(), []byte(`"code":"message_transform_test_failed"`)) ||
		!bytes.Contains(failure.Body.Bytes(), []byte(`"detail":"request · JavaScript execution failed"`)) ||
		bytes.Contains(failure.Body.Bytes(), []byte(privateError)) {
		t.Fatalf("failed sample status=%d body=%s", failure.Code, failure.Body.Bytes())
	}

	turnTimeBody, err := json.Marshal(desktopcontrol.MessageTransformTestInput{
		WireProtocol: "openai_responses",
		Policy: messagetransform.Policy{ResponseJavaScript: `const payload = JSON.parse(response.body);
if (response.streaming && !context.turnTimeShown && payload.type === "response.output_text.delta" && typeof payload.delta === "string") {
  const label = runtime.turn.startedAt + (runtime.device.timeZone ? " · " + runtime.device.timeZone : "");
  payload.delta = runtime.annotations.create("turn-time", label) + "\n" + payload.delta;
  context.turnTimeShown = true;
} else if (!response.streaming && Array.isArray(payload.output)) {
  for (let outputIndex = 0; outputIndex < payload.output.length; outputIndex += 1) {
    const item = payload.output[outputIndex];
    if (!Array.isArray(item.content)) continue;
    for (let contentIndex = 0; contentIndex < item.content.length; contentIndex += 1) {
      const part = item.content[contentIndex];
      if (part.type === "output_text" && typeof part.text === "string") {
        const label = runtime.turn.startedAt + (runtime.device.timeZone ? " · " + runtime.device.timeZone : "");
        part.text = runtime.annotations.create("turn-time", label) + "\n" + part.text;
        outputIndex = payload.output.length;
        break;
      }
    }
  }
}
response.body = JSON.stringify(payload);`},
	})
	if err != nil {
		t.Fatal(err)
	}
	turnTimeResponse := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/message-transforms/actions/test",
		0,
		"",
		turnTimeBody,
	)
	if turnTimeResponse.Code != http.StatusOK {
		t.Fatalf("turn-time sample status=%d body=%s", turnTimeResponse.Code, turnTimeResponse.Body.Bytes())
	}
	var turnTimeResult desktopcontrol.MessageTransformTestResult
	if err := json.Unmarshal(turnTimeResponse.Body.Bytes(), &turnTimeResult); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(turnTimeResult.ResponseAfter.Body, "vibermate:annotation:v1:turn-time:") ||
		!strings.Contains(turnTimeResult.ResponseAfter.Body, "2026-01-02T03:04:05Z · Etc/UTC") {
		t.Fatalf("OpenAI Responses turn-time sample did not demonstrate its change: %s", turnTimeResult.ResponseAfter.Body)
	}
}

func TestEnvironmentDraftPreviewPublishAndHistoricalRevisionRoutes(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(),
		Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(),
		Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(),
		Offline: runtime, Clock: desktopcontrol.SystemClock{},
		ManualCaptures: runtime.ManualCaptures(),
	})
	if err != nil {
		t.Fatal(err)
	}

	list := environmentRequest(t, application, http.MethodGet, "/api/v1/environments", 0, "", nil)
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte(`"id":"system_transparent"`)) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.Bytes())
	}
	var listed desktopcontrol.EnvironmentListResponse
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil ||
		len(listed.Items) == 0 ||
		listed.Items[0].ClientEndpoints == nil ||
		listed.Items[0].PluginBindings == nil ||
		len(listed.Items[0].ClientEndpoints) == 0 ||
		listed.Items[0].ClientEndpoints[0].ProtocolPlans == nil ||
		len(listed.Items[0].ClientEndpoints[0].ProtocolPlans) == 0 ||
		listed.Items[0].ClientEndpoints[0].ProtocolPlans[0].PluginBindings == nil {
		t.Fatalf("Environment directory emitted nullable collections: body=%s err=%v", list.Body.Bytes(), err)
	}

	legacyGlobalEgressBody := []byte(`{
  "expectedDraftRevision": 0,
  "name": "Work",
  "state": "active",
  "clientEndpoints": [],
  "pluginBindings": [],
  "budgetPolicy": {"id":"","revision":0},
  "egressProfile": {"id":"","revision":0,"displayName":"","policy":{},"publishedAt":""},
  "contentRecording": {"mode":"full","retentionDays":30}
}`)
	legacyGlobalEgress := environmentRequest(
		t,
		application,
		http.MethodPut,
		"/api/v1/environments/work/draft",
		0,
		"environment-legacy-global-egress-draft-0001",
		legacyGlobalEgressBody,
	)
	if legacyGlobalEgress.Code != http.StatusUnprocessableEntity {
		t.Fatalf(
			"legacy global egress policy status=%d body=%s",
			legacyGlobalEgress.Code,
			legacyGlobalEgress.Body.Bytes(),
		)
	}

	draftBody := []byte(`{
  "expectedDraftRevision": 0,
  "name": "Work",
  "state": "active",
  "clientEndpoints": [],
  "pluginBindings": [],
  "budgetPolicy": {"id":"","revision":0},
  "contentRecording": {"mode":"full","retentionDays":30}
}`)
	draft := environmentRequest(t, application, http.MethodPut, "/api/v1/environments/work/draft", 0, "environment-draft-0001", draftBody)
	if draft.Code != http.StatusOK {
		t.Fatalf("draft status=%d body=%s", draft.Code, draft.Body.Bytes())
	}
	assertJSONNumber(t, draft.Body.Bytes(), "draftRevision", 1)

	preview := environmentRequest(t, application, http.MethodPost, "/api/v1/environments/work/draft/actions/preview", 1, "environment-preview-01", nil)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.Bytes())
	}
	if !bytes.Contains(preview.Body.Bytes(), []byte(`"continuingCaptures":[]`)) ||
		bytes.Contains(preview.Body.Bytes(), []byte(`"classification"`)) {
		t.Fatalf("preview kept legacy switching semantics: %s", preview.Body.Bytes())
	}

	publish := environmentRequest(t, application, http.MethodPost, "/api/v1/environments/work/draft/actions/publish", 1, "environment-publish-01", nil)
	if publish.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", publish.Code, publish.Body.Bytes())
	}
	assertJSONString(t, publish.Body.Bytes(), "outcome", "committed")

	missingDraft := environmentRequest(t, application, http.MethodGet, "/api/v1/environments/work/draft", 0, "", nil)
	if missingDraft.Code != http.StatusNotFound {
		t.Fatalf("published draft remained current: status=%d body=%s", missingDraft.Code, missingDraft.Body.Bytes())
	}
	secondDraft := environmentRequest(t, application, http.MethodPut, "/api/v1/environments/work/draft", 1, "environment-draft-0002", draftBody)
	if secondDraft.Code != http.StatusOK {
		t.Fatalf("edit published Environment status=%d body=%s", secondDraft.Code, secondDraft.Body.Bytes())
	}
	assertJSONNumber(t, secondDraft.Body.Bytes(), "draftRevision", 2)

	for _, path := range []string{"/api/v1/environments/work", "/api/v1/environments/work/revisions/1"} {
		response := environmentRequest(t, application, http.MethodGet, path, 0, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, response.Code, response.Body.Bytes())
		}
		assertJSONString(t, response.Body.Bytes(), "id", "work")
		assertJSONNumber(t, response.Body.Bytes(), "revision", 1)
	}

	legacy := environmentRequest(t, application, http.MethodGet, "/api/v1/accesses", 0, "", nil)
	if legacy.Code != http.StatusNotFound {
		t.Fatalf("legacy Access route status=%d body=%s", legacy.Code, legacy.Body.Bytes())
	}
}

func TestEnvironmentDraftSavesPreviewsAndPublishesOriginalDestination(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(),
		Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(),
		Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(),
		Offline: runtime, Clock: desktopcontrol.SystemClock{},
		ManualCaptures: runtime.ManualCaptures(),
	})
	if err != nil {
		t.Fatal(err)
	}

	body := []byte(`{
  "expectedDraftRevision": 0,
  "name": "Claude original",
  "state": "active",
  "clientEndpoints": [{
    "id": "endpoint.claude.official",
    "revision": 1,
    "clientOrigin": "https://api.anthropic.com",
    "protocolPlans": [{
      "id": "plan.claude.official",
      "revision": 1,
      "clientProtocol": "anthropic_messages",
      "clientAdapterPolicy": {"id":"adapter.claude.official","revision":1},
      "destination": {"kind":"original"},
	  "egressProfile":{"id":"profile.direct","revision":1,"displayName":"Direct · System DNS","policy":{"proxy":{"kind":"direct"},"resolver":{"kind":"system","transport":"direct"}},"publishedAt":"1970-01-01T00:00:00Z"},
      "pluginBindings":[]
    }]
  }],
  "pluginBindings": [],
  "budgetPolicy": {"id":"","revision":0},
  "contentRecording": {"mode":"full","retentionDays":30}
}`)
	draft := environmentRequest(
		t,
		application,
		http.MethodPut,
		"/api/v1/environments/claude-original/draft",
		0,
		"environment-original-draft-0001",
		body,
	)
	if draft.Code != http.StatusOK {
		t.Fatalf("draft status=%d body=%s", draft.Code, draft.Body.Bytes())
	}
	if !bytes.Contains(draft.Body.Bytes(), []byte(`"clientOrigin":"https://api.anthropic.com"`)) {
		t.Fatalf("draft did not preserve the canonical default-port origin: %s", draft.Body.Bytes())
	}
	preview := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/environments/claude-original/draft/actions/preview",
		1,
		"environment-original-preview-0001",
		nil,
	)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.Bytes())
	}
	publish := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/environments/claude-original/draft/actions/publish",
		1,
		"environment-original-publish-0001",
		nil,
	)
	if publish.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", publish.Code, publish.Body.Bytes())
	}
	assertJSONString(t, publish.Body.Bytes(), "outcome", "committed")
}

func TestEnvironmentDraftPublishesOneAccountAcrossExplicitEndpointProtocols(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime,
		Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(),
		Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
		Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), CodeLibrary: runtime.CodeLibrary(),
		EgressProfiles: runtime.EgressProfiles(), Offline: runtime,
		ManualCaptures: runtime.ManualCaptures(), Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}

	relay := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(relay.Close)
	createdEndpoint := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/upstream-endpoints",
		0,
		"upstream-endpoint-cherry-create-0001",
		[]byte(`{"id":"target.cherry.anthropic","displayName":"Cherry Anthropic","origin":"`+relay.URL+`","backendProtocols":["anthropic_messages","openai_responses"]}`),
	)
	if createdEndpoint.Code != http.StatusCreated {
		t.Fatalf("create Endpoint status=%d body=%s", createdEndpoint.Code, createdEndpoint.Body.Bytes())
	}
	createdAccount := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/provider-accounts",
		0,
		"provider-account-cherry-create-0001",
		[]byte(`{"id":"account.cherry.bearer","displayName":"Cherry","upstreamEndpointId":"target.cherry.anthropic","kind":"bearer_token","secret":"test-only-secret"}`),
	)
	if createdAccount.Code != http.StatusCreated {
		t.Fatalf("create Account status=%d body=%s", createdAccount.Code, createdAccount.Body.Bytes())
	}
	publishEgressProfile := func(id, key, body string) []byte {
		t.Helper()
		response := environmentRequest(t, application, http.MethodPut,
			"/api/v1/egress-profiles/"+id, 0, key, []byte(body))
		if response.Code != http.StatusOK {
			t.Fatalf("publish egress profile status=%d body=%s", response.Code, response.Body.Bytes())
		}
		return response.Body.Bytes()
	}
	anthropicEgressJSON := publishEgressProfile(
		"profile.anthropic", "environment-egress-publish-0001",
		`{"displayName":"Anthropic proxy","policy":{"proxy":{"kind":"socks5","endpoint":"127.0.0.1:1080"},"resolver":{"kind":"doh","dohUrl":"https://resolver.example/dns-query","transport":"proxy"}}}`,
	)
	openAIEgressJSON := publishEgressProfile(
		"profile.openai", "environment-egress-publish-0002",
		`{"displayName":"OpenAI DoH","policy":{"proxy":{"kind":"direct"},"resolver":{"kind":"doh","dohUrl":"https://8.8.8.8/dns-query","transport":"direct"}}}`,
	)
	collection := environmentRequest(t, application, http.MethodPost,
		"/api/v1/code-library/collections", 0, "environment-transform-collection-0001",
		[]byte(`{"id":"privacy","displayName":"Privacy"}`))
	if collection.Code != http.StatusCreated {
		t.Fatalf("create Transform collection status=%d body=%s", collection.Code, collection.Body.Bytes())
	}
	publishedTransform := environmentRequest(t, application, http.MethodPut,
		"/api/v1/code-library/transforms/client-model-context", 0, "environment-transform-publish-0001",
		[]byte(`{"collectionId":"privacy","displayName":"Client model context","policy":{"requestJavaScript":"context.requested = JSON.parse(request.body).model;","responseJavaScript":"response.headers['x-requested-model'] = context.requested;"}}`))
	if publishedTransform.Code != http.StatusOK {
		t.Fatalf("publish Transform status=%d body=%s", publishedTransform.Code, publishedTransform.Body.Bytes())
	}
	var frozenTransform desktopcontrol.CodeLibraryTransformResponse
	if err := json.Unmarshal(publishedTransform.Body.Bytes(), &frozenTransform); err != nil {
		t.Fatal(err)
	}
	frozenTransformJSON, err := json.Marshal(frozenTransform)
	if err != nil {
		t.Fatal(err)
	}
	publishedSelector := environmentRequest(t, application, http.MethodPut,
		"/api/v1/code-library/account-selectors/endpoint-account", 0,
		"environment-selector-publish-0001",
		[]byte(`{"collectionId":"privacy","displayName":"Endpoint account","policy":{"javaScript":"selection.accountId = accounts[0].id;"}}`))
	if publishedSelector.Code != http.StatusOK {
		t.Fatalf("publish Account Selector status=%d body=%s", publishedSelector.Code, publishedSelector.Body.Bytes())
	}
	var frozenSelector desktopcontrol.CodeLibraryAccountSelectorResponse
	if err := json.Unmarshal(publishedSelector.Body.Bytes(), &frozenSelector); err != nil {
		t.Fatal(err)
	}
	frozenSelectorJSON, err := json.Marshal(frozenSelector)
	if err != nil {
		t.Fatal(err)
	}

	draftBody := []byte(`{
  "expectedDraftRevision": 0,
  "name": "Cherry mapped",
  "state": "active",
  "clientEndpoints": [{
    "id": "endpoint.client.anthropic",
    "revision": 1,
    "clientOrigin": "https://api.anthropic.com",
    "protocolPlans": [{
      "id": "plan.client.anthropic",
      "revision": 1,
      "clientProtocol": "anthropic_messages",
      "clientAdapterPolicy": {"id":"adapter.client.anthropic","revision":1},
      "destination": {
        "kind": "upstream",
        "upstream": {
        "routes": [{
          "id": "route.cherry.anthropic",
          "revision": 1,
          "providerTarget": {
            "id":"target.cherry.anthropic",
            "revision":1,
            "origin":"` + relay.URL + `",
            "realmId":"target.cherry.anthropic",
            "capabilities":["messages","streaming","tool_calls"]
          },
          "backendProtocol":"anthropic_messages",
          "accountPolicy": {
            "revision":1,
            "mode":"javascript",
            "selector":` + string(frozenSelectorJSON) + `,
            "accounts":[]
          },
          "modelPolicy":{"revision":1,"mode":"map","mappings":[{"requestedModel":"claude-client-alias","upstreamModel":"dashscope:deepseek-v4-flash-0731"}]},
          "wireProfileRef":"follow-client",
          "pluginBindings":[]
        }],
        "defaultRouteId":"route.cherry.anthropic",
        "routeSet":{"id":"routes.client.anthropic","revision":1,"candidateRouteIds":["route.cherry.anthropic"]}
        }
      },
	  "egressProfile":` + string(anthropicEgressJSON) + `,
	  "transforms":[` + string(frozenTransformJSON) + `],
      "pluginBindings":[]
    }]
  }, {
    "id": "endpoint.client.openai",
    "revision": 1,
    "clientOrigin": "https://api.openai.com",
    "protocolPlans": [{
      "id": "plan.client.openai",
      "revision": 1,
      "clientProtocol": "openai_responses",
      "clientAdapterPolicy": {"id":"adapter.client.openai","revision":1},
      "destination": {
        "kind": "upstream",
        "upstream": {
        "routes": [{
          "id": "route.cherry.openai",
          "revision": 1,
          "providerTarget": {
            "id":"target.cherry.anthropic",
            "revision":1,
            "origin":"` + relay.URL + `",
            "realmId":"target.cherry.anthropic",
            "capabilities":["messages","streaming","tool_calls"]
          },
          "backendProtocol":"openai_responses",
          "accountPolicy": {
            "revision":1,
            "mode":"fixed",
            "fixedAccountId":"account.cherry.bearer",
            "accounts":[]
          },
          "modelPolicy":{"revision":1,"mode":"passthrough","mappings":[]},
          "wireProfileRef":"follow-client",
          "pluginBindings":[]
        }],
        "defaultRouteId":"route.cherry.openai",
        "routeSet":{"id":"routes.client.openai","revision":1,"candidateRouteIds":["route.cherry.openai"]}
        }
      },
		  "egressProfile":` + string(openAIEgressJSON) + `,
	  "transforms":[],
      "pluginBindings":[]
    }]
  }],
  "pluginBindings": [],
  "budgetPolicy": {"id":"","revision":0},
	  "contentRecording": {"mode":"full","retentionDays":30}
}`)
	tamperedBody := bytes.Replace(
		draftBody,
		[]byte("context.requested = JSON.parse(request.body).model;"),
		[]byte("request.headers['x-unpublished-code'] = 'true';"),
		1,
	)
	if bytes.Equal(tamperedBody, draftBody) {
		t.Fatal("test did not alter the frozen Transform revision")
	}
	tampered := environmentRequest(
		t, application, http.MethodPut,
		"/api/v1/environments/cherry-mapped/draft", 0,
		"environment-cherry-tampered-draft-0001", tamperedBody,
	)
	if tampered.Code != http.StatusUnprocessableEntity {
		t.Fatalf("tampered Transform status=%d body=%s", tampered.Code, tampered.Body.Bytes())
	}
	tamperedSelectorBody := bytes.Replace(
		draftBody,
		[]byte("selection.accountId = accounts[0].id;"),
		[]byte("selection.accountId = accounts[accounts.length - 1].id;"),
		1,
	)
	if bytes.Equal(tamperedSelectorBody, draftBody) {
		t.Fatal("test did not alter the frozen Account Selector revision")
	}
	tamperedSelector := environmentRequest(
		t, application, http.MethodPut,
		"/api/v1/environments/cherry-mapped/draft", 0,
		"environment-cherry-selector-tampered-0001", tamperedSelectorBody,
	)
	if tamperedSelector.Code != http.StatusUnprocessableEntity {
		t.Fatalf("tampered Account Selector status=%d body=%s",
			tamperedSelector.Code, tamperedSelector.Body.Bytes())
	}
	tamperedEgressBody := bytes.Replace(
		draftBody,
		[]byte(`"endpoint":"127.0.0.1:1080"`),
		[]byte(`"endpoint":"127.0.0.1:1081"`),
		1,
	)
	if bytes.Equal(tamperedEgressBody, draftBody) {
		t.Fatal("test did not alter the frozen egress Profile revision")
	}
	tamperedEgress := environmentRequest(
		t, application, http.MethodPut,
		"/api/v1/environments/cherry-mapped/draft", 0,
		"environment-cherry-egress-tampered-0001", tamperedEgressBody,
	)
	if tamperedEgress.Code != http.StatusUnprocessableEntity {
		t.Fatalf("tampered egress Profile status=%d body=%s", tamperedEgress.Code, tamperedEgress.Body.Bytes())
	}
	draft := environmentRequest(
		t,
		application,
		http.MethodPut,
		"/api/v1/environments/cherry-mapped/draft",
		0,
		"environment-cherry-draft-0001",
		draftBody,
	)
	if draft.Code != http.StatusOK {
		t.Fatalf("draft status=%d body=%s", draft.Code, draft.Body.Bytes())
	}
	if !bytes.Contains(draft.Body.Bytes(), []byte(`"requestedModel":"claude-client-alias","upstreamModel":"dashscope:deepseek-v4-flash-0731"`)) {
		t.Fatalf("draft changed the provider-owned model ID: %s", draft.Body.Bytes())
	}
	preview := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/environments/cherry-mapped/draft/actions/preview",
		1,
		"environment-cherry-preview-0001",
		nil,
	)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.Bytes())
	}
	publish := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/environments/cherry-mapped/draft/actions/publish",
		1,
		"environment-cherry-publish-0001",
		nil,
	)
	if publish.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", publish.Code, publish.Body.Bytes())
	}
	updatedTransform := environmentRequest(t, application, http.MethodPut,
		"/api/v1/code-library/transforms/client-model-context", 1, "environment-transform-publish-0002",
		[]byte(`{"collectionId":"privacy","displayName":"Client model context","policy":{"requestJavaScript":"request.headers['x-library-revision'] = 'two';","responseJavaScript":""}}`))
	if updatedTransform.Code != http.StatusOK {
		t.Fatalf("publish newer Transform status=%d body=%s", updatedTransform.Code, updatedTransform.Body.Bytes())
	}
	updatedSelector := environmentRequest(t, application, http.MethodPut,
		"/api/v1/code-library/account-selectors/endpoint-account", 1,
		"environment-selector-publish-0002",
		[]byte(`{"collectionId":"privacy","displayName":"Endpoint account","policy":{"javaScript":"selection.accountId = accounts[accounts.length - 1].id;"}}`))
	if updatedSelector.Code != http.StatusOK {
		t.Fatalf("publish newer Account Selector status=%d body=%s",
			updatedSelector.Code, updatedSelector.Body.Bytes())
	}

	reopened := environmentRequest(
		t,
		application,
		http.MethodGet,
		"/api/v1/environments/cherry-mapped",
		0,
		"",
		nil,
	)
	if reopened.Code != http.StatusOK {
		t.Fatalf("reopen status=%d body=%s", reopened.Code, reopened.Body.Bytes())
	}
	var published desktopcontrol.EnvironmentResponse
	if err := json.Unmarshal(reopened.Body.Bytes(), &published); err != nil {
		t.Fatal(err)
	}
	route := &published.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
	if len(route.ModelPolicy.Mappings) != 1 ||
		route.ModelPolicy.Mappings[0].RequestedModel != "claude-client-alias" ||
		route.ModelPolicy.Mappings[0].UpstreamModel != "dashscope:deepseek-v4-flash-0731" {
		t.Fatalf("published model mappings = %#v", route.ModelPolicy.Mappings)
	}
	openAIRoute := &published.ClientEndpoints[1].ProtocolPlans[0].Destination.Upstream.Routes[0]
	if openAIRoute.BackendProtocol != "openai_responses" ||
		openAIRoute.ProviderTarget.ID != "target.cherry.anthropic" ||
		openAIRoute.AccountPolicy.Mode != environment.AccountSelectionFixed ||
		openAIRoute.AccountPolicy.FixedAccountID != "account.cherry.bearer" ||
		len(openAIRoute.AccountPolicy.Accounts) != 1 ||
		openAIRoute.AccountPolicy.Accounts[0].ID != "account.cherry.bearer" {
		t.Fatalf("published shared-account OpenAI route = %#v", openAIRoute)
	}
	if route.AccountPolicy.Mode != environment.AccountSelectionJavaScript ||
		route.AccountPolicy.Selector == nil || route.AccountPolicy.Selector.Revision != 1 ||
		len(route.AccountPolicy.Accounts) != 1 ||
		route.AccountPolicy.Accounts[0].ID != "account.cherry.bearer" {
		t.Fatalf("published selector route = %#v", route.AccountPolicy)
	}
	wantAnthropicEgress := egressnetwork.Policy{
		Proxy: egressnetwork.ProxyPolicy{
			Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:1080",
		},
		Resolver: egressnetwork.ResolverPolicy{
			Kind: egressnetwork.ResolverDoH, DoHURL: "https://resolver.example/dns-query",
			Transport: egressnetwork.ResolverTransportProxy,
		},
	}
	wantOpenAIEgress := egressnetwork.Policy{
		Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxyDirect},
		Resolver: egressnetwork.ResolverPolicy{
			Kind: egressnetwork.ResolverDoH, DoHURL: "https://8.8.8.8/dns-query",
			Transport: egressnetwork.ResolverTransportDirect,
		},
	}
	if got := published.ClientEndpoints[0].ProtocolPlans[0].EgressProfile.Policy; got != wantAnthropicEgress {
		t.Fatalf("published Anthropic egress policy = %#v, want %#v", got, wantAnthropicEgress)
	}
	if got := published.ClientEndpoints[1].ProtocolPlans[0].EgressProfile.Policy; got != wantOpenAIEgress {
		t.Fatalf("published OpenAI egress policy = %#v, want %#v", got, wantOpenAIEgress)
	}
	transforms := published.ClientEndpoints[0].ProtocolPlans[0].Transforms
	if len(transforms) != 1 || transforms[0].Revision != 1 ||
		transforms[0].Policy.RequestJavaScript != "context.requested = JSON.parse(request.body).model;" ||
		transforms[0].Policy.ResponseJavaScript != "response.headers['x-requested-model'] = context.requested;" {
		t.Fatalf("published message Transform revisions = %#v", transforms)
	}

	baseEndpointRevision := published.ClientEndpoints[0].Revision
	basePlanRevision := published.ClientEndpoints[0].ProtocolPlans[0].Revision
	baseRouteRevision := route.Revision
	baseModelPolicyRevision := route.ModelPolicy.Revision
	route.ModelPolicy.Mappings = append(route.ModelPolicy.Mappings, environment.ModelMapping{
		RequestedModel: "claude-second-alias",
		UpstreamModel:  "relay/custom:model_2",
	})
	route.ModelPolicy.Revision = baseModelPolicyRevision + 1
	route.Revision = baseRouteRevision + 2
	published.ClientEndpoints[0].ProtocolPlans[0].Revision = basePlanRevision + 2
	published.ClientEndpoints[0].Revision = baseEndpointRevision + 2
	policySet := published.PolicySet
	updateInput := desktopcontrol.EnvironmentDraftInput{
		ExpectedDraftRevision: 0,
		Name:                  published.Name,
		State:                 published.State,
		ClientEndpoints:       published.ClientEndpoints,
		PluginBindings:        published.PluginBindings,
		BudgetPolicy:          published.BudgetPolicy,
		ContentRecording:      published.ContentRecording,
		LaunchEnvironment: environment.LaunchEnvironmentPolicy{
			SetEnv:    map[string]string{"TEAM_CONTEXT": "team-a"},
			DeleteEnv: []string{"REMOVE_CONTEXT"},
		},
		PolicySet: &policySet,
	}
	cumulativeBody, err := json.Marshal(updateInput)
	if err != nil {
		t.Fatal(err)
	}
	cumulative := environmentRequest(
		t,
		application,
		http.MethodPut,
		"/api/v1/environments/cherry-mapped/draft",
		uint64(published.Revision),
		"environment-cherry-cumulative-draft-0001",
		cumulativeBody,
	)
	if cumulative.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cumulative draft status=%d body=%s", cumulative.Code, cumulative.Body.Bytes())
	}
	assertJSONString(t, cumulative.Body.Bytes(), "code", "invalid_control_request")

	// One UI review is one atomic graph transition. Every changed authority in
	// the exact request body must advance once even if several local gestures
	// edited the same Route before review.
	updateInput.ClientEndpoints[0].Revision = baseEndpointRevision + 1
	updateInput.ClientEndpoints[0].ProtocolPlans[0].Revision = basePlanRevision + 1
	updateInput.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].Revision = baseRouteRevision + 1
	canonicalBody, err := json.Marshal(updateInput)
	if err != nil {
		t.Fatal(err)
	}
	canonical := environmentRequest(
		t,
		application,
		http.MethodPut,
		"/api/v1/environments/cherry-mapped/draft",
		uint64(published.Revision),
		"environment-cherry-canonical-draft-0001",
		canonicalBody,
	)
	if canonical.Code != http.StatusOK {
		t.Fatalf("canonical draft status=%d body=%s", canonical.Code, canonical.Body.Bytes())
	}
	var saved desktopcontrol.EnvironmentDraftResponse
	if err := json.Unmarshal(canonical.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	canonicalPreview := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/environments/cherry-mapped/draft/actions/preview",
		uint64(saved.DraftRevision),
		"environment-cherry-canonical-preview-0001",
		nil,
	)
	if canonicalPreview.Code != http.StatusOK {
		t.Fatalf("canonical preview status=%d body=%s", canonicalPreview.Code, canonicalPreview.Body.Bytes())
	}
	canonicalPublish := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/environments/cherry-mapped/draft/actions/publish",
		uint64(saved.DraftRevision),
		"environment-cherry-canonical-publish-0001",
		nil,
	)
	if canonicalPublish.Code != http.StatusOK {
		t.Fatalf("canonical publish status=%d body=%s", canonicalPublish.Code, canonicalPublish.Body.Bytes())
	}
	finalView := environmentRequest(
		t,
		application,
		http.MethodGet,
		"/api/v1/environments/cherry-mapped",
		0,
		"",
		nil,
	)
	if finalView.Code != http.StatusOK ||
		!bytes.Contains(finalView.Body.Bytes(), []byte(`"requestedModel":"claude-second-alias","upstreamModel":"relay/custom:model_2"`)) {
		t.Fatalf("reopened Environment lost model mapping: status=%d body=%s", finalView.Code, finalView.Body.Bytes())
	}
	var finalEnvironment desktopcontrol.EnvironmentResponse
	if err := json.Unmarshal(finalView.Body.Bytes(), &finalEnvironment); err != nil {
		t.Fatal(err)
	}
	if finalEnvironment.LaunchEnvironment.SetEnv["TEAM_CONTEXT"] != "team-a" ||
		len(finalEnvironment.LaunchEnvironment.DeleteEnv) != 1 ||
		finalEnvironment.LaunchEnvironment.DeleteEnv[0] != "REMOVE_CONTEXT" {
		t.Fatalf(
			"reopened Environment lost LaunchEnvironmentPolicy: %+v",
			finalEnvironment.LaunchEnvironment,
		)
	}
	if got := finalEnvironment.ClientEndpoints[0].ProtocolPlans[0].EgressProfile.Policy; got != wantAnthropicEgress {
		t.Fatalf("reopened Anthropic egress policy = %#v, want %#v", got, wantAnthropicEgress)
	}
	if got := finalEnvironment.ClientEndpoints[1].ProtocolPlans[0].EgressProfile.Policy; got != wantOpenAIEgress {
		t.Fatalf("reopened OpenAI egress policy = %#v, want %#v", got, wantOpenAIEgress)
	}
}

func TestActivityRouteFiltersAndReturnsFrozenEnvironmentReferences(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	environmentID, err := environment.NewEnvironmentID("environment-activity")
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Activities().Record(context.Background(), activity.Event{
		Kind: activity.KindExchangeCompleted, EnvironmentID: environmentID,
		EnvironmentRevision: 3, EnvironmentDigest: "4141414141414141414141414141414141414141414141414141414141414141",
		ClientEndpointID: "endpoint-activity", ClientEndpointRevision: 2,
		ProtocolPlanID: "protocol-activity", ProtocolPlanRevision: 2,
		RouteID: "route-activity", RouteRevision: 4,
		SubjectID: "exchange-activity", Status: activity.StatusSucceeded,
		SourceKind: activity.SourceSystemProxy, SourceDisplayName: "System proxy",
		SourceRecognition: activity.SourceRecognitionUnknown,
		Conversation: agentconversation.Ref{
			ProjectionID: "exchange:exchange-activity",
			Kind:         agentconversation.KindIsolatedExchange,
			Evidence:     agentconversation.EvidenceUndecodedExchange,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(),
		Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(),
		Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(),
		Offline: runtime, Clock: desktopcontrol.SystemClock{},
		ManualCaptures: runtime.ManualCaptures(),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := environmentRequest(t, application, http.MethodGet, "/api/v1/activities?kind=exchange&environmentId=environment-activity", 0, "", nil)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"environment":{"id":"environment-activity","revision":3`)) {
		t.Fatalf("Activity status=%d body=%s", response.Code, response.Body.Bytes())
	}
	legacy := environmentRequest(t, application, http.MethodGet, "/api/v1/activities?accessId=legacy", 0, "", nil)
	if legacy.Code != http.StatusUnprocessableEntity {
		t.Fatalf("legacy Activity filter status=%d body=%s", legacy.Code, legacy.Body.Bytes())
	}
}

func TestConversationRouteReturnsFlatProjectionAndRejectsUnknownQuery(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	environmentID, err := environment.NewEnvironmentID("environment-conversation")
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Activities().Record(context.Background(), activity.Event{
		Kind: activity.KindExchangeCompleted, EnvironmentID: environmentID,
		EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("a", 64),
		ClientEndpointID: "endpoint-conversation", ClientEndpointRevision: 1,
		ProtocolPlanID: "protocol-conversation", ProtocolPlanRevision: 1,
		RouteID: "route-conversation", RouteRevision: 1,
		SubjectID: "exchange-conversation", Status: activity.StatusSucceeded,
		SourceKind: activity.SourceCaptureRun, SourceDisplayName: "Codex",
		SourceRecognition: activity.SourceRecognitionVerified,
		CaptureRunID:      "run-conversation", ConnectionID: "connection-conversation",
		Conversation: agentconversation.Ref{
			ProjectionID: "capture_run:run-conversation:agent:reviewer",
			DisplayName:  "reviewer", Kind: agentconversation.KindAgent,
			Evidence: agentconversation.EvidenceExplicitActor, Actor: "/root/reviewer",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(),
		Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(),
		Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(),
		Offline: runtime, Clock: desktopcontrol.SystemClock{}, ManualCaptures: runtime.ManualCaptures(),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := environmentRequest(t, application, http.MethodGet, "/api/v1/conversations?limit=1", 0, "", nil)
	if response.Code != http.StatusOK ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"id":"capture_run:run-conversation:agent:reviewer"`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"turnCount":1`)) {
		t.Fatalf("Conversation status=%d body=%s", response.Code, response.Body.Bytes())
	}
	filtered := environmentRequest(
		t,
		application,
		http.MethodGet,
		"/api/v1/conversations?limit=1&captureRunId=run-conversation",
		0,
		"",
		nil,
	)
	if filtered.Code != http.StatusOK ||
		!bytes.Contains(filtered.Body.Bytes(), []byte(`"turnCount":1`)) {
		t.Fatalf(
			"filtered Conversation status=%d body=%s",
			filtered.Code,
			filtered.Body.Bytes(),
		)
	}
	invalid := environmentRequest(t, application, http.MethodGet, "/api/v1/conversations?kind=exchange", 0, "", nil)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid Conversation query status=%d body=%s", invalid.Code, invalid.Body.Bytes())
	}
	conflicting := environmentRequest(
		t,
		application,
		http.MethodGet,
		"/api/v1/conversations?captureRunId=run-conversation&manualCaptureId=manual-one",
		0,
		"",
		nil,
	)
	if conflicting.Code != http.StatusUnprocessableEntity {
		t.Fatalf(
			"conflicting Conversation query status=%d body=%s",
			conflicting.Code,
			conflicting.Body.Bytes(),
		)
	}
}

func TestOriginalDestinationConversationDoesNotRequireSyntheticUpstreamRoute(
	t *testing.T,
) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	environmentID, err := environment.NewEnvironmentID("system_transparent")
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Activities().Record(context.Background(), activity.Event{
		Kind: activity.KindExchangeCompleted, EnvironmentID: environmentID,
		EnvironmentRevision: 1, EnvironmentDigest: strings.Repeat("a", 64),
		ClientEndpointID: "endpoint.system.chatgpt", ClientEndpointRevision: 1,
		ProtocolPlanID: "plan.system.chatgpt.responses", ProtocolPlanRevision: 1,
		SubjectID: "exchange-original-conversation", Status: activity.StatusSucceeded,
		SourceKind: activity.SourceCaptureRun, SourceDisplayName: "codex",
		SourceRecognition: activity.SourceRecognitionVerified,
		CaptureRunID:      "run-original-conversation",
		ConnectionID:      "connection-original-conversation",
		Conversation: agentconversation.Ref{
			ProjectionID: "capture_run:run-original-conversation:main",
			DisplayName:  "codex",
			Kind:         agentconversation.KindMain,
			Evidence:     agentconversation.EvidenceCaptureRun,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(),
		Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(),
		Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(),
		Offline: runtime, Clock: desktopcontrol.SystemClock{}, ManualCaptures: runtime.ManualCaptures(),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/api/v1/activities?kind=exchange&captureRunId=run-original-conversation",
		"/api/v1/conversations?captureRunId=run-original-conversation",
	} {
		response := environmentRequest(t, application, http.MethodGet, path, 0, "", nil)
		if response.Code != http.StatusOK ||
			!bytes.Contains(response.Body.Bytes(), []byte(`"id":"system_transparent"`)) ||
			bytes.Contains(response.Body.Bytes(), []byte(`"routeId"`)) ||
			bytes.Contains(response.Body.Bytes(), []byte(`"routeRevision"`)) ||
			bytes.Contains(response.Body.Bytes(), []byte(`"accountId"`)) {
			t.Fatalf("GET %s status=%d body=%s", path, response.Code, response.Body.Bytes())
		}
	}
}

func environmentRequest(t *testing.T, handler http.Handler, method, path string, revision uint64, key string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("If-Match", strconv.FormatUint(revision, 10))
		request.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertJSONString(t *testing.T, body []byte, field, expected string) {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil || object[field] != expected {
		t.Fatalf("field %s=%v want=%q err=%v body=%s", field, object[field], expected, err, body)
	}
}

func assertJSONNumber(t *testing.T, body []byte, field string, expected float64) {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil || object[field] != expected {
		t.Fatalf("field %s=%v want=%v err=%v body=%s", field, object[field], expected, err, body)
	}
}
