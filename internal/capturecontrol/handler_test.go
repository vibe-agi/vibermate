package capturecontrol_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturegrant"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

func TestCaptureControlSeparatesControlAndPerRunCredentials(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	defer fixture.Close(t)
	create := capturecontrol.CreateRequest{
		CWD:            fixture.workspace,
		Command:        []string{"claude", "--print", "private prompt"},
		ExecutablePath: fixture.executable,
	}
	response := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs",
		fixture.controlCredential,
		"",
		create,
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.Bytes())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("private prompt")) {
		t.Fatal("launch grant echoed child argv")
	}
	var grant capturecontrol.LaunchGrant
	decodeRecorder(t, response, &grant)
	if grant.Run.ID == "" ||
		grant.Run.CWD != fixture.workspace ||
		grant.CatalogRevision != 4 ||
		grant.LaunchRecipe != clientadapter.LaunchNodeEnvProxy ||
		grant.Adapter == nil ||
		grant.Adapter.CatalogRevision != grant.CatalogRevision ||
		grant.Adapter.Version != "2.1.220" ||
		grant.ExecutablePath == "" ||
		grant.ProxyAddress != "http://127.0.0.1:32123" ||
		grant.ProxyToken == "" ||
		grant.RunCapability == "" ||
		grant.ProxyToken == grant.RunCapability ||
		grant.RootPEMPath != fixture.authority.Certificate().Path() ||
		len(grant.ProtectedAuthorities) != 1 ||
		grant.ProtectedAuthorities[0] != "api.anthropic.com:443" {
		t.Fatalf("launch grant = %+v", grant)
	}
	proxyCapability, err := capturerun.NewProxyCapability(
		grant.ProxyToken,
	)
	if err != nil {
		t.Fatal(err)
	}
	proxyEvidence, err := fixture.runs.AuthorizeProxy(
		context.Background(),
		proxyCapability,
	)
	if err != nil {
		t.Fatal(err)
	}
	if proxyEvidence.CatalogRevision != grant.CatalogRevision ||
		proxyEvidence.Adapter == nil ||
		proxyEvidence.Adapter.ID != grant.Adapter.ID ||
		proxyEvidence.Adapter.Revision != grant.Adapter.Revision ||
		proxyEvidence.Adapter.Version != grant.Adapter.Version ||
		proxyEvidence.Adapter.CatalogRevision != grant.Adapter.CatalogRevision ||
		proxyEvidence.Adapter.InstallShape != grant.Adapter.InstallShape ||
		proxyEvidence.Adapter.LaunchRecipe != grant.Adapter.LaunchRecipe ||
		grant.Adapter.Source !=
			capturecontrol.ClientAdapterSourcePrelaunchDigestCatalog {
		t.Fatalf(
			"proxy evidence=%+v grant adapter=%+v",
			proxyEvidence,
			grant.Adapter,
		)
	}

	unauthorized := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs",
		"",
		"",
		create,
	)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized create status=%d", unauthorized.Code)
	}
	mixedCreate := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs",
		fixture.controlCredential,
		grant.RunCapability,
		create,
	)
	if mixedCreate.Code != http.StatusUnauthorized {
		t.Fatalf(
			"mixed control/run create status=%d body=%s",
			mixedCreate.Code,
			mixedCreate.Body.Bytes(),
		)
	}

	mixedRun := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs/"+grant.Run.ID+"/actions/heartbeat",
		fixture.controlCredential,
		grant.RunCapability,
		nil,
	)
	if mixedRun.Code != http.StatusForbidden {
		t.Fatalf(
			"mixed control/run heartbeat status=%d body=%s",
			mixedRun.Code,
			mixedRun.Body.Bytes(),
		)
	}

	attach := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs/"+grant.Run.ID+"/actions/attach-process",
		"",
		grant.RunCapability,
		capturecontrol.AttachRequest{ProcessID: 444},
	)
	if attach.Code != http.StatusOK {
		t.Fatalf("attach status=%d body=%s", attach.Code, attach.Body.Bytes())
	}
	var attached capturecontrol.CaptureRunView
	decodeRecorder(t, attach, &attached)
	if attached.ProcessID != 444 ||
		attached.ClientAdapterState != clientadapter.StatusVerified ||
		attached.ClientRecognition != clientadapter.RecognitionVerified ||
		attached.CatalogRevision != grant.CatalogRevision {
		t.Fatalf("attached view = %+v", attached)
	}
	heartbeat := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs/"+grant.Run.ID+"/actions/heartbeat",
		"",
		grant.RunCapability,
		nil,
	)
	if heartbeat.Code != http.StatusOK {
		t.Fatalf("heartbeat status=%d body=%s", heartbeat.Code, heartbeat.Body.Bytes())
	}
	finish := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs/"+grant.Run.ID+"/actions/finish",
		"",
		grant.RunCapability,
		nil,
	)
	if finish.Code != http.StatusNoContent {
		t.Fatalf("finish status=%d body=%s", finish.Code, finish.Body.Bytes())
	}
	rejected := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs/"+grant.Run.ID+"/actions/heartbeat",
		"",
		fixture.controlCredential,
		nil,
	)
	if rejected.Code != http.StatusForbidden {
		t.Fatalf("wrong per-run capability status=%d", rejected.Code)
	}
}

