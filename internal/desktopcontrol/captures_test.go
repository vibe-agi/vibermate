package desktopcontrol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
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
	assertBodyContains(
		t,
		managed.Body.Bytes(),
		`"kind":"managed_run"`,
		`"managedRun":`,
		`"homeDirectory":"/Users/mira"`,
		`"operatingSystem":"darwin"`,
		`"operatingSystemVersion":"26.0"`,
		`"architecture":"arm64"`,
		`"timeZone":"Asia/Singapore"`,
		`"recognition":"verified"`,
		`"clientAdapter":{"id":"claude-code","revision":1,"version":"2.1.220"`,
	)
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
		{ID: "managed-running", ExecutableLabel: "codex", CWD: "/workspace/a", State: capturerun.StateAttached, Observation: capturerun.ObservationWaitingForTraffic, CreatedAt: now.Add(-time.Hour), UpdatedAt: now, ActivityAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), ProcessID: 42},
		{ID: "managed-tie", ExecutableLabel: "claude", CWD: "/workspace/b", State: capturerun.StateFinished, Observation: capturerun.ObservationWaitingForTraffic, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(time.Hour)},
		{ID: "managed-old", ExecutableLabel: "claude", CWD: "/workspace/c", State: capturerun.StateFinished, Observation: capturerun.ObservationWaitingForTraffic, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-3 * time.Minute), ExpiresAt: now.Add(time.Hour)},
	}
	manual := []manualcapture.View{
		{ID: "manual-running", DisplayName: "IDE proxy", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeUntilRevoked, State: manualcapture.StateActive, CredentialRevision: 1, Observation: manualcapture.ObservationWaiting, CreatedAt: now.Add(-time.Hour), UpdatedAt: now, ActivityAt: now},
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

func TestCaptureListAnnotatesEachPageWithOneEnvironmentLookup(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	managed := []capturerun.View{
		{ID: "managed-running", ExecutableLabel: "codex", CWD: "/workspace/a", State: capturerun.StateAttached, Observation: capturerun.ObservationWaitingForTraffic, CreatedAt: now.Add(-time.Hour), UpdatedAt: now, ActivityAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), ProcessID: 42},
		{ID: "managed-old", ExecutableLabel: "claude", CWD: "/workspace/c", State: capturerun.StateFinished, Observation: capturerun.ObservationWaitingForTraffic, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-3 * time.Minute), ExpiresAt: now.Add(time.Hour)},
	}
	manual := []manualcapture.View{
		{ID: "manual-running", DisplayName: "IDE proxy", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeUntilRevoked, State: manualcapture.StateActive, CredentialRevision: 1, Observation: manualcapture.ObservationWaiting, CreatedAt: now.Add(-time.Hour), UpdatedAt: now, ActivityAt: now},
	}
	assignments := &assignmentFixture{items: map[string]captureassignment.Assignment{}}
	assignments.put(captureidentity.KindManagedRun, "managed-running", "work", 7)
	assignments.put(captureidentity.KindManagedRun, "managed-old", "personal", 3)
	application := pagedCaptureApplicationWith(t, managed, manual, assignments)

	first := readCapturePage(t, application, "/api/v1/captures?limit=2")
	assertCaptureKeys(t, first.Items, "manual_capture:manual-running", "managed_run:managed-running")
	assignments.assertLastBatch(t, 1, first.Items)
	assertCaptureEnvironment(t, first.Items[0], "", 0)
	assertCaptureEnvironment(t, first.Items[1], "work", 7)

	second := readCapturePage(t, application, "/api/v1/captures?limit=2&cursor="+url.QueryEscape(first.NextCursor))
	assertCaptureKeys(t, second.Items, "managed_run:managed-old")
	assignments.assertLastBatch(t, 2, second.Items)
	assertCaptureEnvironment(t, second.Items[0], "personal", 3)

	raw := environmentRequest(t, application, http.MethodGet, "/api/v1/captures?limit=2", 0, "", nil)
	var wire struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw.Body.Bytes(), &wire); err != nil || len(wire.Items) != 2 {
		t.Fatalf("raw Capture page = %s, %v", raw.Body.Bytes(), err)
	}
	if _, present := wire.Items[0]["environment"]; present {
		t.Fatalf("unassigned Capture carries an environment: %s", raw.Body.Bytes())
	}
	if string(wire.Items[1]["environment"]) != `{"id":"work","revision":7}` {
		t.Fatalf("assigned Capture environment = %s", wire.Items[1]["environment"])
	}
}

