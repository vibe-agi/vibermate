package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

func seedCaptureEvidence(
	t *testing.T,
	store *Store,
	scopeKind string,
	scopeID string,
	turns int,
) {
	t.Helper()
	repository := store.RawEvidenceRepository()
	observed := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	records := make([]rawevidence.StoredEnvelope, 0, turns)
	for index := 0; index < turns; index++ {
		record := rawEvidenceRecordForTest(
			fmt.Sprintf("writer-%s.%d", scopeID, index),
			uint64(index+1),
			rawevidence.LayerClientIngress,
			[]byte(fmt.Sprintf(
				`{"scope":%q,"turn":%d,"body":%q}`,
				scopeID, index, fmt.Sprintf("unique body for %s turn %d", scopeID, index),
			)),
			[]byte(`{"version":1,"headers":[]}`),
		)
		record.WriterID = "writer-" + scopeID
		record.ScopeKind = rawevidence.ScopeKind(scopeKind)
		record.ScopeID = scopeID
		record.ExchangeID = fmt.Sprintf("%s-exchange-%d", scopeID, index)
		record.ConnectionID = scopeID + "-connection"
		record.AttemptID = scopeID + "-attempt"
		record.ObservedAt = observed.Add(time.Duration(index) * time.Second)
		record.ExpiresAt = record.ObservedAt.AddDate(0, 0, 30)
		records = append(records, record)
	}
	if err := repository.AppendBatch(context.Background(), records, observed); err != nil {
		t.Fatal(err)
	}
}

