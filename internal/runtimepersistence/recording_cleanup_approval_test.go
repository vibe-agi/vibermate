package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func legacyCleanupApproval(t *testing.T, f *egressInspectionFixture) toolapproval.Record {
	t.Helper()
	r := ownedApproval(t, "legacy-proof", "")
	r.OwnerKind = toolapproval.OwnershipUnknown
	r.OwnerScope = recording.Scope{}
	if err := newToolApprovalRepository(f.legacy, newOperationGate()).Create(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	return r
}
func classifyCleanupApproval(t *testing.T, f *egressInspectionFixture, m *InspectionMaintenance, r toolapproval.Record) (recording.Scope, bool, error) {
	t.Helper()
	classifier, ok := any(f.current).(interface {
		ClassifyLegacyApproval(context.Context, *ControlStore, *InspectionMaintenance, toolapproval.Record) (recording.Scope, bool, error)
	})
	if !ok {
		t.Fatal("all-source conservative legacy approval classification unavailable")
	}
	return classifier.ClassifyLegacyApproval(t.Context(), f.control, m, r)
}
func TestRecordingLegacyApprovalRequiresSourceLocalPositiveProof(t *testing.T) {
	for _, mode := range []string{"activity", "missing", "raw-only", "current-cannot-repair-legacy", "activity-with-current-raw"} {
		t.Run(mode, func(t *testing.T) {
			f := newEgressInspectionFixture(t)
			r := legacyCleanupApproval(t, f)
			now := time.Now().UTC().Truncate(time.Millisecond)
			a := inspectionActivity("positive", r.ExchangeID, activity.KindExchangeCompleted, now)
			if mode == "activity" || mode == "activity-with-current-raw" {
				seedFrozenV1Activity(t, &Store{database: f.legacy}, a)
			}
			if mode == "current-cannot-repair-legacy" {
				if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(t.Context(), f.scope, a); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "raw-only" {
				root := rawEvidenceRecordForTest("raw-only", 1, rawevidence.LayerProviderEgress, []byte("body"), []byte(`{"version":1,"headers":[]}`))
				root.ScopeID = "audit-a"
				root.ExchangeID = r.ExchangeID
				if err := newRawEvidenceRepository(f.legacy, newOperationGate()).AppendBatch(t.Context(), []rawevidence.StoredEnvelope{root}, now); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "activity-with-current-raw" {
				root := rawEvidenceRecordForTest("matching-current", 1, rawevidence.LayerProviderEgress, []byte("body"), []byte(`{"version":1,"headers":[]}`))
				root.ScopeID = "audit-a"
				root.ExchangeID = r.ExchangeID
				root.RecordingFence = f.scope.Fence
				if err := f.current.RawEvidenceRepository().AppendBatch(t.Context(), []rawevidence.StoredEnvelope{root}, now); err != nil {
					t.Fatal(err)
				}
			}
			m, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			scope, proven, err := classifyCleanupApproval(t, f, m, r)
			if err != nil || proven != (mode == "activity" || mode == "activity-with-current-raw") || proven && scope != f.scope {
				t.Fatalf("proof=%v scope=%+v err=%v", proven, scope, err)
			}
		})
	}
}
func TestRecordingLegacyApprovalDenialVetoSurvivesConflictingRootPurge(t *testing.T) {
	f := newEgressInspectionFixture(t)
	r := legacyCleanupApproval(t, f)
	now := time.Now().UTC().Truncate(time.Millisecond)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, inspectionActivity("positive", r.ExchangeID, activity.KindExchangeCompleted, now))
	root := rawEvidenceRecordForTest("expired-conflict", 1, rawevidence.LayerProviderEgress, []byte("conflict"), []byte(`{"version":1,"headers":[]}`))
	root.ScopeID = "audit-b"
	root.ExchangeID = r.ExchangeID
	root.ObservedAt = now.Add(-2 * time.Hour)
	root.ExpiresAt = now.Add(-time.Hour)
	if err := newRawEvidenceRepository(f.legacy, newOperationGate()).AppendBatch(t.Context(), []rawevidence.StoredEnvelope{root}, root.ObservedAt); err != nil {
		t.Fatal(err)
	}
	m, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, proven, err := classifyCleanupApproval(t, f, m, r); err != nil || proven {
		t.Fatalf("conflict established owner: %v %v", proven, err)
	}
	if _, err := f.current.PurgeRecordingEvidence(t.Context(), f.control, m, now); err != nil {
		t.Fatal(err)
	}
	var roots int
	if err := f.legacy.QueryRow(`SELECT count(*) FROM runtime_raw_evidence_envelopes WHERE envelope_id='expired-conflict'`).Scan(&roots); err != nil || roots != 0 {
		t.Fatalf("conflict not physically removed: %d %v", roots, err)
	}
	if _, proven, err := classifyCleanupApproval(t, f, m, r); err != nil || proven {
		t.Fatalf("lost conflict falsely established owner: %v %v", proven, err)
	}
	if _, err := newToolApprovalRepository(f.legacy, newOperationGate()).Get(t.Context(), r.ID); err != nil {
		t.Fatalf("unknown approval removed: %v", err)
	}
}

func TestRecordingLegacyApprovalRequiredRemovalRechecksExactQuestionAndFence(t *testing.T) {
	for _, mode := range []string{"deleted", "live", "changed", "explicit", "absent"} {
		t.Run(mode, func(t *testing.T) {
			control := inspectionControlFixture(t)
			seedOwnedApprovalCaptures(t, control)
			r := ownedApproval(t, "legacy-required", "doomed")
			scope := r.OwnerScope
			r.OwnerKind = toolapproval.OwnershipUnknown
			r.OwnerScope = recording.Scope{}
			if mode != "absent" {
				if err := newToolApprovalRepository(control.store.database, newOperationGate()).Create(t.Context(), r); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "live" {
				nativeExec(t, control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='doomed'`)
			}
			if mode == "changed" {
				nativeExec(t, control.store.database, `UPDATE tool_approvals SET revision=revision+1,request_count=request_count+1 WHERE approval_id=?`, r.ID)
			}
			if mode == "explicit" {
				nativeExec(t, control.store.database, `UPDATE tool_approvals SET owner_kind='global',owner_archive_epoch=1 WHERE approval_id=?`, r.ID)
			}
			remover, ok := any(control.ToolApprovalRepository()).(interface {
				RemoveLegacyApproval(context.Context, toolapproval.Record, recording.Scope) (bool, error)
			})
			if !ok {
				t.Fatal("short exact legacy approval removal unavailable")
			}
			removed, err := remover.RemoveLegacyApproval(t.Context(), r, scope)
			if mode == "deleted" {
				if err != nil || !removed {
					t.Fatalf("valid removal: %v %v", removed, err)
				}
			} else if mode == "absent" {
				if err != nil || removed {
					t.Fatalf("absent count: %v %v", removed, err)
				}
			} else {
				if err == nil || removed {
					t.Fatalf("unsafe removal: %v %v", removed, err)
				}
				if _, err := control.ToolApprovalRepository().Get(t.Context(), r.ID); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRecordingLegacyApprovalPurgeCountsCopiesAndPreservesUnknown(t *testing.T) {
	f := newEgressInspectionFixture(t)
	r := legacyCleanupApproval(t, f)
	required := r
	required.Revision = 2
	required.RequestCount = 2
	if err := newToolApprovalRepository(f.control.store.database, newOperationGate()).Create(t.Context(), required); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	seedFrozenV1Activity(t, &Store{database: f.legacy}, inspectionActivity("positive", r.ExchangeID, activity.KindExchangeCompleted, now))
	unknown := r
	unknown.ID = "unproven"
	unknown.AggregateKey = unknown.ID
	unknown.ExchangeID = "unproven-exchange"
	if err := newToolApprovalRepository(f.legacy, newOperationGate()).Create(t.Context(), unknown); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='audit-a'`)
	m, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	purger, ok := any(f.current).(interface {
		PurgeRecordingLegacyApprovals(context.Context, *ControlStore, *InspectionMaintenance, toolapproval.LegacyApprovalRemover) (resourcedeletion.Released, error)
	})
	if !ok {
		t.Fatal("all-copy legacy approval purge unavailable")
	}
	released, err := purger.PurgeRecordingLegacyApprovals(t.Context(), f.control, m, f.control.ToolApprovalRepository().(toolapproval.LegacyApprovalRemover))
	if err != nil || released.Approvals != 2 || !released.ApprovalOwnershipUnknown || !released.CountsIncomplete {
		t.Fatalf("receipt=%+v err=%v", released, err)
	}
	if _, err := f.control.ToolApprovalRepository().Get(t.Context(), r.ID); !errors.Is(err, toolapproval.ErrNotFound) {
		t.Fatalf("required copy survived: %v", err)
	}
	if _, err := newToolApprovalRepository(f.legacy, newOperationGate()).Get(t.Context(), r.ID); !errors.Is(err, toolapproval.ErrNotFound) {
		t.Fatalf("archive copy survived: %v", err)
	}
	if _, err := newToolApprovalRepository(f.legacy, newOperationGate()).Get(t.Context(), unknown.ID); err != nil {
		t.Fatal("unknown lost", err)
	}
}

func TestRecordingLegacyCaptureMetadataCleanupKeepsSharedAndNativeSafety(t *testing.T) {
	for _, revision := range []int{1, 2} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) {
			f := newEgressInspectionFixture(t, revision)
			for _, id := range []string{"audit-a", "audit-b"} {
				nativeExec(t, f.legacy, `INSERT INTO capture_runs(run_id,proxy_capability_hash,control_capability_hash,cwd,canonical_executable_path,executable_label,client_catalog_revision,state,created_at_unix_ms,expires_at_unix_ms,updated_at_unix_ms) VALUES(?,randomblob(32),randomblob(32),'/private','/bin/tool','tool',1,'finished',10,20,20)`, id)
				nativeExec(t, f.legacy, `INSERT INTO acp_observations(run_id,policy,revision,snapshot,final,expires_at_unix_ms) VALUES(?,'{"mode":"full","retentionDays":7}',1,X'01',1,?)`, id, time.Now().Add(time.Hour).UnixMilli())
				nativeExec(t, f.legacy, `INSERT INTO capture_environment_assignments(capture_kind,capture_id,environment_id,environment_revision,environment_digest,assignment_revision,source,launch_environment_id,launch_environment_revision,launch_environment_digest,protected_authorities_json,managed_authorities_json,launch_authority_digest,updated_at_unix_ms) VALUES('managed_run',?,'env',1,randomblob(32),1,'launch','env',1,randomblob(32),'[]','[]',randomblob(32),1)`, id)
			}
			if revision == 2 {
				nativeExec(t, f.legacy, `INSERT INTO native_tool_scopes VALUES('managed_run','audit-a',1,1,'closed',1,20); INSERT INTO native_tool_once VALUES('managed_run','audit-a',randomblob(32),'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',randomblob(32),'once-proof',10,1)`)
			}
			nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='audit-a'`)
			m, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			released, err := f.current.PurgeRecordingEvidence(t.Context(), f.control, m, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if released.Captures != 1 || released.Assignments != 1 {
				t.Fatalf("metadata not accounted: %+v", released)
			}
			for _, table := range []string{"capture_runs", "capture_environment_assignments", "acp_observations"} {
				var count int
				if err := f.legacy.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
					t.Fatalf("%s remaining=%d err=%v", table, count, err)
				}
			}
			if revision == 2 {
				for _, table := range []string{"native_tool_scopes", "native_tool_once"} {
					var count int
					if err := f.legacy.QueryRow(`SELECT count(*) FROM ` + table + ` WHERE capture_id='audit-a'`).Scan(&count); err != nil || count != 1 {
						t.Fatalf("safety %s count=%d err=%v", table, count, err)
					}
				}
			}
		})
	}
}

func TestRecordingLegacyApprovalChecksEveryDeclaredCopy(t *testing.T) {
	for _, mode := range []string{"complete", "missing-proof", "conflicting-binding", "conflicting-owner", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			f := newEgressInspectionFixture(t)
			r := legacyCleanupApproval(t, f)
			now := time.Now().UTC().Truncate(time.Millisecond)
			seedFrozenV1Activity(t, &Store{database: f.legacy}, inspectionActivity("positive", r.ExchangeID, activity.KindExchangeCompleted, now))
			m, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			sources, err := m.cleanupInventory()
			if err != nil {
				t.Fatal(err)
			}
			nativeExec(t, f.legacy, `PRAGMA wal_checkpoint(TRUNCATE)`)
			bytes, err := os.ReadFile(filepath.Join(m.catalog.root, sources[0].RelativePath))
			if err != nil {
				t.Fatal(err)
			}
			extra := sources[0]
			extra.ID = "second"
			extra.RelativePath = "archives/second/runtime.db"
			path := filepath.Join(m.catalog.root, extra.RelativePath)
			if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, bytes, 0600); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			switch mode {
			case "missing-proof":
				nativeExec(t, db, `DELETE FROM runtime_activities`)
			case "conflicting-binding":
				nativeExec(t, db, `UPDATE tool_approvals SET exchange_id='different'`)
			case "conflicting-owner":
				nativeExec(t, db, `UPDATE runtime_activities SET capture_run_id='audit-b'`)
			case "unavailable":
				if err = os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if err = m.Publish([]InspectionSource{sources[0], extra, sources[1]}); err != nil {
				t.Fatal(err)
			}
			owner, proven, err := classifyCleanupApproval(t, f, m, r)
			if mode == "complete" {
				if err != nil || !proven || owner != f.scope {
					t.Fatalf("all-copy positive: %+v %v %v", owner, proven, err)
				}
			} else if proven {
				t.Fatalf("unsafe all-copy proof: %+v %v", owner, err)
			}
		})
	}
}
func TestRecordingLegacyApprovalSemanticManifestIsPositiveProof(t *testing.T) {
	f := newEgressInspectionFixture(t)
	r := legacyCleanupApproval(t, f)
	now := time.Now().UTC().Truncate(time.Millisecond)
	record := transcriptContentRecordFixture(t, r.ExchangeID, now, exchangecontent.ParentRef{CaptureRunID: f.scope.Capture.ID}, []transcriptMessage{{role: protocolcore.RoleUser, text: "retained request"}}, "response")
	if err := newExchangeContentRepository(f.legacy, f.legacy, newOperationGate(), nil).Put(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	m, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	owner, proven, err := classifyCleanupApproval(t, f, m, r)
	if err != nil || !proven || owner != f.scope {
		t.Fatalf("semantic proof: %+v %v %v", owner, proven, err)
	}
}

func TestRecordingLegacyWholeClearDoesNotClaimUnknownOwnershipRemains(t *testing.T) {
	f := newEgressInspectionFixture(t)
	legacyCleanupApproval(t, f)
	if _, _, err := f.control.ClearRecordingArchive(t.Context()); err != nil {
		t.Fatal(err)
	}
	m, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	released, err := f.current.PurgeRecordingLegacyApprovals(t.Context(), f.control, m, f.control.ToolApprovalRepository().(toolapproval.LegacyApprovalRemover))
	if err != nil {
		t.Fatal(err)
	}
	if released.ApprovalOwnershipUnknown || released.CountsIncomplete {
		t.Fatalf("whole clear incorrectly retained unknown: %+v", released)
	}
	removed, err := f.current.PurgeRecordingEvidence(t.Context(), f.control, m, time.Now())
	if err != nil || removed.Approvals != 1 {
		t.Fatalf("whole clear archive approval: %+v %v", removed, err)
	}
}

func TestRecordingNewIntentReclassifiesApprovalBeforeDestroyingItsProof(t *testing.T) {
	f := newEgressInspectionFixture(t)
	r := legacyCleanupApproval(t, f)
	if err := newToolApprovalRepository(f.control.store.database, newOperationGate()).Create(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	a := inspectionActivity("approval-positive", r.ExchangeID, activity.KindExchangeCompleted, time.Now().UTC().Truncate(time.Millisecond))
	a.CaptureRunID = "audit-b"
	seedFrozenV1Activity(t, &Store{database: f.legacy}, a)
	nativeExec(t, f.control.store.database, `INSERT INTO capture_runs(run_id,proxy_capability_hash,control_capability_hash,cwd,canonical_executable_path,executable_label,client_catalog_revision,state,created_at_unix_ms,expires_at_unix_ms,updated_at_unix_ms) VALUES('audit-b',randomblob(32),randomblob(32),'/tmp','/bin/tool','tool',1,'finished',10,20,20)`)
	m, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err = f.current.PurgeRecordingLegacyApprovals(t.Context(), f.control, m, f.control.ToolApprovalRepository().(toolapproval.LegacyApprovalRemover)); err != nil {
		t.Fatal(err)
	}
	heldWriter, err := f.control.store.database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer heldWriter.Close()
	f.control.store.reads.SetMaxOpenConns(1)
	before := f.control.store.database.Stats().WaitCount
	done := make(chan error, 1)
	go func() {
		_, err := f.current.PurgeRecordingArchive(t.Context(), f.control, m, time.Now().UTC(), f.control.ToolApprovalRepository().(toolapproval.LegacyApprovalRemover))
		done <- err
	}()
	deadline := time.Now().Add(60 * time.Second)
	for f.control.store.database.Stats().WaitCount == before {
		if time.Now().After(deadline) {
			t.Fatal("initial proof classification did not reach separate required certification")
		}
		time.Sleep(time.Millisecond)
	}
	heldReads, err := f.control.store.reads.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer heldReads.Close()
	if err = heldWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = f.control.DeleteCapture(t.Context(), "managed_run", "audit-b"); err != nil {
		t.Fatal(err)
	}
	if err = heldReads.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("cleanup did not return")
	}
	if _, err = f.control.ToolApprovalRepository().Get(t.Context(), r.ID); !errors.Is(err, toolapproval.ErrNotFound) {
		t.Fatal("newly deleted owner's required approval retained after its positive proof was destroyed", err)
	}
}
