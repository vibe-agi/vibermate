package desktopcontrol_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

func TestUnifiedCaptureCatalogKeepsSameRawIDInBothKinds(t *testing.T) {
	t.Parallel()
	application := unifiedCaptureApplication(t, newAssignmentFixture())

	page := environmentRequest(t, application, http.MethodGet, "/api/v1/captures?limit=10", 0, "", nil)
	if page.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", page.Code, page.Body.Bytes())
	}
	assertBodyContains(t, page.Body.Bytes(), `"key":"managed_run:same-id"`, `"key":"manual_capture:same-id"`)

	managed := environmentRequest(t, application, http.MethodGet, "/api/v1/captures/managed_run:same-id", 0, "", nil)
	manual := environmentRequest(t, application, http.MethodGet, "/api/v1/captures/manual_capture:same-id", 0, "", nil)
	if managed.Code != http.StatusOK || manual.Code != http.StatusOK {
		t.Fatalf("managed=%d %s manual=%d %s", managed.Code, managed.Body.Bytes(), manual.Code, manual.Body.Bytes())
	}
	assertBodyContains(t, managed.Body.Bytes(), `"kind":"managed_run"`, `"managedRun":`)
	assertBodyContains(t, manual.Body.Bytes(), `"kind":"manual_capture"`, `"manualCapture":`)

	invalid := environmentRequest(t, application, http.MethodGet, "/api/v1/captures/same-id", 0, "", nil)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("untyped Capture key status=%d body=%s", invalid.Code, invalid.Body.Bytes())
	}
	legacy := environmentRequest(t, application, http.MethodGet, "/api/v1/capture-runs", 0, "", nil)
	if legacy.Code != http.StatusNotFound {
		t.Fatalf("legacy route status=%d body=%s", legacy.Code, legacy.Body.Bytes())
	}
}

func TestCaptureAssignmentRoutesExposeCASAndEveryBoundary(t *testing.T) {
	t.Parallel()
	assignments := newAssignmentFixture()
	application := unifiedCaptureApplication(t, assignments)

	get := environmentRequest(t, application, http.MethodGet, "/api/v1/captures/managed_run:same-id/environment-assignment", 0, "", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.Bytes())
	}
	assertBodyContains(t, get.Body.Bytes(), `"captureKey":"managed_run:same-id"`, `"revision":1`)

	hot := assignmentMutation(t, application, "managed_run:same-id", 1, "environment-hot", "capture-switch-hot")
	if hot.Code != http.StatusOK {
		t.Fatalf("hot status=%d body=%s", hot.Code, hot.Body.Bytes())
	}
	assertBodyContains(t, hot.Body.Bytes(), `"boundary":"hot_switch"`, `"applied":true`, `"closedConnections":[]`)
	replayed := assignmentMutation(t, application, "managed_run:same-id", 1, "environment-hot", "capture-switch-hot")
	if replayed.Code != hot.Code || replayed.Body.String() != hot.Body.String() || assignments.switchCount() != 1 {
		t.Fatalf(
			"idempotent replay status=%d body=%s switches=%d",
			replayed.Code,
			replayed.Body.Bytes(),
			assignments.switchCount(),
		)
	}

	stale := assignmentMutation(t, application, "managed_run:same-id", 1, "environment-other", "capture-switch-stale")
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.Bytes())
	}

	reconnect := assignmentMutation(t, application, "manual_capture:same-id", 1, "environment-reconnect", "capture-switch-reconnect")
	if reconnect.Code != http.StatusOK {
		t.Fatalf("reconnect status=%d body=%s", reconnect.Code, reconnect.Body.Bytes())
	}
	assertBodyContains(t, reconnect.Body.Bytes(), `"boundary":"reconnect_required"`, `"closedConnections":["connection-1"]`)

	restart := assignmentMutation(t, application, "managed_run:same-id", 2, "environment-restart", "capture-switch-restart")
	if restart.Code != http.StatusOK {
		t.Fatalf("restart status=%d body=%s", restart.Code, restart.Body.Bytes())
	}
	assertBodyContains(t, restart.Body.Bytes(), `"boundary":"restart_required"`, `"applied":false`, `"reasonCode":"capture_restart_required"`)
}

func assignmentMutation(t *testing.T, handler http.Handler, key string, revision uint64, target, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	body := []byte(`{"environmentId":"` + target + `"}`)
	return environmentRequest(t, handler, http.MethodPatch, "/api/v1/captures/"+key+"/environment-assignment", revision, idempotencyKey, body)
}

func unifiedCaptureApplication(t *testing.T, assignments captureassignment.Controller) http.Handler {
	t.Helper()
	runtime := startRuntime(t)
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: assignments, Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
		Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Accounts: runtime.ProviderAccounts(), Offline: runtime,
		CaptureRuns: captureReaderFixture{view: capturerun.View{
			ID: "same-id", ExecutableLabel: "claude", CWD: "/workspace", State: capturerun.StateFinished,
			Observation: capturerun.ObservationObserved, CreatedAt: now, UpdatedAt: now,
			ExpiresAt: now.Add(time.Hour),
		}},
		ManualCaptures: manualCaptureFixture{view: manualcapture.View{
			ID: "same-id", DisplayName: "Manual app", ClientClass: manualcapture.ClientDesktopApp,
			Lifetime: manualcapture.LifetimeUntilRevoked, State: manualcapture.StateActive,
			CredentialRevision: 1, Observation: manualcapture.ObservationWaiting,
			CreatedAt: now, UpdatedAt: now,
		}},
		Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return application
}

