package desktopcontrol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
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

func TestUnifiedCaptureCatalogPaginatesRunningBeforeCompleteHistory(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	managed := []capturerun.View{
		{ID: "managed-running", ExecutableLabel: "codex", CWD: "/workspace/a", State: capturerun.StateAttached, Observation: capturerun.ObservationWaitingForTraffic, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), ProcessID: 42},
		{ID: "managed-tie", ExecutableLabel: "claude", CWD: "/workspace/b", State: capturerun.StateFinished, Observation: capturerun.ObservationWaitingForTraffic, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(time.Hour)},
		{ID: "managed-old", ExecutableLabel: "claude", CWD: "/workspace/c", State: capturerun.StateFinished, Observation: capturerun.ObservationWaitingForTraffic, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-3 * time.Minute), ExpiresAt: now.Add(time.Hour)},
	}
	manual := []manualcapture.View{
		{ID: "manual-running", DisplayName: "IDE proxy", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeUntilRevoked, State: manualcapture.StateActive, CredentialRevision: 1, Observation: manualcapture.ObservationWaiting, CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
		{ID: "manual-tie", DisplayName: "Old proxy", ClientClass: manualcapture.ClientOther, Lifetime: manualcapture.LifetimeUntilRevoked, State: manualcapture.StateRevoked, CredentialRevision: 1, Observation: manualcapture.ObservationWaiting, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-2 * time.Minute)},
	}
	application := pagedCaptureApplication(t, managed, manual)

	first := readCapturePage(t, application, "/api/v1/captures?limit=2")
	assertCaptureKeys(t, first.Items,
		"manual_capture:manual-running",
		"managed_run:managed-running",
	)
	if first.NextCursor == "" {
		t.Fatal("first Capture page did not expose a continuation cursor")
	}
	second := readCapturePage(
		t,
		application,
		"/api/v1/captures?limit=2&cursor="+url.QueryEscape(first.NextCursor),
	)
	assertCaptureKeys(t, second.Items,
		"managed_run:managed-tie",
		"manual_capture:manual-tie",
	)
	if second.NextCursor == "" {
		t.Fatal("second Capture page did not expose a continuation cursor")
	}
	third := readCapturePage(
		t,
		application,
		"/api/v1/captures?limit=2&cursor="+url.QueryEscape(second.NextCursor),
	)
	assertCaptureKeys(t, third.Items, "managed_run:managed-old")
	if third.NextCursor != "" {
		t.Fatalf("terminal Capture page cursor=%q, want empty", third.NextCursor)
	}

	invalid := environmentRequest(
		t,
		application,
		http.MethodGet,
		"/api/v1/captures?limit=2&cursor=not-a-cursor",
		0,
		"",
		nil,
	)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid Capture cursor status=%d body=%s", invalid.Code, invalid.Body.Bytes())
	}
}

func readCapturePage(
	t *testing.T,
	handler http.Handler,
	path string,
) desktopcontrol.CaptureListResponse {
	t.Helper()
	response := environmentRequest(t, handler, http.MethodGet, path, 0, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("Capture page status=%d body=%s", response.Code, response.Body.Bytes())
	}
	var page desktopcontrol.CaptureListResponse
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&page); err != nil {
		t.Fatalf("decode Capture page: %v", err)
	}
	return page
}

