package desktopcontrol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/clienttarget"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

// The published routing sets beside System Transparent. "upstream" sends
// Claude's and Codex's API to an Upstream route; "responses-upstream" sends
// only the Responses API there and Claude's to its original destination.
// "later-upstream" starts as "responses-upstream", and its second revision,
// published after its runs launched and applied to them, adds Claude's.
const (
	upstreamEnvironment          environment.EnvironmentID = "upstream"
	responsesUpstreamEnvironment environment.EnvironmentID = "responses-upstream"
	laterUpstreamEnvironment     environment.EnvironmentID = "later-upstream"
)

// protectionScenario is one run the route is asked about: what the run's
// tier is, which routing set it is assigned, and what the route must answer.
type protectionScenario struct {
	id          string
	view        capturerun.View
	environment environment.EnvironmentID // empty: no assignment
	want        desktopcontrol.ClientProtectionsResponse
}

func TestClientProtectionsFollowTierRouteAndWorkspace(t *testing.T) {
	t.Parallel()
	fixture := newClientProtectionsFixture(t)
	for _, scenario := range clientProtectionScenarios(t) {
		t.Run(scenario.id, func(t *testing.T) {
			t.Parallel()
			body := fixture.read(t, "managed_run:"+scenario.id, http.StatusOK)
			assertClientProtections(t, body, scenario.want)
		})
	}

	t.Run("recognized Claude Code run stored by the Runtime", func(t *testing.T) {
		t.Parallel()
		// The Runtime's own capture runs name the client from the index its
		// catalog built, as the Desktop and the Server compose them.
		body := fixture.read(t, fixture.storedRun.Key(), http.StatusOK)
		assertClientProtections(t, body, desktopcontrol.ClientProtectionsResponse{
			CaptureKey: fixture.storedRun.Key(), Client: "claude-code",
			Protections: []desktopcontrol.ClientProtectionResponse{
				fallbackRow(recognizedBasis, desktopcontrol.ClientProtectionOn, ""),
				toolsRow(recognizedBasis, desktopcontrol.ClientProtectionOn, ""),
			},
		})
	})
	t.Run("recognized Claude Code run given a later revision", func(t *testing.T) {
		t.Parallel()
		// The launch authority stays what the run launched with, Claude's
		// API at its original destination, while the data plane follows the
		// revision applied since, which routes it Upstream.
		body := fixture.read(t, fixture.appliedRun.Key(), http.StatusOK)
		assertClientProtections(t, body, desktopcontrol.ClientProtectionsResponse{
			CaptureKey: fixture.appliedRun.Key(), Client: "claude-code",
			Protections: []desktopcontrol.ClientProtectionResponse{
				fallbackRow(recognizedBasis, desktopcontrol.ClientProtectionInactive, originalReason),
				toolsRow(recognizedBasis, desktopcontrol.ClientProtectionOn, ""),
			},
		})
	})
	t.Run("manual Capture", func(t *testing.T) {
		t.Parallel()
		body := fixture.read(t, fixture.manual.Key(), http.StatusOK)
		assertClientProtections(t, body, desktopcontrol.ClientProtectionsResponse{
			CaptureKey: fixture.manual.Key(), Protections: []desktopcontrol.ClientProtectionResponse{},
		})
	})
	t.Run("errors", func(t *testing.T) {
		t.Parallel()
		for _, test := range []struct {
			key    string
			query  string
			status int
			reason desktopcontrol.ReasonCode
		}{
			{"managed_run:missing-run", "", http.StatusNotFound, desktopcontrol.ReasonCaptureNotFound},
			{"manual_capture:missing-manual", "", http.StatusNotFound, desktopcontrol.ReasonCaptureNotFound},
			{"managed_run:recognized-claude", "?view=all", http.StatusUnprocessableEntity, desktopcontrol.ReasonInvalidRequest},
			{"recognized-claude", "", http.StatusUnprocessableEntity, desktopcontrol.ReasonInvalidRequest},
			{"session:recognized-claude", "", http.StatusUnprocessableEntity, desktopcontrol.ReasonInvalidRequest},
			// The routing set cannot be resolved, or the assignment cannot
			// be read: the App shows no protections.
			{"managed_run:unresolvable-claude", "", http.StatusServiceUnavailable, desktopcontrol.ReasonCaptureUnavailable},
			{"managed_run:unreadable-claude", "", http.StatusServiceUnavailable, desktopcontrol.ReasonCaptureUnavailable},
		} {
			response := fixture.request(t, "/api/v1/captures/"+test.key+"/client-protections"+test.query)
			if response.Code != test.status {
				t.Fatalf("%s%s status=%d body=%s, want %d", test.key, test.query, response.Code, response.Body.Bytes(), test.status)
			}
			assertJSONString(t, response.Body.Bytes(), "code", string(test.reason))
		}
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			response := environmentRequest(t, fixture.application, method,
				"/api/v1/captures/managed_run:recognized-claude/client-protections", 1,
				"client-protections-are-read-only", []byte(`{}`))
			if response.Code != http.StatusNotFound {
				t.Fatalf("%s status=%d body=%s, want the read-only route", method, response.Code, response.Body.Bytes())
			}
			assertJSONString(t, response.Body.Bytes(), "code", string(desktopcontrol.ReasonRouteNotFound))
		}
		read := httptest.NewRequest(http.MethodGet, "/api/v1/captures/managed_run:recognized-claude/client-protections", nil)
		if scope := fixture.application.RequiredScope(read); scope != desktopcontrol.ScopeRead {
			t.Fatalf("the route needs scope %q, want read", scope)
		}
	})
}