type captureReaderFixture struct{ view capturerun.View }

func (fixture captureReaderFixture) ListRuns(context.Context, capturerun.PageRequest) (capturerun.Page, error) {
	return capturerun.Page{Items: []capturerun.View{fixture.view}}, nil
}
func (fixture captureReaderFixture) GetRun(_ context.Context, id string) (capturerun.View, error) {
	if id != fixture.view.ID {
		return capturerun.View{}, capturerun.ErrNotFound
	}
	return fixture.view, nil
}

type manualCaptureFixture struct{ view manualcapture.View }

func (fixture manualCaptureFixture) Create(context.Context, manualcapture.CreateCommand) (manualcapture.Grant, error) {
	return manualcapture.Grant{}, errors.New("not implemented")
}
func (fixture manualCaptureFixture) Rotate(context.Context, manualcapture.RotateCommand) (manualcapture.Grant, error) {
	return manualcapture.Grant{}, errors.New("not implemented")
}
func (fixture manualCaptureFixture) Revoke(context.Context, manualcapture.RevokeCommand) (manualcapture.View, error) {
	return manualcapture.View{}, errors.New("not implemented")
}
func (fixture manualCaptureFixture) Get(_ context.Context, _ manualcapture.OwnerScope, id manualcapture.ID) (manualcapture.View, error) {
	if id.String() != fixture.view.ID {
		return manualcapture.View{}, manualcapture.ErrNotFound
	}
	return fixture.view, nil
}
func (fixture manualCaptureFixture) List(context.Context, manualcapture.PageRequest) (manualcapture.Page, error) {
	return manualcapture.Page{Items: []manualcapture.View{fixture.view}}, nil
}

type assignmentFixture struct {
	mu       sync.Mutex
	items    map[string]captureassignment.Assignment
	switches int
}

func newAssignmentFixture() *assignmentFixture {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	items := make(map[string]captureassignment.Assignment)
	for _, kind := range []captureidentity.Kind{captureidentity.KindManagedRun, captureidentity.KindManualCapture} {
		reference, _ := captureidentity.New(kind, "same-id")
		items[reference.Key()] = captureassignment.Assignment{
			Capture: reference, EnvironmentID: "system_transparent", Revision: 1,
			Source: captureassignment.SourceSystemTransparent, UpdatedAt: now,
		}
	}
	return &assignmentFixture{items: items}
}

func (fixture *assignmentFixture) Create(_ context.Context, command captureassignment.CreateCommand) (captureassignment.Assignment, error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	assignment := captureassignment.Assignment{Capture: command.Capture, EnvironmentID: command.EnvironmentID, Revision: 1, Source: command.Source, UpdatedAt: time.Now().UTC().Truncate(time.Millisecond)}
	fixture.items[command.Capture.Key()] = assignment
	return assignment, nil
}
func (fixture *assignmentFixture) Resolve(_ context.Context, reference captureidentity.Reference) (captureassignment.Assignment, error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	assignment, exists := fixture.items[reference.Key()]
	if !exists {
		return captureassignment.Assignment{}, captureassignment.ErrAssignmentNotFound
	}
	return assignment, nil
}
func (fixture *assignmentFixture) Switch(_ context.Context, command captureassignment.SwitchCommand) (captureassignment.SwitchResult, error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.switches++
	current, exists := fixture.items[command.Capture.Key()]
	if !exists {
		return captureassignment.SwitchResult{}, captureassignment.ErrAssignmentNotFound
	}
	if current.Revision != command.ExpectedRevision {
		return captureassignment.SwitchResult{Assignment: current}, captureassignment.ErrAssignmentConflict
	}
	if command.TargetEnvironmentID == "environment-restart" {
		return captureassignment.SwitchResult{Assignment: current, Boundary: captureassignment.BoundaryRestartRequired}, captureassignment.ErrLaunchRestartRequired
	}
	current.EnvironmentID = command.TargetEnvironmentID
	current.Revision++
	current.Source = command.Source
	fixture.items[command.Capture.Key()] = current
	boundary := captureassignment.BoundaryHotSwitch
	closed := []string(nil)
	if command.TargetEnvironmentID == "environment-reconnect" {
		boundary = captureassignment.BoundaryReconnectRequired
		closed = []string{"connection-1"}
	}
	return captureassignment.SwitchResult{Assignment: current, Boundary: boundary, ClosedConnections: closed}, nil
}

func (fixture *assignmentFixture) switchCount() int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.switches
}

func assertBodyContains(t *testing.T, body []byte, values ...string) {
	t.Helper()
	for _, value := range values {
		if !bytes.Contains(body, []byte(value)) {
			t.Fatalf("body=%s missing %s", body, value)
		}
	}
}
