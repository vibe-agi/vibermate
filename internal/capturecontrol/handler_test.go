package capturecontrol_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturegrant"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

func TestCaptureControlSeparatesControlAndPerRunCredentials(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	defer fixture.Close(t)
	create := capturecontrol.CreateRequest{
		EnvironmentID:  testEnvironmentID,
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

func TestCaptureControlRejectsMissingOrInvalidEnvironment(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	defer fixture.Close(t)
	for _, environmentID := range []string{"", "Bad ID"} {
		response := fixture.DoJSON(
			t,
			http.MethodPost,
			"/api/v1/capture-runs",
			fixture.controlCredential,
			"",
			capturecontrol.CreateRequest{
				EnvironmentID:  environmentID,
				CWD:            fixture.workspace,
				Command:        []string{"claude"},
				ExecutablePath: fixture.executable,
			},
		)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf(
				"environmentId %q status=%d body=%s",
				environmentID,
				response.Code,
				response.Body.Bytes(),
			)
		}
	}
}

func TestCaptureAuthorityFailureObservesThenFinishesTheActiveRun(t *testing.T) {
	t.Parallel()
	sawActive := false
	fixture := newFixture(t, func(options *capturegrant.Options) {
		reader, ok := options.Runs.(capturerun.Reader)
		if !ok {
			t.Fatal("CaptureRun controller does not expose its read projection")
		}
		options.Authorities = inspectingFailingAuthorities{
			reader:    reader,
			sawActive: &sawActive,
		}
	})
	defer fixture.Close(t)
	response := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/capture-runs",
		fixture.controlCredential,
		"",
		capturecontrol.CreateRequest{
			EnvironmentID:  testEnvironmentID,
			CWD:            fixture.workspace,
			Command:        []string{"claude", "--print", "prompt"},
			ExecutablePath: fixture.executable,
		},
	)
	if response.Code != http.StatusServiceUnavailable || !sawActive {
		t.Fatalf(
			"authority failure status=%d sawActive=%t body=%s",
			response.Code,
			sawActive,
			response.Body.Bytes(),
		)
	}
	page, err := fixture.runs.ListRuns(
		context.Background(),
		capturerun.PageRequest{Limit: 10},
	)
	if err != nil || len(page.Items) != 1 ||
		page.Items[0].State != capturerun.StateFinished {
		t.Fatalf("unused CaptureRun page=%+v err=%v", page, err)
	}
}

