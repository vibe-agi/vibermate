package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

func TestRecordingPhysicalUsageCleanupPreservesLiveCopiesAndDeniesExpiredIdentity(t *testing.T) {
	for _, revision := range []int{1, 2} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) {
			f := newUsageInspectionFixtureRevision(t, revision)
			expired := f.value("expired-copy", "profile")
			expired.StartedAt = f.now.Add(-10 * 24 * time.Hour)
			expired.OccurredAt = expired.StartedAt
			live := f.value("live-copy", "profile")
			for _, current := range []bool{false, true} {
				f.put(t, current, expired, f.now.Add(30*24*time.Hour), 3)
				f.put(t, current, live, f.now.Add(30*24*time.Hour), 3)
			}
			nativeExec(t, f.control.store.database, `INSERT INTO runtime_usage_legacy_retention_caps(archive_id,retention_days,after_sequence,through_sequence) VALUES('legacy',7,0,2)`)
			if err := f.current.PublishInspectionMirror(t.Context(), f.control); err != nil {
				t.Fatal(err)
			}
			token, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
			if err != nil {
				t.Fatal(err)
			}
			defer token.Close()
			cleanup, ok := any(f.current).(interface {
				PurgeExpiredRecordingUsage(context.Context, *ControlStore, *InspectionMaintenance, time.Time) (uint64, error)
			})
			if !ok {
				t.Fatal("physical all-source Usage cleanup unavailable")
			}
			removed, err := cleanup.PurgeExpiredRecordingUsage(t.Context(), f.control, token, f.now)
			if err != nil || removed != 2 {
				t.Fatalf("actual purge: removed=%d err=%v", removed, err)
			}
			for _, db := range []interface {
				QueryRowContext(context.Context, string, ...any) *sql.Row
			}{f.legacy, f.current.store.database} {
				var remaining int
				if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM runtime_usage_observations WHERE exchange_id='live-copy'`).Scan(&remaining); err != nil || remaining != 1 {
					t.Fatalf("live source copy lost: %d %v", remaining, err)
				}
			}
			if err := f.current.PublishInspectionMirror(t.Context(), f.control); err != nil {
				t.Fatal(err)
			}
			if err := token.Close(); err != nil {
				t.Fatal(err)
			}
			if _, calls, err := f.scan(t); err != nil || calls != 1 {
				t.Fatalf("after physical purge: calls=%d %v", calls, err)
			}
			epoch, _ := recording.NewEpoch(1)
			stamp, _ := recording.NewStamp(epoch, captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "usage-owner"}, recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1})
			if err := f.current.PutCollectedUsage(t.Context(), runtimeusage.CollectedObservation{Value: expired, Stamp: stamp, PolicyRevision: 3, ExpiresAt: f.now.Add(365 * 24 * time.Hour)}); !errors.Is(err, runtimeusage.ErrObservationDiscarded) {
				t.Fatalf("late newer-policy copy resurrected: %v", err)
			}
		})
	}
}

func TestRecordingPhysicalCompactionRefusesRetainedWAL(t *testing.T) {
	f := newUsageInspectionFixture(t)
	value := f.value("held-wal", "profile")
	f.put(t, true, value, f.now.Add(time.Hour), 3)
	held, err := f.current.store.reads.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	var count int
	if err = held.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM runtime_usage_observations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	token, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	nativeExec(t, f.current.store.database, `DELETE FROM runtime_usage_observations WHERE exchange_id='held-wal'`)
	compactor, ok := any(f.current).(interface {
		CompactRecordingSource(context.Context, *InspectionMaintenance, int) error
	})
	if !ok {
		t.Fatal("verified physical compaction unavailable")
	}
	if err = compactor.CompactRecordingSource(t.Context(), token, 1); err == nil {
		t.Fatal("retained WAL reader was called physical completion")
	}
	if err = held.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = compactor.CompactRecordingSource(t.Context(), token, 1); err != nil {
		t.Fatal(err)
	}
	var walBytes int64
	if st, err := os.Stat(f.current.store.databasePath + "-wal"); err == nil {
		walBytes = st.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if walBytes != 0 {
		t.Fatalf("physical completion retained WAL bytes: %d", walBytes)
	}
}

func TestRecordingPhysicalAuditClosurePrecedesExpiringProof(t *testing.T) {
	for _, revision := range []int{1, 2} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) {
			f := newEgressInspectionFixture(t, revision)
			a := providerAttempt(t, "expired-root")
			f.put(t, false, a)
			f.put(t, true, a)
			linkedInput := normalizedEgressStart(providerAttempt(t, "later-link"))
			linkedInput.ConnectionID = "other-connection"
			linkedInput.Parent.ExchangeID = a.Parent().ExchangeID
			linked, err := egressaudit.New(linkedInput)
			if err != nil {
				t.Fatal(err)
			}
			f.put(t, true, linked)
			now := time.Now().UTC().Truncate(time.Millisecond)
			root := rawEvidenceRecordForTest("expired-proof", 1, rawevidence.LayerProviderEgress, []byte("shared retained body"), []byte(`{"version":1,"headers":[]}`))
			root.ScopeID = "audit-a"
			root.AttemptID = a.ID()
			root.ConnectionID = a.ConnectionID()
			root.ExchangeID = a.Parent().ExchangeID
			root.ObservedAt = now.Add(-2 * time.Hour)
			root.ExpiresAt = now.Add(-time.Hour)
			unrelated := root
			unrelated.EnvelopeID = "unrelated-proof"
			unrelated.Watermark = 2
			unrelated.ScopeID = "audit-b"
			unrelated.AttemptID = "unrelated-attempt"
			unrelated.ConnectionID = "unrelated-connection"
			unrelated.ExchangeID = "unrelated-exchange"
			unrelated.ExpiresAt = now.Add(time.Hour)
			unique := rawEvidenceRecordForTest("unique-expired-proof", 3, rawevidence.LayerProviderEgress, []byte("unshared erased body"), []byte(`{"version":1,"headers":[]}`))
			unique.ScopeID = root.ScopeID
			unique.AttemptID = root.AttemptID
			unique.ConnectionID = root.ConnectionID
			unique.ExchangeID = root.ExchangeID
			unique.ObservedAt = root.ObservedAt
			unique.ExpiresAt = root.ExpiresAt
			if err = newRawEvidenceRepository(f.legacy, newOperationGate()).AppendBatch(t.Context(), []rawevidence.StoredEnvelope{root, unrelated, unique}, root.ObservedAt); err != nil {
				t.Fatal(err)
			}
			token, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
			if err != nil {
				t.Fatal(err)
			}
			defer token.Close()
			cleanup, ok := any(f.current).(interface {
				PurgeRecordingEvidence(context.Context, *ControlStore, *InspectionMaintenance, time.Time) (resourcedeletion.Released, error)
			})
			if !ok {
				t.Fatal("physical certified audit closure unavailable")
			}
			released, err := cleanup.PurgeRecordingEvidence(t.Context(), f.control, token, now)
			if err != nil || released.Envelopes != 2 || released.Attempts != 3 {
				t.Fatalf("cleanup receipt: %+v %v", released, err)
			}
			var bodies, roots int
			if err = f.legacy.QueryRow(`SELECT (SELECT count(*) FROM runtime_evidence_bodies),(SELECT count(*) FROM runtime_raw_evidence_envelopes WHERE envelope_id='unrelated-proof')`).Scan(&bodies, &roots); err != nil || bodies != 1 || roots != 1 {
				t.Fatalf("shared unrelated root damaged: bodies=%d roots=%d %v", bodies, roots, err)
			}
			if err = f.current.PublishInspectionMirror(t.Context(), f.control); err != nil {
				t.Fatal(err)
			}
			terminal, err := a.Finish(egressaudit.TerminalInput{Outcome: egressaudit.OutcomeCompleted, CompletedAt: a.StartedAt().Add(time.Second)})
			if err != nil {
				t.Fatal(err)
			}
			if err = f.current.EgressAttemptRepository().(egressaudit.ScopedAttemptStore).PutScopedAttempt(t.Context(), f.scope, terminal); !errors.Is(err, recording.ErrObservationDiscarded) {
				t.Fatalf("late terminal revived after proof removal: %v", err)
			}
			if err = f.current.EgressAttemptRepository().(egressaudit.ScopedAttemptStore).PutScopedAttempt(t.Context(), f.scope, linked); !errors.Is(err, recording.ErrObservationDiscarded) {
				t.Fatalf("late linked callback revived: %v", err)
			}
		})
	}
}

func TestRecordingPhysicalNativeHistoryAndIndependentScopeRoots(t *testing.T) {
	f := newEgressInspectionFixture(t)
	_, history := nativeIndependentHistoryFixture(t)
	history.Scope = f.scope
	history.Observation.Operation.Capture = f.scope.Capture
	history.Observation.Operation.RecordingScope = f.scope
	if err := f.current.PutNativeHistory(t.Context(), history); err != nil {
		t.Fatal(err)
	}
	if err := f.current.ConversationIdentityRepository().(activity.ScopedConversationIdentityWriter).PutScopedConversationIdentity(t.Context(), f.scope, "root-without-activity", agentconversation.ClientIdentity{Client: "codex", SessionID: "session", SessionResumable: true, ProviderResponseID: "root-without-activity", Source: agentconversation.ClientIdentitySourceProtocolEvidence, Confidence: "exact", ObservedAt: time.Now().UTC(), ProtocolIDs: []agentconversation.ClientEvidenceValue{}, Attributes: []agentconversation.ClientEvidenceValue{}}); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, f.current.store.database, `INSERT INTO runtime_raw_inspection_reveal_audits(layout_id,source_set,source_generation,source_id,recording_scope_kind,recording_scope_id,recording_archive_epoch,recording_capture_generation,visibility_revision,usage_policy_revision,envelope_id,exchange_id,actor_id,outcome,occurred_at_unix_ms) VALUES('inspection-layout',?,1,'legacy','managed_run','audit-a',1,1,1,1,'missing-envelope','missing-exchange','actor','unavailable',1)`, strings.Repeat("a", 64))
	nativeExec(t, f.control.store.database, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_id='audit-a';UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
	token, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	if _, err = f.current.PurgeRecordingEvidence(t.Context(), f.control, token, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"native_history_snapshots", "native_history_latest", "native_inspection_index", "runtime_exchange_agent_identities", "runtime_raw_inspection_reveal_audits"} {
		var n int
		if err = f.current.store.database.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("independent scoped root %s retained %d: %v", table, n, err)
		}
	}
}

func TestRecordingPhysicalUsageCleanupRefusesImmutableConflict(t *testing.T) {
	f := newUsageInspectionFixture(t)
	v := f.value("conflicting", "first")
	f.put(t, false, v, f.now.Add(-time.Minute), 0)
	changed := v
	changed.EnvironmentName = "different immutable label"
	f.put(t, true, changed, f.now.Add(time.Hour), 3)
	token, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	if removed, err := f.current.PurgeExpiredRecordingUsage(t.Context(), f.control, token, f.now); !errors.Is(err, ErrInspectionUnavailable) || removed != 0 {
		t.Fatalf("conflict deleted source proof: %d %v", removed, err)
	}
	for _, db := range []*sql.DB{f.current.store.database, f.legacy} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM runtime_usage_observations WHERE exchange_id='conflicting'`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("conflicting source changed: %d %v", n, err)
		}
	}
}

