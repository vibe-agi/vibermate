package desktopcontrol_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/modelcatalog"
)

type clientModelDirectoryStub struct {
	provider string
	items    []modelcatalog.Metadata
}

func (stub *clientModelDirectoryStub) ListProvider(
	_ context.Context,
	provider string,
) ([]modelcatalog.Metadata, error) {
	stub.provider = provider
	return append([]modelcatalog.Metadata(nil), stub.items...), nil
}

func TestUpstreamEndpointControlOwnsTheAccountBoundary(t *testing.T) {
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

	listed := environmentRequest(t, application, http.MethodGet, "/api/v1/upstream-endpoints", 0, "", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.Bytes())
	}
	var page desktopcontrol.UpstreamEndpointPage
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 || page.Items[0].ID != "target.claude.official" ||
		page.Items[1].ID != "target.codex.official" || page.Items[2].ID != "target.openai.official" {
		t.Fatalf("built-in UpstreamEndpoints = %+v", page.Items)
	}

	created := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/upstream-endpoints",
		0,
		"upstream-endpoint-create-0001",
		[]byte(`{"id":"target.team.anthropic","displayName":"Team relay","origin":"https://relay.example.com","backendProtocols":["anthropic_messages"]}`),
	)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.Bytes())
	}
	assertJSONString(t, created.Body.Bytes(), "id", "target.team.anthropic")
	assertJSONString(t, created.Body.Bytes(), "origin", "https://relay.example.com")

	cleartext := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/upstream-endpoints",
		0,
		"upstream-endpoint-create-http-0001",
		[]byte(`{"id":"target.spark.anthropic","displayName":"Spark","origin":"http://spark-2a59:8888","backendProtocols":["anthropic_messages"]}`),
	)
	if cleartext.Code != http.StatusCreated {
		t.Fatalf("cleartext create status=%d body=%s", cleartext.Code, cleartext.Body.Bytes())
	}
	assertJSONString(t, cleartext.Body.Bytes(), "id", "target.spark.anthropic")
	assertJSONString(t, cleartext.Body.Bytes(), "origin", "http://spark-2a59:8888")

	account := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/provider-accounts",
		0,
		"provider-account-team-create-0001",
		[]byte(`{"id":"account.team.anthropic","displayName":"Team","upstreamEndpointId":"target.team.anthropic","kind":"anthropic_api_key","secret":"private-test-secret"}`),
	)
	if account.Code != http.StatusCreated {
		t.Fatalf("account status=%d body=%s", account.Code, account.Body.Bytes())
	}
	assertJSONString(t, account.Body.Bytes(), "upstreamEndpointId", "target.team.anthropic")
}

func TestUpstreamEndpointControlAllowsOneOriginAcrossExplicitProtocols(t *testing.T) {
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

	const sharedOrigin = "http://127.0.0.1:23333"
	for _, endpoint := range []struct {
		id        string
		name      string
		protocols string
	}{
		{id: "target.shared.openai", name: "Shared OpenAI", protocols: `["openai_responses","openai_chat"]`},
		{id: "target.shared.anthropic", name: "Shared Anthropic", protocols: `["anthropic_messages"]`},
	} {
		response := environmentRequest(
			t,
			application,
			http.MethodPost,
			"/api/v1/upstream-endpoints",
			0,
			"upstream-endpoint-shared-origin-"+endpoint.id,
			[]byte(`{"id":"`+endpoint.id+`","displayName":"`+endpoint.name+`","origin":"`+sharedOrigin+`","backendProtocols":`+endpoint.protocols+`}`),
		)
		if response.Code != http.StatusCreated {
			t.Fatalf(
				"create %s Endpoint at shared origin status=%d body=%s",
				endpoint.id,
				response.Code,
				response.Body.Bytes(),
			)
		}
		assertJSONString(t, response.Body.Bytes(), "origin", sharedOrigin)
	}

	listed := environmentRequest(
		t,
		application,
		http.MethodGet,
		"/api/v1/upstream-endpoints",
		0,
		"",
		nil,
	)
	if listed.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.Bytes())
	}
	var page desktopcontrol.UpstreamEndpointPage
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	protocolsByID := make(map[string][]string, len(page.Items))
	for _, endpoint := range page.Items {
		if endpoint.Origin == sharedOrigin {
			protocolsByID[endpoint.ID] = endpoint.BackendProtocols
		}
	}
	if len(protocolsByID) != 2 ||
		len(protocolsByID["target.shared.openai"]) != 2 ||
		protocolsByID["target.shared.openai"][0] != "openai_responses" ||
		protocolsByID["target.shared.openai"][1] != "openai_chat" ||
		len(protocolsByID["target.shared.anthropic"]) != 1 ||
		protocolsByID["target.shared.anthropic"][0] != "anthropic_messages" {
		t.Fatalf("shared-origin protocols = %#v", protocolsByID)
	}
}

