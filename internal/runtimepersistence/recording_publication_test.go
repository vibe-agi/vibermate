package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/recording"
	"path/filepath"
	"testing"
	"time"
)

// Missing publication leaves the actual independent reader unavailable. A
// publisher must copy all 600 fences, not publish only its first bounded page.
func TestRecordingMirrorPublishesCompleteBoundedSnapshot(t *testing.T) {
	control := inspectionControlFixture(t)
	options, current := inspectionSourcesFixture(t)
	tx, err := control.store.database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 600; i++ {
		if _, err = tx.Exec(`INSERT INTO recording_capture_fences VALUES('managed_run',?,7,0)`, fmt.Sprintf("capture-%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, control.store.database, `INSERT INTO runtime_usage_retention_rules VALUES(7,1); INSERT INTO runtime_usage_legacy_retention_caps VALUES('legacy',3,11,71)`)
	publisher, ok := any(current).(interface {
		PublishInspectionMirror(context.Context, *ControlStore) error
	})
	if !ok {
		t.Fatal("production mirror publication unavailable")
	}
	if err = publisher.PublishInspectionMirror(context.Background(), control); err != nil {
		t.Fatal(err)
	}
	snapshot, err := control.ReadInspectionSnapshot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
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
	defer lease.close()
	read, err := lease.conn.BeginTx(lease.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer read.Rollback()
	mirror, err := readInspectionMirror(lease.ctx, read, "s1", snapshot.Stamp)
	if err != nil {
		t.Fatal(err)
	}
	var fences, caps, legacy int
	if err = read.QueryRow(`SELECT fence_count,current_cap_count,legacy_cap_count FROM s1.inspection_mirror_generations WHERE generation=?`, mirror.Generation).Scan(&fences, &caps, &legacy); err != nil {
		t.Fatal(err)
	}
	if fences != 600 || caps != 1 || legacy != 1 {
		t.Fatalf("partial publication: %d/%d/%d", fences, caps, legacy)
	}
}

// The history writer is genuinely held. Required control policy publication
// must finish before that writer is released; the stale build must not activate.
func TestRecordingMirrorHeldHistoryDoesNotHoldControl(t *testing.T) {
	control := inspectionControlFixture(t)
	_, current := inspectionSourcesFixture(t)
	publisher, ok := any(current).(interface {
		PublishInspectionMirror(context.Context, *ControlStore) error
	})
	if !ok {
		t.Fatal("production mirror publication unavailable")
	}
	tx, err := current.store.database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO inspection_mirror_generations VALUES(1,'layout',1,1,1,1,365,NULL,0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- publisher.PublishInspectionMirror(context.Background(), control) }()
	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	policy, err := control.UsagePolicyRepository().UsagePolicy(deadline)
	if err != nil {
		t.Fatal(err)
	}
	policy.RetentionDays = 7
	if _, err = control.UsagePolicyRepository().SetUsagePolicy(deadline, policy, time.Now().UTC()); err != nil {
		t.Fatal("history blocked required control:", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	err = <-result
	// Depending on scheduling, the publisher either snapshots after the change
	// or refuses its stale stamp. Both must leave no active old-policy mirror.
	if err != nil && !errors.Is(err, ErrInspectionChanged) {
		t.Fatal(err)
	}
	var stale int
	if err = current.store.reads.QueryRow(`SELECT count(*) FROM inspection_mirror_active a JOIN inspection_mirror_generations g USING(generation) WHERE usage_policy_revision=1`).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Fatal("stale policy mirror activated")
	}
}

func TestRecordingMirrorChangedBetweenPagesNeverPublishesPartial(t *testing.T) {
	control := inspectionControlFixture(t)
	options, current := inspectionSourcesFixture(t)
	if err := current.PublishInspectionMirror(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	var previous int64
	if err := current.store.reads.QueryRow(`SELECT generation FROM inspection_mirror_active`).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	tx, err := control.store.database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 600; i++ {
		if _, err = tx.Exec(`INSERT INTO recording_capture_fences VALUES('managed_run',?,7,0)`, fmt.Sprintf("capture-%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(`UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	pages := 0
	export := func(ctx context.Context, stamp recording.InspectionStamp, cursor InspectionExportCursor) (InspectionExportPage, error) {
		pages++
		if pages == 2 {
			nativeExec(t, control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='capture-0000'; UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
		}
		page, err := control.ExportInspectionPage(ctx, stamp, cursor)
		if len(page.rows) > 256 {
			t.Fatal("unbounded control export")
		}
		return page, err
	}
	if err = current.publishInspectionMirror(t.Context(), control, export); !errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("mixed control snapshot published: %v", err)
	}
	var active, complete int64
	if err = current.store.reads.QueryRow(`SELECT a.generation,g.complete FROM inspection_mirror_active a JOIN inspection_mirror_generations g USING(generation)`).Scan(&active, &complete); err != nil {
		t.Fatal(err)
	}
	if active != previous || complete != 1 {
		t.Fatal("failed build changed active publication")
	}
	// Crash/reopen must discard only staging, rebuild all pages, and preserve the
	// deletion that occurred between pages instead of reviving the old fence.
	if err = current.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err = OpenObservations(t.Context(), ObservationOptions{DatabasePath: filepath.Join(options.Root, "observations.db"), LayoutID: "layout", ArchiveID: "current", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer current.Shutdown(context.Background())
	if err = current.PublishInspectionMirror(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	var count, deleted int
	if err = current.store.reads.QueryRow(`SELECT count(*),sum(deleted) FROM inspection_mirror_fences WHERE mirror_generation=(SELECT generation FROM inspection_mirror_active)`).Scan(&count, &deleted); err != nil {
		t.Fatal(err)
	}
	if count != 600 || deleted != 1 {
		t.Fatalf("restart publication incomplete: count=%d deleted=%d", count, deleted)
	}
}
