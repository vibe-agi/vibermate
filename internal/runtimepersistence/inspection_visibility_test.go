package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
)

func inspectionControlFixture(t *testing.T) *ControlStore {
	t.Helper()
	control, err := OpenControl(context.Background(), ControlOptions{DatabasePath: filepath.Join(t.TempDir(), "runtime.db"), LayoutID: "layout", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { control.Shutdown(context.Background()) })
	return control
}
func TestInspectionControlSnapshotIsBoundedAndReleased(t *testing.T) {
	control := inspectionControlFixture(t)
	nativeExec(t, control.store.database, `INSERT INTO recording_capture_fences VALUES('managed_run','visible',3,0),('manual_capture','deleted',2,1)`)
	visible := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "visible"}
	deleted := captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: "deleted"}
	missing := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "missing"}
	snapshot, err := control.ReadInspectionSnapshot(context.Background(), []captureidentity.Reference{visible, deleted, missing})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Stamp.LayoutID != "layout" || snapshot.Stamp.ArchiveEpoch != 1 || snapshot.Stamp.VisibilityRevision != 1 || snapshot.Stamp.UsagePolicyRevision != 1 {
		t.Fatal("wrong control stamp")
	}
	if !snapshot.Allows(recording.Scope{Capture: visible, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 3}}) || snapshot.Allows(recording.Scope{Capture: deleted, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 2}}) || snapshot.Allows(recording.Scope{Capture: missing, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}) {
		t.Fatal("capture visibility fence violated")
	}
	if !snapshot.Allows(recording.Scope{Fence: recording.Fence{ArchiveEpoch: 1}}) || snapshot.Allows(recording.Scope{Capture: visible, Fence: recording.Fence{ArchiveEpoch: 2, CaptureGeneration: 3}}) {
		t.Fatal("global/old epoch visibility violated")
	}
	control.store.reads.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := control.store.reads.Conn(ctx)
	if err != nil {
		t.Fatal("snapshot retained its control connection")
	}
	conn.Close()
	if err = control.RecheckInspectionStamp(ctx, snapshot.Stamp); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, control.store.database, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
	if err = control.RecheckInspectionStamp(ctx, snapshot.Stamp); !errors.Is(err, ErrInspectionChanged) {
		t.Fatal("stale visibility stamp accepted")
	}
	var refs []captureidentity.Reference
	for i := 0; i < 257; i++ {
		refs = append(refs, captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: fmt.Sprintf("capture-%d", i)})
	}
	if _, err = control.ReadInspectionSnapshot(ctx, refs); err == nil {
		t.Fatal("unbounded scoped snapshot accepted")
	}
}