// The App parses api/samples/client-protections.json, so a change to what
// this route sends fails a test on both sides. VIBERMATE_UPDATE=1 rewrites it.
func TestClientProtectionsSampleIsCurrent(t *testing.T) {
	t.Parallel()
	fixture := newClientProtectionsFixture(t)
	sampled := []string{
		"verified-claude", "verified-codex", "recognized-claude",
		"recognized-claude-transparent", "recognized-claude-companion",
		"recognized-codex", "recognized-claude-switched-off",
		"recognized-codex-switched-off", "generic-claude",
	}
	entries := make([]json.RawMessage, 0, len(sampled))
	seen := map[string]bool{}
	for _, id := range sampled {
		body := fixture.read(t, "managed_run:"+id, http.StatusOK)
		entries = append(entries, json.RawMessage(bytes.TrimSpace(body)))
		var response desktopcontrol.ClientProtectionsResponse
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Protections) == 0 {
			seen["empty list"] = true
		}
		for _, row := range response.Protections {
			for _, value := range []string{"id " + row.ID, "basis " + string(row.Basis), "state " + string(row.State),
				"reason " + string(row.Reason), "appliedBy " + string(row.AppliedBy)} {
				seen[value] = true
			}
		}
	}
	// The sample is the App's only fixture for this route, so it holds every
	// value of every closed set, and a run with no protections.
	for _, value := range []string{
		"id core_owned_streaming_fallback", "id structured_workspace_tools",
		"id responses_data_keepalive", "id responses_websocket_http_fallback",
		"basis exact_release", "basis recognized_client", "basis operation_catalog",
		"state on", "state inactive", "state off",
		"reason original_destination", "reason remote_workspace", "reason operator_disabled",
		"appliedBy launcher", "appliedBy runtime", "empty list",
	} {
		if !seen[value] {
			t.Errorf("the sample has no %s", value)
		}
	}
	encoded, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("..", "..", "api", "samples", "client-protections.json")
	if os.Getenv("VIBERMATE_UPDATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read client protection sample: %v", err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatalf("the client protection sample is stale; rerun with VIBERMATE_UPDATE=1\n--- stored\n%s\n--- current\n%s", current, encoded)
	}
}