func TestCaptureListReadsAssignmentsThroughTheProductRuntime(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
		Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), Offline: runtime,
		CaptureRuns: runtime.CaptureRunReader(), ManualCaptures: runtime.ManualCaptures(),
		Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	create := func(name string) captureidentity.Reference {
		t.Helper()
		grant, err := runtime.ManualCaptures().Create(ctx, manualcapture.CreateCommand{
			Owner: manualcapture.NewLocalOwnerScope(), DisplayName: name,
			ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeUntilRevoked,
		})
		if err != nil {
			t.Fatal(err)
		}
		reference, err := captureidentity.New(captureidentity.KindManualCapture, grant.Capture.ID)
		if err != nil {
			t.Fatal(err)
		}
		return reference
	}
	assigned, unassigned := create("Assigned proxy"), create("Unassigned proxy")
	assignment, err := runtime.CaptureAssignments().Create(ctx, captureassignment.CreateCommand{
		Capture: assigned, EnvironmentID: environment.SystemTransparentID,
		Source: captureassignment.SourceManualCreate,
	})
	if err != nil {
		t.Fatal(err)
	}
	page := readCapturePage(t, application, "/api/v1/captures?limit=10")
	if len(page.Items) != 2 {
		t.Fatalf("Capture page = %+v", page.Items)
	}
	for _, item := range page.Items {
		switch item.Key {
		case assigned.Key():
			assertCaptureEnvironment(t, item, environment.SystemTransparentID, assignment.EnvironmentRevision)
		case unassigned.Key():
			assertCaptureEnvironment(t, item, "", 0)
		default:
			t.Fatalf("unexpected Capture %s", item.Key)
		}
	}
}

func TestCaptureListReportsTheRevisionAppliedAfterLaunch(t *testing.T) {
	t.Parallel()
	assignments := newAssignmentFixture()
	application := unifiedCaptureApplication(t, assignments)
	applied := environmentRequest(
		t, application, http.MethodPost,
		"/api/v1/captures/managed_run:same-id/environment-assignment/actions/apply-latest",
		1, "apply-latest-before-list", nil,
	)
	if applied.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", applied.Code, applied.Body.Bytes())
	}
	page := readCapturePage(t, application, "/api/v1/captures?limit=10")
	for _, item := range page.Items {
		switch item.Key {
		case "managed_run:same-id":
			assertCaptureEnvironment(t, item, "system_transparent", 2)
		case "manual_capture:same-id":
			assertCaptureEnvironment(t, item, "system_transparent", 1)
		default:
			t.Fatalf("unexpected Capture %s", item.Key)
		}
	}
	single := environmentRequest(t, application, http.MethodGet, "/api/v1/captures/managed_run:same-id", 0, "", nil)
	if single.Code != http.StatusOK || bytes.Contains(single.Body.Bytes(), []byte(`"environment"`)) {
		t.Fatalf("single Capture read changed shape: status=%d body=%s", single.Code, single.Body.Bytes())
	}
}

func TestCaptureListOmitsEnvironmentWhenTheLookupFails(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "storage error", err: errors.Join(captureassignment.ErrAssignmentUnavailable, errors.New("database is locked"))},
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "runtime stopping", err: captureassignment.ErrRuntimeStopping},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assignments := newAssignmentFixture()
			assignments.batchErr = test.err
			application := unifiedCaptureApplication(t, assignments)
			response := environmentRequest(t, application, http.MethodGet, "/api/v1/captures?limit=10", 0, "", nil)
			if response.Code != http.StatusOK {
				t.Fatalf("list status=%d body=%s", response.Code, response.Body.Bytes())
			}
			if bytes.Contains(response.Body.Bytes(), []byte(`"environment"`)) {
				t.Fatalf("failed lookup still annotated the page: %s", response.Body.Bytes())
			}
			page := readCapturePage(t, application, "/api/v1/captures?limit=10")
			assertCaptureKeys(t, page.Items, "manual_capture:same-id", "managed_run:same-id")
			if assignments.batchCalls != 2 || !assignments.batchDeadline {
				t.Fatalf("lookup calls=%d bounded=%t", assignments.batchCalls, assignments.batchDeadline)
			}
		})
	}
}

