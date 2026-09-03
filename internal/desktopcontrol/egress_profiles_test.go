package desktopcontrol_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

func TestEgressProfilesPublishImmutableRevisions(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(),
		Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(),
		Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(),
		CodeLibrary: runtime.CodeLibrary(), EgressProfiles: runtime.EgressProfiles(),
		Offline: runtime, Clock: desktopcontrol.SystemClock{}, ManualCaptures: runtime.ManualCaptures(),
	})
	if err != nil {
		t.Fatal(err)
	}

	first := environmentRequest(t, application, http.MethodPut,
		"/api/v1/egress-profiles/profile.office", 0, "publish-egress-profile-0001",
		[]byte(`{
			"displayName":"Office",
			"policy":{
				"proxy":{"kind":"socks5","endpoint":"127.0.0.1:7890"},
				"resolver":{"kind":"doh","dohUrl":"https://8.8.8.8/dns-query","transport":"proxy"}
			}
		}`))
	assertEgressProfileRevision(t, first, 1, "127.0.0.1:7890")

	second := environmentRequest(t, application, http.MethodPut,
		"/api/v1/egress-profiles/profile.office", 1, "publish-egress-profile-0002",
		[]byte(`{
			"displayName":"Office",
			"policy":{
				"proxy":{"kind":"socks5","endpoint":"127.0.0.1:7891"},
				"resolver":{"kind":"system","transport":"direct"}
			}
		}`))
	assertEgressProfileRevision(t, second, 2, "127.0.0.1:7891")

	catalog := environmentRequest(t, application, http.MethodGet, "/api/v1/egress-profiles", 0, "", nil)
	if catalog.Code != http.StatusOK {
		t.Fatalf("list profiles status=%d body=%s", catalog.Code, catalog.Body.Bytes())
	}
	var listed struct {
		Items []struct {
			ID       string `json:"id"`
			Revision uint64 `json:"revision"`
		} `json:"items"`
	}
	if err := json.Unmarshal(catalog.Body.Bytes(), &listed); err != nil ||
		len(listed.Items) != 2 || listed.Items[0].ID != "profile.direct" ||
		listed.Items[1].ID != "profile.office" || listed.Items[1].Revision != 2 {
		t.Fatalf("profiles=%+v err=%v body=%s", listed, err, catalog.Body.Bytes())
	}

	historical := environmentRequest(t, application, http.MethodGet,
		"/api/v1/egress-profiles/profile.office/revisions/1", 0, "", nil)
	assertEgressProfileRevision(t, historical, 1, "127.0.0.1:7890")
}

func assertEgressProfileRevision(t *testing.T, response interface {
	Result() *http.Response
}, wantRevision uint64, wantEndpoint string) {
	t.Helper()
	httpResponse := response.Result()
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode != http.StatusOK {
		var body any
		_ = json.NewDecoder(httpResponse.Body).Decode(&body)
		t.Fatalf("egress profile status=%d body=%v", httpResponse.StatusCode, body)
	}
	var profile struct {
		Revision uint64 `json:"revision"`
		Policy   struct {
			Proxy struct {
				Endpoint string `json:"endpoint"`
			} `json:"proxy"`
		} `json:"policy"`
	}
	if err := json.NewDecoder(httpResponse.Body).Decode(&profile); err != nil ||
		profile.Revision != wantRevision || profile.Policy.Proxy.Endpoint != wantEndpoint {
		t.Fatalf("profile=%+v want revision=%d endpoint=%q err=%v", profile, wantRevision, wantEndpoint, err)
	}
}