const (
	exactBasis      = desktopcontrol.ClientProtectionExactRelease
	recognizedBasis = desktopcontrol.ClientProtectionRecognizedClient
	catalogBasis    = desktopcontrol.ClientProtectionOperationCatalog
	originalReason  = desktopcontrol.ClientProtectionOriginalDestination
)

func fallbackRow(basis desktopcontrol.ClientProtectionBasis, state desktopcontrol.ClientProtectionState, reason desktopcontrol.ClientProtectionReason) desktopcontrol.ClientProtectionResponse {
	return desktopcontrol.ClientProtectionResponse{
		ID: "core_owned_streaming_fallback", Basis: basis, State: state, Reason: reason,
		AppliedBy: desktopcontrol.ClientProtectionLauncher,
	}
}

func toolsRow(basis desktopcontrol.ClientProtectionBasis, state desktopcontrol.ClientProtectionState, reason desktopcontrol.ClientProtectionReason) desktopcontrol.ClientProtectionResponse {
	return desktopcontrol.ClientProtectionResponse{
		ID: "structured_workspace_tools", Basis: basis, State: state, Reason: reason,
		AppliedBy: desktopcontrol.ClientProtectionRuntime,
	}
}

func keepaliveRow(basis desktopcontrol.ClientProtectionBasis, state desktopcontrol.ClientProtectionState, reason desktopcontrol.ClientProtectionReason) desktopcontrol.ClientProtectionResponse {
	return desktopcontrol.ClientProtectionResponse{
		ID: "responses_data_keepalive", Basis: basis, State: state, Reason: reason,
		AppliedBy: desktopcontrol.ClientProtectionRuntime,
	}
}

// websocketRow is the same for every Codex: the operation catalog answers
// every client's upgrade with 426.
var websocketRow = desktopcontrol.ClientProtectionResponse{
	ID: "responses_websocket_http_fallback", Basis: catalogBasis,
	State: desktopcontrol.ClientProtectionOn, AppliedBy: desktopcontrol.ClientProtectionRuntime,
}

