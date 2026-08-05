package desktopcontrol_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

// A person can read what is in force, change it, and see the change reflected
// without restarting anything.
func TestConnectionRulesAreReadableAndEditable(t *testing.T) {
	t.Parallel()

	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	now := time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)
	readToken := capability(0x31)
	writeToken := capability(0x32)
	authenticator, err := desktopcontrol.NewAuthenticator(
		desktopcontrol.CapabilityGrant{
			ReadToken:  readToken,
			WriteToken: writeToken,
			ExpiresAt:  now.Add(time.Hour),
		},
		fixedClock{now: now},
	)
	if err != nil {
		t.Fatal(err)
	}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness:       readyState(true),
		Status:          runtime,
		Accesses:        runtime.AccessWriter(),
		AccessDeletion:  runtime.AccessDeleter(),
		Clock:           desktopcontrol.SystemClock{},
		AccessCatalog:   runtime.AccessCatalog(),
		Resolver:        runtime.SnapshotResolver(),
		Credentials:     runtime.Credentials(),
		Activities:      runtime.Activities(),
		Connections:     runtime.ConnectionEvents(),
		Egress:          runtime.EgressAttempts(),
		Approvals:       runtime.ToolApprovals(),
		Offline:         runtime,
		ConnectionRules: runtime.ConnectionRules(),
	})
	if err != nil {
		t.Fatal(err)
	}
	const authority = "127.0.0.1:43131"
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{
		Authority:      authority,
		AllowedOrigins: []string{"tauri://localhost"},
		Authenticator:  authenticator,
		Application:    application,
		Bootstrap:      emptyBootstrap(),
		CLIControl: http.HandlerFunc(func(
			writer http.ResponseWriter,
			_ *http.Request,
		) {
			writer.WriteHeader(http.StatusNoContent)
		}),
		ManualCaptures:   rejectingManualCaptureHandler{},
		DesktopPrincipal: desktopManualPrincipal(t),
	})
	if err != nil {
		t.Fatal(err)
	}

	read := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/policies/connections",
		readToken,
		nil,
	)
	if read.Code != http.StatusOK {
		t.Fatalf("read status = %d body = %s", read.Code, read.Body)
	}
	var current desktopcontrol.ConnectionRuleSetResponse
	decodeResponse(t, read, &current)
	if current.Revision == 0 || current.Default.Decision != "ask" {
		t.Fatalf("shipped set = %+v", current)
	}

	body, err := json.Marshal(desktopcontrol.ConnectionRuleSetInput{
		Rules: []desktopcontrol.ConnectionRuleInput{{
			ID:       "allow.one-host",
			Priority: 100,
			Decision: "allow",
			Match:    "exact_host_port",
			Host:     "api.example.com",
			Port:     443,
		}},
		Default: desktopcontrol.ConnectionRuleInput{
			ID:       "default.deny",
			Decision: "deny",
			Match:    "any",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	written := patchRules(
		t,
		router,
		authority,
		writeToken,
		current.Revision,
		"connection-rules-replace-0001",
		body,
	)
	if written.Code != http.StatusOK {
		t.Fatalf("replace status = %d body = %s", written.Code, written.Body)
	}
	var replaced desktopcontrol.ConnectionRuleSetResponse
	decodeResponse(t, written, &replaced)
	if replaced.Revision != current.Revision+1 ||
		len(replaced.Rules) != 1 ||
		replaced.Rules[0].ID != "allow.one-host" {
		t.Fatalf("replaced set = %+v", replaced)
	}

	// The same change replayed against the revision it was prepared for is a
	// conflict, because that revision is no longer current.
	stale := patchRules(
		t,
		router,
		authority,
		writeToken,
		current.Revision,
		"connection-rules-replace-0002",
		body,
	)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale replace status = %d", stale.Code)
	}

	// A set that would allow everything by default is refused, and the rules
	// in force do not change.
	wildcard, err := json.Marshal(desktopcontrol.ConnectionRuleSetInput{
		Default: desktopcontrol.ConnectionRuleInput{
			ID:       "default.allow-everything",
			Decision: "allow",
			Match:    "any",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	refused := patchRules(
		t,
		router,
		authority,
		writeToken,
		replaced.Revision,
		"connection-rules-replace-0003",
		wildcard,
	)
	if refused.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wildcard status = %d body = %s", refused.Code, refused.Body)
	}
	// The set in force is still the one the accepted change installed.
	if runtime.ConnectionRules().Current().Default.Decision != "deny" {
		t.Fatal("a refused set changed the default in force")
	}
}

func patchRules(
	t *testing.T,
	handler http.Handler,
	authority string,
	token string,
	revision uint64,
	key string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()

	request := newRequest(
		http.MethodPatch,
		authority,
		"/api/v1/policies/connections",
		token,
		body,
	)
	request.Header.Set("If-Match", strconv.FormatUint(revision, 10))
	request.Header.Set("Idempotency-Key", key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
