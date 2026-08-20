package desktopcontrol_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

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
		[]byte(`{"id":"target.team.anthropic","displayName":"Team relay","origin":"https://relay.example.com","kind":"anthropic"}`),
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
		[]byte(`{"id":"target.spark.anthropic","displayName":"Spark","origin":"http://spark-2a59:8888","kind":"anthropic"}`),
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
