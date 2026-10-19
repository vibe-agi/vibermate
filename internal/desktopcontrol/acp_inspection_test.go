package desktopcontrol_test

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

type acpHTTPInspection struct {
	*runtimepersistence.ACPInspectionReader
	afterRead func()
	checks    int
	reads     int
}

func (r *acpHTTPInspection) Read(ctx context.Context, id string, now int64) (acpobservation.Record, error) {
	r.reads++
	value, err := r.ACPInspectionReader.Read(ctx, id, now)
	if err == nil && r.afterRead != nil {
		r.afterRead()
	}
	return value, err
}
func (r *acpHTTPInspection) Check(ctx context.Context, t recording.InspectionToken) error {
	r.checks++
	return r.ACPInspectionReader.Check(ctx, t)
}
func TestACPHTTPUsesIndependentInspectorAndFinalGuard(t *testing.T) {
	for _, mode := range []string{"visible", "changed", "expired"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			controlPath := filepath.Join(root, "control.db")
			control, err := runtimepersistence.OpenControl(ctx, runtimepersistence.ControlOptions{DatabasePath: controlPath, LayoutID: "acp-http", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer control.Shutdown(ctx)
			current, err := runtimepersistence.OpenObservations(ctx, runtimepersistence.ObservationOptions{DatabasePath: filepath.Join(root, "observations.db"), LayoutID: "acp-http", ArchiveID: "current", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer current.Shutdown(ctx)
			db, err := sql.Open("sqlite", controlPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(`INSERT INTO recording_capture_fences VALUES('managed_run','acp-http-run',1,0)`); err != nil {
				t.Fatal(err)
			}
			record := acpobservation.Record{RunID: "acp-http-run", Scope: recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "acp-http-run"}, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}, Policy: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingMetadataOnly, RetentionDays: 1}, ExpiresAtMillis: time.Now().Add(time.Hour).UnixMilli(), Snapshot: acpobservation.Snapshot{Revision: 1, Agent: acpobservation.Agent{Name: "independent-retained-agent"}, Incomplete: true}}
			if err = current.PutACP(ctx, record); err != nil {
				t.Fatal(err)
			}
			catalog, err := runtimepersistence.NewInspectionCatalog(runtimepersistence.InspectionCatalogOptions{Root: root, LayoutID: "acp-http", Generation: 1, Sources: []runtimepersistence.InspectionSource{{ID: "current", RelativePath: "observations.db", Database: runtimepersistence.SchemaState{Identity: "vibermate-observations", Revision: 1}, Current: true}}})
			if err != nil {
				t.Fatal(err)
			}
			defer catalog.Close(ctx)
			reader := &acpHTTPInspection{ACPInspectionReader: runtimepersistence.NewACPInspectionReader(catalog, control)}
			if mode == "changed" {
				reader.afterRead = func() {
					if _, err := db.Exec(`UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`); err != nil {
						t.Fatal(err)
					}
				}
			}
			clock := &acpHTTPClock{now: time.Now()}
			if mode == "expired" {
				reader.afterRead = func() { clock.now = time.UnixMilli(record.ExpiresAtMillis + 1) }
			}
			fixture := newAuditFixtureConfigured(t, nil, func(o *desktopcontrol.Options) {
				o.Clock = clock
				o.ACPInspection = reader
				o.ACPMarkers = control.ACPRegistrations()
			})
			response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, "/api/v1/captures/managed_run:acp-http-run/acp", fixture.readToken, nil)
			expected := http.StatusOK
			if mode != "visible" {
				expected = http.StatusServiceUnavailable
			}
			if response.Code != expected || reader.checks != 1 {
				t.Fatalf("ACP independent HTTP/guard: status=%d checks=%d body=%s", response.Code, reader.checks, response.Body.String())
			}
			if mode == "visible" && (!strings.Contains(response.Body.String(), "independent-retained-agent") || !strings.Contains(response.Body.String(), `"incomplete":true`)) {
				t.Fatalf("retained snapshot missing: %s", response.Body.String())
			}
			if mode != "visible" && strings.Contains(response.Body.String(), "independent-retained-agent") {
				t.Fatal("stale snapshot disclosed")
			}
		})
	}
}

type acpHTTPClock struct{ now time.Time }

func (c *acpHTTPClock) Now() time.Time { return c.now }
func TestACPCaptureMarkersDoNotOpenOptionalInspection(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control, err := runtimepersistence.OpenControl(ctx, runtimepersistence.ControlOptions{DatabasePath: filepath.Join(root, "control.db"), LayoutID: "acp-markers", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Shutdown(ctx)
	catalog, err := runtimepersistence.NewInspectionCatalog(runtimepersistence.InspectionCatalogOptions{Root: root, LayoutID: "acp-markers", Generation: 1, Sources: []runtimepersistence.InspectionSource{{ID: "current", RelativePath: "observations.db", Database: runtimepersistence.SchemaState{Identity: "vibermate-observations", Revision: 1}, Current: true}}})
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(ctx)
	reader := &acpHTTPInspection{ACPInspectionReader: runtimepersistence.NewACPInspectionReader(catalog, control)}
	now := time.Now()
	fixture := newAuditFixtureConfigured(t, nil, func(o *desktopcontrol.Options) { o.ACPInspection = reader; o.ACPMarkers = control.ACPRegistrations() }, captureReaderFixture{view: capturerun.View{ID: "control-only-run", State: capturerun.StateFinished, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}})
	response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, "/api/v1/captures", fixture.readToken, nil)
	if response.Code != http.StatusOK || reader.reads != 0 || reader.checks != 0 || !strings.Contains(response.Body.String(), "control-only-run") {
		t.Fatalf("optional inspection blocked control marker read: status=%d reads=%d checks=%d body=%s", response.Code, reader.reads, reader.checks, response.Body.String())
	}
}