func clientProtectionScenarios(t *testing.T) []protectionScenario {
	t.Helper()
	index := func(state clientadapter.ProtectionSwitch) clientadapter.RecognizedClients {
		t.Helper()
		clients, err := clientadapter.NewRecognizedClients(clientadapter.BuiltInCatalog(), state)
		if err != nil {
			t.Fatal(err)
		}
		return clients
	}
	indexes := map[clientadapter.ProtectionSwitch]clientadapter.RecognizedClients{
		clientadapter.ProtectionsOn:  index(clientadapter.ProtectionsOn),
		clientadapter.ProtectionsOff: index(clientadapter.ProtectionsOff),
	}
	on := desktopcontrol.ClientProtectionOn
	inactive := desktopcontrol.ClientProtectionInactive
	off := desktopcontrol.ClientProtectionOff
	local := workspaceidentity.EvidenceLocalLauncher
	companion := workspaceidentity.EvidenceRegisteredCompanion
	revision := clientadapter.BuiltInCatalog().Revision()
	verified := clientadapter.RecognitionVerified
	recognized := clientadapter.RecognitionRecognized
	claude := releaseEvidence(clientadapter.ClaudeCode221220DarwinARM64(), revision)
	// 0.145.0 carries the WebSocket bit too, which no reader acts on, so it
	// still gets one WebSocket row.
	codex := releaseEvidence(clientadapter.CodexCLI01450DarwinARM64(), revision)
	scenario := func(
		id, label string,
		recognition clientadapter.Recognition,
		adapter *clientadapter.Evidence,
		catalogRevision clientadapter.CatalogRevision,
		workspace workspaceidentity.Evidence,
		switched clientadapter.ProtectionSwitch,
		routing environment.EnvironmentID,
		client string,
		rows ...desktopcontrol.ClientProtectionResponse,
	) protectionScenario {
		view := capturerun.View{
			ID: id, ExecutableLabel: label, CWD: "/Users/example/project",
			State: capturerun.StateAttached, Observation: capturerun.ObservationObserved,
			Recognition: recognition, CatalogRevision: catalogRevision, Adapter: adapter,
			WorkspaceEvidence: workspace,
		}
		// As the Runtime's capture runs fill it: only for a recognized run
		// without release evidence, from the index of the built-in catalog
		// under the Runtime's switch, which names nothing at another revision.
		if recognition == recognized && adapter == nil {
			view.RecognizedClient, _ = indexes[switched].Client(catalogRevision, label)
		}
		if rows == nil {
			rows = []desktopcontrol.ClientProtectionResponse{}
		}
		return protectionScenario{
			id: id, view: view, environment: routing,
			want: desktopcontrol.ClientProtectionsResponse{
				CaptureKey: "managed_run:" + id, Client: client, Protections: rows,
			},
		}
	}
	switchOn, switchOff := clientadapter.ProtectionsOn, clientadapter.ProtectionsOff
	transparent := environment.SystemTransparentID
	return []protectionScenario{
		scenario("verified-claude", "claude", verified, claude, revision, local, switchOn, upstreamEnvironment, "claude-code",
			fallbackRow(exactBasis, on, ""), toolsRow(exactBasis, on, "")),
		// A verified release keeps setting its fallback on every protected
		// launch, Original Destination included (unchanged behaviour).
		scenario("verified-claude-transparent", "claude", verified, claude, revision, local, switchOn, transparent, "claude-code",
			fallbackRow(exactBasis, on, ""), toolsRow(exactBasis, inactive, originalReason)),
		scenario("verified-codex", "codex", verified, codex, revision, local, switchOn, upstreamEnvironment, "codex-cli",
			keepaliveRow(exactBasis, on, ""), websocketRow),
		scenario("recognized-claude", "claude", recognized, nil, revision, local, switchOn, upstreamEnvironment, "claude-code",
			fallbackRow(recognizedBasis, on, ""), toolsRow(recognizedBasis, on, "")),
		scenario("recognized-claude-transparent", "claude", recognized, nil, revision, local, switchOn, transparent, "claude-code",
			fallbackRow(recognizedBasis, inactive, originalReason), toolsRow(recognizedBasis, inactive, originalReason)),
		// An Upstream plan for another protocol gives neither anything to
		// act on.
		scenario("recognized-claude-responses-upstream", "claude", recognized, nil, revision, local, switchOn, responsesUpstreamEnvironment, "claude-code",
			fallbackRow(recognizedBasis, inactive, originalReason), toolsRow(recognizedBasis, inactive, originalReason)),
		scenario("recognized-claude-companion", "claude", recognized, nil, revision, companion, switchOn, upstreamEnvironment, "claude-code",
			fallbackRow(recognizedBasis, on, ""), toolsRow(recognizedBasis, off, desktopcontrol.ClientProtectionRemoteWorkspace)),
		scenario("recognized-claude-no-workspace", "claude", recognized, nil, revision, "", switchOn, upstreamEnvironment, "claude-code",
			fallbackRow(recognizedBasis, on, ""), toolsRow(recognizedBasis, off, desktopcontrol.ClientProtectionRemoteWorkspace)),
		scenario("recognized-codex", "codex", recognized, nil, revision, local, switchOn, upstreamEnvironment, "codex-cli",
			keepaliveRow(recognizedBasis, on, ""), websocketRow),
		scenario("recognized-codex-transparent", "codex", recognized, nil, revision, local, switchOn, transparent, "codex-cli",
			keepaliveRow(recognizedBasis, inactive, originalReason), websocketRow),
		// The Runtime's switch withdraws what the Runtime applies; the
		// launcher's own switch governs the fallback row.
		scenario("recognized-claude-switched-off", "claude", recognized, nil, revision, local, switchOff, upstreamEnvironment, "claude-code",
			fallbackRow(recognizedBasis, on, ""), toolsRow(recognizedBasis, off, desktopcontrol.ClientProtectionOperatorDisabled)),
		scenario("recognized-codex-switched-off", "codex", recognized, nil, revision, local, switchOff, upstreamEnvironment, "codex-cli",
			keepaliveRow(recognizedBasis, off, desktopcontrol.ClientProtectionOperatorDisabled), websocketRow),
		scenario("generic-claude", "claude", clientadapter.RecognitionUnverified, nil, revision, local, switchOn, upstreamEnvironment, ""),
		scenario("stale-claude", "claude", recognized, nil, revision-1, local, switchOn, upstreamEnvironment, ""),
		scenario("unassigned-claude", "claude", recognized, nil, revision, local, switchOn, "", "claude-code"),
	}
}