func TestManualCaptureControlHidesInternalEpochAndUsesOpaqueStateTags(
	t *testing.T,
) {
	t.Parallel()

	fixture := newFixture(t)
	defer fixture.Close(t)

	contextResponse := fixture.DoJSON(
		t,
		http.MethodGet,
		"/api/v1/manual-captures/context?environmentId="+testEnvironmentID,
		fixture.controlCredential,
		"",
		nil,
	)
	if contextResponse.Code != http.StatusOK ||
		contextResponse.Header().Get("Cache-Control") != "no-store" ||
		bytes.Contains(contextResponse.Body.Bytes(), []byte("generation")) ||
		bytes.Contains(contextResponse.Body.Bytes(), []byte("credentialRevision")) {
		t.Fatalf(
			"context status=%d headers=%v body=%s",
			contextResponse.Code,
			contextResponse.Header(),
			contextResponse.Body.Bytes(),
		)
	}
	var captureContext capturecontrol.ManualCaptureContext
	decodeRecorder(t, contextResponse, &captureContext)
	if captureContext.ConfirmationToken == "" ||
		captureContext.ProxyAddress != "http://127.0.0.1:32123" ||
		captureContext.EnvironmentID != testEnvironmentID ||
		captureContext.Root == nil || captureContext.Root.DERSHA256 == "" ||
		captureContext.Root.PEMPath == "" {
		t.Fatalf("context=%+v", captureContext)
	}

	expiresIn := int64((24 * time.Hour) / time.Second)
	createResponse := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/manual-captures",
		fixture.controlCredential,
		"",
		capturecontrol.ManualCaptureCreateRequest{
			EnvironmentID:     testEnvironmentID,
			DisplayName:       "Terminal in project alpha",
			ClientClass:       manualcapture.ClientCLI,
			Lifetime:          manualcapture.LifetimeTemporary,
			ExpiresInSeconds:  &expiresIn,
			ConfirmationToken: captureContext.ConfirmationToken,
		},
	)
	firstTag := createResponse.Header().Get("ETag")
	if createResponse.Code != http.StatusCreated || firstTag == "" ||
		createResponse.Header().Get("Cache-Control") != "no-store" ||
		bytes.Contains(createResponse.Body.Bytes(), []byte("credentialRevision")) ||
		bytes.Contains(createResponse.Body.Bytes(), []byte(`"revision"`)) {
		t.Fatalf(
			"create status=%d headers=%v body=%s",
			createResponse.Code,
			createResponse.Header(),
			createResponse.Body.Bytes(),
		)
	}
	var created capturecontrol.ManualCaptureGrant
	decodeRecorder(t, createResponse, &created)
	if created.Capture.ID == "" || created.ProxyPassword == "" ||
		created.ProxyUsername != manualcapture.ProxyUsername {
		t.Fatalf("grant=%+v", created)
	}

	detailPath := "/api/v1/manual-captures/" + created.Capture.ID
	detail := fixture.DoJSON(
		t,
		http.MethodGet,
		detailPath,
		fixture.controlCredential,
		"",
		nil,
	)
	if detail.Code != http.StatusOK || detail.Header().Get("ETag") != firstTag ||
		bytes.Contains(detail.Body.Bytes(), []byte("proxyPassword")) ||
		bytes.Contains(detail.Body.Bytes(), []byte("credentialRevision")) {
		t.Fatalf("detail status=%d headers=%v body=%s", detail.Code, detail.Header(), detail.Body.Bytes())
	}

	proxyCredential, err := manualcapture.NewProxyCredential(created.ProxyPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manuals.AuthorizeProxy(context.Background(), proxyCredential); err != nil {
		t.Fatal(err)
	}
	observed := fixture.DoJSON(
		t,
		http.MethodGet,
		detailPath,
		fixture.controlCredential,
		"",
		nil,
	)
	if observed.Code != http.StatusOK || observed.Header().Get("ETag") != firstTag {
		t.Fatalf("observation changed mutation tag: headers=%v body=%s", observed.Header(), observed.Body.Bytes())
	}

	stale := `"mc_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"`
	staleRotate := fixture.DoJSONWithHeaders(
		t,
		http.MethodPost,
		detailPath+"/actions/rotate-credential",
		fixture.controlCredential,
		"",
		nil,
		map[string]string{"If-Match": stale},
	)
	if staleRotate.Code != http.StatusConflict {
		t.Fatalf("stale rotate status=%d body=%s", staleRotate.Code, staleRotate.Body.Bytes())
	}

	rotate := fixture.DoJSONWithHeaders(
		t,
		http.MethodPost,
		detailPath+"/actions/rotate-credential",
		fixture.controlCredential,
		"",
		nil,
		map[string]string{"If-Match": firstTag},
	)
	secondTag := rotate.Header().Get("ETag")
	if rotate.Code != http.StatusOK || secondTag == "" || secondTag == firstTag ||
		bytes.Contains(rotate.Body.Bytes(), []byte("credentialRevision")) {
		t.Fatalf("rotate status=%d headers=%v body=%s", rotate.Code, rotate.Header(), rotate.Body.Bytes())
	}
	var rotated capturecontrol.ManualCaptureGrant
	decodeRecorder(t, rotate, &rotated)
	if rotated.ProxyPassword == "" || rotated.ProxyPassword == created.ProxyPassword {
		t.Fatal("rotation did not return one new credential")
	}
	if _, err := fixture.manuals.AuthorizeProxy(context.Background(), proxyCredential); err == nil {
		t.Fatal("old proxy credential remained active after rotation")
	}

	revoke := fixture.DoJSONWithHeaders(
		t,
		http.MethodPost,
		detailPath+"/actions/revoke",
		fixture.controlCredential,
		"",
		nil,
		map[string]string{"If-Match": secondTag},
	)
	if revoke.Code != http.StatusNoContent || revoke.Body.Len() != 0 {
		t.Fatalf("revoke status=%d body=%s", revoke.Code, revoke.Body.Bytes())
	}
}

func TestManualCaptureCreateRejectsStaleConfirmationWithoutMutation(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	defer fixture.Close(t)
	expiresIn := int64((24 * time.Hour) / time.Second)
	response := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/manual-captures",
		fixture.controlCredential,
		"",
		capturecontrol.ManualCaptureCreateRequest{
			EnvironmentID:     testEnvironmentID,
			DisplayName:       "Stale review",
			ClientClass:       manualcapture.ClientOther,
			Lifetime:          manualcapture.LifetimeTemporary,
			ExpiresInSeconds:  &expiresIn,
			ConfirmationToken: "ctx_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		},
	)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale confirmation status=%d body=%s", response.Code, response.Body.Bytes())
	}
	page, err := fixture.manuals.List(context.Background(), manualcapture.PageRequest{
		Owner: manualcapture.NewLocalOwnerScope(),
	})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("stale confirmation mutated authority: page=%+v err=%v", page, err)
	}
}

