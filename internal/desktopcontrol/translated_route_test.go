package desktopcontrol_test

import (
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

// A draft is compiled by the production compiler when it is saved, so these
// drafts show which problem a user sees for a route the compiler refuses.
func TestEnvironmentDraftExplainsRefusedTranslations(t *testing.T) {
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
	for key, account := range map[string]string{
		"translated-route-account-chatgpt":   `{"id":"chatgpt-work","displayName":"ChatGPT Work","upstreamEndpointId":"target.codex.official","kind":"bearer_token","secret":"synthetic-test-only-secret"}`,
		"translated-route-account-anthropic": `{"id":"anthropic-work","displayName":"Anthropic Work","upstreamEndpointId":"target.claude.official","kind":"anthropic_api_key","secret":"synthetic-test-only-secret"}`,
	} {
		created := environmentRequest(t, application, http.MethodPost, "/api/v1/provider-accounts", 0, key, []byte(account))
		if created.Code != http.StatusCreated {
			t.Fatalf("create account status=%d body=%s", created.Code, created.Body.Bytes())
		}
	}
	chatGPTTarget := `{"id":"target.codex.official","revision":1,"origin":"https://chatgpt.com","realmId":"openai.chatgpt","capabilities":["messages","streaming","tool_calls"]}`
	anthropicTarget := `{"id":"target.claude.official","revision":1,"origin":"https://api.anthropic.com","realmId":"anthropic.official","capabilities":["messages","streaming","tool_calls"]}`
	for _, test := range []struct {
		name, environmentID, clientProtocol, clientOrigin, target, backend, account, displayName string
		status                                                                                   int
		code                                                                                     string
	}{
		{
			"Claude Code to a ChatGPT plan", "claude-chatgpt", "anthropic_messages", "https://api.anthropic.com",
			chatGPTTarget, "openai_responses", "chatgpt-work", "ChatGPT Work",
			http.StatusUnprocessableEntity, "environment_chatgpt_plan_native_only",
		},
		{
			"Codex to Anthropic Messages", "codex-anthropic", "openai_responses", "https://api.openai.com",
			anthropicTarget, "anthropic_messages", "anthropic-work", "Anthropic Work",
			http.StatusUnprocessableEntity, "environment_translation_unavailable",
		},
		{
			"Codex to its ChatGPT plan", "codex-chatgpt", "openai_responses", "https://api.openai.com",
			chatGPTTarget, "openai_responses", "chatgpt-work", "ChatGPT Work",
			http.StatusOK, "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{
  "expectedDraftRevision":0,
  "name":"Translated route",
  "state":"active",
  "clientEndpoints":[{
    "id":"endpoint.client",
    "revision":1,
    "clientOrigin":"` + test.clientOrigin + `",
    "protocolPlans":[{
      "id":"plan.client",
      "revision":1,
      "clientProtocol":"` + test.clientProtocol + `",
      "clientAdapterPolicy":{"id":"adapter.client","revision":1},
      "destination":{
        "kind":"upstream",
        "upstream":{
          "routes":[{
            "id":"route.client",
            "revision":1,
            "providerTarget":` + test.target + `,
            "backendProtocol":"` + test.backend + `",
            "accountPolicy":{"revision":1,"mode":"fixed","fixedAccountId":"` + test.account + `","accounts":[{"id":"` + test.account + `","revision":1,"displayName":"` + test.displayName + `"}]},
            "modelPolicy":{"revision":1,"mode":"passthrough","mappings":[]},
            "wireProfileRef":"follow-client",
            "pluginBindings":[]
          }],
          "defaultRouteId":"route.client",
          "routeSet":{"id":"routes.client","revision":1,"candidateRouteIds":["route.client"]}
        }
      },
      "egressProfile":{"id":"profile.direct","revision":1,"displayName":"Direct \u00b7 System DNS","policy":{"proxy":{"kind":"direct"},"resolver":{"kind":"system","transport":"direct"}},"publishedAt":"1970-01-01T00:00:00Z"},
      "transforms":[],
      "pluginBindings":[]
    }]
  }],
  "pluginBindings":[],
  "budgetPolicy":{"id":"","revision":0},
  "contentRecording":{"mode":"full","retentionDays":30}
}`)
			draft := environmentRequest(t, application, http.MethodPut,
				"/api/v1/environments/"+test.environmentID+"/draft", 0, "translated-route-draft-"+test.environmentID, body)
			if draft.Code != test.status {
				t.Fatalf("draft status=%d body=%s", draft.Code, draft.Body.Bytes())
			}
			if test.code != "" {
				assertJSONString(t, draft.Body.Bytes(), "code", test.code)
			}
		})
	}
}
