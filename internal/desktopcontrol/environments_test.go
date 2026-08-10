package desktopcontrol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vibe-agi/vibermate/internal/activity"
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
		listed.Items[0].PluginBindings == nil {
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
	assertJSONString(t, preview.Body.Bytes(), "classification", "hot_switch")

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

func TestEnvironmentDraftAcceptsCanonicalOriginalCredentialRoute(t *testing.T) {
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
      "mode": "managed",
      "upstreamPlan": {
        "routes": [{
          "id": "route.claude.official",
          "revision": 1,
          "providerTarget": {
            "id":"target.claude.official",
            "revision":1,
            "origin":"https://api.anthropic.com",
            "realmId":"anthropic.official",
            "capabilities":["messages","streaming","tool_calls"]
          },
          "backendProtocol":"anthropic_messages",
          "accountPolicy": {
            "revision":1,
            "mode":"client_passthrough",
            "preferredAccountId":"",
            "candidateAccountIds":[],
            "accountRevisions":{},
            "failoverPolicy":"off"
          },
          "modelPolicy":{"revision":1,"mode":"passthrough","fixedModel":""},
          "wireProfileRef":"follow-client",
          "pluginBindings":[]
        }],
        "defaultRouteId":"route.claude.official",
        "routeSet":{"id":"routes.claude.official","revision":1,"candidateRouteIds":["route.claude.official"]}
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
