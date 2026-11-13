package desktopcontrol_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

// The real Runtime's reader answers the script counts. Its activity reader is
// wrapped, so a missing forward would make this route permanently report the
// counts as unavailable.
func TestScriptActivityRouteIsAnsweredByTheRealRuntime(t *testing.T) {
	t.Parallel()

	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness:    readyState(true),
		Status:       runtime,
		Environments: runtime.Environments(),
		Assignments:  runtime.CaptureAssignments(),
		Clock:        desktopcontrol.SystemClock{},
		Activities:   runtime.Activities(),
		Contents:     runtime.ExchangeContents(),
		Connections:  runtime.ConnectionEvents(),
		Egress:       runtime.EgressAttempts(),
		Approvals:    runtime.ToolApprovals(),
		Endpoints:    runtime.UpstreamEndpoints(),
		Accounts:     runtime.ProviderAccounts(),
		Offline:      runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	get := func(query string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/scripts/activity"+query, nil).WithContext(ctx)
		response := httptest.NewRecorder()
		application.ServeHTTP(response, request)
		return response
	}
	var body *httptest.ResponseRecorder
	for deadline := time.Now().Add(5 * time.Second); ; {
		body = get("?days=7")
		if body.Code == http.StatusOK || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if body.Code != http.StatusOK {
		t.Fatalf("script activity: %d %s", body.Code, body.Body.String())
	}
	var result activity.ScriptActivity
	if err := json.Unmarshal(body.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Scripts == nil || result.Since.IsZero() || result.CoveredFrom.Before(result.Since) {
		t.Fatalf("script activity body: %s", body.Body.String())
	}
	for _, query := range []string{"?days=0", "?days=91", "?days=x", "?days=7&days=8", "?since=1"} {
		if code := get(query).Code; code != http.StatusUnprocessableEntity {
			t.Fatalf("%s answered %d", query, code)
		}
	}
}
