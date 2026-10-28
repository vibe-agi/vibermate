package desktopcontrol_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

// ADR 0025: the capture scope is saved, previewed and published through the
// Control API like the rest of an Environment, and returned as stored.
func TestEnvironmentCaptureScopeWireRoundTrip(t *testing.T) {
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
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
	draft := func(revision uint64, key string, scope any) *bytes.Buffer {
		body, _ := json.Marshal(map[string]any{
			"expectedDraftRevision": 0, "name": "Work", "state": "active",
			"clientEndpoints": []any{}, "pluginBindings": []any{},
			"budgetPolicy":     map[string]any{"id": "", "revision": 0},
			"contentRecording": map[string]any{"mode": "off", "retentionDays": 0},
			"captureScope":     scope,
		})
		response := environmentRequest(t, application, http.MethodPut, "/api/v1/environments/work/draft", revision, key, body)
		return response.Body
	}

	refused := environmentRequest(t, application, http.MethodPut, "/api/v1/environments/work/draft", 0, "capture-scope-public-001",
		mustJSON(t, map[string]any{
			"expectedDraftRevision": 0, "name": "Work", "state": "active",
			"clientEndpoints": []any{}, "pluginBindings": []any{},
			"budgetPolicy":     map[string]any{"id": "", "revision": 0},
			"contentRecording": map[string]any{"mode": "off", "retentionDays": 0},
			"captureScope": map[string]any{"rules": []any{
				map[string]any{"id": "public", "kind": "prefix", "value": "8.8.8.0/24"},
			}},
		}))
	if refused.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a public prefix was accepted: %d %s", refused.Code, refused.Body.String())
	}

	scope := map[string]any{
		"rules": []any{
			map[string]any{"id": "lan", "kind": "prefix", "value": "192.168.0.0/16"},
			map[string]any{"id": "intranet", "kind": "suffix", "value": "corp.example"},
		},
		"allowUserAdditions": true,
	}
	saved := draft(0, "capture-scope-draft-001", scope)
	var response struct {
		Candidate struct {
			CaptureScope struct {
				Rules []struct {
					ID string `json:"id"`
				} `json:"rules"`
				AllowUserAdditions bool `json:"allowUserAdditions"`
			} `json:"captureScope"`
		} `json:"candidate"`
	}
	if err := json.Unmarshal(saved.Bytes(), &response); err != nil {
		t.Fatalf("draft response %s: %v", saved.String(), err)
	}
	rules := response.Candidate.CaptureScope.Rules
	if len(rules) != 2 || rules[0].ID != "intranet" || rules[1].ID != "lan" || !response.Candidate.CaptureScope.AllowUserAdditions {
		t.Fatalf("saved capture scope = %+v (rules are stored by ID)", response.Candidate.CaptureScope)
	}
	for _, action := range []string{"preview", "publish"} {
		result := environmentRequest(t, application, http.MethodPost, "/api/v1/environments/work/draft/actions/"+action, 1, "capture-scope-"+action+"-001", nil)
		if result.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", action, result.Code, result.Body.String())
		}
		if action == "publish" && !bytes.Contains(result.Body.Bytes(), []byte(`"captureScope"`)) {
			t.Fatalf("published Environment lost its capture scope: %s", result.Body.String())
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