func deletionFixtureDigest(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func seedCaptureGraph(
	t *testing.T,
	store *Store,
	scopeKind string,
	scopeID string,
	turns int,
) {
	t.Helper()
	ctx := context.Background()
	if scopeKind == "managed_run" {
		if _, err := store.database.ExecContext(ctx, `
			INSERT INTO capture_runs(
			  run_id, proxy_capability_hash, control_capability_hash, cwd,
			  canonical_executable_path, executable_label,
			  client_catalog_revision, state, created_at_unix_ms,
			  expires_at_unix_ms, updated_at_unix_ms
			) VALUES(?, randomblob(32), randomblob(32), '/tmp', '/bin/echo',
			  'Claude Code', 1, 'finished', 1, 2, 2)
		`, scopeID); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := store.database.ExecContext(ctx, `
			INSERT INTO manual_captures(
			  capture_id, owner_kind, owner_id, display_name, client_class,
			  lifetime, state, credential_revision, proxy_credential_hash,
			  observation, created_at_unix_ms, updated_at_unix_ms
			) VALUES(?, 'local_installation', '', 'Desktop client', 'desktop_app',
			  'until_revoked', 'revoked', 1, randomblob(32),
			  'waiting_for_traffic', 1, 2)
		`, scopeID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.database.ExecContext(ctx, `
		INSERT INTO capture_environment_assignments(
		  capture_kind, capture_id, environment_id, assignment_revision,
		  source, launch_environment_id, launch_environment_revision,
		  launch_environment_digest, protected_authorities_json,
		  managed_authorities_json, launch_authority_digest,
		  updated_at_unix_ms
		) VALUES(?, ?, 'environment.fixture', 1, 'launch',
		  'environment.fixture', 1, randomblob(32), '[]', '[]',
		  randomblob(32), 1)
	`, scopeKind, scopeID); err != nil {
		t.Fatal(err)
	}

	exchangeID := scopeID + "-exchange-0"
	connectionID := scopeID + "-connection"
	attemptID := scopeID + "-attempt"
	activityColumn := "capture_run_id"
	sourceKind := "capture_run"
	if scopeKind == "manual_capture" {
		activityColumn = "manual_capture_id"
		sourceKind = "manual_proxy"
	}
	if _, err := store.database.ExecContext(ctx, `
		INSERT INTO runtime_activities(
		  activity_id, occurred_at_unix_ms, kind, subject_id, status,
		  source_kind, `+activityColumn+`, connection_id
		) VALUES(?, 1, 'approval.pending', ?, 'pending', ?, ?, ?)
	`, scopeID+"-activity", scopeID+"-approval", sourceKind, scopeID, connectionID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, `
		INSERT INTO runtime_connection_events(
		  connection_id, source_confidence, requested_host, port,
		  decryption, phase, started_at_unix_ms, ended_at_unix_ms, outcome
		) VALUES(?, 'verified', 'api.example.test', 443,
		  'mitm', 'closed', 1, 2, 'completed')
	`, connectionID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, `
		INSERT INTO runtime_egress_attempts(
		  attempt_id, connection_id, purpose, payload_class, parent_kind,
		  parent_id, parent_exchange_id, caller_kind, target_origin,
		  policy_id, policy_revision, policy_authority, rule_id, proxy_id,
		  started_at_unix_ms, completed_at_unix_ms, outcome
		) VALUES(?, ?, 'provider_attempt', 'client_semantic',
		  'upstream_attempt', ?, ?, 'core', 'https://api.example.test',
		  'policy.fixture', 1, 'environment', 'rule.fixture', 'direct',
		  1, 2, 'completed')
	`, attemptID, connectionID, attemptID, exchangeID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, `
		INSERT INTO tool_approvals(
		  approval_id, revision, kind, exchange_id, environment_id,
		  environment_revision, environment_digest, route_id,
		  route_revision, subject_refs_json, subject_labels_json,
		  aggregate_key, state, created_at_unix_ms, expires_at_unix_ms
		) VALUES(?, 1, 'tool_intent', ?, 'environment.fixture', 1,
		  randomblob(32), 'route.fixture', 1, ?, ?, ?,
		  'pending', 1, 2)
	`, scopeID+"-approval", exchangeID, []byte(`["x"]`), []byte(`["x"]`),
		scopeID+"-aggregate"); err != nil {
		t.Fatal(err)
	}

	blockDigest := deletionFixtureDigest(scopeID + "-block")
	messageDigest := deletionFixtureDigest(scopeID + "-message")
	transcriptDigest := deletionFixtureDigest(scopeID + "-transcript")
	if _, err := store.database.ExecContext(ctx, `
		INSERT INTO runtime_exchange_content_blocks(
		  digest, plain_bytes, codec, payload
		) VALUES(?, 7, 'identity', ?)
	`, blockDigest, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, `
		INSERT INTO runtime_exchange_content_messages(
		  digest, role, block_manifest
		) VALUES(?, 'user', ?)
	`, messageDigest, blockDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, `
		INSERT INTO runtime_exchange_content_transcripts(
		  digest, message_digest, depth
		) VALUES(?, ?, 1)
	`, transcriptDigest, messageDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(ctx, `
		INSERT INTO runtime_exchange_contents(
		  exchange_id, scope_kind, scope_id, mode, recorded_at_unix_ms,
		  expires_at_unix_ms, request_transcript_digest,
		  expected_transcript_digest, request_message_count,
		  expected_message_count, inherited_message_count, manifest_json
		) VALUES(?, ?, ?, 'full', 1, 2, ?, ?, 1, 1, 0, ?)
	`, exchangeID, scopeKind, scopeID, transcriptDigest, transcriptDigest,
		[]byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	seedCaptureEvidence(t, store, scopeKind, scopeID, turns)
}

// Deleting a Capture has to release the bytes, not just the rows. Content is
// addressed by digest, so an envelope can go while the body it named stays
// reachable from another Capture — and stays on disk if nothing sweeps.
func TestDeletingACaptureReleasesItsBytesAndLeavesOtherCapturesIntact(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)

	seedCaptureGraph(t, store, "managed_run", "run-doomed", 8)
	seedCaptureGraph(t, store, "managed_run", "run-kept", 8)
	chunksBefore := countRows(t, store, "runtime_evidence_chunks")
	bodiesBefore := countRows(t, store, "runtime_evidence_bodies")
	if chunksBefore == 0 || bodiesBefore == 0 {
		t.Fatal("the seed stored no content-addressed bytes")
	}

	deletion, err := store.DeleteCapture(
		context.Background(), "managed_run", "run-doomed",
	)
	if err != nil {
		t.Fatal(err)
	}
	wantDeletion := (CaptureDeletion{
		Exchanges: 1, Envelopes: 8, Activities: 1, Connections: 1,
		Attempts: 1, Approvals: 1, Assignments: 1, Captures: 1,
	})
	if deletion != wantDeletion {
		t.Fatalf("deletion = %+v, want %+v", deletion, wantDeletion)
	}

	if got := countRows(t, store, "runtime_evidence_bodies"); got >= bodiesBefore {
		t.Fatalf(
			"bodies = %d, was %d; deleting a Capture did not release its bytes",
			got, bodiesBefore,
		)
	}
	if got := countRows(t, store, "runtime_evidence_chunks"); got >= chunksBefore {
		t.Fatalf("chunks = %d, was %d; unreferenced chunks survived", got, chunksBefore)
	}

	kept, err := store.RawEvidenceRepository().ListExchange(
		context.Background(), "run-kept-exchange-3",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Fatalf("the other Capture lost evidence: %d envelopes", len(kept))
	}
	body, err := store.RawEvidenceRepository().ListExchange(
		context.Background(), "run-doomed-exchange-3",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Fatalf("the deleted Capture still answers: %d envelopes", len(body))
	}
	for table, predicate := range map[string]string{
		"capture_runs":                    "run_id = 'run-doomed'",
		"capture_environment_assignments": "capture_id = 'run-doomed'",
		"runtime_activities":              "capture_run_id = 'run-doomed'",
		"runtime_connection_events":       "connection_id = 'run-doomed-connection'",
		"runtime_egress_attempts":         "attempt_id = 'run-doomed-attempt'",
		"tool_approvals":                  "approval_id = 'run-doomed-approval'",
	} {
		var count int
		if err := store.database.QueryRow(
			`SELECT COUNT(*) FROM ` + table + ` WHERE ` + predicate,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s kept %d rows from the deleted Capture", table, count)
		}
	}
	if got := countRows(t, store, "capture_runs"); got != 1 {
		t.Fatalf("the other Capture authority was deleted: %d rows", got)
	}
}

func TestDeletingAManualCapturePurgesItsFullEvidenceGraph(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)

	seedCaptureGraph(t, store, "manual_capture", "manual-doomed", 4)
	deletion, err := store.DeleteCapture(
		context.Background(), "manual_capture", "manual-doomed",
	)
	if err != nil {
		t.Fatal(err)
	}
	wantDeletion := (CaptureDeletion{
		Exchanges: 1, Envelopes: 4, Activities: 1, Connections: 1,
		Attempts: 1, Approvals: 1, Assignments: 1, Captures: 1,
	})
	if deletion != wantDeletion {
		t.Fatalf("deletion = %+v, want %+v", deletion, wantDeletion)
	}
	for _, table := range evidenceTables {
		if got := countRows(t, store, table); got != 0 {
			t.Fatalf("%s kept %d rows from the deleted ManualCapture", table, got)
		}
	}
}

func TestDeletingACaptureRejectsATargetThatNamesNoCapture(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)

	for _, target := range []struct{ kind, id string }{
		{"", "run-1"},
		{"managed_run", ""},
		{"invented_kind", "run-1"},
	} {
		if _, err := store.DeleteCapture(
			context.Background(), target.kind, target.id,
		); !errors.Is(err, ErrInvalidCaptureTarget) {
			t.Fatalf("DeleteCapture(%q, %q) error = %v", target.kind, target.id, err)
		}
	}
}

func TestDeletingAMissingCaptureDoesNotReturnAFakeReceipt(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)

	if _, err := store.DeleteCapture(
		context.Background(), "managed_run", "run-missing",
	); !errors.Is(err, resourcedeletion.ErrTargetNotFound) {
		t.Fatalf("DeleteCapture error = %v, want ErrTargetNotFound", err)
	}
}

// A clear must empty the archive and leave the database usable. The three
// runtime tables it spares are each load-bearing, so this asserts what stays as
// carefully as what goes.
func TestClearingTheArchiveEmptiesEvidenceAndKeepsTheDatabaseUsable(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, databasePath)
	seedCaptureGraph(t, store, "managed_run", "run-one", 6)
	seedCaptureGraph(t, store, "manual_capture", "manual-two", 6)

	saltBefore, err := store.RawEvidenceRepository().RedactionSalt(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	cleared, err := store.ClearEvidence(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Envelopes != 12 {
		t.Fatalf("cleared = %+v, want 12 envelopes", cleared)
	}
	for _, table := range evidenceTables {
		if got := countRows(t, store, table); got != 0 {
			t.Fatalf("%s kept %d rows after a clear", table, got)
		}
	}

	// The schema revision has to survive, or a cleared database is an
	// unopenable one rather than an empty one.
	if got := countRows(t, store, "runtime_metadata"); got == 0 {
		t.Fatal("the clear removed the schema revision")
	}
	// The redaction salt has to survive, or a clear quietly changes what every
	// later redaction digest means.
	saltAfter, err := store.RawEvidenceRepository().RedactionSalt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(saltAfter) != string(saltBefore) {
		t.Fatal("the clear rotated the redaction salt")
	}

	// Writing has to keep working, and watermarks must not restart: envelope
	// identity is (writer_id, watermark) and it is UNIQUE.
	seedCaptureEvidence(t, store, "managed_run", "run-three", 3)
	if got := countRows(t, store, "runtime_raw_evidence_envelopes"); got != 3 {
		t.Fatalf("envelopes = %d after writing to a cleared archive, want 3", got)
	}
	shutdownTestStore(t, store)

	// And it has to reopen.
	reopened := openTestStore(t, databasePath)
	defer shutdownTestStore(t, reopened)
	if got := countRows(t, reopened, "runtime_raw_evidence_envelopes"); got != 3 {
		t.Fatalf("envelopes = %d after reopening a cleared archive, want 3", got)
	}
}
