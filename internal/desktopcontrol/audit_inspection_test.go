package desktopcontrol_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

type auditHTTPConnections struct {
	*runtimepersistence.ConnectionInspectionReader
	afterRead func()
	checks    int
}

func (r *auditHTTPConnections) List(ctx context.Context, q connectionevent.PageRequest) (connectionevent.Page, error) {
	v, err := r.ConnectionInspectionReader.List(ctx, q)
	if err == nil && r.afterRead != nil {
		r.afterRead()
	}
	return v, err
}
func (r *auditHTTPConnections) Timeline(ctx context.Context, id string) (connectionevent.Timeline, error) {
	v, err := r.ConnectionInspectionReader.Timeline(ctx, id)
	if err == nil && r.afterRead != nil {
		r.afterRead()
	}
	return v, err
}
func (r *auditHTTPConnections) Check(ctx context.Context, t recording.InspectionToken) error {
	r.checks++
	return r.ConnectionInspectionReader.Check(ctx, t)
}

type auditHTTPEgress struct {
	*runtimepersistence.EgressInspectionReader
	afterRead func()
	checks    int
}

func (r *auditHTTPEgress) List(ctx context.Context, q egressaudit.PageRequest) (egressaudit.Page, error) {
	v, err := r.EgressInspectionReader.List(ctx, q)
	if err == nil && r.afterRead != nil {
		r.afterRead()
	}
	return v, err
}
func (r *auditHTTPEgress) Check(ctx context.Context, t recording.InspectionToken) error {
	r.checks++
	return r.EgressInspectionReader.Check(ctx, t)
}

