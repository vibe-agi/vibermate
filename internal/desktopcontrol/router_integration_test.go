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

	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

const desktopControlIntegrationStartupTimeout = 60 * time.Second

type rejectingManualCaptureHandler struct{}

func (rejectingManualCaptureHandler) ServeHTTP(
	writer http.ResponseWriter,
	_ *http.Request,
	_ controlprincipal.Principal,
) {
	writer.WriteHeader(http.StatusServiceUnavailable)
}

type recordingManualCaptureHandler struct {
	called        bool
	authorization string
	principal     controlprincipal.Principal
}

func (handler *recordingManualCaptureHandler) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
	principal controlprincipal.Principal,
) {
	handler.called = true
	handler.authorization = request.Header.Get("Authorization")
	handler.principal = principal
	writer.WriteHeader(http.StatusNoContent)
}

func desktopManualPrincipal(t *testing.T) controlprincipal.Principal {
	t.Helper()
	principal, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                 "desktop-app:test",
		Kind:               controlprincipal.KindDesktopApp,
		CredentialRevision: 1,
		AllowedGrantKinds: []controlprincipal.GrantKind{
			controlprincipal.GrantManualCapture,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return principal
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
		Readiness:    readyState(true),
		Status:       runtime,
		Environments: runtime.Environments(),
		Assignments:  runtime.CaptureAssignments(),
		Clock:        desktopcontrol.SystemClock{},
		Activities:   runtime.Activities(),
		Contents:     runtime.ExchangeContents(),
		Connections:  runtime.ConnectionEvents(),
		Egress:       runtime.EgressAttempts(),
		Approvals:    approvalAuthority,
		Accounts:     runtime.ProviderAccounts(),
		Offline:      runtime,
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
		CLIControl: http.HandlerFunc(func(
			http.ResponseWriter,
			*http.Request,
		) {
		}),
		ManualCaptures:   rejectingManualCaptureHandler{},
		DesktopPrincipal: desktopManualPrincipal(t),
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
		len(pending.SubjectLabels) != 1 ||
		pending.SubjectLabels[0] != "read_file" {
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
		Accounts:     runtime.ProviderAccounts(),
		Offline:      runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	const authority = "127.0.0.1:43128"
	router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{
		Authority:        authority,
		AllowedOrigins:   []string{"tauri://localhost"},
		Authenticator:    authenticator,
		Application:      application,
		Bootstrap:        emptyBootstrap(),
		CLIControl:       http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		ManualCaptures:   rejectingManualCaptureHandler{},
		DesktopPrincipal: desktopManualPrincipal(t),
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
	method := http.MethodPost
	if bytes.HasSuffix([]byte(path), []byte("/actions/apply")) {
		method = http.MethodPut
	}
	return doMutationWithMethod(
		t,
		handler,
		authority,
		method,
		path,
		token,
		revision,
		key,
		body,
	)
}

func doMutationWithMethod(
	t *testing.T,
	handler http.Handler,
	authority string,
	method string,
	path string,
	token string,
	revision uint64,
	key string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	request := newRequest(
		method,
		authority,
		path,
		token,
		body,
	)
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

func doRevisionedRequest(
	t *testing.T,
	handler http.Handler,
	authority string,
	method string,
	path string,
	token string,
	revision uint64,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	request := newRequest(method, authority, path, token, body)
	request.Header.Set("If-Match", strconv.FormatUint(revision, 10))
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
	// This starts the complete production runtime, including all embedded SQLite
	// migrations. Under the full repository race job many such fixtures compete
	// for CPU, so this harness bound must not masquerade as a product deadline.
	ctx, cancel := context.WithTimeout(
		context.Background(),
		desktopControlIntegrationStartupTimeout,
	)
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

func (store *credentialStoreFixture) Delete(
	_ context.Context,
	reference secretstore.Reference,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	item, found := store.items[reference.String()]
	if !found {
		return secretstore.ErrNotFound
	}
	clear(item.bytes)
	delete(store.items, reference.String())
	return nil
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

func validToolDecisionRequest(t *testing.T) exchange.ToolDecisionRequest {
	t.Helper()
	environmentID, err := environment.NewEnvironmentID("environment-control-approval")
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
	decisionContext, err := exchange.NewToolDecisionContext(
		environment.DefaultPolicySet(), "", false, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := exchange.NewToolDecisionRequest(
		"exchange-control-approval",
		environmentID,
		1,
		environment.CandidateDigest{0x41},
		"route-control-approval",
		1,
		decisionContext,
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