// releaseEvidence is the evidence a digest match on release stores.
func releaseEvidence(release clientadapter.Release, revision clientadapter.CatalogRevision) *clientadapter.Evidence {
	return &clientadapter.Evidence{
		ID: release.ID, Revision: release.Revision, Version: release.Version,
		CatalogRevision: revision, InstallShape: release.InstallShape,
		ReleaseSHA256: strings.Repeat("a", 64), LaunchRecipe: release.LaunchRecipe,
		Features: release.Features,
	}
}

type clientProtectionsFixture struct {
	application *desktopcontrol.Handler
	storedRun   captureidentity.Reference
	appliedRun  captureidentity.Reference
	manual      captureidentity.Reference
}

// newClientProtectionsFixture composes the route over a production Runtime
// whose routing sets and assignments are real. Each scenario's run is served
// beside the Runtime's own capture runs, so its tier is exact.
func newClientProtectionsFixture(t *testing.T) clientProtectionsFixture {
	t.Helper()
	paths, err := productruntime.NewRuntimePaths(filepath.Join(t.TempDir(), "runtime-data"))
	if err != nil {
		t.Fatal(err)
	}
	runtime := startConfiguredRuntime(t, paths, newCredentialStoreFixture(), func(options *productruntime.Options) {
		options.ClientCatalog = clientadapter.BuiltInCatalog()
	})
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	runs := scenarioRuns{views: map[string]capturerun.View{}, Reader: runtime.CaptureRunReader()}
	assignments := &scenarioAssignments{
		Controller: runtime.CaptureAssignments(),
		forced:     map[string]captureassignment.Assignment{},
		failed:     map[string]error{},
	}
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: assignments, Activities: runtime.Activities(), Contents: runtime.ExchangeContents(),
		Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(),
		Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(),
		Accounts: runtime.ProviderAccounts(), Offline: runtime,
		CaptureRuns: runs, ManualCaptures: runtime.ManualCaptures(),
		Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	origin := publishProtectionEnvironments(t, application)

	ctx := context.Background()
	// assign creates the assignment the issuer creates at launch, with the
	// client profile of the client the run was verified or recognized as.
	assign := func(reference captureidentity.Reference, routing environment.EnvironmentID, client string) captureassignment.Assignment {
		t.Helper()
		profile, err := clienttarget.NewProfile(client, clienttarget.FromEnvironment(nil))
		if err != nil {
			t.Fatal(err)
		}
		source := captureassignment.SourceLaunch
		if routing == environment.SystemTransparentID {
			source = captureassignment.SourceSystemTransparent
		}
		assignment, err := runtime.CaptureAssignments().Create(ctx, captureassignment.CreateCommand{
			Capture: reference, EnvironmentID: routing, Source: source, ClientProfile: profile,
		})
		if err != nil {
			t.Fatalf("assign %s to %s: %v", reference.Key(), routing, err)
		}
		return assignment
	}
	managed := func(id string) captureidentity.Reference {
		t.Helper()
		reference, err := captureidentity.New(captureidentity.KindManagedRun, id)
		if err != nil {
			t.Fatal(err)
		}
		return reference
	}
	profileClient := func(view capturerun.View) string {
		if view.Adapter != nil {
			return view.Adapter.ID
		}
		if view.Recognition == clientadapter.RecognitionRecognized {
			if strings.HasPrefix(view.ExecutableLabel, "codex") {
				return "codex-cli"
			}
			return "claude-code"
		}
		return ""
	}
	for _, scenario := range clientProtectionScenarios(t) {
		runs.views[scenario.id] = scenario.view
		if scenario.environment != "" {
			assign(managed(scenario.id), scenario.environment, profileClient(scenario.view))
		}
	}

	// A run whose assignment names a revision the routing set never had, and
	// one whose assignment cannot be read.
	for _, id := range []string{"unresolvable-claude", "unreadable-claude"} {
		view := runs.views["recognized-claude"]
		view.ID = id
		runs.views[id] = view
	}
	unresolvable := assign(managed("unresolvable-claude"), upstreamEnvironment, "claude-code")
	unresolvable.EnvironmentRevision = 99
	assignments.forced["managed_run:unresolvable-claude"] = unresolvable
	assignments.failed["managed_run:unreadable-claude"] = fmt.Errorf("%w: database is locked", captureassignment.ErrAssignmentUnavailable)

	// Recognized local runs stored through the Runtime's own capture runs, so
	// the Runtime's index names their client. A later revision of a routing
	// set can be published only over runs the Runtime knows.
	store := func() captureidentity.Reference {
		t.Helper()
		directory, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		workspace, err := runtime.WorkspaceIdentity().ResolveLocal(ctx, directory)
		if err != nil {
			t.Fatal(err)
		}
		grant, err := runtime.CaptureRuns().Create(ctx, capturerun.CreateCommand{
			CWD: directory, CanonicalExecutablePath: filepath.Join(directory, "claude"),
			ExecutableLabel: "claude", CatalogRevision: clientadapter.BuiltInCatalog().Revision(),
			Recognition: clientadapter.RecognitionRecognized, Workspace: workspace, Lifetime: time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := runtime.CaptureRuns().Finish(context.Background(), grant.Run.ID, grant.ControlCapability); err != nil {
				t.Error(err)
			}
		})
		return managed(grant.Run.ID)
	}
	stored := store()
	assign(stored, upstreamEnvironment, "claude-code")
	applied := store()
	launched := assign(applied, laterUpstreamEnvironment, "claude-code")
	publishProtectionEnvironment(t, application, origin, laterUpstreamEnvironment, 2, true)
	current, err := runtime.CaptureAssignments().ApplyLatest(ctx, applied, launched.Revision)
	if err != nil || current.EnvironmentRevision != 2 || current.LaunchAuthority != launched.LaunchAuthority {
		t.Fatalf("apply the later revision to %s: %+v, %v", applied.Key(), current, err)
	}

	created, err := runtime.ManualCaptures().Create(ctx, manualcapture.CreateCommand{
		Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "IDE proxy",
		ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeUntilRevoked,
	})
	if err != nil {
		t.Fatal(err)
	}
	manual, err := captureidentity.New(captureidentity.KindManualCapture, created.Capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.CaptureAssignments().Create(ctx, captureassignment.CreateCommand{
		Capture: manual, EnvironmentID: upstreamEnvironment, Source: captureassignment.SourceManualCreate,
	}); err != nil {
		t.Fatal(err)
	}
	return clientProtectionsFixture{application: application, storedRun: stored, appliedRun: applied, manual: manual}
}

func (fixture clientProtectionsFixture) request(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	return environmentRequest(t, fixture.application, http.MethodGet, path, 0, "", nil)
}

func (fixture clientProtectionsFixture) read(t *testing.T, key string, status int) []byte {
	t.Helper()
	response := fixture.request(t, "/api/v1/captures/"+key+"/client-protections")
	if response.Code != status {
		t.Fatalf("%s status=%d body=%s, want %d", key, response.Code, response.Body.Bytes(), status)
	}
	return response.Body.Bytes()
}

func assertClientProtections(t *testing.T, body []byte, want desktopcontrol.ClientProtectionsResponse) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var got desktopcontrol.ClientProtectionsResponse
	if err := decoder.Decode(&got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("protections = %s\nwant %+v", body, want)
	}
	// An empty list is a list, and an absent client or reason is absent.
	if len(want.Protections) == 0 && !bytes.Contains(body, []byte(`"protections":[]`)) {
		t.Fatalf("an empty list is not encoded as []: %s", body)
	}
	if want.Client == "" && bytes.Contains(body, []byte(`"client"`)) {
		t.Fatalf("a run naming no client carries one: %s", body)
	}
	if bytes.Contains(body, []byte(`"reason":""`)) {
		t.Fatalf("a protection that is on carries a reason: %s", body)
	}
}

