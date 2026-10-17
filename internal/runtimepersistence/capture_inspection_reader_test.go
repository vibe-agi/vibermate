package runtimepersistence

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/recording"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCaptureInspectionCombinesSparseLegacyAndOriginalCurrentFacts(t *testing.T) {
	for _, revision := range []int{1, 2} {
		t.Run(string(rune('0'+revision)), func(t *testing.T) {
			f := newEgressInspectionFixture(t, revision)
			now := time.Now().UTC().Truncate(time.Millisecond)
			nativeExec(t, f.legacy, `INSERT INTO capture_runs(run_id,proxy_capability_hash,control_capability_hash,cwd,canonical_executable_path,executable_label,client_catalog_revision,state,created_at_unix_ms,expires_at_unix_ms,updated_at_unix_ms,observation,first_observed_at_unix_ms) VALUES(?,randomblob(32),randomblob(32),'/tmp','/bin/tool','tool',1,'finished',?,?,?,'observed',?)`, f.scope.Capture.ID, now.Add(-time.Hour).UnixMilli(), now.Add(time.Hour).UnixMilli(), now.UnixMilli(), now.Add(-time.Minute).UnixMilli())
			// This point read has one requested fence and needs no global SQL mirror.
			nativeExec(t, f.current.store.database, `DELETE FROM inspection_mirror_active`)
			reader := NewCaptureInspectionReader(f.reader.catalog, f.control)
			legacy, err := reader.ReadCaptureObservation(context.Background(), f.scope.Capture)
			if err != nil || legacy.FirstObservedAt == nil || !legacy.FirstObservedAt.Equal(now.Add(-time.Minute)) || legacy.LastObservedAt != nil || legacy.Selection.CredentialRevision != 0 || legacy.Selection.Scope != f.scope {
				t.Fatalf("sparse original managed fact: %+v %v", legacy, err)
			}
			if err = f.current.PutCaptureObservation(context.Background(), f.scope, recording.CaptureObservation{ObservedAt: now, CredentialRevision: 0}); err != nil {
				t.Fatal(err)
			}
			combined, err := reader.ReadCaptureObservation(context.Background(), f.scope.Capture)
			if err != nil || combined.FirstObservedAt == nil || combined.LastObservedAt == nil || !combined.FirstObservedAt.Equal(*legacy.FirstObservedAt) || !combined.LastObservedAt.Equal(now) {
				t.Fatalf("combined known facts: %+v %v", combined, err)
			}
		})
	}
}

type captureSelectionChangingControl struct {
	*ControlStore
	beforeCheck func()
}

func (c *captureSelectionChangingControl) RecheckCaptureInspectionSelection(ctx context.Context, s recording.CaptureInspectionSelection) error {
	if c.beforeCheck != nil {
		fn := c.beforeCheck
		c.beforeCheck = nil
		fn()
	}
	return c.ControlStore.RecheckCaptureInspectionSelection(ctx, s)
}

