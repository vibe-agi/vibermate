package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

// ErrInvalidCaptureTarget rejects a deletion that does not name one Capture.
var ErrInvalidCaptureTarget = errors.New("Capture deletion target is invalid")

// CaptureDeletion reports what a Capture deletion released.
//
// The counts are what the user gave up, so they are the answer a confirmation
// dialog needs before the fact and the receipt it needs after.
type CaptureDeletion = resourcedeletion.Released

// DeleteCapture removes one Capture and every piece of evidence owned by it.
//
// It is one transaction on purpose. The evidence graph spans semantic content,
// raw traffic, Activity, connections, egress attempts, approvals, assignments
// and the Capture authority itself. Splitting the work would leave a
// half-deleted Capture visible between commits with no way for the user to
// finish it.
//
// Both content-addressed planes are reclaimed in the same transaction: a
// body or block is released when the last Exchange naming it goes, which cannot
// be known until the deletes have run.
func (store *Store) DeleteCapture(
	ctx context.Context,
	captureKind string,
	captureID string,
) (CaptureDeletion, error) {
	if store == nil || ctx == nil ||
		(captureKind != "managed_run" && captureKind != "manual_capture") ||
		captureID == "" {
		return CaptureDeletion{}, ErrInvalidCaptureTarget
	}
	operation, finish, err := store.operations.begin(ctx)
	if err != nil {
		return CaptureDeletion{}, err
	}
	defer finish()
	transaction, err := store.database.BeginTx(operation, nil)
	if err != nil {
		return CaptureDeletion{}, fmt.Errorf("begin Capture deletion: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	capture := captureidentity.Reference{Kind: captureidentity.Kind(captureKind), ID: captureID}
	if err := revokeNativeScope(operation, transaction, capture, time.Now().UTC()); err != nil {
		return CaptureDeletion{}, err
	}

	if err := selectCaptureEvidence(
		operation, transaction, captureKind, captureID,
	); err != nil {
		return CaptureDeletion{}, err
	}
	deletion := CaptureDeletion{}
	nativeApprovals, err := deleteCounted(operation, transaction, `DELETE FROM native_tool_approvals WHERE operation_id IN(SELECT operation_id FROM native_tool_operations WHERE capture_kind=? AND capture_id=?)`, captureKind, captureID)
	if err != nil {
		return CaptureDeletion{}, err
	}
	deletion.Approvals, err = deleteCounted(
		operation, transaction,
		`DELETE FROM tool_approvals
		  WHERE exchange_id IN (SELECT id FROM capture_purge_exchange_ids)`,
	)
	if err != nil {
		return CaptureDeletion{}, fmt.Errorf("delete Capture approvals: %w", err)
	}
	deletion.Approvals += nativeApprovals
	deletion.Attempts, err = deleteCounted(
		operation, transaction,
		`DELETE FROM runtime_egress_attempts
		  WHERE attempt_id IN (SELECT id FROM capture_purge_attempt_ids)`,
	)
	if err != nil {
		return CaptureDeletion{}, fmt.Errorf("delete Capture egress attempts: %w", err)
	}
	deletion.Connections, err = deleteCounted(
		operation, transaction,
		`DELETE FROM runtime_connection_events
		  WHERE (recording_scope_kind=? AND recording_scope_id=?) OR (recording_archive_epoch=0 AND connection_id IN (SELECT id FROM capture_purge_connection_ids))`, captureKind, captureID,
	)
	if err != nil {
		return CaptureDeletion{}, fmt.Errorf("delete Capture connection evidence: %w", err)
	}
	if _, err := deleteCounted(
		operation, transaction,
		`DELETE FROM runtime_exchange_agent_identities
		  WHERE (recording_scope_kind=? AND recording_scope_id=?) OR (recording_archive_epoch=0 AND exchange_id IN (SELECT id FROM capture_purge_exchange_ids))`, captureKind, captureID,
	); err != nil {
		return CaptureDeletion{}, fmt.Errorf("delete Capture Agent identities: %w", err)
	}
	exchanges, err := deleteExchangeContent(
		operation, transaction,
		`DELETE FROM runtime_exchange_contents
		  WHERE scope_kind = ? AND scope_id = ?`,
		captureKind, captureID,
	)
	if err != nil {
		return CaptureDeletion{}, fmt.Errorf("delete Capture content evidence: %w", err)
	}
	deletion.Exchanges = uint64(exchanges)
	envelopes, err := deleteRawEvidence(
		operation, transaction,
		`DELETE FROM runtime_raw_evidence_envelopes
		  WHERE scope_kind = ? AND scope_id = ?`,
		captureKind, captureID,
	)
	if err != nil {
		return CaptureDeletion{}, fmt.Errorf("delete Capture raw evidence: %w", err)
	}
	deletion.Envelopes = uint64(envelopes)
	if _, err = transaction.ExecContext(operation, `DELETE FROM runtime_raw_inspection_reveal_audits WHERE recording_scope_kind=? AND recording_scope_id=?`, captureKind, captureID); err != nil {
		return CaptureDeletion{}, err
	}
	if _, err := deleteNativeContents(operation, transaction, `operation_id IN(SELECT operation_id FROM native_tool_operations WHERE capture_kind=? AND capture_id=?)`, captureKind, captureID); err != nil {
		return CaptureDeletion{}, err
	}
	if _, err := transaction.ExecContext(operation, `DELETE FROM native_tool_operations WHERE capture_kind=? AND capture_id=?`, captureKind, captureID); err != nil {
		return CaptureDeletion{}, err
	}
	if _, err := transaction.ExecContext(operation, `UPDATE native_tool_scopes SET state='closed',replay_barrier=1 WHERE capture_kind=? AND capture_id=?`, captureKind, captureID); err != nil {
		return CaptureDeletion{}, err
	}
	activityColumn := "capture_run_id"
	if captureKind == "manual_capture" {
		activityColumn = "manual_capture_id"
	}
	deletion.Activities, err = deleteCounted(
		operation, transaction,
		`DELETE FROM runtime_activities WHERE `+activityColumn+` = ?`,
		captureID,
	)
	if err != nil {
		return CaptureDeletion{}, fmt.Errorf("delete Capture Activities: %w", err)
	}
	deletion.Assignments, err = deleteCounted(
		operation, transaction,
		`DELETE FROM capture_environment_assignments
		  WHERE capture_kind = ? AND capture_id = ?`,
		captureKind, captureID,
	)
	if err != nil {
		return CaptureDeletion{}, fmt.Errorf("delete Capture Environment assignment: %w", err)
	}
	authorityTable := "capture_runs"
	if _, err := transaction.ExecContext(operation, `DELETE FROM runtime_usage_observations WHERE `+activityColumn+`=?`, captureID); err != nil {
		return CaptureDeletion{}, err
	}
	authorityColumn := "run_id"
	if captureKind == "manual_capture" {
		authorityTable = "manual_captures"
		authorityColumn = "capture_id"
	}
	deletion.Captures, err = deleteCounted(
		operation, transaction,
		`DELETE FROM `+authorityTable+` WHERE `+authorityColumn+` = ?`,
		captureID,
	)
	if err != nil {
		return CaptureDeletion{}, fmt.Errorf("delete Capture authority: %w", err)
	}
	if deletion.Captures == 0 {
		return CaptureDeletion{}, resourcedeletion.ErrTargetNotFound
	}

	if err := dropCaptureEvidenceSelection(operation, transaction); err != nil {
		return CaptureDeletion{}, err
	}
	if err := transaction.Commit(); err != nil {
		return CaptureDeletion{}, fmt.Errorf("commit Capture deletion: %w", err)
	}
	return deletion, nil
}

// selectCaptureEvidence freezes the cross-table ownership graph before any of
// its roots are removed. The schema deliberately stores exact IDs rather than
// foreign keys across the runtime projections, so this small transaction-local
// index is the authority for the purge below.
func selectCaptureEvidence(
	ctx context.Context,
	transaction *sql.Tx,
	captureKind string,
	captureID string,
) error {
	if err := dropCaptureEvidenceSelection(ctx, transaction); err != nil {
		return err
	}
	for _, statement := range []string{
		`CREATE TEMP TABLE capture_purge_exchange_ids(
			id TEXT PRIMARY KEY NOT NULL
		) WITHOUT ROWID`,
		`CREATE TEMP TABLE capture_purge_connection_ids(
			id TEXT PRIMARY KEY NOT NULL
		) WITHOUT ROWID`,
		`CREATE TEMP TABLE capture_purge_attempt_ids(
			id TEXT PRIMARY KEY NOT NULL
		) WITHOUT ROWID`,
	} {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create Capture evidence selection: %w", err)
		}
	}
	activityColumn := "capture_run_id"
	if captureKind == "manual_capture" {
		activityColumn = "manual_capture_id"
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT OR IGNORE INTO capture_purge_connection_ids(id) SELECT connection_id FROM runtime_connection_events WHERE recording_scope_kind=? AND recording_scope_id=?`, []any{captureKind, captureID}},
		{`INSERT OR IGNORE INTO capture_purge_attempt_ids(id) SELECT attempt_id FROM runtime_egress_attempts WHERE purpose<>'client_tool_service' AND recording_scope_kind=? AND recording_scope_id=?`, []any{captureKind, captureID}},
		{
			`INSERT OR IGNORE INTO capture_purge_exchange_ids(id)
			 SELECT exchange_id FROM runtime_exchange_contents
			 WHERE scope_kind = ? AND scope_id = ?`,
			[]any{captureKind, captureID},
		},
		{
			`INSERT OR IGNORE INTO capture_purge_exchange_ids(id)
			 SELECT exchange_id FROM runtime_raw_evidence_envelopes
			 WHERE scope_kind = ? AND scope_id = ?`,
			[]any{captureKind, captureID},
		},
		{
			`INSERT OR IGNORE INTO capture_purge_exchange_ids(id)
			 SELECT subject_id FROM runtime_activities
			 WHERE ` + activityColumn + ` = ?
			   AND kind IN ('exchange.started', 'exchange.completed')`,
			[]any{captureID},
		},
		{
			`INSERT OR IGNORE INTO capture_purge_connection_ids(id)
			 SELECT connection_id FROM runtime_raw_evidence_envelopes
			 WHERE scope_kind = ? AND scope_id = ? AND connection_id <> ''`,
			[]any{captureKind, captureID},
		},
		{
			`INSERT OR IGNORE INTO capture_purge_connection_ids(id)
			 SELECT connection_id FROM runtime_activities
			 WHERE ` + activityColumn + ` = ? AND connection_id <> ''`,
			[]any{captureID},
		},
		{
			`INSERT OR IGNORE INTO capture_purge_attempt_ids(id) SELECT attempt_id FROM native_tool_once WHERE capture_kind=? AND capture_id=?`,
			[]any{captureKind, captureID},
		},
		{
			`INSERT OR IGNORE INTO capture_purge_attempt_ids(id)
			 SELECT attempt_id FROM runtime_raw_evidence_envelopes
			 WHERE scope_kind = ? AND scope_id = ? AND attempt_id <> ''`,
			[]any{captureKind, captureID},
		},
		{
			`INSERT OR IGNORE INTO capture_purge_attempt_ids(id)
			 SELECT attempt_id FROM runtime_egress_attempts
			 WHERE purpose<>'client_tool_service' AND recording_archive_epoch=0 AND (parent_exchange_id IN (
			   SELECT id FROM capture_purge_exchange_ids
			 ) OR connection_id IN (
			   SELECT id FROM capture_purge_connection_ids
			 ))`,
			nil,
		},
		{
			`INSERT OR IGNORE INTO capture_purge_connection_ids(id)
			 SELECT connection_id FROM runtime_egress_attempts
			 WHERE attempt_id IN (SELECT id FROM capture_purge_attempt_ids)
			   AND connection_id <> ''`,
			nil,
		},
		{
			`INSERT OR IGNORE INTO capture_purge_attempt_ids(id)
			 SELECT attempt_id FROM runtime_egress_attempts
			 WHERE purpose<>'client_tool_service' AND recording_archive_epoch=0 AND connection_id IN (SELECT id FROM capture_purge_connection_ids)`,
			nil,
		},
	}
	for _, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return fmt.Errorf("select Capture evidence graph: %w", err)
		}
	}
	return nil
}

func dropCaptureEvidenceSelection(ctx context.Context, transaction *sql.Tx) error {
	for _, table := range []string{
		"capture_purge_attempt_ids",
		"capture_purge_connection_ids",
		"capture_purge_exchange_ids",
	} {
		if _, err := transaction.ExecContext(ctx, `DROP TABLE IF EXISTS `+table); err != nil {
			return fmt.Errorf("drop Capture evidence selection: %w", err)
		}
	}
	return nil
}

func deleteCounted(
	ctx context.Context,
	transaction *sql.Tx,
	statement string,
	arguments ...any,
) (uint64, error) {
	result, err := transaction.ExecContext(ctx, statement, arguments...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected < 0 {
		return 0, err
	}
	return uint64(affected), nil
}

// ArchiveClear reports what a full clear released.
type ArchiveClear = resourcedeletion.Released

// evidenceTables lists what "clear the archive" means, in delete order.
//
// The list is explicit rather than derived, because the interesting question is
// what it leaves behind. Configuration stays: Environments, Endpoints,
// Accounts and connection rules are what the user set up, not what the runtime
// observed. Three runtime tables stay too, and each for a
// reason a reader would otherwise have to guess at:
//
//   - runtime_metadata carries the schema revision. Clearing it would make the
//     database unopenable rather than empty.
//   - runtime_raw_evidence_redaction holds the per-database redaction salt.
//     Nothing survives the clear to correlate against, and keeping it means a
//     clear does not quietly change what redaction digests mean.
//   - runtime_raw_evidence_writer_sessions holds durable watermarks. Envelope
//     identity is (writer_id, watermark) and it is UNIQUE, so resetting the
//     watermark would let a later envelope collide with one that is gone.
var evidenceTables = []string{
	"runtime_usage_observations",
	"runtime_usage_retention_caps",
	"tool_approvals",
	"native_tool_approvals",
	"native_tool_contents",
	"native_tool_operations",
	"runtime_exchange_agent_identities",
	"runtime_activities",
	"runtime_connection_events",
	"runtime_egress_attempts",
	// Reveal audits cascade from here.
	"runtime_raw_evidence_envelopes",
	"runtime_raw_inspection_reveal_audits",
	"runtime_evidence_bodies",
	"runtime_evidence_chunks",
	"runtime_exchange_contents",
	"runtime_exchange_content_transcripts",
	"runtime_exchange_content_messages",
	"runtime_exchange_content_blocks",
	"capture_environment_assignments",
	"capture_runs",
	"manual_captures",
}

// ClearEvidence empties the archive in one transaction.
//
// Design 06 section 8.2 makes this a distinct, deliberate, confirmable action
// rather than a side effect of stopping or uninstalling. Section 8.1 bounds
// what it may promise: logical records go and queries stop answering, but
// nothing here claims physical erasure from SSD, snapshots or backups.
func (store *Store) ClearEvidence(ctx context.Context) (ArchiveClear, error) {
	if store == nil || ctx == nil {
		return ArchiveClear{}, ErrInvalidCaptureTarget
	}
	operation, finish, err := store.operations.begin(ctx)
	if err != nil {
		return ArchiveClear{}, err
	}
	defer finish()
	transaction, err := store.database.BeginTx(operation, nil)
	if err != nil {
		return ArchiveClear{}, fmt.Errorf("begin archive clear: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	cleared := ArchiveClear{}
	if err := revokeNativeScopesWhere(operation, transaction, "state<>'closed'", time.Now().UTC()); err != nil {
		return ArchiveClear{}, err
	}
	if _, err := transaction.ExecContext(operation, `UPDATE native_tool_scopes SET state='closed',replay_barrier=1`); err != nil {
		return ArchiveClear{}, err
	}
	// Reject pre-clear terminal callbacks even for traffic without a Capture.
	// Keep the collection setting, but begin a fresh observation interval.
	if _, err := transaction.ExecContext(operation, `UPDATE runtime_usage_policy SET revision=revision+1,
 collecting_since_unix_ms=max(coalesce(collecting_since_unix_ms,0),cast(unixepoch('subsec')*1000 as integer)+1,
 coalesce((SELECT max(occurred_at_unix_ms)+1 FROM runtime_usage_observations),0)) WHERE enabled=1`); err != nil {
		return ArchiveClear{}, err
	}
	for _, table := range evidenceTables {
		count, deleteErr := deleteCounted(
			operation, transaction, `DELETE FROM `+table,
		)
		if deleteErr != nil {
			return ArchiveClear{}, fmt.Errorf("clear %s: %w", table, deleteErr)
		}
		switch table {
		case "tool_approvals", "native_tool_approvals":
			cleared.Approvals += count
		case "runtime_exchange_contents":
			cleared.Exchanges = count
		case "runtime_raw_evidence_envelopes":
			cleared.Envelopes = count
		case "runtime_activities":
			cleared.Activities = count
		case "runtime_connection_events":
			cleared.Connections = count
		case "runtime_egress_attempts":
			cleared.Attempts = count
		case "capture_environment_assignments":
			cleared.Assignments = count
		case "capture_runs", "manual_captures":
			cleared.Captures += count
		}
	}
	if err := transaction.Commit(); err != nil {
		return ArchiveClear{}, fmt.Errorf("commit archive clear: %w", err)
	}
	// Logical clear already succeeded and must be reported as success. Reclaim
	// freelist pages while callers still hold the archive barrier. Detach from
	// the request deadline so a slow VACUUM cannot turn a wipe into HTTP 503;
	// reclaim failure only leaves the file large, never revives deleted rows.
	reclaim, cancel := context.WithTimeout(context.WithoutCancel(operation), 2*time.Minute)
	defer cancel()
	_ = store.reclaimEvidencePages(reclaim)
	return cleared, nil
}

// reclaimEvidencePages checkpoints the WAL then VACUUMs. Call only after a
// committed destructive archive action while Capture creation is barred.
func (store *Store) reclaimEvidencePages(ctx context.Context) error {
	if store == nil || store.database == nil {
		return ErrInvalidCaptureTarget
	}
	if _, err := store.database.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("checkpoint archive WAL: %w", err)
	}
	if _, err := store.database.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("vacuum archive: %w", err)
	}
	return nil
}