func assertCaptureEnvironment(
	t *testing.T,
	item desktopcontrol.CaptureResponse,
	id environment.EnvironmentID,
	revision environment.Revision,
) {
	t.Helper()
	if id == "" {
		if item.Environment != nil {
			t.Fatalf("%s environment = %+v, want none", item.Key, *item.Environment)
		}
		return
	}
	if item.Environment == nil || item.Environment.ID != id || item.Environment.Revision != revision {
		t.Fatalf("%s environment = %+v, want %s@%d", item.Key, item.Environment, id, revision)
	}
}

func TestCaptureAssignmentRouteIsReadOnlyAfterLaunch(t *testing.T) {
	t.Parallel()
	assignments := newAssignmentFixture()
	application := unifiedCaptureApplication(t, assignments)

	get := environmentRequest(t, application, http.MethodGet, "/api/v1/captures/managed_run:same-id/environment-assignment", 0, "", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.Bytes())
	}
	assertBodyContains(t, get.Body.Bytes(), `"captureKey":"managed_run:same-id"`, `"revision":1`)

	mutation := environmentRequest(
		t,
		application,
		http.MethodPatch,
		"/api/v1/captures/managed_run:same-id/environment-assignment",
		1,
		"capture-assignment-is-read-only",
		[]byte(`{"environmentId":"environment-other"}`),
	)
	if mutation.Code != http.StatusNotFound {
		t.Fatalf("mutation status=%d body=%s", mutation.Code, mutation.Body.Bytes())
	}
	assertBodyContains(t, mutation.Body.Bytes(), `"code":"control_route_not_found"`)
}

func TestCaptureAssignmentAppliesLatestPublishedEnvironmentAtAnExplicitAction(t *testing.T) {
	t.Parallel()
	assignments := newAssignmentFixture()
	application := unifiedCaptureApplication(t, assignments)
	response := environmentRequest(
		t,
		application,
		http.MethodPost,
		"/api/v1/captures/managed_run:same-id/environment-assignment/actions/apply-latest",
		1,
		"apply-latest-environment",
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", response.Code, response.Body.Bytes())
	}
	assertBodyContains(
		t,
		response.Body.Bytes(),
		`"captureKey":"managed_run:same-id"`,
		`"revision":2`,
		`"environmentRevision":2`,
		`"launchEnvironmentRevision":1`,
	)
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
			ExpiresAt: now.Add(time.Hour), CatalogRevision: 1,
			Runtime: capturerun.RuntimeMetadata{
				LocalUserName: "mira", HomeDirectory: "/Users/mira",
				OperatingSystem: "darwin", OperatingSystemVersion: "26.0",
				Architecture: "arm64", TimeZone: "Asia/Singapore",
			},
			Recognition: clientadapter.RecognitionVerified,
			Adapter: &clientadapter.Evidence{
				ID: "claude-code", Revision: 1, Version: "2.1.220", CatalogRevision: 1,
				InstallShape:  clientadapter.InstallNativeSingleBinary,
				ReleaseSHA256: "8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081",
				LaunchRecipe:  clientadapter.LaunchNodeEnvProxy,
			},
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
	return pagedCaptureApplicationWith(t, managed, manual, newAssignmentFixture())
}