func TestCaptureInspectionManualUsesActualCurrentCredentialRevision(t *testing.T) {
	for _, revision := range []int{1, 2} {
		t.Run(string(rune('0'+revision)), func(t *testing.T) { testCaptureInspectionManualRevision(t, revision) })
	}
}
func testCaptureInspectionManualRevision(t *testing.T, revision int) {
	f := newEgressInspectionFixture(t, revision)
	// Use the normal catalog budget; this fixture performs real frozen schema
	// validation and an actual rotation during the final check under -race.
	f.reader.catalog.timeout = 30 * time.Second
	now := time.Now().UTC().Truncate(time.Millisecond)
	clock := &manualCaptureClock{now: now}
	manager := newManualCaptureManager(t, f.control.store, clock, &manualCaptureRandom{})
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	owner := manualcapture.NewLocalOwnerScope()
	grant, err := manager.Create(context.Background(), manualcapture.CreateCommand{Owner: owner, DisplayName: "Manual", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	ref := captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: grant.Capture.ID}
	scope := recording.Scope{Capture: ref, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}
	nativeExec(t, f.legacy, `INSERT INTO manual_captures(capture_id,owner_kind,owner_id,display_name,client_class,lifetime,state,credential_revision,proxy_credential_hash,observation,created_at_unix_ms,updated_at_unix_ms,last_observed_at_unix_ms) VALUES(?,'local_installation','','Manual','cli','until_revoked','active',1,randomblob(32),'observed',?,?,?)`, ref.ID, now.Add(-time.Hour).UnixMilli(), now.UnixMilli(), now.Add(-time.Minute).UnixMilli())
	if err = f.current.PutCaptureObservation(context.Background(), scope, recording.CaptureObservation{ObservedAt: now.Add(time.Hour), CredentialRevision: 99}); err != nil {
		t.Fatal(err)
	}
	reader := NewCaptureInspectionReader(f.reader.catalog, f.control)
	original, err := reader.ReadCaptureObservation(context.Background(), ref)
	if err != nil || original.FirstObservedAt != nil || original.LastObservedAt == nil || !original.LastObservedAt.Equal(now.Add(-time.Minute)) || original.Selection.CredentialRevision != 1 {
		t.Fatalf("manual sparse revision1: %+v %v", original, err)
	}
	before, err := f.control.ReadInspectionSnapshot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := manualcapture.ParseID(ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Rotate(context.Background(), manualcapture.RotateCommand{Owner: owner, ID: id, ExpectedCredentialRevision: 1}); err != nil {
		t.Fatal(err)
	}
	after, err := f.control.ReadInspectionSnapshot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if before.Stamp != after.Stamp {
		t.Fatal("test needs actual credential rotation independent of visibility stamp")
	}
	if err = reader.CheckCaptureObservation(context.Background(), original); !errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("outer old revision remained valid: %v", err)
	}
	unknown, err := reader.ReadCaptureObservation(context.Background(), ref)
	if err != nil || unknown.FirstObservedAt != nil || unknown.LastObservedAt != nil || unknown.Selection.CredentialRevision != 2 {
		t.Fatalf("old/future evidence became current: %+v %v", unknown, err)
	}
	if err = f.current.PutCaptureObservation(context.Background(), scope, recording.CaptureObservation{ObservedAt: now, CredentialRevision: 2}); err != nil {
		t.Fatal(err)
	}
	current, err := reader.ReadCaptureObservation(context.Background(), ref)
	if err != nil || current.FirstObservedAt == nil || current.LastObservedAt == nil || !current.FirstObservedAt.Equal(now) || !current.LastObservedAt.Equal(now) {
		t.Fatalf("current revision2 evidence: %+v %v", current, err)
	}
	reader.control = &captureSelectionChangingControl{ControlStore: f.control, beforeCheck: func() {
		if _, err = manager.Rotate(context.Background(), manualcapture.RotateCommand{Owner: owner, ID: id, ExpectedCredentialRevision: 2}); err != nil {
			t.Fatal(err)
		}
	}}
	if value, err := reader.ReadCaptureObservation(context.Background(), ref); !errors.Is(err, ErrInspectionChanged) || value.FirstObservedAt != nil || value.LastObservedAt != nil {
		t.Fatalf("rotation during point read disclosed: %+v %v", value, err)
	}
}

func TestCaptureInspectionMissingCorruptDeletedAndClearedFacts(t *testing.T) {
	for _, mode := range []string{"missing", "deleted", "missing-fence", "clear", "generation", "malformed-current", "malformed-legacy", "waiting-with-time"} {
		t.Run(mode, func(t *testing.T) {
			f := newEgressInspectionFixture(t)
			now := time.Now().UTC().Truncate(time.Millisecond)
			if mode != "missing" {
				if err := f.current.PutCaptureObservation(context.Background(), f.scope, recording.CaptureObservation{ObservedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "deleted":
				nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='audit-a'`)
			case "missing-fence":
				nativeExec(t, f.control.store.database, `DELETE FROM recording_capture_fences WHERE capture_id='audit-a'`)
			case "clear":
				nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET archive_epoch=2`)
			case "generation":
				nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET generation=2 WHERE capture_id='audit-a'`)
			case "malformed-current":
				nativeExec(t, f.current.store.database, `PRAGMA ignore_check_constraints=ON;UPDATE capture_observations SET first_observed_at_unix_ms=last_observed_at_unix_ms+1;PRAGMA ignore_check_constraints=OFF`)
			case "malformed-legacy", "waiting-with-time":
				nativeExec(t, f.legacy, `INSERT INTO capture_runs(run_id,proxy_capability_hash,control_capability_hash,cwd,canonical_executable_path,executable_label,client_catalog_revision,state,created_at_unix_ms,expires_at_unix_ms,updated_at_unix_ms,observation,first_observed_at_unix_ms) VALUES(?,randomblob(32),randomblob(32),'/tmp','/bin/tool','tool',1,'finished',?,?,?,'waiting_for_traffic',?)`, f.scope.Capture.ID, now.Add(-time.Hour).UnixMilli(), now.Add(time.Hour).UnixMilli(), now.UnixMilli(), now.UnixMilli())
				if mode == "malformed-legacy" {
					nativeExec(t, f.legacy, `PRAGMA ignore_check_constraints=ON;UPDATE capture_runs SET observation='unknown-value';PRAGMA ignore_check_constraints=OFF`)
				}
			}
			value, err := NewCaptureInspectionReader(f.reader.catalog, f.control).ReadCaptureObservation(context.Background(), f.scope.Capture)
			switch mode {
			case "malformed-current", "malformed-legacy", "waiting-with-time":
				if err == nil || value.FirstObservedAt != nil || value.LastObservedAt != nil {
					t.Fatalf("corrupt source disguised as result: %+v %v", value, err)
				}
			default:
				if err != nil || value.FirstObservedAt != nil || value.LastObservedAt != nil {
					t.Fatalf("unknown hidden fact: %+v %v", value, err)
				}
			}
		})
	}
}

func TestCaptureInspectionRetainsLeaseThroughFinalSourceCheck(t *testing.T) {
	f := newEgressInspectionFixture(t)
	if err := f.current.PutCaptureObservation(context.Background(), f.scope, recording.CaptureObservation{ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	type finished struct {
		token *InspectionMaintenance
		err   error
	}
	done := make(chan finished, 1)
	control := &captureSelectionChangingControl{ControlStore: f.control, beforeCheck: func() {
		var busy, frames, checkpointed int
		if err := f.current.store.database.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &checkpointed); err != nil || busy != 0 {
			t.Fatalf("Capture SQL held over control recheck: %d %v", busy, err)
		}
		f.reader.catalog.mu.Lock()
		changed := f.reader.catalog.changed
		active := f.reader.catalog.active
		f.reader.catalog.mu.Unlock()
		if active != 1 {
			t.Fatalf("Capture source owner released early: %d", active)
		}
		go func() {
			token, err := f.reader.catalog.BeginMaintenance(context.Background(), 2)
			done <- finished{token, err}
		}()
		select {
		case <-changed:
		case <-time.After(time.Second):
			t.Fatal("maintenance did not reserve generation")
		}
	}}
	value, err := NewCaptureInspectionReader(f.reader.catalog, control).ReadCaptureObservation(context.Background(), f.scope.Capture)
	if !errors.Is(err, ErrInspectionChanged) || value.FirstObservedAt != nil || value.LastObservedAt != nil {
		t.Fatalf("maintenance disclosed stale Capture times: %+v %v", value, err)
	}
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	result.token.Close()
}

func TestCaptureInspectionMissingSourceIsUnavailableWithoutBlockingControl(t *testing.T) {
	f := newEgressInspectionFixture(t)
	if err := f.current.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.reader.catalog.root, "observations.db")); err != nil {
		t.Fatal(err)
	}
	reader := NewCaptureInspectionReader(f.reader.catalog, f.control)
	if value, err := reader.ReadCaptureObservation(context.Background(), f.scope.Capture); err == nil || value.FirstObservedAt != nil || value.LastObservedAt != nil {
		t.Fatalf("missing source became unknown successful evidence: %+v %v", value, err)
	}
	if selected, err := f.control.ReadCaptureInspectionSelection(context.Background(), f.scope.Capture); err != nil || !selected.Visible {
		t.Fatalf("optional source poisoned required selection: %+v %v", selected, err)
	}
}