func TestUpstreamEndpointControlCreatesOneExplicitMultiProtocolRealm(t *testing.T) {
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

	created := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/upstream-endpoints",
		0,
		"upstream-endpoint-multi-protocol-create-0001",
		[]byte(`{
  "id":"target.shared.multi",
  "displayName":"Shared protocol relay",
  "origin":"http://127.0.0.1:23333",
  "backendProtocols":["anthropic_messages","openai_responses","openai_chat"]
}`),
	)
	if created.Code != http.StatusCreated {
		t.Fatalf("create multi-protocol Endpoint status=%d body=%s", created.Code, created.Body.Bytes())
	}
	var endpoint desktopcontrol.UpstreamEndpointResponse
	if err := json.Unmarshal(created.Body.Bytes(), &endpoint); err != nil {
		t.Fatal(err)
	}
	if endpoint.ID != "target.shared.multi" || endpoint.RealmID != endpoint.ID ||
		len(endpoint.BackendProtocols) != 3 ||
		endpoint.BackendProtocols[0] != "anthropic_messages" ||
		endpoint.BackendProtocols[1] != "openai_responses" ||
		endpoint.BackendProtocols[2] != "openai_chat" ||
		len(endpoint.AccountKinds) != 2 {
		t.Fatalf("multi-protocol Endpoint = %#v", endpoint)
	}

	account := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/provider-accounts",
		0,
		"provider-account-multi-protocol-create-0001",
		[]byte(`{
  "id":"account.shared.multi",
  "displayName":"Shared Bearer",
  "upstreamEndpointId":"target.shared.multi",
  "kind":"bearer_token",
  "secret":"multi-protocol-test-secret"
}`),
	)
	if account.Code != http.StatusCreated {
		t.Fatalf("create multi-protocol Account status=%d body=%s", account.Code, account.Body.Bytes())
	}
	assertJSONString(t, account.Body.Bytes(), "upstreamEndpointId", "target.shared.multi")
	assertJSONString(t, account.Body.Bytes(), "realmId", "target.shared.multi")
}

