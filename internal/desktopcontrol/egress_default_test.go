package desktopcontrol_test

import (
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"net/http"
	"testing"
)

func TestEgressDefaultControlUsesRealRuntimeAndStrictMutation(t *testing.T) {
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	app, err := desktopcontrol.New(desktopcontrol.Options{Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), CodeLibrary: runtime.CodeLibrary(), EgressProfiles: runtime.EgressProfiles(), EgressDefaults: runtime.EgressDefaults(), Offline: runtime, Clock: desktopcontrol.SystemClock{}, ManualCaptures: runtime.ManualCaptures()})
	if err != nil {
		t.Fatal(err)
	}
	initial := environmentRequest(t, app, http.MethodGet, "/api/v1/egress-default", 0, "", nil)
	if initial.Code != http.StatusOK {
		t.Fatalf("default route status=%d", initial.Code)
	}
	var view struct {
		Revision  uint64
		Profile   any
		Effective struct {
			Source          string
			DefaultRevision uint64
		}
	}
	if json.Unmarshal(initial.Body.Bytes(), &view) != nil || view.Revision != 1 || view.Profile != nil || view.Effective.Source != "builtin" || view.Effective.DefaultRevision != 1 {
		t.Fatalf("initial %s", initial.Body.Bytes())
	}
	selected := environmentRequest(t, app, http.MethodPut, "/api/v1/egress-default", 1, "default-select-direct-0001", []byte(`{"profile":{"id":"profile.direct","revision":1}}`))
	if selected.Code != 200 {
		t.Fatalf("selected status=%d body=%s", selected.Code, selected.Body.Bytes())
	}
	replay := environmentRequest(t, app, http.MethodPut, "/api/v1/egress-default", 1, "default-select-direct-0001", []byte(`{"profile":{"id":"profile.direct","revision":1}}`))
	if replay.Code != 200 || replay.Body.String() != selected.Body.String() {
		t.Fatal("idempotent default changed")
	}
	for i, bad := range []string{`{}`, `{"Profile":null}`, `{"profile":null,"profile":null}`, `{"profile":null,"pro\u0066ile":null}`, `{"profile":{"id":"profile.direct","Revision":1}}`, `{"profile":{"id":"profile.direct","revision":1,"policy":{}}}`, `{"profile":{"id":"profile.direct","id":"profile.direct","revision":1}}`, `{"profile":{"id":"profile.direct","revision":null}}`} {
		response := environmentRequest(t, app, http.MethodPut, "/api/v1/egress-default", 2, "invalid-default-body-000"+string(rune('a'+i)), []byte(bad))
		if response.Code != 422 {
			t.Errorf("accepted body %s status%d", bad, response.Code)
		}
	}
	stale := environmentRequest(t, app, http.MethodPut, "/api/v1/egress-default", 1, "default-stale-clear-0001", []byte(`{"profile":null}`))
	if stale.Code != 409 {
		t.Fatalf("stale %d", stale.Code)
	}
	cleared := environmentRequest(t, app, http.MethodPut, "/api/v1/egress-default", 2, "default-clear-unset-0001", []byte(`{"profile":null}`))
	if cleared.Code != 200 {
		t.Fatalf("clear %d", cleared.Code)
	}
}
