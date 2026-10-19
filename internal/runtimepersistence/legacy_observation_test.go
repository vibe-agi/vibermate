package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func inspectionSourcesFixture(t *testing.T) (InspectionCatalogOptions, *ObservationStore) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	current, err := OpenObservations(context.Background(), ObservationOptions{DatabasePath: filepath.Join(root, "observations.db"), LayoutID: "layout", ArchiveID: "current", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { current.Shutdown(context.Background()) })
	legacy, db := extractionFixture(t, 2)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "archives", "legacy")
	if err = os.MkdirAll(archive, 0700); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(archive, "runtime.db"), bytes, 0600); err != nil {
		t.Fatal(err)
	}
	// These functional fixtures include actual attachment/schema validation.
	// Use the production default read budget; explicit cancellation/deadline
	// tests retain their own short caller contexts.
	return InspectionCatalogOptions{Root: root, LayoutID: "layout", Generation: 1, Concurrency: 2, Sources: []InspectionSource{
		{ID: "legacy", RelativePath: "archives/legacy/runtime.db", Database: SchemaState{Identity: "vibermate-runtime", Revision: 2, InitializedAt: "2026-10-03T00:00:00Z"}},
		{ID: "current", RelativePath: "observations.db", Database: SchemaState{Identity: "vibermate-observations", Revision: 1}, Current: true},
	}}, current
}

func TestInspectionCatalogIsLazyAndNeverAttachesControl(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	options := InspectionCatalogOptions{Root: root, LayoutID: "layout", Generation: 1, Sources: []InspectionSource{{ID: "current", RelativePath: "observations.db", Database: SchemaState{Identity: "vibermate-observations", Revision: 1}, Current: true}}}
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(context.Background())
	if _, err = os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("constructor opened optional storage")
	}
	if lease, err := catalog.acquire(context.Background()); err == nil {
		lease.close()
		t.Fatal("missing source reported complete")
	}
	options.Sources[0].RelativePath = "runtime.db"
	if _, err = NewInspectionCatalog(options); err == nil {
		t.Fatal("control attachment path admitted")
	}
	options, _ = inspectionSourcesFixture(t)
	catalog, err = NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(context.Background())
	lease, err := catalog.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.close()
	var name string
	if err = lease.conn.QueryRowContext(lease.ctx, `SELECT display_name FROM s0.code_library_collections WHERE collection_id='authored'`).Scan(&name); err != nil || name != "Preserved bytes" {
		t.Fatalf("legacy projection unavailable: %v", err)
	}
	if _, err = lease.conn.ExecContext(lease.ctx, `UPDATE s0.code_library_collections SET display_name='mutation'`); err == nil {
		t.Fatal("archive connection was writable")
	}
}

func TestInspectionCatalogHonorsPinnedAttachmentLimit(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	limit, err := sqlite.Limit(conn, sqlite3.SQLITE_LIMIT_ATTACHED, -1)
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	options := InspectionCatalogOptions{Root: filepath.Join(t.TempDir(), "missing"), LayoutID: "layout", Generation: 1}
	for i := 0; i < limit; i++ {
		id := fmt.Sprintf("archive-%d", i)
		options.Sources = append(options.Sources, InspectionSource{ID: id, RelativePath: "archives/" + id + "/runtime.db", Database: SchemaState{Identity: "vibermate-runtime", Revision: 2, InitializedAt: "2026-10-03T00:00:00Z"}})
	}
	options.Sources = append(options.Sources, InspectionSource{ID: "current", RelativePath: "observations.db", Database: SchemaState{Identity: "vibermate-observations", Revision: 1}, Current: true})
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(context.Background())
	if lease, err := catalog.acquire(context.Background()); !errors.Is(err, ErrInspectionSourceLimit) {
		if lease != nil {
			lease.close()
		}
		t.Fatalf("inventory silently omitted/unbounded: %v", err)
	}
}