func TestRecordingPhysicalUsageCleanupCoversBeyondReaderAttachmentLimit(t *testing.T) {
	f := newUsageInspectionFixture(t)
	v := f.value("late-archive-expiry", "profile")
	f.put(t, false, v, f.now.Add(-time.Minute), 0)
	f.put(t, true, v, f.now.Add(time.Hour), 3)
	if err := f.legacy.Close(); err != nil {
		t.Fatal(err)
	}
	c := f.reader.catalog
	original := c.sources[0]
	bytes, err := os.ReadFile(filepath.Join(c.root, original.RelativePath))
	if err != nil {
		t.Fatal(err)
	}
	sources := []InspectionSource{original}
	for i := 1; i < 12; i++ {
		id := fmt.Sprintf("legacy%d", i)
		dir := filepath.Join(c.root, "archives", id)
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "runtime.db"), bytes, 0600); err != nil {
			t.Fatal(err)
		}
		source := original
		source.ID = id
		source.RelativePath = "archives/" + id + "/runtime.db"
		sources = append(sources, source)
	}
	sources = append(sources, c.sources[1])
	expanded, err := NewInspectionCatalog(InspectionCatalogOptions{Root: c.root, LayoutID: c.layout, Sources: sources, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer expanded.Close(context.Background())
	token, err := expanded.BeginMaintenance(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	removed, err := f.current.PurgeExpiredRecordingUsage(t.Context(), f.control, token, f.now)
	if err != nil || removed != 13 {
		t.Fatalf("declared inventory omitted: removed=%d err=%v", removed, err)
	}
}

func TestRecordingPhysicalUsageCleanupRestartAndFutureTightening(t *testing.T) {
	f := newUsageInspectionFixture(t)
	live := f.value("future-tightening", "profile")
	live.StartedAt = f.now.Add(-10 * 24 * time.Hour)
	live.OccurredAt = live.StartedAt
	for _, current := range []bool{false, true} {
		f.put(t, current, live, f.now.Add(30*24*time.Hour), 3)
	}
	token, err := f.reader.catalog.BeginMaintenance(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := f.current.PurgeExpiredRecordingUsage(t.Context(), f.control, token, f.now)
	if err != nil || removed != 0 {
		t.Fatalf("live inherited policy proof removed: %d %v", removed, err)
	}
	token.Close()
	nativeExec(t, f.control.store.database, `INSERT INTO runtime_usage_retention_rules(retention_days,through_policy_revision) VALUES(7,3);UPDATE runtime_usage_policy SET revision=revision+1`)
	token, err = f.reader.catalog.BeginMaintenance(t.Context(), 3)
	if err != nil {
		t.Fatal(err)
	}
	removed, err = f.current.PurgeExpiredRecordingUsage(t.Context(), f.control, token, f.now)
	if err != nil || removed != 2 {
		t.Fatalf("future tightening lost legacy semantics: %d %v", removed, err)
	}
	token.Close()
	path := f.current.store.databasePath
	source := f.reader.catalog.sources[1]
	if err = f.current.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenObservations(t.Context(), ObservationOptions{DatabasePath: path, LayoutID: f.reader.catalog.layout, ArchiveID: source.ID, BusyTimeout: DefaultBusyTimeout, CommitReconcileTimeout: DefaultCommitReconcileTimeout})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Shutdown(context.Background())
	if err = reopened.PublishInspectionMirror(t.Context(), f.control); err != nil {
		t.Fatal(err)
	}
	epoch, _ := recording.NewEpoch(1)
	stamp, _ := recording.NewStamp(epoch, captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "usage-owner"}, recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1})
	if err = reopened.PutCollectedUsage(t.Context(), runtimeusage.CollectedObservation{Value: live, Stamp: stamp, PolicyRevision: 4, ExpiresAt: f.now.Add(365 * 24 * time.Hour)}); !errors.Is(err, runtimeusage.ErrObservationDiscarded) {
		t.Fatalf("restart forgot whole-ID denial: %v", err)
	}
}

func TestRecordingExpiredNativeHistoryCannotRepopulateCleanup(t *testing.T) {
	store, h := nativeIndependentHistoryFixture(t)
	delta := -40 * 24 * time.Hour
	h.Observation.Operation.CreatedAt = h.Observation.Operation.CreatedAt.Add(delta)
	completed := h.Observation.Operation.CompletedAt.Add(delta)
	h.Observation.Operation.CompletedAt = &completed
	h.Observation.Operation.MaximumEventAt = h.Observation.Operation.MaximumEventAt.Add(delta)
	h.Observation.Operation.MetadataExpiresAt = h.Observation.Operation.MetadataExpiresAt.Add(delta)
	h.Observation.EventAt = h.Observation.EventAt.Add(delta)
	h.Observation.MaximumEventAt = h.Observation.MaximumEventAt.Add(delta)
	h.Observation.MetadataExpiresAt = h.Observation.MetadataExpiresAt.Add(delta)
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := store.PutNativeHistory(t.Context(), h); !errors.Is(err, recording.ErrObservationDiscarded) {
		t.Fatalf("expired original history callback was retained: %v", err)
	}
}

func TestRecordingPhysicalNativeAllCopiesAndOlderSnapshots(t *testing.T) {
	reader, current, control, op := nativeInspectionLegacyFixture(t)
	h := nativetool.RecordedHistory{Scope: op.RecordingScope, Observation: nativetool.HistoryObservation{Operation: op, EventKind: "completed", EventAt: *op.CompletedAt, MaximumEventAt: op.MaximumEventAt, MetadataExpiresAt: op.MetadataExpiresAt}}
	if err := current.PutNativeHistory(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	older := h
	older.Observation.Operation.Revision--
	older.Observation.Operation.State = "started"
	older.Observation.Operation.CompletedAt = nil
	older.Observation.Operation.Reason = ""
	older.Observation.Operation.BytesIn = nil
	older.Observation.Operation.BytesOut = nil
	older.Observation.EventKind = "started"
	if err := current.PutNativeHistory(t.Context(), older); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(reader.catalog.root, reader.catalog.sources[0].RelativePath)
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	var onceBefore int
	if err = legacy.QueryRow(`SELECT count(*) FROM native_tool_once`).Scan(&onceBefore); err != nil {
		t.Fatal(err)
	}
	token, err := reader.catalog.BeginMaintenance(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	// The clock advances beyond the one original Receive expiry; revisions do
	// not extend it. The same declared operation exists in both actual schemas.
	if _, err = current.PurgeRecordingEvidence(t.Context(), control, token, op.MetadataExpiresAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"native_history_latest", "native_history_snapshots", "native_inspection_index"} {
		var n int
		if err = current.store.database.QueryRow(`SELECT count(*) FROM `+table+` WHERE operation_id=?`, op.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("old snapshot survived in %s: %d %v", table, n, err)
		}
	}
	var operations, contents, onceAfter int
	if err = legacy.QueryRow(`SELECT (SELECT count(*) FROM native_tool_operations WHERE operation_id=?),(SELECT count(*) FROM native_tool_contents WHERE operation_id=?),(SELECT count(*) FROM native_tool_once)`, op.ID, op.ID).Scan(&operations, &contents, &onceAfter); err != nil || operations != 0 || contents != 0 || onceAfter != onceBefore {
		t.Fatalf("legacy cleanup altered safety or retained expired roots: %d %d once=%d/%d %v", operations, contents, onceAfter, onceBefore, err)
	}
}