func independentAuditHTTPFixture(t *testing.T) (*auditFixture, *auditHTTPConnections, *auditHTTPEgress, func()) {
	t.Helper()
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(root, "control.db")
	currentPath := filepath.Join(root, "observations.db")
	control, err := runtimepersistence.OpenControl(ctx, runtimepersistence.ControlOptions{DatabasePath: controlPath, LayoutID: "audit-http", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { control.Shutdown(ctx) })
	current, err := runtimepersistence.OpenObservations(ctx, runtimepersistence.ObservationOptions{DatabasePath: currentPath, LayoutID: "audit-http", ArchiveID: "current", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { current.Shutdown(ctx) })
	snapshot, err := control.ReadInspectionSnapshot(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := control.UsagePolicyRepository().UsagePolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", currentPath)
	if err != nil {
		t.Fatal(err)
	}
	var since any
	if policy.CollectingSince != nil {
		since = policy.CollectingSince.UnixMilli()
	}
	_, err = db.Exec(`INSERT INTO inspection_mirror_generations VALUES(1,?,?,?,?,?,?,?,?,?,?,1)`, snapshot.Stamp.LayoutID, snapshot.Stamp.ArchiveEpoch, snapshot.Stamp.VisibilityRevision, snapshot.Stamp.UsagePolicyRevision, policy.Enabled, policy.RetentionDays, since, 0, 0, 0)
	if err == nil {
		_, err = db.Exec(`INSERT INTO inspection_mirror_active VALUES(1,1)`)
	}
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	scope := recording.Scope{Fence: recording.Fence{ArchiveEpoch: 1}}
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := 1; i <= 2; i++ {
		e := connectionevent.Event{ConnectionID: fmt.Sprintf("connection-%d", i), SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseFailed, StartedAt: now, EndedAt: now.Add(time.Second), Outcome: connectionevent.OutcomeFailed, ErrorClass: "client_disconnected"}
		if err = current.ConnectionEventRepository().(connectionevent.ScopedStore).PutConnection(ctx, scope, e); err != nil {
			t.Fatal(err)
		}
		a, err := egressaudit.New(egressaudit.NewInput{ID: fmt.Sprintf("attempt-%d", i), Purpose: egressaudit.PurposeUpdate, PayloadClass: egressaudit.PayloadRuntime, Parent: egressaudit.ParentRef{Kind: egressaudit.ParentRuntimeAction, ID: "refresh"}, Caller: egressaudit.CallerCore, TargetOrigin: "https://updates.example.test", Decision: egressaudit.DecisionRef{PolicyID: "runtime", PolicyRevision: 1, Authority: egressaudit.AuthorityRuntime, RuleID: "runtime", ProxyID: "direct"}, StartedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		if err = current.EgressAttemptRepository().(egressaudit.ScopedAttemptStore).PutScopedAttempt(ctx, scope, a); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := runtimepersistence.NewInspectionCatalog(runtimepersistence.InspectionCatalogOptions{Root: root, LayoutID: "audit-http", Generation: 1, Sources: []runtimepersistence.InspectionSource{{ID: "current", RelativePath: "observations.db", Database: runtimepersistence.SchemaState{Identity: "vibermate-observations", Revision: 1}, Current: true}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close(ctx) })
	connections := &auditHTTPConnections{ConnectionInspectionReader: runtimepersistence.NewConnectionInspectionReader(catalog, control)}
	egress := &auditHTTPEgress{EgressInspectionReader: runtimepersistence.NewEgressInspectionReader(catalog, control)}
	fixture := newAuditFixtureConfigured(t, nil, func(o *desktopcontrol.Options) { o.Connections = connections; o.Egress = egress })
	mutate := func() {
		db, err := sql.Open("sqlite", controlPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err = db.Exec(`UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`); err != nil {
			t.Fatal(err)
		}
	}
	return fixture, connections, egress, mutate
}
func TestAuditHTTPConsumesOpaqueConnectionAndEgressCursors(t *testing.T) {
	for _, path := range []string{"/api/v1/connections", "/api/v1/egress-attempts"} {
		t.Run(path, func(t *testing.T) {
			fixture, _, _, _ := independentAuditHTTPFixture(t)
			first := doRequest(t, fixture.router, fixture.authority, http.MethodGet, path+"?limit=1", fixture.readToken, nil)
			var page struct {
				NextCursor string            `json:"nextCursor"`
				Items      []json.RawMessage `json:"items"`
			}
			if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil || first.Code != 200 || len(page.Items) != 1 || page.NextCursor == "" {
				t.Fatalf("first independent page: %d %s", first.Code, first.Body.String())
			}
			next := doRequest(t, fixture.router, fixture.authority, http.MethodGet, path+"?limit=1&cursor="+page.NextCursor, fixture.readToken, nil)
			if next.Code != 200 {
				t.Fatalf("opaque source cursor rejected/reinterpreted: %d %s", next.Code, next.Body.String())
			}
			if next.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("inspection cache policy lost")
			}
		})
	}
}
func TestAuditHTTPRechecksAfterActualIndependentRead(t *testing.T) {
	for _, view := range []string{"connections", "timeline", "egress"} {
		t.Run(view, func(t *testing.T) {
			fixture, connections, egress, mutate := independentAuditHTTPFixture(t)
			path := "/api/v1/connections"
			connections.afterRead = mutate
			if view == "timeline" {
				path += "/connection-1"
			}
			if view == "egress" {
				path = "/api/v1/egress-attempts"
				egress.afterRead = mutate
				connections.afterRead = nil
			}
			response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, path, fixture.readToken, nil)
			checks := connections.checks
			if view == "egress" {
				checks = egress.checks
			}
			if response.Code != http.StatusConflict || checks != 1 {
				t.Fatalf("stale audit response published: status=%d checks=%d body=%s", response.Code, checks, response.Body.String())
			}
		})
	}
}

func TestAuditHTTPRejectsInvalidFiltersAsRequests(t *testing.T) {
	fixture, _, _, _ := independentAuditHTTPFixture(t)
	for _, query := range []string{"purpose=invented", "parentKind=made_up&parentId=valid", "parentKind=runtime_action", "parentId=orphan", "unknown=value", "cursor="} {
		response := doRequest(t, fixture.router, fixture.authority, http.MethodGet, "/api/v1/egress-attempts?"+query, fixture.readToken, nil)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid filter became storage failure: %s => %d %s", query, response.Code, response.Body.String())
		}
	}
}
