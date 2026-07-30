package desktopcontrol_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accessapply"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func TestDesktopControlAppliesAccessAndControlsOfflineHoldWithScopedAuth(
	t *testing.T,
) {
	t.Parallel()

	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	now := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	readToken := capability(0x11)
	writeToken := capability(0x22)
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
		Readiness:   readyState(true),
		Status:      runtime,
		Accesses:    runtime.AccessWriter(),
		Resolver:    runtime.SnapshotResolver(),
		Credentials: runtime.Credentials(),
		Activities:  runtime.Activities(),
		Connections: runtime.ConnectionEvents(),
		Approvals:   runtime.ToolApprovals(),
		Offline:     runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	capture := http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.WriteHeader(http.StatusNoContent)
	})
	const authority = "127.0.0.1:43127"
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{
		Authority:      authority,
		AllowedOrigins: []string{"tauri://localhost"},
		Authenticator:  authenticator,
		Application:    application,
		Bootstrap:      emptyBootstrap(),
		CaptureRuns:    capture,
	})
	if err != nil {
		t.Fatal(err)
	}

	status := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/status",
		readToken,
		nil,
	)
	if status.Code != http.StatusOK {
		t.Fatalf("status code=%d body=%s", status.Code, status.Body.Bytes())
	}
	var statusBody desktopcontrol.StatusResponse
	decodeResponse(t, status, &statusBody)
	if !statusBody.Ready ||
		statusBody.Generation != runtime.Status().InstanceID ||
		statusBody.StatusKey != "runtime.state.initialized" {
		t.Fatalf("status response = %+v", statusBody)
	}

	emptyActivities := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/activities?limit=10",
		readToken,
		nil,
	)
	if emptyActivities.Code != http.StatusOK {
		t.Fatalf(
			"empty activities code=%d body=%s",
			emptyActivities.Code,
			emptyActivities.Body.Bytes(),
		)
	}
	var emptyActivityPage activity.Page
	decodeResponse(t, emptyActivities, &emptyActivityPage)
	if emptyActivityPage.Items == nil || len(emptyActivityPage.Items) != 0 {
		t.Fatalf("empty Activity page = %+v", emptyActivityPage)
	}
	emptyConnections := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/connections?limit=10",
		readToken,
		nil,
	)
	if emptyConnections.Code != http.StatusOK {
		t.Fatalf(
			"empty connections code=%d body=%s",
			emptyConnections.Code,
			emptyConnections.Body.Bytes(),
		)
	}
	var emptyConnectionPage connectionevent.Page
	decodeResponse(t, emptyConnections, &emptyConnectionPage)
	if emptyConnectionPage.Items == nil ||
		len(emptyConnectionPage.Items) != 0 {
		t.Fatalf("empty ConnectionEvent page = %+v", emptyConnectionPage)
	}
	connection, err := runtime.ConnectionEvents().Start(
		context.Background(),
		connectionevent.Attempt{
			Source: connectionevent.Source{
				Confidence: connectionevent.SourceConfidenceUnknown,
			},
			RequestedHost: "api.anthropic.com:443",
			Port:          443,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Decide(
		context.Background(),
		connectionevent.DecisionEvidence{
			Source: connectionevent.Source{
				IngressID:  "run-control",
				Label:      "claude",
				Confidence: connectionevent.SourceConfidenceConfigured,
			},
			Decision:   connectionevent.DecisionDeny,
			RuleID:     "control-test-deny",
			Decryption: connectionevent.DecryptionNone,
			ErrorClass: "control-test-deny",
		},
	); err != nil {
		t.Fatal(err)
	}
	connections := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/connections?limit=10",
		readToken,
		nil,
	)
	if connections.Code != http.StatusOK {
		t.Fatalf(
			"connections code=%d body=%s",
			connections.Code,
			connections.Body.Bytes(),
		)
	}
	var connectionPage connectionevent.Page
	decodeResponse(t, connections, &connectionPage)
	if len(connectionPage.Items) != 2 ||
		connectionPage.Items[0].Outcome != connectionevent.OutcomeDenied {
		t.Fatalf("ConnectionEvent page = %+v", connectionPage)
	}
	timelineResponse := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/connections/"+connection.ID(),
		readToken,
		nil,
	)
	if timelineResponse.Code != http.StatusOK {
		t.Fatalf(
			"connection timeline code=%d body=%s",
			timelineResponse.Code,
			timelineResponse.Body.Bytes(),
		)
	}
	var timeline connectionevent.Timeline
	decodeResponse(t, timelineResponse, &timeline)
	if timeline.ConnectionID != connection.ID() ||
		len(timeline.Events) != 2 {
		t.Fatalf("ConnectionEvent timeline = %+v", timeline)
	}
	invalidCursor := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/connections?cursor=42",
		readToken,
		nil,
	)
	if invalidCursor.Code != http.StatusUnprocessableEntity {
		t.Fatalf(
			"invalid cursor code=%d body=%s",
			invalidCursor.Code,
			invalidCursor.Body.Bytes(),
		)
	}

	initialHold := runtime.OfflineHoldSnapshot()
	entered := doMutation(
		t,
		router,
		authority,
		"/api/v1/offline-hold/actions/enter",
		writeToken,
		initialHold.Revision,
		"offline-enter-0001",
		nil,
	)
	if entered.Code != http.StatusOK {
		t.Fatalf("enter code=%d body=%s", entered.Code, entered.Body.Bytes())
	}
	var held offlinehold.Snapshot
	decodeResponse(t, entered, &held)
	if held.State != offlinehold.StateHeld || !held.SafeToDisconnect {
		t.Fatalf("held snapshot = %+v", held)
	}
	resumed := doMutation(
		t,
		router,
		authority,
		"/api/v1/offline-hold/actions/resume",
		writeToken,
		held.Revision,
		"offline-resume-001",
		nil,
	)
	if resumed.Code != http.StatusOK {
		t.Fatalf("resume code=%d body=%s", resumed.Code, resumed.Body.Bytes())
	}
	var online offlinehold.Snapshot
	decodeResponse(t, resumed, &online)
	if online.State != offlinehold.StateOnline {
		t.Fatalf("resumed snapshot = %+v", online)
	}

	notConfigured := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/accesses/missing-access/plan",
		readToken,
		nil,
	)
	if notConfigured.Code != http.StatusNotFound ||
		!bytes.Contains(
			notConfigured.Body.Bytes(),
			[]byte(`"reasonCode":"access_not_configured"`),
		) {
		t.Fatalf(
			"missing plan code=%d body=%s",
			notConfigured.Code,
			notConfigured.Body.Bytes(),
		)
	}

	input := validApplyInput()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	applied := doMutation(
		t,
		router,
		authority,
		"/api/v1/accesses/access-control/actions/apply",
		writeToken,
		0,
		"access-apply-0001",
		encoded,
	)
	if applied.Code != http.StatusOK {
		t.Fatalf("apply code=%d body=%s", applied.Code, applied.Body.Bytes())
	}
	appliedBody := append([]byte(nil), applied.Body.Bytes()...)
	var applyResult desktopcontrol.AccessApplyResponse
	decodeResponse(t, applied, &applyResult)
	if applyResult.Outcome != access.WriteOutcomeCommitted ||
		applyResult.Revision != 1 ||
		len(applyResult.PlanHash) != 64 {
		t.Fatalf("Access apply response = %+v", applyResult)
	}
	accessID, _ := access.NewAccessID("access-control")
	active, err := runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil || active.Revision() != 1 {
		t.Fatalf("active Access revision=%d err=%v", active.Revision(), err)
	}
	plan := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/accesses/access-control/plan",
		readToken,
		nil,
	)
	if plan.Code != http.StatusOK ||
		plan.Header().Get("ETag") != `"revision-1"` {
		t.Fatalf(
			"plan code=%d ETag=%q body=%s",
			plan.Code,
			plan.Header().Get("ETag"),
			plan.Body.Bytes(),
		)
	}
	var planSummary desktopcontrol.AccessPlanSummaryResponse
	decodeResponse(t, plan, &planSummary)
	if planSummary.AccessID != "access-control" ||
		planSummary.Revision != 1 ||
		planSummary.PlanHash != applyResult.PlanHash ||
		len(planSummary.Profiles) != 1 ||
		planSummary.Profiles[0] != "access-control-profile" ||
		len(planSummary.AccountBindings) != 1 ||
		planSummary.AccountBindings[0].ID != "access-control-account" ||
		planSummary.AccountBindings[0].ProfileID != "access-control-profile" {
		t.Fatalf("plan summary = %+v", planSummary)
	}

	replayed := doMutation(
		t,
		router,
		authority,
		"/api/v1/accesses/access-control/actions/apply",
		writeToken,
		0,
		"access-apply-0001",
		encoded,
	)
	if replayed.Code != applied.Code ||
		!bytes.Equal(replayed.Body.Bytes(), appliedBody) {
		t.Fatalf(
			"idempotent replay code=%d body=%s",
			replayed.Code,
			replayed.Body.Bytes(),
		)
	}
	input.Access.Name = "Different command"
	different, _ := json.Marshal(input)
	conflict := doMutation(
		t,
		router,
		authority,
		"/api/v1/accesses/access-control/actions/apply",
		writeToken,
		0,
		"access-apply-0001",
		different,
	)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("idempotency conflict code=%d body=%s", conflict.Code, conflict.Body.Bytes())
	}
	active, err = runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil ||
		active.Revision() != 1 ||
		active.Binding().Name != "Control Access" {
		t.Fatalf("idempotency conflict changed active plan: %+v err=%v", active.Binding(), err)
	}

	credentialPath := "/api/v1/accesses/access-control/profiles/" +
		"access-control-profile/credentials/access-control-account"
	missingCredential := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		credentialPath,
		readToken,
		nil,
	)
	if missingCredential.Code != http.StatusOK ||
		missingCredential.Header().Get("ETag") != `"revision-0"` {
		t.Fatalf(
			"missing credential code=%d ETag=%q body=%s",
			missingCredential.Code,
			missingCredential.Header().Get("ETag"),
			missingCredential.Body.Bytes(),
		)
	}
	var missingCredentialView struct {
		CredentialID   string               `json:"credentialId"`
		ProfileID      string               `json:"profileId"`
		SecretState    secretstore.State    `json:"secretState"`
		SecretRevision secretstore.Revision `json:"secretRevision"`
	}
	decodeResponse(t, missingCredential, &missingCredentialView)
	if missingCredentialView.SecretState != secretstore.StateMissing ||
		missingCredentialView.SecretRevision != 0 {
		t.Fatalf("missing credential = %+v", missingCredentialView)
	}
	secretBody := []byte(`{"secret":"provider-secret-value"}`)
	replacedCredential := doMutation(
		t,
		router,
		authority,
		credentialPath+"/actions/replace-secret",
		writeToken,
		0,
		"credential-replace-0001",
		secretBody,
	)
	if replacedCredential.Code != http.StatusOK {
		t.Fatalf(
			"replace credential code=%d body=%s",
			replacedCredential.Code,
			replacedCredential.Body.Bytes(),
		)
	}
	if bytes.Contains(replacedCredential.Body.Bytes(), []byte("provider-secret-value")) ||
		bytes.Contains(replacedCredential.Body.Bytes(), []byte("secret://")) {
		t.Fatalf("credential response exposed secret authority: %s", replacedCredential.Body.Bytes())
	}
	var configuredCredentialView struct {
		CredentialID   string               `json:"credentialId"`
		ProfileID      string               `json:"profileId"`
		SecretState    secretstore.State    `json:"secretState"`
		SecretRevision secretstore.Revision `json:"secretRevision"`
	}
	decodeResponse(t, replacedCredential, &configuredCredentialView)
	if configuredCredentialView.SecretState != secretstore.StateConfigured ||
		configuredCredentialView.SecretRevision != 1 {
		t.Fatalf("configured credential = %+v", configuredCredentialView)
	}
	staleCredential := doMutation(
		t,
		router,
		authority,
		credentialPath+"/actions/replace-secret",
		writeToken,
		0,
		"credential-replace-0002",
		[]byte(`{"secret":"different-provider-secret"}`),
	)
	if staleCredential.Code != http.StatusConflict {
		t.Fatalf(
			"stale credential code=%d body=%s",
			staleCredential.Code,
			staleCredential.Body.Bytes(),
		)
	}

	activities := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/activities?limit=10",
		readToken,
		nil,
	)
	if activities.Code != http.StatusOK {
		t.Fatalf("activities code=%d body=%s", activities.Code, activities.Body.Bytes())
	}
	var page activity.Page
	decodeResponse(t, activities, &page)
	foundApply := false
	foundCredential := false
	for _, record := range page.Items {
		if record.Kind == activity.KindAccessApplied &&
			record.AccessID == "access-control" {
			foundApply = true
		}
		if record.Kind == activity.KindCredentialSecretReplaced &&
			record.AccessID == "access-control" &&
			record.SubjectID == "access-control-account" {
			foundCredential = true
		}
	}
	if !foundApply || !foundCredential {
		t.Fatalf("Activity page does not contain Access apply: %+v", page)
	}
}

