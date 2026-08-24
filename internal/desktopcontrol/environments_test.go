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
	"github.com/vibe-agi/vibermate/internal/environment"
)

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

	draftBody := []byte(`{
  "expectedDraftRevision": 0,
  "name": "Work",
  "state": "active",
  "clientEndpoints": [],
  "pluginBindings": [],
  "budgetPolicy": {"id":"","revision":0},
  "egressPolicy": {"id":"","revision":0,"mode":""},
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
      "pluginBindings":[]
    }]
  }],
  "pluginBindings": [],
  "budgetPolicy": {"id":"","revision":0},
  "egressPolicy": {"id":"","revision":0,"mode":""},
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
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime,
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
            "preferredAccountId":"account.cherry.bearer",
            "candidateAccountIds":["account.cherry.bearer"],
            "accountRevisions":{"account.cherry.bearer":1},
            "failoverPolicy":"off"
          },
          "modelPolicy":{"revision":1,"mode":"map","mappings":[{"requestedModel":"claude-client-alias","upstreamModel":"dashscope:deepseek-v4-flash-0731"}]},
          "wireProfileRef":"follow-client",
          "pluginBindings":[]
        }],
        "defaultRouteId":"route.cherry.anthropic",
        "routeSet":{"id":"routes.client.anthropic","revision":1,"candidateRouteIds":["route.cherry.anthropic"]}
        }
      },
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
            "preferredAccountId":"account.cherry.bearer",
            "candidateAccountIds":["account.cherry.bearer"],
            "accountRevisions":{"account.cherry.bearer":1},
            "failoverPolicy":"off"
          },
          "modelPolicy":{"revision":1,"mode":"passthrough","mappings":[]},
          "wireProfileRef":"follow-client",
          "pluginBindings":[]
        }],
        "defaultRouteId":"route.cherry.openai",
        "routeSet":{"id":"routes.client.openai","revision":1,"candidateRouteIds":["route.cherry.openai"]}
        }
      },
      "pluginBindings":[]
    }]
  }],
  "pluginBindings": [],
  "budgetPolicy": {"id":"","revision":0},
  "egressPolicy": {"id":"","revision":0,"mode":""},
  "contentRecording": {"mode":"full","retentionDays":30}
}`)
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
		openAIRoute.AccountPolicy.PreferredAccountID != "account.cherry.bearer" {
		t.Fatalf("published shared-account OpenAI route = %#v", openAIRoute)
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
		EgressPolicy:          published.EgressPolicy,
		ContentRecording:      published.ContentRecording,
		PolicySet:             &policySet,
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
