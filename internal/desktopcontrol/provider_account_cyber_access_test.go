package desktopcontrol_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

func TestAccountCyberAccessSettingsPreserveOldClientsAndUseSettingsCAS(t *testing.T) {
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
	created := environmentRequest(t, application, http.MethodPost, "/api/v1/provider-accounts", 0,
		"cyber-create-account", []byte(`{"id":"cyber","displayName":"Cyber","upstreamEndpointId":"target.codex.official","kind":"bearer_token","secret":"synthetic-cyber-bearer"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	assertCyberAccessSettingsProjection(t, created.Body.Bytes(), "standard", 1)
	for _, step := range []struct {
		name     string
		expected uint64
		body     string
		want     string
		revision uint64
	}{
		{"standard-default", 1, `{"automaticRefresh":false,"egressProfile":null,"cyberAccessProgram":"standard"}`, "standard", 1},
		{"blue", 1, `{"automaticRefresh":false,"egressProfile":null,"cyberAccessProgram":"daybreak_blue"}`, "daybreak_blue", 2},
		{"red", 2, `{"automaticRefresh":false,"egressProfile":null,"cyberAccessProgram":"daybreak_red"}`, "daybreak_red", 3},
		{"noop", 3, `{"automaticRefresh":false,"egressProfile":null,"cyberAccessProgram":"daybreak_red"}`, "daybreak_red", 3},
		{"old-client", 3, `{"automaticRefresh":false,"egressProfile":null}`, "daybreak_red", 3},
		{"standard-reset", 3, `{"automaticRefresh":false,"egressProfile":null,"cyberAccessProgram":"standard"}`, "standard", 4},
	} {
		response := environmentRequest(t, application, http.MethodPut, "/api/v1/provider-accounts/cyber/settings", step.expected, "cyber-settings-"+step.name, []byte(step.body))
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", step.name, response.Code, response.Body.String())
		}
		assertCyberAccessSettingsProjection(t, response.Body.Bytes(), step.want, step.revision)
	}
	stale := environmentRequest(t, application, http.MethodPut, "/api/v1/provider-accounts/cyber/settings", 3, "cyber-settings-stale", []byte(`{"automaticRefresh":false,"egressProfile":null,"cyberAccessProgram":"daybreak_blue"}`))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale settings accepted: %d %s", stale.Code, stale.Body.String())
	}
	for _, invalid := range []struct{ name, value string }{{"null", "null"}, {"boolean-true", "true"}, {"boolean-false", "false"}, {"unknown", `"unknown"`}, {"follow-client", `"follow_client"`}, {"empty", `""`}, {"number", "1"}, {"object", "{}"}} {
		response := environmentRequest(t, application, http.MethodPut, "/api/v1/provider-accounts/cyber/settings", 4, "cyber-invalid-"+invalid.name, []byte(`{"automaticRefresh":false,"egressProfile":null,"cyberAccessProgram":`+invalid.value+`}`))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid preference %s accepted: %d %s", invalid.value, response.Code, response.Body.String())
		}
	}
	got := environmentRequest(t, application, http.MethodGet, "/api/v1/provider-accounts/cyber", 0, "", nil)
	if got.Code != http.StatusOK {
		t.Fatalf("get: %d %s", got.Code, got.Body.String())
	}
	assertCyberAccessSettingsProjection(t, got.Body.Bytes(), "standard", 4)
}

func TestAccountCyberAccessSettingsRejectUnsupportedOrigin(t *testing.T) {
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
	created := environmentRequest(t, application, http.MethodPost, "/api/v1/provider-accounts", 0,
		"cyber-api-create", []byte(`{"id":"api-account","displayName":"API account","upstreamEndpointId":"target.openai.official","kind":"bearer_token","secret":"synthetic-api-bearer"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	for _, preference := range []string{"daybreak_blue", "daybreak_red"} {
		enabled := environmentRequest(t, application, http.MethodPut, "/api/v1/provider-accounts/api-account/settings", 1, "cyber-api-"+preference, []byte(`{"automaticRefresh":false,"egressProfile":null,"cyberAccessProgram":"`+preference+`"}`))
		if enabled.Code != http.StatusUnprocessableEntity {
			t.Fatalf("API origin accepted %s: %d %s", preference, enabled.Code, enabled.Body.String())
		}
	}
	standard := environmentRequest(t, application, http.MethodPut, "/api/v1/provider-accounts/api-account/settings", 1, "cyber-api-standard", []byte(`{"automaticRefresh":false,"egressProfile":null,"cyberAccessProgram":"standard"}`))
	if standard.Code != http.StatusOK {
		t.Fatalf("unsupported origin rejected Standard: %d %s", standard.Code, standard.Body.String())
	}
	assertCyberAccessSettingsProjection(t, standard.Body.Bytes(), "standard", 1)
}

func assertCyberAccessSettingsProjection(t *testing.T, body []byte, preference string, settingsRevision uint64) {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["cyberAccessProgram"] != preference || fields["settingsRevision"] != float64(settingsRevision) || fields["revision"] != float64(1) {
		t.Fatalf("setting projection changed consent or account identity: %s", body)
	}
}