func TestDesktopControlApprovalRouteResolvesDurableAuthority(
	t *testing.T,
) {
	t.Parallel()

	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	openContext, cancelOpen := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	store, err := runtimepersistence.Open(
		openContext,
		runtimepersistence.Options{
			DatabasePath: filepath.Join(
				t.TempDir(),
				"approval-data",
				"runtime.db",
			),
			BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
			CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
		},
	)
	cancelOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownStore(t, store)
	approvalAuthority, err := toolapproval.New(
		context.Background(),
		toolapproval.DefaultOptions(store.ToolApprovalRepository()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownApprovalAuthority(t, approvalAuthority)
	now := time.Now().UTC()
	readToken := capability(0x25)
	writeToken := capability(0x26)
	authenticator, err := desktopcontrol.NewAuthenticator(
		desktopcontrol.CapabilityGrant{
			ReadToken:  readToken,
			WriteToken: writeToken,
			ExpiresAt:  now.Add(time.Minute),
		},
		fixedClock{now: now},
	)
	if err != nil {
		t.Fatal(err)
	}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness:   readyState(true),
		Status:      runtime,
		Accesses:    runtime.AccessWriter(),
		Resolver:    runtime.SnapshotResolver(),
		Credentials: runtime.Credentials(),
		Activities:  runtime.Activities(),
		Connections: runtime.ConnectionEvents(),
		Approvals:   approvalAuthority,
		Offline:     runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	const authority = "127.0.0.1:43129"
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{
		Authority:      authority,
		AllowedOrigins: []string{"tauri://localhost"},
		Authenticator:  authenticator,
		Application:    application,
		Bootstrap:      emptyBootstrap(),
		CaptureRuns: http.HandlerFunc(func(
			http.ResponseWriter,
			*http.Request,
		) {
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	decisionRequest := validToolDecisionRequest(t)
	decisionContext, cancelDecision := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancelDecision()
	type decisionResult struct {
		decision exchange.ToolDecision
		err      error
	}
	resolved := make(chan decisionResult, 1)
	go func() {
		decision, decideErr := approvalAuthority.Decide(
			decisionContext,
			decisionRequest,
		)
		resolved <- decisionResult{decision: decision, err: decideErr}
	}()

	var pending toolapproval.View
	deadline := time.Now().Add(5 * time.Second)
	for pending.ID == "" {
		response := doRequest(
			t,
			router,
			authority,
			http.MethodGet,
			"/api/v1/approvals?state=pending&limit=10",
			readToken,
			nil,
		)
		if response.Code != http.StatusOK {
			t.Fatalf(
				"pending approvals code=%d body=%s",
				response.Code,
				response.Body.Bytes(),
			)
		}
		if bytes.Contains(response.Body.Bytes(), []byte("private-argument-marker")) {
			t.Fatal("approval response exposed raw tool arguments")
		}
		var page toolapproval.Page
		decodeResponse(t, response, &page)
		if len(page.Items) != 0 {
			pending = page.Items[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("durable pending approval was not visible through control")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pending.State != toolapproval.StatePending ||
		len(pending.ToolNames) != 1 ||
		pending.ToolNames[0] != "read_file" {
		t.Fatalf("pending approval = %+v", pending)
	}

	body, err := json.Marshal(desktopcontrol.ApprovalDecisionInput{
		Decision: toolapproval.DecisionAllowOnce,
		Scope:    toolapproval.ScopeRequest,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := doMutation(
		t,
		router,
		authority,
		"/api/v1/approvals/"+pending.ID+"/actions/decide",
		writeToken,
		pending.Revision,
		"approval-decision-0001",
		body,
	)
	if response.Code != http.StatusOK {
		t.Fatalf(
			"decide approval code=%d body=%s",
			response.Code,
			response.Body.Bytes(),
		)
	}
	var allowed toolapproval.View
	decodeResponse(t, response, &allowed)
	if allowed.State != toolapproval.StateAllowed ||
		allowed.Revision != pending.Revision+1 {
		t.Fatalf("allowed approval = %+v", allowed)
	}

	select {
	case outcome := <-resolved:
		if outcome.err != nil ||
			outcome.decision.Outcome != exchange.ToolDecisionApproved {
			t.Fatalf(
				"exchange decision = %+v err=%v",
				outcome.decision,
				outcome.err,
			)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("control decision did not release the exchange")
	}

	stored := doRequest(
		t,
		router,
		authority,
		http.MethodGet,
		"/api/v1/approvals/"+pending.ID,
		readToken,
		nil,
	)
	if stored.Code != http.StatusOK {
		t.Fatalf("stored approval code=%d body=%s", stored.Code, stored.Body.Bytes())
	}
	var storedView toolapproval.View
	decodeResponse(t, stored, &storedView)
	if storedView.State != toolapproval.StateAllowed ||
		storedView.Decision != toolapproval.DecisionAllowOnce {
		t.Fatalf("stored approval = %+v", storedView)
	}
}

func TestDesktopControlRejectsCapabilityAndTransportBoundaryConfusion(t *testing.T) {
	t.Parallel()

	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	now := time.Now().UTC()
	readToken := capability(0x31)
	writeToken := capability(0x32)
	authenticator, err := desktopcontrol.NewAuthenticator(
		desktopcontrol.CapabilityGrant{
			ReadToken:  readToken,
			WriteToken: writeToken,
			ExpiresAt:  now.Add(time.Minute),
		},
		fixedClock{now: now},
	)
	if err != nil {
		t.Fatal(err)
	}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness:   readyState(true),
		Status:      runtime,
		Accesses:    runtime.AccessWriter(),
		Resolver:    runtime.SnapshotResolver(),
		Credentials: runtime.Credentials(),
		Activities:  runtime.Activities(),
		Connections: runtime.ConnectionEvents(),
		Approvals:   runtime.ToolApprovals(),
		Offline:     runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	const authority = "127.0.0.1:43128"
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{
		Authority:      authority,
		AllowedOrigins: []string{"tauri://localhost"},
		Authenticator:  authenticator,
		Application:    application,
		Bootstrap:      emptyBootstrap(),
		CaptureRuns:    http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		token     string
		host      string
		remote    string
		origin    string
		forwarded string
		fetchSite string
		want      int
	}{
		{
			name:   "write token cannot read",
			token:  writeToken,
			host:   authority,
			remote: "127.0.0.1:50000",
			origin: "tauri://localhost",
			want:   http.StatusUnauthorized,
		},
		{
			name:   "launcher-shaped token cannot read",
			token:  capability(0x33),
			host:   authority,
			remote: "127.0.0.1:50000",
			origin: "tauri://localhost",
			want:   http.StatusUnauthorized,
		},
		{
			name:   "LAN remote rejected",
			token:  readToken,
			host:   authority,
			remote: "192.0.2.2:50000",
			origin: "tauri://localhost",
			want:   http.StatusForbidden,
		},
		{
			name:   "localhost authority rejected",
			token:  readToken,
			host:   "localhost:43128",
			remote: "127.0.0.1:50000",
			origin: "tauri://localhost",
			want:   http.StatusForbidden,
		},
		{
			name:   "cross origin rejected",
			token:  readToken,
			host:   authority,
			remote: "127.0.0.1:50000",
			origin: "https://attacker.example",
			want:   http.StatusForbidden,
		},
		{
			name:      "forwarded request rejected",
			token:     readToken,
			host:      authority,
			remote:    "127.0.0.1:50000",
			origin:    "tauri://localhost",
			forwarded: "for=127.0.0.1",
			want:      http.StatusForbidden,
		},
		{
			name:      "forged same origin metadata rejected",
			token:     readToken,
			host:      authority,
			remote:    "127.0.0.1:50000",
			origin:    "tauri://localhost",
			fetchSite: "same-origin",
			want:      http.StatusForbidden,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodGet,
				"http://"+authority+"/api/v1/status",
				nil,
			)
			request.Host = test.host
			request.RemoteAddr = test.remote
			request.Header.Set("Origin", test.origin)
			fetchSite := test.fetchSite
			if fetchSite == "" {
				fetchSite = "cross-site"
			}
			request.Header.Set("Sec-Fetch-Site", fetchSite)
			request.Header.Set("Sec-Fetch-Mode", "cors")
			request.Header.Set("Sec-Fetch-Dest", "empty")
			request.Header.Set("Authorization", "Bearer "+test.token)
			if test.forwarded != "" {
				request.Header.Set("Forwarded", test.forwarded)
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.Bytes())
			}
			if bytes.Contains(recorder.Body.Bytes(), []byte(test.token)) {
				t.Fatal("authorization failure reflected a capability")
			}
		})
	}
}

func doMutation(
	t *testing.T,
	handler http.Handler,
	authority string,
	path string,
	token string,
	revision uint64,
	key string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	request := newRequest(
		http.MethodPost,
		authority,
		path,
		token,
		body,
	)
	if bytes.HasSuffix([]byte(path), []byte("/actions/apply")) {
		request.Method = http.MethodPut
	}
	request.Header.Set("If-Match", json.Number(
		strconv.FormatUint(revision, 10),
	).String())
	request.Header.Set("Idempotency-Key", key)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func doRequest(
	t *testing.T,
	handler http.Handler,
	authority string,
	method string,
	path string,
	token string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	request := newRequest(method, authority, path, token, body)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func newRequest(
	method string,
	authority string,
	path string,
	token string,
	body []byte,
) *http.Request {
	request := httptest.NewRequest(
		method,
		"http://"+authority+path,
		bytes.NewReader(body),
	)
	request.Host = authority
	request.RemoteAddr = "127.0.0.1:50000"
	request.Header.Set("Origin", "tauri://localhost")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	request.Header.Set("Sec-Fetch-Mode", "cors")
	request.Header.Set("Sec-Fetch-Dest", "empty")
	request.Header.Set("Authorization", "Bearer "+token)
	return request
}

func decodeResponse(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	output any,
) {
	t.Helper()
	decoder := json.NewDecoder(recorder.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		t.Fatal(err)
	}
}

func startRuntime(t *testing.T) *productruntime.Runtime {
	t.Helper()
	paths, err := productruntime.NewRuntimePaths(
		filepath.Join(t.TempDir(), "runtime-data"),
	)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runtime, err := productruntime.Start(ctx, productruntime.Options{
		Paths:          paths,
		Host:           hostcontract.Desktop(),
		OfflineHold:    gate,
		Secrets:        newCredentialStoreFixture(),
		Approvals:      toolapproval.DefaultConfig(),
		ExchangeHold:   exchange.DefaultHoldPolicy(),
		Clock:          productruntime.SystemClock{},
		InstanceIDs:    productruntime.NewCryptographicInstanceIDSource(),
		SecurityRandom: rand.Reader,
		Lifecycle:      productruntime.DefaultLifecycleOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func shutdownRuntime(t *testing.T, runtime *productruntime.Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func shutdownApprovalAuthority(
	t *testing.T,
	authority *toolapproval.Authority,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := authority.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func shutdownStore(t *testing.T, store *runtimepersistence.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

type credentialStoreFixture struct {
	mu    sync.Mutex
	items map[string]credentialStoreItem
}

type credentialStoreItem struct {
	bytes    []byte
	revision secretstore.Revision
}

func newCredentialStoreFixture() *credentialStoreFixture {
	return &credentialStoreFixture{items: make(map[string]credentialStoreItem)}
}

func (store *credentialStoreFixture) Read(
	_ context.Context,
	reference secretstore.Reference,
) (*secretstore.Value, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	item, found := store.items[reference.String()]
	if !found {
		return nil, secretstore.ErrNotFound
	}
	return secretstore.NewValue(item.bytes)
}

func (store *credentialStoreFixture) Inspect(
	_ context.Context,
	reference secretstore.Reference,
) (secretstore.Metadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	item, found := store.items[reference.String()]
	if !found {
		return secretstore.Metadata{State: secretstore.StateMissing}, nil
	}
	return secretstore.Metadata{
		State:    secretstore.StateConfigured,
		Revision: item.revision,
	}, nil
}

func (store *credentialStoreFixture) Replace(
	_ context.Context,
	command secretstore.ReplaceCommand,
) (secretstore.Metadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	current := store.items[command.Reference.String()]
	if current.revision != command.ExpectedRevision {
		return secretstore.Metadata{}, secretstore.ErrRevisionConflict
	}
	value, err := command.Value.CopyBytes()
	if err != nil {
		return secretstore.Metadata{}, err
	}
	defer clear(value)
	revision := current.revision + 1
	store.items[command.Reference.String()] = credentialStoreItem{
		bytes:    bytes.Clone(value),
		revision: revision,
	}
	return secretstore.Metadata{
		State:    secretstore.StateConfigured,
		Revision: revision,
	}, nil
}

type fixedClock struct {
	now time.Time
}

type readyState bool

func (ready readyState) Ready() bool {
	return bool(ready)
}

func (clock fixedClock) Now() time.Time {
	return clock.now
}

func capability(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func emptyBootstrap() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
}

func validApplyInput() accessapply.Input {
	return accessapply.Input{
		ExpectedRevision: 0,
		Access: accessapply.AccessInput{
			ID:                "access-control",
			Name:              "Control Access",
			Description:       "Executable control Access",
			Status:            "enabled",
			AgentEndpointID:   "access-control-endpoint",
			DefaultRouteSetID: "access-control-routes",
			ProfileIDs:        []string{"access-control-profile"},
			EgressPolicyID:    "access-control-egress",
		},
		AgentEndpoint: accessapply.AgentEndpointInput{
			ID:            "access-control-endpoint",
			ClientOrigin:  "https://agent.example.test:443",
			ClientDialect: "anthropic-messages",
		},
		Profiles: []accessapply.ProfileInput{{
			ID:                  "access-control-profile",
			Name:                "OpenAI Chat",
			Description:         "Fixed profile",
			BackendDialect:      "openai-chat",
			TargetID:            "access-control-target",
			TransportProfileRef: access.TransportProfileObservedClientH1Value,
			DefaultModelPolicy: accessapply.ModelPolicyInput{
				Mode:       "fixed",
				FixedModel: "gpt-4.1-mini",
			},
			AccountBindingIDs:       []string{"access-control-account"},
			DefaultAccountBindingID: "access-control-account",
		}},
		ProviderTargets: []accessapply.ProviderTargetInput{{
			ID:        "access-control-target",
			ProfileID: "access-control-profile",
			Origin:    "https://api.openai.com:443/v1",
			Protocol:  "openai-chat",
			Capabilities: []string{
				"messages",
				"streaming",
				"tool_calls",
			},
		}},
		AccountBindings: []accessapply.AccountBindingInput{{
			ID:            "access-control-account",
			ProfileID:     "access-control-profile",
			Label:         "Primary",
			SecretRef:     "secret://provider/access-control",
			AuthDriverRef: "static_header",
			Enabled:       true,
		}},
		RouteSets: []accessapply.RouteSetInput{{
			ID:                  "access-control-routes",
			CandidateProfileIDs: []string{"access-control-profile"},
		}},
		EgressPolicy: accessapply.EgressPolicyInput{
			ID:   "access-control-egress",
			Mode: "direct",
		},
		PluginPlan: accessapply.PluginPlanInput{
			Mode:       "pass_through",
			BindingIDs: []string{},
		},
	}
}

func validToolDecisionRequest(t *testing.T) exchange.ToolDecisionRequest {
	t.Helper()
	accessID, err := access.NewAccessID("access-control-approval")
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := protocolcore.NewJSONObject(
		[]byte(`{"path":"private-argument-marker"}`),
		protocolcore.MaxToolJSONBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	callKey, err := protocolcore.NewCallKey("openai-chat", "call-control-1")
	if err != nil {
		t.Fatal(err)
	}
	request, err := exchange.NewToolDecisionRequest(
		"exchange-control-approval",
		accessID,
		1,
		access.PlanHash{0x41},
		[]protocolcore.ToolIntent{{
			ResponseID: "response-control-approval",
			Ordinal:    0,
			Call: protocolcore.ToolCall{
				Key:       callKey,
				Name:      "read_file",
				Arguments: arguments,
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}
