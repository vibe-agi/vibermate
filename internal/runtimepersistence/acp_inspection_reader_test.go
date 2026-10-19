package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type acpInspectionFixture struct {
	reader  *ACPInspectionReader
	current *ObservationStore
	control *ControlStore
	legacy  *sql.DB
	record  acpobservation.Record
	now     int64
}

func newACPInspectionFixture(t *testing.T, revisions ...int) *acpInspectionFixture {
	t.Helper()
	options, current := inspectionSourcesFixture(t)
	if len(revisions) > 0 && revisions[0] == 1 {
		path, db := extractionFixture(t, 1)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(options.Root, "archives/legacy/runtime.db"), data, 0600); err != nil {
			t.Fatal(err)
		}
		options.Sources[0].Database.Revision = 1
	}
	control := inspectionControlFixture(t)
	nativeExec(t, control.store.database, `INSERT INTO recording_capture_fences VALUES('managed_run','acp-owner',1,0)`)
	legacy, err := sql.Open("sqlite", filepath.Join(options.Root, "archives/legacy/runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { legacy.Close() })
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close(context.Background()) })
	now := time.Now().UTC().Truncate(time.Millisecond)
	record := acpobservation.Record{RunID: "acp-owner", Scope: recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "acp-owner"}, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}, Policy: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 30}, ExpiresAtMillis: now.Add(time.Hour).UnixMilli(), Snapshot: acpobservation.Snapshot{Revision: 1, Agent: acpobservation.Agent{Name: "Retained agent"}, Sessions: []acpobservation.Session{{ID: "session", Operation: "new"}}, Prompts: []acpobservation.Prompt{{Sequence: 1, SessionID: "session", State: "pending", UserText: "Retained request"}}, Incomplete: true}}
	return &acpInspectionFixture{reader: NewACPInspectionReader(catalog, control), current: current, control: control, legacy: legacy, record: record, now: now.UnixMilli()}
}
func (f *acpInspectionFixture) legacyRecord(t *testing.T, r acpobservation.Record) {
	t.Helper()
	policy, err := json.Marshal(r.Policy)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := json.Marshal(r.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	nativeExec(t, f.legacy, `INSERT INTO capture_runs(run_id,proxy_capability_hash,control_capability_hash,cwd,canonical_executable_path,executable_label,client_catalog_revision,state,created_at_unix_ms,expires_at_unix_ms,updated_at_unix_ms) VALUES(?,randomblob(32),randomblob(32),'/tmp','/bin/tool','tool',1,'finished',?,?,?)`, r.RunID, f.now-3600000, r.ExpiresAtMillis, f.now-1)
	nativeExec(t, f.legacy, `INSERT INTO acp_observations(run_id,policy,expires_at_unix_ms,snapshot,revision,final) VALUES(?,?,?,?,?,?)`, r.RunID, string(policy), r.ExpiresAtMillis, snapshot, r.Snapshot.Revision, r.Snapshot.Final)
}
func TestACPInspectionChoosesHighestConsistentSnapshotWithoutControlHistoryJoin(t *testing.T) {
	f := newACPInspectionFixture(t)
	f.legacyRecord(t, f.record)
	current := f.record
	current.Snapshot.Revision = 3
	current.Snapshot.Prompts = append([]acpobservation.Prompt(nil), current.Snapshot.Prompts...)
	current.Snapshot.Prompts[0].AgentText = "Current response"
	if err := f.current.PutACP(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	got, err := f.reader.Read(context.Background(), f.record.RunID, f.now)
	if err != nil || got.Snapshot.Revision != 3 || got.Snapshot.Prompts[0].AgentText != "Current response" || !got.Snapshot.Incomplete || got.Scope != f.record.Scope {
		t.Fatalf("independent ACP snapshot: %+v %v", got, err)
	}
}
func TestACPInspectionEqualRevisionConflictRefuses(t *testing.T) {
	f := newACPInspectionFixture(t)
	f.legacyRecord(t, f.record)
	current := f.record
	current.Snapshot.Agent.Name = "Conflicting agent"
	if err := f.current.PutACP(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reader.Read(context.Background(), f.record.RunID, f.now); !errors.Is(err, acpobservation.ErrConflict) {
		t.Fatalf("equal revision conflict selected silently: %v", err)
	}
}

func TestACPInspectionRetainsExpiryAndRejectsInvalidSnapshots(t *testing.T) {
	for _, mode := range []string{"expired-null", "expired-preserved", "oversize", "column-mismatch", "policy-conflict", "final-conflict", "deleted", "generation", "epoch"} {
		t.Run(mode, func(t *testing.T) {
			f := newACPInspectionFixture(t)
			legacy := f.record
			if mode == "expired-null" || mode == "expired-preserved" {
				legacy.ExpiresAtMillis = f.now - 1
			}
			if mode == "final-conflict" {
				code := 0
				legacy.Snapshot.Final = true
				legacy.Snapshot.ExitCode = &code
				legacy.Snapshot.Prompts = append([]acpobservation.Prompt(nil), legacy.Snapshot.Prompts...)
				legacy.Snapshot.Prompts[0].State = "completed"
			}
			f.legacyRecord(t, legacy)
			switch mode {
			case "expired-null":
				nativeExec(t, f.legacy, `UPDATE acp_observations SET snapshot=NULL`)
			case "oversize":
				nativeExec(t, f.legacy, `UPDATE acp_observations SET snapshot=zeroblob(?)`, acpobservation.MaxSnapshotBytes+1)
			case "column-mismatch":
				nativeExec(t, f.legacy, `UPDATE acp_observations SET revision=2`)
			case "policy-conflict", "final-conflict":
				current := f.record
				current.Snapshot.Revision = 2
				if mode == "policy-conflict" {
					current.Policy.RetentionDays = 7
				}
				if err := f.current.PutACP(context.Background(), current); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='acp-owner'`)
			case "generation":
				nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET generation=2 WHERE capture_id='acp-owner'`)
			case "epoch":
				nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET archive_epoch=2`)
			}
			got, err := f.reader.Read(context.Background(), f.record.RunID, f.now)
			switch mode {
			case "expired-null", "expired-preserved":
				if err != nil || !got.Expired || got.Snapshot.Revision != 0 || got.Policy != legacy.Policy {
					t.Fatalf("expired marker fabricated/lost: %+v %v", got, err)
				}
				if mode == "expired-preserved" {
					var present int
					if err = f.legacy.QueryRow(`SELECT snapshot IS NOT NULL FROM acp_observations WHERE run_id=?`, f.record.RunID).Scan(&present); err != nil || present != 1 {
						t.Fatalf("reader mutated original archive retention: %d %v", present, err)
					}
				}
			case "deleted", "generation", "epoch":
				if !errors.Is(err, acpobservation.ErrNotFound) {
					t.Fatalf("stale original scope visible: %+v %v", got, err)
				}
			case "policy-conflict", "final-conflict":
				if !errors.Is(err, acpobservation.ErrConflict) {
					t.Fatalf("conflicting publication accepted: %+v %v", got, err)
				}
			default:
				if err == nil {
					t.Fatalf("malformed snapshot accepted: %+v", got)
				}
			}
		})
	}
}
func TestACPInspectionHigherLegacyRevisionAndFinalGuard(t *testing.T) {
	f := newACPInspectionFixture(t)
	legacy := f.record
	legacy.Snapshot.Revision = 4
	f.legacyRecord(t, legacy)
	if err := f.current.PutACP(context.Background(), f.record); err != nil {
		t.Fatal(err)
	}
	got, err := f.reader.Read(context.Background(), f.record.RunID, f.now)
	if err != nil || got.Snapshot.Revision != 4 {
		t.Fatalf("source ordinal overrode snapshot revision: %+v %v", got, err)
	}
	visibility := &usageInspectionChangingVisibility{ControlStore: f.control, beforeCheck: func() {
		nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
	}}
	f.reader.visibility = visibility
	got, err = f.reader.Read(context.Background(), f.record.RunID, f.now)
	if !errors.Is(err, ErrInspectionChanged) || got.RunID != "" {
		t.Fatalf("stale ACP snapshot published: %+v %v", got, err)
	}
}

func TestACPInspectionFrozenV1AndCurrentOriginalScope(t *testing.T) {
	f := newACPInspectionFixture(t, 1)
	f.legacyRecord(t, f.record)
	got, err := f.reader.Read(context.Background(), f.record.RunID, f.now)
	if err != nil || got.Snapshot.Agent.Name != f.record.Snapshot.Agent.Name {
		t.Fatalf("frozen v1 snapshot lost: %+v %v", got, err)
	}
	current := f.record
	current.Scope.Fence.CaptureGeneration = 2
	if err = f.current.PutACP(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, f.control.store.database, `UPDATE runtime_storage_state SET archive_epoch=2`)
	if _, err = f.reader.Read(context.Background(), f.record.RunID, f.now); !errors.Is(err, acpobservation.ErrNotFound) {
		t.Fatalf("stale current original relabeled: %v", err)
	}
}
func TestACPInspectionSourceGenerationChangesBeforePublication(t *testing.T) {
	f := newACPInspectionFixture(t)
	f.legacyRecord(t, f.record)
	type maintenanceResult struct {
		token *InspectionMaintenance
		err   error
	}
	done := make(chan maintenanceResult, 1)
	f.reader.visibility = &usageInspectionChangingVisibility{ControlStore: f.control, beforeCheck: func() {
		f.reader.catalog.mu.Lock()
		changed := f.reader.catalog.changed
		f.reader.catalog.mu.Unlock()
		go func() {
			token, err := f.reader.catalog.BeginMaintenance(context.Background(), 2)
			done <- maintenanceResult{token, err}
		}()
		select {
		case <-changed:
		case <-time.After(time.Second):
			t.Fatal("maintenance failed to advance source generation")
		}
	}}
	got, err := f.reader.Read(context.Background(), f.record.RunID, f.now)
	if !errors.Is(err, ErrInspectionChanged) || got.RunID != "" {
		t.Fatalf("stale source snapshot published: %+v %v", got, err)
	}
	finished := <-done
	if finished.err != nil {
		t.Fatal(finished.err)
	}
	finished.token.Close()
}