func TestCaptureControlCredentialDoesNotExpireWithDiscoveryLease(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	defer fixture.Close(t)
	fixture.clock.now = fixture.clock.now.Add(2 * time.Minute)
	response := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs",
		fixture.controlCredential,
		"",
		capturecontrol.CreateRequest{
			CWD:            fixture.workspace,
			Command:        []string{"claude"},
			ExecutablePath: fixture.executable,
		},
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("generation control status=%d body=%s", response.Code, response.Body.Bytes())
	}
}

func TestLocalUserLabelDoesNotChangeMachineWorkspaceIdentity(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	defer fixture.Close(t)
	for _, user := range []string{"alice", "bob"} {
		response := fixture.DoJSON(
			t,
			http.MethodPost,
			"/api/v1/capture-runs",
			fixture.controlCredential,
			"",
			capturecontrol.CreateRequest{
				CWD:            fixture.workspace,
				Command:        []string{"claude"},
				ExecutablePath: fixture.executable,
				LocalUserLabel: user,
			},
		)
		if response.Code != http.StatusCreated {
			t.Fatalf("create for %s status=%d body=%s", user, response.Code, response.Body.Bytes())
		}
	}
	page, err := fixture.runs.ListRuns(
		context.Background(),
		capturerun.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("run count = %d", len(page.Items))
	}
	byUser := make(map[string]capturerun.View, len(page.Items))
	for _, run := range page.Items {
		byUser[run.LocalUserLabel] = run
	}
	alice, aliceOK := byUser["alice"]
	bob, bobOK := byUser["bob"]
	if !aliceOK || !bobOK {
		t.Fatalf("local user labels = %#v", byUser)
	}
	if alice.MachineID == "" || alice.WorkspaceID == "" ||
		alice.MachineID != bob.MachineID ||
		alice.WorkspaceID != bob.WorkspaceID ||
		alice.WorkspaceLabel != bob.WorkspaceLabel {
		t.Fatalf("user labels changed stable scope: alice=%+v bob=%+v", alice, bob)
	}
}