func assertCaptureKeys(
	t *testing.T,
	items []desktopcontrol.CaptureResponse,
	expected ...string,
) {
	t.Helper()
	actual := make([]string, 0, len(items))
	for _, item := range items {
		actual = append(actual, item.Key)
	}
	if len(actual) != len(expected) {
		t.Fatalf("Capture keys=%v, want %v", actual, expected)
	}
	for index := range expected {
		if actual[index] != expected[index] {
			t.Fatalf("Capture keys=%v, want %v", actual, expected)
		}
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
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime,
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

func pagedCaptureApplication(
	t *testing.T,
	managed []capturerun.View,
	manual []manualcapture.View,
) http.Handler {
	t.Helper()
	runtime := startRuntime(t)
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: newAssignmentFixture(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
		Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime,
		CaptureRuns:    capturePageReaderFixture{items: managed},
		ManualCaptures: manualCapturePageFixture{items: manual},
		Clock:          desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return application
}

type capturePageReaderFixture struct{ items []capturerun.View }

func (fixture capturePageReaderFixture) ListRuns(
	_ context.Context,
	request capturerun.PageRequest,
) (capturerun.Page, error) {
	items := append([]capturerun.View(nil), fixture.items...)
	sort.Slice(items, func(left, right int) bool {
		leftRunning := items[left].State == capturerun.StateCreated || items[left].State == capturerun.StateAttached
		rightRunning := items[right].State == capturerun.StateCreated || items[right].State == capturerun.StateAttached
		if leftRunning != rightRunning {
			return leftRunning
		}
		if !items[left].UpdatedAt.Equal(items[right].UpdatedAt) {
			return items[left].UpdatedAt.After(items[right].UpdatedAt)
		}
		return items[left].ID < items[right].ID
	})
	items = filterCaptureRunFixture(items, request.Cursor)
	limit := request.Normalized().Limit
	if len(items) > limit {
		items = items[:limit]
	}
	return capturerun.Page{Items: items}, nil
}

func (fixture capturePageReaderFixture) GetRun(
	_ context.Context,
	id string,
) (capturerun.View, error) {
	for _, item := range fixture.items {
		if item.ID == id {
			return item, nil
		}
	}
	return capturerun.View{}, capturerun.ErrNotFound
}

func filterCaptureRunFixture(
	items []capturerun.View,
	cursor *capturerun.PageCursor,
) []capturerun.View {
	if cursor == nil {
		return items
	}
	filtered := make([]capturerun.View, 0, len(items))
	for _, item := range items {
		running := item.State == capturerun.StateCreated || item.State == capturerun.StateAttached
		if fixturePageAfter(running, item.UpdatedAt, item.ID, cursor.Running, cursor.UpdatedAt, cursor.AfterID, cursor.IncludeAtUpdatedAt) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

type manualCapturePageFixture struct{ items []manualcapture.View }

func (fixture manualCapturePageFixture) Create(context.Context, manualcapture.CreateCommand) (manualcapture.Grant, error) {
	return manualcapture.Grant{}, errors.New("not implemented")
}
func (fixture manualCapturePageFixture) Rotate(context.Context, manualcapture.RotateCommand) (manualcapture.Grant, error) {
	return manualcapture.Grant{}, errors.New("not implemented")
}
func (fixture manualCapturePageFixture) Revoke(context.Context, manualcapture.RevokeCommand) (manualcapture.View, error) {
	return manualcapture.View{}, errors.New("not implemented")
}
func (fixture manualCapturePageFixture) Get(_ context.Context, _ manualcapture.OwnerScope, id manualcapture.ID) (manualcapture.View, error) {
	for _, item := range fixture.items {
		if item.ID == id.String() {
			return item, nil
		}
	}
	return manualcapture.View{}, manualcapture.ErrNotFound
}
func (fixture manualCapturePageFixture) List(_ context.Context, request manualcapture.PageRequest) (manualcapture.Page, error) {
	items := append([]manualcapture.View(nil), fixture.items...)
	sort.Slice(items, func(left, right int) bool {
		leftRunning := items[left].State == manualcapture.StateActive
		rightRunning := items[right].State == manualcapture.StateActive
		if leftRunning != rightRunning {
			return leftRunning
		}
		if !items[left].UpdatedAt.Equal(items[right].UpdatedAt) {
			return items[left].UpdatedAt.After(items[right].UpdatedAt)
		}
		return items[left].ID < items[right].ID
	})
	if cursor := request.Cursor; cursor != nil {
		filtered := make([]manualcapture.View, 0, len(items))
		for _, item := range items {
			if fixturePageAfter(
				item.State == manualcapture.StateActive,
				item.UpdatedAt,
				item.ID,
				cursor.Running,
				cursor.UpdatedAt,
				cursor.AfterID,
				cursor.IncludeAtUpdatedAt,
			) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	limit := request.Normalized().Limit
	if len(items) > limit {
		items = items[:limit]
	}
	return manualcapture.Page{Items: items}, nil
}

func fixturePageAfter(
	running bool,
	updatedAt time.Time,
	id string,
	cursorRunning bool,
	cursorUpdatedAt time.Time,
	afterID string,
	includeAtUpdatedAt bool,
) bool {
	if running != cursorRunning {
		return !running
	}
	if !updatedAt.Equal(cursorUpdatedAt) {
		return updatedAt.Before(cursorUpdatedAt)
	}
	return includeAtUpdatedAt && id > afterID
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