// scenarioRuns serves each scenario's run, and every other run from the
// Runtime's own capture runs.
type scenarioRuns struct {
	capturerun.Reader
	views map[string]capturerun.View
}

func (runs scenarioRuns) GetRun(ctx context.Context, id string) (capturerun.View, error) {
	if view, found := runs.views[id]; found {
		return view, nil
	}
	return runs.Reader.GetRun(ctx, id)
}

// scenarioAssignments reads the Runtime's assignments, except for the
// Captures a test forces or fails.
type scenarioAssignments struct {
	captureassignment.Controller
	forced map[string]captureassignment.Assignment
	failed map[string]error
}

func (assignments *scenarioAssignments) Resolve(ctx context.Context, reference captureidentity.Reference) (captureassignment.Assignment, error) {
	if err, found := assignments.failed[reference.Key()]; found {
		return captureassignment.Assignment{}, err
	}
	if assignment, found := assignments.forced[reference.Key()]; found {
		return assignment, nil
	}
	return assignments.Controller.Resolve(ctx, reference)
}

// publishProtectionEnvironments publishes the routing sets through the App's
// own routes, with one Upstream endpoint and one Account, and returns the
// endpoint's origin.
func publishProtectionEnvironments(t *testing.T, application http.Handler) string {
	t.Helper()
	relay := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(relay.Close)
	endpoint := environmentRequest(t, application, http.MethodPost, "/api/v1/upstream-endpoints", 0,
		"client-protections-endpoint-0001",
		[]byte(`{"id":"target.protections","displayName":"Protections relay","origin":"`+relay.URL+
			`","backendProtocols":["anthropic_messages","openai_responses"]}`))
	if endpoint.Code != http.StatusCreated {
		t.Fatalf("create Endpoint status=%d body=%s", endpoint.Code, endpoint.Body.Bytes())
	}
	account := environmentRequest(t, application, http.MethodPost, "/api/v1/provider-accounts", 0,
		"client-protections-account-0001",
		[]byte(`{"id":"account.protections","displayName":"Protections","upstreamEndpointId":"target.protections","kind":"bearer_token","secret":"test-only-secret"}`))
	if account.Code != http.StatusCreated {
		t.Fatalf("create Account status=%d body=%s", account.Code, account.Body.Bytes())
	}
	publishProtectionEnvironment(t, application, relay.URL, upstreamEnvironment, 1, true)
	publishProtectionEnvironment(t, application, relay.URL, responsesUpstreamEnvironment, 1, false)
	publishProtectionEnvironment(t, application, relay.URL, laterUpstreamEnvironment, 1, false)
	return relay.URL
}