func pagedCaptureApplicationWith(
	t *testing.T,
	managed []capturerun.View,
	manual []manualcapture.View,
	assignments captureassignment.Controller,
) http.Handler {
	t.Helper()
	runtime := startRuntime(t)
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: assignments, Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
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
		if !items[left].ActivityTime().Equal(items[right].ActivityTime()) {
			return items[left].ActivityTime().After(items[right].ActivityTime())
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
		if fixturePageAfter(running, item.ActivityTime(), item.ID, cursor.Running, cursor.ActivityAt, cursor.AfterID, cursor.IncludeAtActivityAt) {
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
		if !items[left].ActivityTime().Equal(items[right].ActivityTime()) {
			return items[left].ActivityTime().After(items[right].ActivityTime())
		}
		return items[left].ID < items[right].ID
	})
	if cursor := request.Cursor; cursor != nil {
		filtered := make([]manualcapture.View, 0, len(items))
		for _, item := range items {
			if fixturePageAfter(
				item.State == manualcapture.StateActive,
				item.ActivityTime(),
				item.ID,
				cursor.Running,
				cursor.ActivityAt,
				cursor.AfterID,
				cursor.IncludeAtActivityAt,
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
	mu    sync.Mutex
	items map[string]captureassignment.Assignment
	// The batch fields record how the list used ResolveMany and let a test
	// fail it.
	batchErr        error
	batchCalls      int
	batchReferences []captureidentity.Reference
	batchDeadline   bool
}

func (fixture *assignmentFixture) put(
	kind captureidentity.Kind,
	id string,
	environmentID environment.EnvironmentID,
	revision environment.Revision,
) {
	reference, _ := captureidentity.New(kind, id)
	var launchDigest, currentDigest environment.CandidateDigest
	launchDigest[0], currentDigest[0] = 1, byte(revision)
	authority, _ := environment.NewLaunchAuthorityBoundaryFromScopes(environmentID, 1, launchDigest, nil, nil)
	if revision == 1 {
		currentDigest = launchDigest
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.items[reference.Key()] = captureassignment.Assignment{
		Capture: reference, EnvironmentID: environmentID,
		EnvironmentRevision: revision, EnvironmentDigest: currentDigest,
		Revision: 1, Source: captureassignment.SourceLaunch, LaunchAuthority: authority,
		UpdatedAt: time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC),
	}
}

func (fixture *assignmentFixture) assertLastBatch(
	t *testing.T,
	calls int,
	items []desktopcontrol.CaptureResponse,
) {
	t.Helper()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.batchCalls != calls || !fixture.batchDeadline || len(fixture.batchReferences) != len(items) {
		t.Fatalf("lookup calls=%d bounded=%t references=%v, want %d call(s) for %d items",
			fixture.batchCalls, fixture.batchDeadline, fixture.batchReferences, calls, len(items))
	}
	for index, item := range items {
		if fixture.batchReferences[index].Key() != item.Key {
			t.Fatalf("lookup references=%v do not match the page", fixture.batchReferences)
		}
	}
}

func newAssignmentFixture() *assignmentFixture {
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	items := make(map[string]captureassignment.Assignment)
	for _, kind := range []captureidentity.Kind{captureidentity.KindManagedRun, captureidentity.KindManualCapture} {
		reference, _ := captureidentity.New(kind, "same-id")
		var digest environment.CandidateDigest
		digest[0] = 1
		authority, _ := environment.NewLaunchAuthorityBoundaryFromScopes(
			"system_transparent", 1, digest, nil, nil,
		)
		items[reference.Key()] = captureassignment.Assignment{
			Capture: reference, EnvironmentID: "system_transparent",
			EnvironmentRevision: 1, EnvironmentDigest: digest,
			Revision: 1, Source: captureassignment.SourceSystemTransparent,
			LaunchAuthority: authority, UpdatedAt: now,
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
func (fixture *assignmentFixture) CreateForLaunch(
	ctx context.Context,
	command captureassignment.CreateCommand,
) (captureassignment.Assignment, environment.LaunchEnvironmentPolicy, error) {
	assignment, err := fixture.Create(ctx, command)
	return assignment, environment.LaunchEnvironmentPolicy{}, err
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
func (fixture *assignmentFixture) ResolveMany(
	ctx context.Context,
	references []captureidentity.Reference,
) (map[captureidentity.Reference]captureassignment.Assignment, error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.batchCalls++
	fixture.batchReferences = append([]captureidentity.Reference(nil), references...)
	_, fixture.batchDeadline = ctx.Deadline()
	if fixture.batchErr != nil {
		return nil, fixture.batchErr
	}
	result := make(map[captureidentity.Reference]captureassignment.Assignment, len(references))
	for _, reference := range references {
		if assignment, exists := fixture.items[reference.Key()]; exists {
			result[reference] = assignment
		}
	}
	return result, nil
}
func (fixture *assignmentFixture) ApplyLatest(
	_ context.Context,
	reference captureidentity.Reference,
	expected captureassignment.Revision,
) (captureassignment.Assignment, error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	assignment, exists := fixture.items[reference.Key()]
	if !exists {
		return captureassignment.Assignment{}, captureassignment.ErrAssignmentNotFound
	}
	if assignment.Revision != expected {
		return assignment, captureassignment.ErrAssignmentConflict
	}
	assignment.Revision++
	assignment.EnvironmentRevision++
	assignment.EnvironmentDigest[0]++
	assignment.UpdatedAt = assignment.UpdatedAt.Add(time.Second)
	fixture.items[reference.Key()] = assignment
	return assignment, nil
}
func assertBodyContains(t *testing.T, body []byte, values ...string) {
	t.Helper()
	for _, value := range values {
		if !bytes.Contains(body, []byte(value)) {
			t.Fatalf("body=%s missing %s", body, value)
		}
	}
}