func TestManualCaptureSystemTransparentNeverReturnsRootMaterial(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	defer fixture.Close(t)
	contextResponse := fixture.DoJSON(
		t,
		http.MethodGet,
		"/api/v1/manual-captures/context?environmentId=system_transparent",
		fixture.controlCredential,
		"",
		nil,
	)
	if contextResponse.Code != http.StatusOK ||
		bytes.Contains(contextResponse.Body.Bytes(), []byte(`"root"`)) {
		t.Fatalf("transparent context status=%d body=%s", contextResponse.Code, contextResponse.Body.Bytes())
	}
	var captureContext capturecontrol.ManualCaptureContext
	decodeRecorder(t, contextResponse, &captureContext)
	if captureContext.EnvironmentID != "system_transparent" || captureContext.Root != nil ||
		len(captureContext.ProtectedAuthorities) != 0 || len(captureContext.ManagedAuthorities) != 0 {
		t.Fatalf("transparent context=%+v", captureContext)
	}
	expiresIn := int64((24 * time.Hour) / time.Second)
	createResponse := fixture.DoJSON(
		t,
		http.MethodPost,
		"/api/v1/manual-captures",
		fixture.controlCredential,
		"",
		capturecontrol.ManualCaptureCreateRequest{
			EnvironmentID: "system_transparent", DisplayName: "Transparent terminal",
			ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeTemporary,
			ExpiresInSeconds: &expiresIn, ConfirmationToken: captureContext.ConfirmationToken,
		},
	)
	if createResponse.Code != http.StatusCreated ||
		bytes.Contains(createResponse.Body.Bytes(), []byte(`"root"`)) {
		t.Fatalf("transparent create status=%d body=%s", createResponse.Code, createResponse.Body.Bytes())
	}
	var grant capturecontrol.ManualCaptureGrant
	decodeRecorder(t, createResponse, &grant)
	if grant.EnvironmentID != "system_transparent" || grant.AssignmentRevision != 1 ||
		grant.Root != nil || len(grant.ProtectedAuthorities) != 0 || len(grant.ManagedAuthorities) != 0 {
		t.Fatalf("transparent grant=%+v", grant)
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
			EnvironmentID:  testEnvironmentID,
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
				EnvironmentID:  testEnvironmentID,
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
			EnvironmentID:  testEnvironmentID,
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

func TestCaptureControlUsesTransparentGenericLaunchWithoutProtectedAuthorities(
	t *testing.T,
) {
	t.Parallel()

	fixture := newFixture(t, func(options *capturegrant.Options) {
		options.Authorities = fixedAuthorities{}
	})
	defer fixture.Close(t)
	grant := fixture.createRun(t)

	if grant.LaunchRecipe != clientadapter.LaunchGeneric ||
		grant.RootPEMPath != "" ||
		grant.Adapter != nil ||
		grant.Signer != nil ||
		len(grant.ProtectedAuthorities) != 0 ||
		len(grant.ManagedCredentialAuthorities) != 0 {
		t.Fatalf("transparent launch grant = %+v", grant)
	}
	if grant.Run.ClientAdapter == nil ||
		grant.Recognition != clientadapter.RecognitionVerified {
		t.Fatalf("transparent launch lost client identity evidence: %+v", grant)
	}
	if err := grant.Validate(); err != nil {
		t.Fatalf("transparent launch grant is invalid: %v", err)
	}
}

type fixture struct {
	handler           *capturecontrol.Handler
	store             *runtimepersistence.Store
	runs              *capturerun.Manager
	manuals           *manualcapture.Manager
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
	manualOptions := manualcapture.DefaultOptions(store.ManualCaptureRepository())
	manualOptions.Clock = clock
	manuals, err := manualcapture.NewManager(context.Background(), manualOptions)
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
			Features: clientadapter.
				FeatureCoreOwnedStreamingFallback,
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
		Runs:           runs,
		ManualCaptures: manuals,
		Verifier:       verifier,
		Authorities:    fixedAuthorities{"api.anthropic.com:443"},
		ProxyOrigin:    "http://127.0.0.1:32123",
		Generation: base64.RawURLEncoding.EncodeToString(
			bytes.Repeat([]byte{0x31}, 32),
		),
		RootIdentity: authority.Identity(),
		Root:         authority.Certificate(),
		RunLifetime:  2 * time.Minute,
		Workspaces:   workspaceResolver,
	}
	for _, override := range overrides {
		override(&issuerOptions)
	}
	issuer, err := capturegrant.New(issuerOptions)
	if err != nil {
		t.Fatal(err)
	}
	manualHandler, err := capturecontrol.NewManualHandler(issuer)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := capturecontrol.New(capturecontrol.Options{
		Runs:        runs,
		Principals:  principals,
		Issuer:      issuer,
		Manual:      manualHandler,
		RunLifetime: 2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		handler:           handler,
		store:             store,
		runs:              runs,
		manuals:           manuals,
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
	return fixture.DoJSONWithHeaders(
		t,
		method,
		path,
		bearer,
		runCapability,
		body,
		nil,
	)
}

func (fixture *fixture) DoJSONWithHeaders(
	t *testing.T,
	method, path, bearer, runCapability string,
	body any,
	headers map[string]string,
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
	for name, value := range headers {
		request.Header.Set(name, value)
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
	if err := fixture.manuals.Shutdown(context.Background()); err != nil {
		t.Errorf("shutdown manual captures: %v", err)
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

func (authorities fixedAuthorities) Review(
	_ context.Context,
	environmentID environment.EnvironmentID,
) (capturegrant.CaptureAuthorityReview, error) {
	set, err := authorities.authoritySet(
		captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "review"},
		environmentID,
		captureassignment.SourceLaunch,
	)
	return set.Review(), err
}

func (authorities fixedAuthorities) AssignAndResolve(
	_ context.Context,
	capture captureidentity.Reference,
	environmentID environment.EnvironmentID,
) (capturegrant.CaptureAuthoritySet, error) {
	source := captureassignment.SourceLaunch
	if capture.Kind == captureidentity.KindManualCapture {
		source = captureassignment.SourceManualCreate
	}
	return authorities.authoritySet(capture, environmentID, source)
}

func (authorities fixedAuthorities) Resolve(
	_ context.Context,
	capture captureidentity.Reference,
) (capturegrant.CaptureAuthoritySet, error) {
	return authorities.authoritySet(capture, testEnvironmentID, captureassignment.SourceLaunch)
}

func (authorities fixedAuthorities) authoritySet(
	capture captureidentity.Reference,
	environmentID environment.EnvironmentID,
	source captureassignment.Source,
) (capturegrant.CaptureAuthoritySet, error) {
	protected := []string(authorities)
	managed := []string(authorities)
	if environmentID == environment.SystemTransparentID {
		protected = nil
		managed = nil
	}
	var candidateDigest environment.CandidateDigest
	candidateDigest[0] = 1
	boundary, err := environment.NewLaunchAuthorityBoundaryFromScopes(
		environmentID, 1, candidateDigest, protected, managed,
	)
	if err != nil {
		return capturegrant.CaptureAuthoritySet{}, err
	}
	return capturegrant.NewCaptureAuthoritySet(captureassignment.Assignment{
		Capture: capture, EnvironmentID: environmentID, Revision: 1,
		Source: source, LaunchAuthority: boundary,
		UpdatedAt: time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC),
	})
}

type inspectingFailingAuthorities struct {
	reader    capturerun.Reader
	sawActive *bool
}

func (resolver inspectingFailingAuthorities) AssignAndResolve(
	ctx context.Context,
	_ captureidentity.Reference,
	_ environment.EnvironmentID,
) (capturegrant.CaptureAuthoritySet, error) {
	page, err := resolver.reader.ListRuns(
		ctx,
		capturerun.PageRequest{Limit: 10},
	)
	if err != nil {
		return capturegrant.CaptureAuthoritySet{}, err
	}
	for _, run := range page.Items {
		if run.State == capturerun.StateCreated ||
			run.State == capturerun.StateAttached {
			*resolver.sawActive = true
		}
	}
	return capturegrant.CaptureAuthoritySet{}, errors.New(
		"injected authority resolution failure",
	)
}

func (resolver inspectingFailingAuthorities) Review(
	context.Context,
	environment.EnvironmentID,
) (capturegrant.CaptureAuthorityReview, error) {
	return capturegrant.CaptureAuthorityReview{}, errors.New("injected authority review failure")
}

func (resolver inspectingFailingAuthorities) Resolve(
	context.Context,
	captureidentity.Reference,
) (capturegrant.CaptureAuthoritySet, error) {
	return capturegrant.CaptureAuthoritySet{}, errors.New("injected authority resolution failure")
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
