package desktopcontrol_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/conversationprojection"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

type captureHTTPReader struct {
	*runtimepersistence.CaptureInspectionReader
	afterRead func()
	calls     int
}

func (r *captureHTTPReader) ReadCaptureObservation(ctx context.Context, ref captureidentity.Reference) (recording.CaptureObservationView, error) {
	r.calls++
	value, err := r.CaptureInspectionReader.ReadCaptureObservation(ctx, ref)
	if err == nil && r.afterRead != nil {
		r.afterRead()
	}
	return value, err
}
func captureObservationHTTPFixture(t *testing.T, configure ...func(*desktopcontrol.Options, activityHTTPRoles)) (*auditFixture, *captureHTTPReader, captureidentity.Reference, func()) {
	t.Helper()
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control, err := runtimepersistence.OpenControl(ctx, runtimepersistence.ControlOptions{DatabasePath: filepath.Join(root, "control.db"), LayoutID: "capture-http", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { control.Shutdown(ctx) })
	current, err := runtimepersistence.OpenObservations(ctx, runtimepersistence.ObservationOptions{DatabasePath: filepath.Join(root, "observations.db"), LayoutID: "capture-http", ArchiveID: "current", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { current.Shutdown(ctx) })
	owner := manualcapture.NewLocalOwnerScope()
	manager, err := manualcapture.NewManager(ctx, manualcapture.DefaultOptions(control.ManualCaptureRepository()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(ctx) })
	grant, err := manager.Create(ctx, manualcapture.CreateCommand{Owner: owner, DisplayName: "Observed manual", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	ref := captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: grant.Capture.ID}
	if err = current.PutCaptureObservation(ctx, recording.Scope{Capture: ref, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}, recording.CaptureObservation{ObservedAt: time.Now().UTC().Truncate(time.Millisecond), CredentialRevision: 1}); err != nil {
		t.Fatal(err)
	}
	catalog, err := runtimepersistence.NewInspectionCatalog(runtimepersistence.InspectionCatalogOptions{Root: root, LayoutID: "capture-http", Generation: 1, Sources: []runtimepersistence.InspectionSource{{ID: "current", RelativePath: "observations.db", Database: runtimepersistence.SchemaState{Identity: "vibermate-observations", Revision: 1}, Current: true}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close(ctx) })
	reader := &captureHTTPReader{CaptureInspectionReader: runtimepersistence.NewCaptureInspectionReader(catalog, control)}
	limits, err := exchangecontent.DefaultSourceLimits()
	if err != nil {
		t.Fatal(err)
	}
	activities, err := runtimepersistence.NewActivityInspectionReader(catalog, control, limits)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := runtimepersistence.NewContentInspectionReader(catalog, control, limits)
	if err != nil {
		t.Fatal(err)
	}
	seedHTTPInspectionMirror(t, current, control, filepath.Join(root, "observations.db"), []captureidentity.Reference{ref})
	fixture := newAuditFixtureConfigured(t, nil, func(o *desktopcontrol.Options) {
		o.Activities = nil
		o.ConversationIndexer = nil
		indexer, err := conversationprojection.New(conversationprojection.Options{Activities: activities, Contents: contents, IdentityInspection: activities, CaptureRuns: o.CaptureRuns})
		if err != nil {
			t.Fatal(err)
		}
		o.ConversationIdentityInspection = indexer
		o.ActivityInspection = activities
		o.Contents = contents
		o.CaptureObservations = reader
		o.Egress = runtimepersistence.NewEgressInspectionReader(catalog, control)
		for _, setup := range configure {
			setup(o, activityHTTPRoles{root: root, current: current, control: control, catalog: catalog, ref: ref})
		}
	})
	rotate := func() {
		id, err := manualcapture.ParseID(ref.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = manager.Rotate(ctx, manualcapture.RotateCommand{Owner: owner, ID: id, ExpectedCredentialRevision: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// The test's summary roots are independently empty optional tables, while
	// the observation comes from its real scoped writer and actual control row.
	db, err := sql.Open("sqlite", filepath.Join(root, "observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM runtime_activities`).Scan(&count); err != nil || (len(configure) == 0 && count != 0) {
		t.Fatalf("unexpected request roots: %d %v", count, err)
	}
	return fixture, reader, ref, rotate
}
func TestCaptureObservationHTTPKeepsPositiveFactSeparateFromZeroRequests(t *testing.T) {
	fixture, reader, ref, _ := captureObservationHTTPFixture(t)
	response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, "/api/v1/activities/summary?manualCaptureId="+ref.ID, fixture.readToken, nil)
	var value map[string]json.RawMessage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil {
		t.Fatalf("summary: %d %s", response.Code, response.Body)
	}
	var observation map[string]any
	if json.Unmarshal(value["captureObservation"], &observation) != nil || observation["captureId"] != ref.ID || observation["credentialRevision"] != float64(1) || observation["firstObservedAt"] == nil || observation["lastObservedAt"] == nil {
		t.Fatalf("missing separate scoped observation: %s", response.Body)
	}
	if string(value["requests"]) != "0" || string(value["firstObservedAt"]) != "null" || string(value["lastObservedAt"]) != "null" || reader.calls != 1 {
		t.Fatalf("observation fabricated requests: %s calls%d", response.Body, reader.calls)
	}
}
func TestCaptureObservationHTTPRechecksManualRevisionAfterRead(t *testing.T) {
	fixture, reader, ref, rotate := captureObservationHTTPFixture(t)
	reader.afterRead = rotate
	response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, "/api/v1/activities/summary?manualCaptureId="+ref.ID, fixture.readToken, nil)
	if response.Code != 409 {
		t.Fatalf("rotated observation escaped: %d %s", response.Code, response.Body)
	}
	if phase := response.Header().Get("X-Vibermate-Inspection-Phase"); phase != "final_capture" {
		t.Fatalf("capture conflict phase = %q, want final_capture", phase)
	}
}
func TestCaptureObservationHTTPDoesNotReadForSessionSummary(t *testing.T) {
	fixture, reader, _, _ := captureObservationHTTPFixture(t)
	response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, "/api/v1/activities/summary?client=codex&sessionId=session", fixture.readToken, nil)
	if response.Code != 200 || reader.calls != 0 {
		t.Fatalf("session fabricated Capture point: %d %s calls%d", response.Code, response.Body, reader.calls)
	}
}