func TestInspectionMirrorRequiresCompleteMatchingSnapshot(t *testing.T) {
	for _, state := range []string{"missing", "partial", "stale", "count-mismatch", "bad-cap", "complete"} {
		t.Run(state, func(t *testing.T) {
			control := inspectionControlFixture(t)
			nativeExec(t, control.store.database, `INSERT INTO recording_capture_fences VALUES('managed_run','visible',1,0);INSERT INTO runtime_usage_retention_rules VALUES(7,1);INSERT INTO runtime_usage_legacy_retention_caps VALUES('legacy',3,11,71)`)
			snapshot, err := control.ReadInspectionSnapshot(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			options, observations := inspectionSourcesFixture(t)
			if state != "missing" {
				nativeExec(t, observations.store.database, `INSERT INTO inspection_mirror_generations(generation,layout_id,archive_epoch,visibility_revision,usage_policy_revision,usage_enabled,usage_retention_days,usage_collecting_since_unix_ms,fence_count,current_cap_count,legacy_cap_count,complete) VALUES(1,'layout',1,1,1,1,365,NULL,1,1,1,1);
INSERT INTO inspection_mirror_fences VALUES(1,'managed_run','visible',1,0);
INSERT INTO inspection_mirror_usage_caps VALUES(1,7,1);
INSERT INTO inspection_mirror_legacy_caps VALUES(1,'legacy',3,11,71);
INSERT INTO inspection_mirror_active VALUES(1,1)`)
				policy, policyErr := control.UsagePolicyRepository().UsagePolicy(context.Background())
				if policyErr != nil {
					t.Fatal(policyErr)
				}
				var since any
				if policy.CollectingSince != nil {
					since = policy.CollectingSince.UnixMilli()
				}
				if _, err = observations.store.database.Exec(`UPDATE inspection_mirror_generations SET usage_enabled=?,usage_retention_days=?,usage_collecting_since_unix_ms=?`, policy.Enabled, policy.RetentionDays, since); err != nil {
					t.Fatal(err)
				}

				switch state {
				case "partial":
					nativeExec(t, observations.store.database, `UPDATE inspection_mirror_generations SET complete=0`)
				case "stale":
					nativeExec(t, observations.store.database, `UPDATE inspection_mirror_generations SET visibility_revision=2`)
				case "count-mismatch":
					nativeExec(t, observations.store.database, `DELETE FROM inspection_mirror_fences`)
				case "bad-cap":
					nativeExec(t, observations.store.database, `PRAGMA ignore_check_constraints=ON;UPDATE inspection_mirror_legacy_caps SET after_sequence=through_sequence;PRAGMA ignore_check_constraints=OFF`)
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
			defer lease.close()
			tx, err := lease.conn.BeginTx(lease.ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			mirror, err := readInspectionMirror(lease.ctx, tx, "s1", snapshot.Stamp)
			if state != "complete" {
				if !errors.Is(err, ErrInspectionUnavailable) {
					t.Fatalf("incomplete/stale mirror admitted: %v", err)
				}
				return
			}
			if err != nil || mirror.Generation != 1 || mirror.Policy.RetentionDays != 365 || mirror.Stamp != snapshot.Stamp {
				t.Fatalf("complete mirror unavailable: %v", err)
			}
		})
	}
}

func TestInspectionBoundaryRechecksAfterIndependentHistoryRead(t *testing.T) {
	control := inspectionControlFixture(t)
	options, _ := inspectionSourcesFixture(t)
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(context.Background())
	boundary := NewInspectionBoundary(control, catalog)
	token, err := boundary.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := catalog.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := lease.conn.BeginTx(lease.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tx.QueryContext(lease.ctx, `SELECT display_name FROM s0.code_library_collections`)
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatal("history fixture absent")
	}
	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = control.store.database.ExecContext(deadline, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`); err != nil {
		t.Fatal("history held control publication")
	}
	rows.Close()
	tx.Rollback()
	lease.close()
	if err = boundary.Check(context.Background(), token); !errors.Is(err, ErrInspectionChanged) {
		t.Fatal("outer response accepted changed visibility")
	}
	token, err = boundary.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	maintenance, err := catalog.BeginMaintenance(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	maintenance.Close()
	if err = boundary.Check(context.Background(), token); !errors.Is(err, ErrInspectionChanged) {
		t.Fatal("outer response accepted changed source generation")
	}
}

func TestInspectionBoundaryRefusesUnrelatedControlLayout(t *testing.T) {
	control := inspectionControlFixture(t)
	options, _ := inspectionSourcesFixture(t)
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(context.Background())
	if _, err = control.store.database.Exec(`UPDATE runtime_storage_state SET layout_id='unrelated'`); err != nil {
		t.Fatal(err)
	}
	if _, err = NewInspectionBoundary(control, catalog).Begin(context.Background()); !errors.Is(err, ErrInspectionUnavailable) {
		t.Fatalf("unrelated authority accepted: %v", err)
	}
}

// Controlled fixture publication only. Production bounded export and atomic
// publication races are Task5; no control read remains open during these writes.
func seedCompleteInspectionMirror(t *testing.T, current *ObservationStore, control *ControlStore) {
	t.Helper()
	ctx := context.Background()
	snapshot, err := control.ReadInspectionSnapshot(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := control.UsagePolicyRepository().UsagePolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type relation struct {
		source, columns, target string
		rows                    [][]any
	}
	relations := []relation{{source: "recording_capture_fences", columns: "capture_kind,capture_id,generation,deleted", target: "inspection_mirror_fences"}, {source: "runtime_usage_retention_rules", columns: "retention_days,through_policy_revision", target: "inspection_mirror_usage_caps"}, {source: "runtime_usage_legacy_retention_caps", columns: "archive_id,retention_days,after_sequence,through_sequence", target: "inspection_mirror_legacy_caps"}}
	for i := range relations {
		r := &relations[i]
		rows, err := control.store.reads.QueryContext(ctx, `SELECT `+r.columns+` FROM `+r.source)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			v := make([]any, len(strings.Split(r.columns, ",")))
			dest := make([]any, len(v))
			for j := range v {
				dest[j] = &v[j]
			}
			if err = rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			r.rows = append(r.rows, v)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := current.store.database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, table := range []string{"inspection_mirror_active", "inspection_mirror_fences", "inspection_mirror_usage_caps", "inspection_mirror_legacy_caps", "inspection_mirror_generations"} {
		if _, err = tx.Exec(`DELETE FROM ` + table); err != nil {
			t.Fatal(err)
		}
	}
	var since any
	if policy.CollectingSince != nil {
		since = policy.CollectingSince.UnixMilli()
	}
	stamp := snapshot.Stamp
	if _, err = tx.Exec(`INSERT INTO inspection_mirror_generations VALUES(1,?,?,?,?,?,?,?,?,?,?,1)`, stamp.LayoutID, stamp.ArchiveEpoch, stamp.VisibilityRevision, stamp.UsagePolicyRevision, policy.Enabled, policy.RetentionDays, since, len(relations[0].rows), len(relations[1].rows), len(relations[2].rows)); err != nil {
		t.Fatal(err)
	}
	for _, r := range relations {
		for _, row := range r.rows {
			args := append([]any{1}, row...)
			if _, err = tx.Exec(`INSERT INTO `+r.target+` VALUES(`+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)`, args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = tx.Exec(`INSERT INTO inspection_mirror_active VALUES(1,1)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
