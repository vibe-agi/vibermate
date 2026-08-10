package desktopcontrol_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

func TestProviderAccountControlStoresCredentialWithoutReturningItAndCompilesManagedEnvironment(
	t *testing.T,
) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime,
		Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(),
		Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
		Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime,
		ManualCaptures: runtime.ManualCaptures(), Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}

	const secret = "sk-ant-private-control-sentinel"
	created := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/provider-accounts",
		0,
		"provider-account-create-0001",
		[]byte(`{
  "id":"anthropic-work",
  "displayName":"Anthropic Work",
	"upstreamEndpointId":"target.claude.official",
  "kind":"anthropic_api_key",
  "secret":"`+secret+`"
}`),
	)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.Bytes())
	}
	assertProviderAccountResponseSafe(t, created.Body.Bytes(), secret)
	assertJSONString(t, created.Body.Bytes(), "id", "anthropic-work")
	assertJSONString(t, created.Body.Bytes(), "credentialState", "ready")
	assertJSONNumber(t, created.Body.Bytes(), "credentialEpoch", 1)

	for _, path := range []string{
		"/api/v1/provider-accounts",
		"/api/v1/provider-accounts/anthropic-work",
	} {
		response := environmentRequest(t, application, http.MethodGet, path, 0, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, response.Code, response.Body.Bytes())
		}
		assertProviderAccountResponseSafe(t, response.Body.Bytes(), secret)
	}

	replaced := environmentRequest(
		t,
		application,
		http.MethodPut,
		"/api/v1/provider-accounts/anthropic-work/credential",
		1,
		"provider-account-secret-0001",
		[]byte(`{"secret":"sk-ant-replaced-control-sentinel"}`),
	)
	if replaced.Code != http.StatusOK {
		t.Fatalf("replace status=%d body=%s", replaced.Code, replaced.Body.Bytes())
	}
	assertProviderAccountResponseSafe(t, replaced.Body.Bytes(), "sk-ant-replaced-control-sentinel")
	assertJSONNumber(t, replaced.Body.Bytes(), "credentialEpoch", 2)

	managedDraft := environmentRequest(
		t,
		application,
		http.MethodPut,
		"/api/v1/environments/managed-work/draft",
		0,
		"managed-environment-draft-0001",
		[]byte(`{
  "expectedDraftRevision":0,
  "name":"Managed work",
  "state":"active",
  "clientEndpoints":[{
    "id":"endpoint.managed.anthropic",
    "revision":1,
    "clientOrigin":"https://api.anthropic.com",
    "protocolPlans":[{
      "id":"plan.managed.anthropic",
      "revision":1,
      "clientProtocol":"anthropic_messages",
      "clientAdapterPolicy":{"id":"adapter.managed.anthropic","revision":1},
      "mode":"managed",
      "upstreamPlan":{
        "routes":[{
          "id":"route.managed.anthropic",
          "revision":1,
		  "providerTarget":{"id":"target.claude.official","revision":1,"origin":"https://api.anthropic.com","realmId":"anthropic.official","capabilities":["messages","streaming","tool_calls"]},
          "backendProtocol":"anthropic_messages",
		  "accountPolicy":{"revision":1,"mode":"managed","preferredAccountId":"anthropic-work","candidateAccountIds":["anthropic-work"],"accountRevisions":{"anthropic-work":1},"failoverPolicy":"off"},
          "modelPolicy":{"revision":1,"mode":"passthrough","fixedModel":""},
          "wireProfileRef":"follow-client",
          "pluginBindings":[]
        }],
        "defaultRouteId":"route.managed.anthropic",
        "routeSet":{"id":"routes.managed.anthropic","revision":1,"candidateRouteIds":["route.managed.anthropic"]}
      },
      "pluginBindings":[]
    }]
  }],
  "pluginBindings":[],
  "budgetPolicy":{"id":"","revision":0},
  "egressPolicy":{"id":"","revision":0,"mode":""},
  "contentRecording":{"mode":"full","retentionDays":30}
}`),
	)
	if managedDraft.Code != http.StatusOK {
		t.Fatalf("managed Environment draft status=%d body=%s", managedDraft.Code, managedDraft.Body.Bytes())
	}
	published := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/environments/managed-work/draft/actions/publish",
		1,
		"managed-environment-publish-0001",
		nil,
	)
	if published.Code != http.StatusOK {
		t.Fatalf("managed Environment publish status=%d body=%s", published.Code, published.Body.Bytes())
	}
	assertProviderAccountResponseSafe(t, published.Body.Bytes(), secret)

	blocked := environmentRequest(
		t,
		application,
		http.MethodDelete,
		"/api/v1/provider-accounts/anthropic-work",
		2,
		"provider-account-delete-blocked-0001",
		nil,
	)
	if blocked.Code != http.StatusOK {
		t.Fatalf("blocked delete status=%d body=%s", blocked.Code, blocked.Body.Bytes())
	}
	var blockedResult desktopcontrol.ProviderAccountDeleteResponse
	if err := json.Unmarshal(blocked.Body.Bytes(), &blockedResult); err != nil {
		t.Fatal(err)
	}
	if blockedResult.Deleted || blockedResult.ReferenceCount != 1 || len(blockedResult.References) != 1 ||
		blockedResult.References[0].EnvironmentID != "managed-work" ||
		blockedResult.References[0].EnvironmentRevision != 1 ||
		blockedResult.References[0].RouteID != "route.managed.anthropic" {
		t.Fatalf("blocked account deletion = %+v", blockedResult)
	}

	const unusedSecret = "sk-openai-unused-control-sentinel"
	unused := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/provider-accounts",
		0,
		"provider-account-create-unused-0001",
		[]byte(`{"id":"openai-unused","displayName":"Unused","upstreamEndpointId":"target.openai.official","kind":"openai_api_key","secret":"`+unusedSecret+`"}`),
	)
	if unused.Code != http.StatusCreated {
		t.Fatalf("create unused status=%d body=%s", unused.Code, unused.Body.Bytes())
	}
	deleted := environmentRequest(
		t,
		application,
		http.MethodDelete,
		"/api/v1/provider-accounts/openai-unused",
		1,
		"provider-account-delete-unused-0001",
		nil,
	)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete unused status=%d body=%s", deleted.Code, deleted.Body.Bytes())
	}
	var deletedResult desktopcontrol.ProviderAccountDeleteResponse
	if err := json.Unmarshal(deleted.Body.Bytes(), &deletedResult); err != nil {
		t.Fatal(err)
	}
	if !deletedResult.Deleted || deletedResult.ReferenceCount != 0 || len(deletedResult.References) != 0 {
		t.Fatalf("deleted account result = %+v", deletedResult)
	}
	assertProviderAccountResponseSafe(t, deleted.Body.Bytes(), unusedSecret)
	missing := environmentRequest(t, application, http.MethodGet, "/api/v1/provider-accounts/openai-unused", 0, "", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("deleted account GET status=%d body=%s", missing.Code, missing.Body.Bytes())
	}
}

func TestProviderAccountControlKeepsClaudeOAuthDistinctFromAnthropicAPIKey(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime,
		Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(),
		Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
		Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime,
		ManualCaptures: runtime.ManualCaptures(), Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}

	const token = "oauth-control-sentinel"
	created := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/provider-accounts",
		0,
		"provider-account-oauth-create-0001",
		[]byte(`{
  "id":"claude-oauth",
  "displayName":"Claude OAuth",
	"upstreamEndpointId":"target.claude.official",
  "kind":"claude_oauth_token",
  "secret":"`+token+`"
}`),
	)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.Bytes())
	}
	assertProviderAccountResponseSafe(t, created.Body.Bytes(), token)
	assertJSONString(t, created.Body.Bytes(), "kind", "claude_oauth_token")
	assertJSONString(t, created.Body.Bytes(), "realmId", "anthropic.official")
}

func assertProviderAccountResponseSafe(t *testing.T, body []byte, secret string) {
	t.Helper()
	for _, forbidden := range [][]byte{
		[]byte(secret), []byte("secretReference"), []byte("secret://"),
	} {
		if bytes.Contains(body, forbidden) {
			t.Fatalf("ProviderAccount response exposed secret material: %s", body)
		}
	}
}
