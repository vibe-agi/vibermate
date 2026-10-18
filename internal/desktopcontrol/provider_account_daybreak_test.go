package desktopcontrol_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

func TestAccountDaybreakBlueSettingsPreserveOldClientsAndUseSettingsCAS(t *testing.T) {
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
		"daybreak-create-account", []byte(`{"id":"daybreak","displayName":"Daybreak","upstreamEndpointId":"target.codex.official","kind":"bearer_token","secret":"synthetic-daybreak-bearer"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	assertDaybreakSettingsProjection(t, created.Body.Bytes(), false, 1)
	for _, step := range []struct {
		name     string
		expected uint64
		body     string
		want     bool
		revision uint64
	}{
		{"enable", 1, `{"automaticRefresh":false,"egressProfile":null,"daybreakBlueEnabled":true}`, true, 2},
		{"noop", 2, `{"automaticRefresh":false,"egressProfile":null,"daybreakBlueEnabled":true}`, true, 2},
		{"old-client", 2, `{"automaticRefresh":false,"egressProfile":null}`, true, 2},
		{"disable", 2, `{"automaticRefresh":false,"egressProfile":null,"daybreakBlueEnabled":false}`, false, 3},
	} {
		response := environmentRequest(t, application, http.MethodPut, "/api/v1/provider-accounts/daybreak/settings", step.expected, "daybreak-settings-"+step.name, []byte(step.body))
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", step.name, response.Code, response.Body.String())
		}
		assertDaybreakSettingsProjection(t, response.Body.Bytes(), step.want, step.revision)
	}
	stale := environmentRequest(t, application, http.MethodPut, "/api/v1/provider-accounts/daybreak/settings", 2, "daybreak-settings-stale", []byte(`{"automaticRefresh":false,"egressProfile":null,"daybreakBlueEnabled":true}`))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale settings accepted: %d %s", stale.Code, stale.Body.String())
	}
	for _, invalid := range []struct{ name, value string }{{"null", "null"}, {"string", `"true"`}, {"number", "1"}, {"object", "{}"}} {
		response := environmentRequest(t, application, http.MethodPut, "/api/v1/provider-accounts/daybreak/settings", 3, "daybreak-invalid-"+invalid.name, []byte(`{"automaticRefresh":false,"egressProfile":null,"daybreakBlueEnabled":`+invalid.value+`}`))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("nonboolean %s accepted: %d %s", invalid.value, response.Code, response.Body.String())
		}
	}
	got := environmentRequest(t, application, http.MethodGet, "/api/v1/provider-accounts/daybreak", 0, "", nil)
	if got.Code != http.StatusOK {
		t.Fatalf("get: %d %s", got.Code, got.Body.String())
	}
	assertDaybreakSettingsProjection(t, got.Body.Bytes(), false, 3)
}

func TestAccountDaybreakBlueSettingsRejectUnsupportedOrigin(t *testing.T) {
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
		"daybreak-api-create", []byte(`{"id":"api-account","displayName":"API account","upstreamEndpointId":"target.openai.official","kind":"bearer_token","secret":"synthetic-api-bearer"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	enabled := environmentRequest(t, application, http.MethodPut, "/api/v1/provider-accounts/api-account/settings", 1, "daybreak-api-enable", []byte(`{"automaticRefresh":false,"egressProfile":null,"daybreakBlueEnabled":true}`))
	if enabled.Code != http.StatusUnprocessableEntity {
		t.Fatalf("API origin enabled Daybreak Blue: %d %s", enabled.Code, enabled.Body.String())
	}
	disabled := environmentRequest(t, application, http.MethodPut, "/api/v1/provider-accounts/api-account/settings", 1, "daybreak-api-disable", []byte(`{"automaticRefresh":false,"egressProfile":null,"daybreakBlueEnabled":false}`))
	if disabled.Code != http.StatusOK {
		t.Fatalf("unsupported origin cannot unset: %d %s", disabled.Code, disabled.Body.String())
	}
	assertDaybreakSettingsProjection(t, disabled.Body.Bytes(), false, 1)
}

func assertDaybreakSettingsProjection(t *testing.T, body []byte, enabled bool, settingsRevision uint64) {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["daybreakBlueEnabled"] != enabled || fields["settingsRevision"] != float64(settingsRevision) || fields["revision"] != float64(1) {
		t.Fatalf("setting projection changed consent or account identity: %s", body)
	}
}