func TestCaptureControlKeepsUnknownClientBuildGeneric(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	defer fixture.Close(t)
	if err := os.WriteFile(
		fixture.executable,
		[]byte("#!/bin/sh\nexit 9\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	response := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs",
		fixture.controlCredential,
		"",
		capturecontrol.CreateRequest{
			CWD:            fixture.workspace,
			Command:        []string{"claude"},
			ExecutablePath: fixture.executable,
		},
	)
	if response.Code != http.StatusCreated {
		t.Fatalf(
			"unknown build create status=%d body=%s",
			response.Code,
			response.Body.Bytes(),
		)
	}
	var grant capturecontrol.LaunchGrant
	decodeRecorder(t, response, &grant)
	if grant.CatalogRevision != 4 ||
		grant.LaunchRecipe != clientadapter.LaunchGeneric ||
		grant.Adapter != nil ||
		grant.RootPEMPath != "" {
		t.Fatalf("unknown build launch grant = %+v", grant)
	}
	proxyCapability, err := capturerun.NewProxyCapability(
		grant.ProxyToken,
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := fixture.runs.AuthorizeProxy(
		context.Background(),
		proxyCapability,
	)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.CatalogRevision != 4 || evidence.Adapter != nil {
		t.Fatalf("unknown build proxy evidence = %+v", evidence)
	}
}

type fixture struct {
	handler           *capturecontrol.Handler
	store             *runtimepersistence.Store
	runs              *capturerun.Manager
	authority         *localca.Authority
	workspaces        *workspaceidentity.Manager
	clock             *fakeClock
	controlCredential string
	workspace         string
	executable        string
}

// fixtureOverride adjusts the handler a fixture builds. Recognition needs a
// verifier that reports it and something able to answer, and neither can come
// from a file on disk in a unit test.
type fixtureOverride func(*capturegrant.Options)

func newFixture(t *testing.T, overrides ...fixtureOverride) *fixture {
	t.Helper()
	directory := t.TempDir()
	store, err := runtimepersistence.Open(
		context.Background(),
		runtimepersistence.Options{
			DatabasePath:           filepath.Join(directory, "data", "runtime.db"),
			BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
			CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)}
	runOptions := capturerun.DefaultOptions(store.CaptureRunRepository())
	runOptions.Clock = clock
	runs, err := capturerun.NewManager(context.Background(), runOptions)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := localca.Open(
		context.Background(),
		localca.DefaultOptions(
			filepath.Join(directory, "ca"),
			context.Background(),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(directory, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "claude")
	executableContent := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(
		executable,
		executableContent,
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	executableDigest := sha256.Sum256(executableContent)
	catalog, err := clientadapter.NewCatalog(
		4,
		[]clientadapter.Release{{
			ID:              "claude-code",
			Revision:        1,
			Version:         "2.1.220",
			OperatingSystem: runtime.GOOS,
			Architecture:    runtime.GOARCH,
			InstallShape:    clientadapter.InstallNativeSingleBinary,
			InvocationLabel: "claude",
			ArtifactRoot:    ".",
			Artifacts: []clientadapter.Artifact{{
				Role:   clientadapter.ArtifactEntrypoint,
				SHA256: hex.EncodeToString(executableDigest[:]),
			}},
			LaunchRecipe: clientadapter.LaunchNodeEnvProxy,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := clientadapter.NewReleaseVerifier(catalog)
	if err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	principal, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                 "local-cli:test-instance",
		Kind:               controlprincipal.KindLocalCLI,
		CredentialRevision: 1,
		AllowedGrantKinds: []controlprincipal.GrantKind{
			controlprincipal.GrantCaptureRun,
			controlprincipal.GrantManualCapture,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	principals, err := controlprincipal.NewAuthority(
		controlprincipal.CredentialGrant{
			Credential: token,
			Principal:  principal,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	workspaces, err := workspaceidentity.Open(
		context.Background(),
		filepath.Join(directory, "identity"),
		bytes.NewReader(bytes.Repeat([]byte{0x53}, 64)),
		clock.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	workspaceResolver, err := capturegrant.NewLocalWorkspaceResolver(workspaces)
	if err != nil {
		t.Fatal(err)
	}
	issuerOptions := capturegrant.Options{
		Runs:        runs,
		Verifier:    verifier,
		Authorities: fixedAuthorities{"api.anthropic.com:443"},
		ProxyOrigin: "http://127.0.0.1:32123",
		Root:        authority.Certificate(),
		RunLifetime: 2 * time.Minute,
		Workspaces:  workspaceResolver,
	}
	for _, override := range overrides {
		override(&issuerOptions)
	}
	issuer, err := capturegrant.New(issuerOptions)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := capturecontrol.New(capturecontrol.Options{
		Runs:        runs,
		Principals:  principals,
		Issuer:      issuer,
		RunLifetime: 2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		handler:           handler,
		store:             store,
		runs:              runs,
		authority:         authority,
		workspaces:        workspaces,
		clock:             clock,
		controlCredential: token,
		workspace:         workspace,
		executable:        executable,
	}
}

func (fixture *fixture) DoJSON(
	t *testing.T,
	method, path, bearer, runCapability string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, "http://127.0.0.1"+path, bytes.NewReader(encoded))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if runCapability != "" {
		request.Header.Set(capturecontrol.RunCapabilityHeader, runCapability)
	}
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	return recorder
}

func (fixture *fixture) Close(t *testing.T) {
	t.Helper()
	if err := fixture.runs.Shutdown(context.Background()); err != nil {
		t.Errorf("shutdown runs: %v", err)
	}
	if err := fixture.authority.Shutdown(context.Background()); err != nil {
		t.Errorf("shutdown authority: %v", err)
	}
	if err := fixture.workspaces.Shutdown(context.Background()); err != nil {
		t.Errorf("shutdown workspaces: %v", err)
	}
	if err := fixture.store.Shutdown(context.Background()); err != nil {
		t.Errorf("shutdown store: %v", err)
	}
}

type fakeClock struct {
	now time.Time
}

func (clock *fakeClock) Now() time.Time {
	return clock.now
}

type fixedAuthorities []string

func (authorities fixedAuthorities) ActiveClientAuthorities() ([]string, error) {
	return append([]string(nil), authorities...), nil
}

func decodeRecorder(
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