func TestUpstreamEndpointControlRejectsImplicitOrInvalidProtocols(t *testing.T) {
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

	for _, test := range []struct {
		name string
		body string
	}{
		{
			name: "legacy single kind",
			body: `{"id":"target.invalid.kind","displayName":"Invalid","origin":"https://relay.example","kind":"anthropic"}`,
		},
		{
			name: "empty selection",
			body: `{"id":"target.invalid.empty","displayName":"Invalid","origin":"https://relay.example","backendProtocols":[]}`,
		},
		{
			name: "duplicate selection",
			body: `{"id":"target.invalid.duplicate","displayName":"Invalid","origin":"https://relay.example","backendProtocols":["anthropic_messages","anthropic_messages"]}`,
		},
		{
			name: "unknown protocol",
			body: `{"id":"target.invalid.unknown","displayName":"Invalid","origin":"https://relay.example","backendProtocols":["provider_inferred"]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := environmentRequest(
				t,
				application,
				http.MethodPost,
				"/api/v1/upstream-endpoints",
				0,
				"upstream-endpoint-invalid-protocols-"+strings.ReplaceAll(test.name, " ", "-"),
				[]byte(test.body),
			)
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.Bytes())
			}
		})
	}
}

func TestUpstreamEndpointModelsExposeLiveAvailability(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/models" {
			t.Fatalf("unexpected catalog request %s %s", request.Method, request.URL.Path)
		}
		if authorization := request.Header.Get("Authorization"); authorization != "Bearer private-model-catalog-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.Header.Get("X-Api-Key") != "" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = writer.Write([]byte(`{"data":[{"id":"dashscope:deepseek-v4-flash-0731","owned_by":"relay","max_model_len":1048576}]}`))
	}))
	t.Cleanup(server.Close)
	catalog, err := modelcatalog.New(modelcatalog.Options{
		Endpoints: runtime.UpstreamEndpoints(), Credentials: runtime.ProviderAccounts(),
		Transport: runtime, Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatalf("new model catalog: %v", err)
	}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime,
		Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(),
		Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
		Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Models: catalog, Offline: runtime,
		ManualCaptures: runtime.ManualCaptures(), Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	created := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/upstream-endpoints",
		0,
		"upstream-endpoint-models-create-0001",
		[]byte(`{"id":"target.spark.models","displayName":"DGX Spark","origin":"`+server.URL+`/v1","backendProtocols":["openai_responses","openai_chat"]}`),
	)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.Bytes())
	}
	account := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/provider-accounts",
		0,
		"provider-account-models-create-0001",
		[]byte(`{"id":"account.spark.models","displayName":"Spark catalog","upstreamEndpointId":"target.spark.models","kind":"bearer_token","secret":"private-model-catalog-secret"}`),
	)
	if account.Code != http.StatusCreated {
		t.Fatalf("create Account status=%d body=%s", account.Code, account.Body.Bytes())
	}

	models := environmentRequest(
		t, application, http.MethodGet,
		"/api/v1/upstream-endpoints/target.spark.models/models?accountId=account.spark.models&refresh=1", 0, "", nil,
	)
	if models.Code != http.StatusOK {
		t.Fatalf("models status=%d body=%s", models.Code, models.Body.Bytes())
	}
	var response desktopcontrol.UpstreamModelCatalogResponse
	if err := json.Unmarshal(models.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode models response: %v", err)
	}
	if response.EndpointID != "target.spark.models" || response.AccountID != "account.spark.models" ||
		response.AccountRevision != 1 || response.CredentialEpoch != 1 ||
		response.AvailabilitySource != modelcatalog.AvailabilitySourceEndpoint ||
		len(response.Models) != 1 || response.Models[0].ID != "dashscope:deepseek-v4-flash-0731" ||
		!response.Models[0].VerifiedAvailable || response.Models[0].ContextLimit != 1_048_576 {
		t.Fatalf("unexpected models response: %#v", response)
	}
}

func TestUpstreamEndpointModelsUsesTheSelectedAccountsAuthenticationMode(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)

	type observedAuthentication struct {
		authorization string
		anthropicKey  string
	}
	observed := make(chan observedAuthentication, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/models" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		authentication := observedAuthentication{
			authorization: request.Header.Get("Authorization"),
			anthropicKey:  request.Header.Get("X-Api-Key"),
		}
		observed <- authentication
		if authentication.authorization != "Bearer discovery-test-token" ||
			authentication.anthropicKey != "" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"id":"dashscope_deepseek-v4-flash-0731"}]}`))
	}))
	t.Cleanup(server.Close)
	catalog, err := modelcatalog.New(modelcatalog.Options{
		Endpoints: runtime.UpstreamEndpoints(), Credentials: runtime.ProviderAccounts(),
		Transport: runtime, Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatalf("new model catalog: %v", err)
	}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime,
		Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(),
		Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
		Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Models: catalog, Offline: runtime,
		ManualCaptures: runtime.ManualCaptures(), Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}

	created := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/upstream-endpoints",
		0,
		"upstream-endpoint-auth-mode-create-0001",
		[]byte(`{"id":"target.auth-mode.models","displayName":"Auth mode relay","origin":"`+server.URL+`","backendProtocols":["anthropic_messages"]}`),
	)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.Bytes())
	}
	for _, account := range []struct {
		id   string
		kind string
		key  string
	}{
		{id: "account.auth-mode.anthropic", kind: "anthropic_api_key", key: "provider-account-auth-mode-anthropic-0001"},
		{id: "account.auth-mode.bearer", kind: "bearer_token", key: "provider-account-auth-mode-bearer-0001"},
	} {
		response := environmentRequest(
			t,
			application,
			http.MethodPost,
			"/api/v1/provider-accounts",
			0,
			account.key,
			[]byte(`{"id":"`+account.id+`","displayName":"Discovery test","upstreamEndpointId":"target.auth-mode.models","kind":"`+account.kind+`","secret":"discovery-test-token"}`),
		)
		if response.Code != http.StatusCreated {
			t.Fatalf("create Account kind=%s status=%d body=%s", account.kind, response.Code, response.Body.Bytes())
		}
	}

	anthropic := environmentRequest(
		t, application, http.MethodGet,
		"/api/v1/upstream-endpoints/target.auth-mode.models/models?accountId=account.auth-mode.anthropic&refresh=1", 0, "", nil,
	)
	if anthropic.Code != http.StatusBadGateway {
		t.Fatalf("Anthropic-key discovery status=%d body=%s", anthropic.Code, anthropic.Body.Bytes())
	}
	assertJSONString(t, anthropic.Body.Bytes(), "code", "model_catalog_authentication_rejected")
	first := <-observed
	if first.authorization != "" || first.anthropicKey != "discovery-test-token" {
		t.Fatal("Anthropic API Key Account did not use X-Api-Key exclusively")
	}

	bearer := environmentRequest(
		t, application, http.MethodGet,
		"/api/v1/upstream-endpoints/target.auth-mode.models/models?accountId=account.auth-mode.bearer&refresh=1", 0, "", nil,
	)
	if bearer.Code != http.StatusOK {
		t.Fatalf("Bearer discovery status=%d body=%s", bearer.Code, bearer.Body.Bytes())
	}
	second := <-observed
	if second.authorization != "Bearer discovery-test-token" || second.anthropicKey != "" {
		t.Fatal("Bearer token Account did not use Authorization: Bearer exclusively")
	}
	var response desktopcontrol.UpstreamModelCatalogResponse
	if err := json.Unmarshal(bearer.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Models) != 1 || response.Models[0].ID != "dashscope_deepseek-v4-flash-0731" {
		t.Fatalf("opaque Endpoint model IDs changed: %#v", response.Models)
	}

	var otherEndpointRequests atomic.Int32
	otherServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		otherEndpointRequests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"id":"must-not-be-observed"}]}`))
	}))
	t.Cleanup(otherServer.Close)
	otherEndpoint := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/upstream-endpoints",
		0,
		"upstream-endpoint-auth-mode-other-0001",
		[]byte(`{"id":"target.auth-mode.other","displayName":"Other relay","origin":"`+otherServer.URL+`/v1","backendProtocols":["anthropic_messages"]}`),
	)
	if otherEndpoint.Code != http.StatusCreated {
		t.Fatalf("create other Endpoint status=%d body=%s", otherEndpoint.Code, otherEndpoint.Body.Bytes())
	}
	mismatched := environmentRequest(
		t, application, http.MethodGet,
		"/api/v1/upstream-endpoints/target.auth-mode.other/models?accountId=account.auth-mode.bearer&refresh=1", 0, "", nil,
	)
	if mismatched.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cross-Endpoint Account status=%d body=%s", mismatched.Code, mismatched.Body.Bytes())
	}
	if otherEndpointRequests.Load() != 0 {
		t.Fatal("cross-Endpoint Account reached the upstream transport")
	}
}

func TestClientModelsUseModelsDevOnlyForTheRequestSide(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	directory := &clientModelDirectoryStub{items: []modelcatalog.Metadata{{
		CanonicalID:  "anthropic/claude-opus-4-6",
		DisplayName:  "Claude Opus 4.6",
		Description:  "Request-side metadata only",
		Reasoning:    true,
		ToolCalls:    true,
		ContextLimit: 1_000_000,
		OutputLimit:  128_000,
	}}}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime,
		Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(),
		Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
		Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), ClientModels: directory, Offline: runtime,
		ManualCaptures: runtime.ManualCaptures(), Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}

	models := environmentRequest(
		t, application, http.MethodGet,
		"/api/v1/client-models?protocol=anthropic_messages", 0, "", nil,
	)
	if models.Code != http.StatusOK {
		t.Fatalf("client models status=%d body=%s", models.Code, models.Body.Bytes())
	}
	if directory.provider != "anthropic" {
		t.Fatalf("models.dev provider = %q, want anthropic", directory.provider)
	}
	var response desktopcontrol.ClientModelCatalogResponse
	if err := json.Unmarshal(models.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode client models response: %v", err)
	}
	if response.Protocol != "anthropic_messages" || response.ProviderID != "anthropic" ||
		response.MetadataSource != modelcatalog.MetadataSourceModelsDev || len(response.Models) != 1 ||
		response.Models[0].ID != "claude-opus-4-6" ||
		response.Models[0].CanonicalID != "anthropic/claude-opus-4-6" {
		t.Fatalf("unexpected client models response: %#v", response)
	}
}
