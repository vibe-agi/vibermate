package desktopcontrol_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"net/http"
	"path/filepath"
	"testing"
)

func TestEnvironmentExtendedPublishedChoiceReopens(t *testing.T) {
	for _, proxy := range []egressnetwork.ProxyPolicy{{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:1080"}, {Kind: egressnetwork.ProxyVLESS, CredentialRef: "secret://egress-vless/control-fixture", CredentialRevision: 1}} {
		t.Run(string(proxy.Kind), func(t *testing.T) {
			paths, err := productruntime.NewRuntimePaths(filepath.Join(t.TempDir(), "data"))
			if err != nil {
				t.Fatal(err)
			}
			secrets := newCredentialStoreFixture()
			runtime := startRuntimeAtPaths(t, paths, secrets)
			defer func() { shutdownRuntime(t, runtime) }()
			appFor := func() *desktopcontrol.Handler {
				app, e := desktopcontrol.New(desktopcontrol.Options{Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), EgressProfiles: runtime.EgressProfiles(), Offline: runtime, Clock: desktopcontrol.SystemClock{}, ManualCaptures: runtime.ManualCaptures()})
				if e != nil {
					t.Fatal(e)
				}
				return app
			}
			p, err := runtime.EgressProfiles().Publish(t.Context(), egressprofile.PublishCommand{ID: "profile.extended", DisplayName: "Extended", Policy: egressnetwork.Policy{Proxy: proxy, DirectDomains: "excluded.example"}})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			body := []byte(fmt.Sprintf(`{"expectedDraftRevision":0,"name":"Extended","state":"active","clientEndpoints":[{"id":"endpoint.extended","revision":1,"clientOrigin":"https://api.anthropic.com","protocolPlans":[{"id":"plan.extended","revision":1,"clientProtocol":"anthropic_messages","clientAdapterPolicy":{"id":"adapter.extended","revision":1},"destination":{"kind":"original"},"egressProfile":%s,"pluginBindings":[]}]}],"pluginBindings":[],"budgetPolicy":{"id":"","revision":0},"contentRecording":{"mode":"full","retentionDays":30}}`, encoded))
			app := appFor()
			response := environmentRequest(t, app, http.MethodPut, "/api/v1/environments/extended/draft", 0, "extended-draft-0001", body)
			if response.Code != 200 {
				t.Fatalf("draft %d %s", response.Code, response.Body.Bytes())
			}
			response = environmentRequest(t, app, http.MethodPost, "/api/v1/environments/extended/draft/actions/publish", 1, "extended-publish-0001", nil)
			if response.Code != 200 {
				t.Fatalf("publish %d %s", response.Code, response.Body.Bytes())
			}
			shutdownRuntime(t, runtime)
			runtime = startRuntimeAtPaths(t, paths, secrets)
			response = environmentRequest(t, appFor(), http.MethodGet, "/api/v1/environments/extended/revisions/1", 0, "", nil)
			if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), encoded) {
				t.Fatalf("reopened profile changed: %d %s", response.Code, response.Body.Bytes())
			}
		})
	}
}