// publishProtectionEnvironment publishes one revision of a routing set whose
// Responses API is Upstream, and whose Claude API is Upstream only when
// anthropic is set. Each revision that changes the Claude plan bumps it.
func publishProtectionEnvironment(
	t *testing.T,
	application http.Handler,
	origin string,
	id environment.EnvironmentID,
	revision uint64,
	anthropic bool,
) {
	t.Helper()
	direct, err := json.Marshal(egressprofile.Direct())
	if err != nil {
		t.Fatal(err)
	}
	plan := func(name, protocol string, upstream bool, revision uint64) string {
		destination := `{"kind":"original"}`
		if upstream {
			destination = `{"kind":"upstream","upstream":{
				"routes":[{"id":"route.` + name + `","revision":1,
					"providerTarget":{"id":"target.protections","revision":1,"origin":"` + origin + `",
						"realmId":"target.protections","capabilities":["messages","streaming","tool_calls"]},
					"backendProtocol":"` + protocol + `",
					"accountPolicy":{"revision":1,"mode":"fixed","fixedAccountId":"account.protections",
						"accounts":[{"id":"account.protections","revision":1,"displayName":"Protections"}]},
					"modelPolicy":{"revision":1,"mode":"passthrough","mappings":[]},
					"wireProfileRef":"follow-client","pluginBindings":[]}],
				"defaultRouteId":"route.` + name + `",
				"routeSet":{"id":"routes.` + name + `","revision":1,"candidateRouteIds":["route.` + name + `"]}}}`
		}
		return fmt.Sprintf(`{"id":"plan.%s","revision":%d,"clientProtocol":"%s",
			"clientAdapterPolicy":{"id":"adapter.%s","revision":1},
			"destination":%s,"egressProfile":%s,"transforms":[],"pluginBindings":[]}`,
			name, revision, protocol, name, destination, direct)
	}
	body := []byte(fmt.Sprintf(`{"expectedDraftRevision":0,"name":"%s","state":"active",
		"clientEndpoints":[
			{"id":"endpoint.anthropic","revision":%d,"clientOrigin":"https://api.anthropic.com",
				"protocolPlans":[%s]},
			{"id":"endpoint.openai","revision":1,"clientOrigin":"https://api.openai.com",
				"protocolPlans":[%s]}],
		"pluginBindings":[],"budgetPolicy":{"id":"","revision":0},
		"contentRecording":{"mode":"full","retentionDays":30}}`,
		id, revision, plan("anthropic", "anthropic_messages", anthropic, revision),
		plan("openai", "openai_responses", true, 1)))
	path := "/api/v1/environments/" + string(id) + "/draft"
	key := fmt.Sprintf("client-protections-%s-%d", id, revision)
	draft := environmentRequest(t, application, http.MethodPut, path, revision-1, key+"-draft", body)
	if draft.Code != http.StatusOK {
		t.Fatalf("draft %s@%d status=%d body=%s", id, revision, draft.Code, draft.Body.Bytes())
	}
	var saved struct {
		DraftRevision uint64 `json:"draftRevision"`
	}
	if err := json.Unmarshal(draft.Body.Bytes(), &saved); err != nil || saved.DraftRevision == 0 {
		t.Fatalf("draft %s@%d revision: %s, %v", id, revision, draft.Body.Bytes(), err)
	}
	preview := environmentRequest(t, application, http.MethodPost, path+"/actions/preview", saved.DraftRevision, key+"-preview", nil)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview %s@%d status=%d body=%s", id, revision, preview.Code, preview.Body.Bytes())
	}
	publish := environmentRequest(t, application, http.MethodPost, path+"/actions/publish", saved.DraftRevision, key+"-publish", nil)
	if publish.Code != http.StatusOK {
		t.Fatalf("publish %s@%d status=%d body=%s", id, revision, publish.Code, publish.Body.Bytes())
	}
	assertJSONString(t, publish.Body.Bytes(), "outcome", "committed")
}