func TestInspectionLeaseSurvivesCancellationUntilActualClose(t *testing.T) {
	options, _ := inspectionSourcesFixture(t)
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	lease, err := catalog.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	if maintenance, err := catalog.BeginMaintenance(deadline, 2); err == nil {
		maintenance.Close()
		t.Fatal("maintenance declared canceled open lease drained")
	}
	if err = catalog.Check(lease.version); !errors.Is(err, ErrInspectionChanged) {
		t.Fatal("maintenance did not invalidate old generation")
	}
	if err = lease.close(); err != nil {
		t.Fatal(err)
	}
	maintenance, err := catalog.BeginMaintenance(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if err = maintenance.Publish(options.Sources); err != nil {
		t.Fatal(err)
	}
	if err = maintenance.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := catalog.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next.close()
}

func TestInspectionCursorBindsQueryGenerationAndTuple(t *testing.T) {
	options, _ := inspectionSourcesFixture(t)
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(context.Background())
	encoded, err := catalog.encodeCursor("native", "managed_run/capture", inspectionPosition{Ordinal: 1, Boundary: 17})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 256 {
		t.Fatal("cursor is not constant size")
	}
	position, err := catalog.decodeCursor("native", "managed_run/capture", encoded)
	if err != nil || position != (inspectionPosition{Ordinal: 1, Boundary: 17}) {
		t.Fatal("overlapping local sequence lost source")
	}
	if _, err = catalog.decodeCursor("usage", "managed_run/capture", encoded); err == nil {
		t.Fatal("cursor crossed domain")
	}
	if _, err = catalog.decodeCursor("native", "manual_capture/capture", encoded); err == nil {
		t.Fatal("cursor crossed scope")
	}
	maintenance, err := catalog.BeginMaintenance(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	maintenance.Close()
	if _, err = catalog.decodeCursor("native", "managed_run/capture", encoded); !errors.Is(err, ErrInspectionChanged) {
		t.Fatal("stale maintenance cursor accepted")
	}
}

func TestInspectionArchiveRetainedWALIsReadWithoutChangingDatabaseBytes(t *testing.T) {
	options, _ := inspectionSourcesFixture(t)
	path := filepath.Join(options.Root, options.Sources[0].RelativePath)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL;PRAGMA wal_autocheckpoint=0;INSERT INTO code_library_collections VALUES('wal-only','Archived WAL value')`); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		files[suffix], err = os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	for suffix, bytes := range files {
		if err = os.WriteFile(path+suffix, bytes, 0600); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(context.Background())
	lease, err := catalog.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var label string
	if err = lease.conn.QueryRowContext(lease.ctx, `SELECT display_name FROM s0.code_library_collections WHERE collection_id='wal-only'`).Scan(&label); err != nil || label != "Archived WAL value" {
		t.Fatal("retained WAL was ignored")
	}
	if err = lease.close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal"} {
		actual, err := os.ReadFile(path + suffix)
		if err != nil || string(actual) != string(files[suffix]) {
			t.Fatal("archive DB/WAL changed during read")
		}
	}
}

func TestInspectionSourceReplacementRequiresGenerationChange(t *testing.T) {
	options, _ := inspectionSourcesFixture(t)
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(context.Background())
	lease, err := catalog.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lease.close()
	path := filepath.Join(options.Root, options.Sources[0].RelativePath)
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path+".replacement", bytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path+".replacement", path); err != nil {
		t.Fatal(err)
	}
	if lease, err = catalog.acquire(context.Background()); !errors.Is(err, ErrInspectionChanged) {
		if lease != nil {
			lease.close()
		}
		t.Fatalf("unannounced physical replacement accepted: %v", err)
	}
	maintenance, err := catalog.BeginMaintenance(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	maintenance.Close()
	lease, err = catalog.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lease.close()
}

func TestInspectionConcurrencyAndShutdownRemainOwned(t *testing.T) {
	options, _ := inspectionSourcesFixture(t)
	options.Concurrency = 1
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := catalog.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	if other, err := catalog.acquire(deadline); !errors.Is(err, context.DeadlineExceeded) {
		if other != nil {
			other.close()
		}
		t.Fatalf("unbounded inspection admission: %v", err)
	}
	cancel()
	deadline, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	if err = catalog.Close(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("shutdown declared live source lease closed")
	}
	cancel()
	if err = lease.close(); err != nil {
		t.Fatal(err)
	}
	if err = catalog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestInspectionRecoveryCatalogBlocksAdmissionAtPersistedGeneration(t *testing.T) {
	options, _ := inspectionSourcesFixture(t)
	options.Generation = 17
	catalog, token, err := NewRecoveringInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(context.Background())
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	lease, err := catalog.acquire(ctx)
	if err == nil {
		lease.close()
		t.Fatal("recovering fresh catalog admitted a reader before cleanup")
	}
	if token == nil {
		t.Fatal("startup recovery lacks exclusive token")
	}
	defer token.Close()
	if catalog.Version().Generation != 17 {
		t.Fatalf("recovery changed durable generation: %+v", catalog.Version())
	}
	if _, err := token.cleanupInventory(); err != nil {
		t.Fatal(err)
	}
	if err := token.Close(); err != nil {
		t.Fatal(err)
	}
	if retry, err := catalog.BeginMaintenance(t.Context(), 17); err == nil {
		retry.Close()
		t.Fatal("live equal-generation retry admitted")
	}
	lease, err = catalog.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	lease.close()
}

// Warm only immutable reference construction. The tested catalog must not pin
// a legacy file identity before this fixture finishes authoring that file.
func warmInspectionFixtureReferences(t *testing.T, catalog *InspectionCatalog) {
	t.Helper()
	warm, err := NewInspectionCatalog(InspectionCatalogOptions{Root: catalog.root, LayoutID: catalog.layout, Generation: catalog.Version().Generation, Sources: catalog.sources})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := warm.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	lease, err := warm.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.close(); err != nil {
		t.Fatal(err)
	}
}
