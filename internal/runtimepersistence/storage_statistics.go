package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

// StorageStatistics is a page-level snapshot. It never reads or decompresses
// retained bodies; EvidenceBytes is the SQLite page allocation of the tables
// that own semantic and Raw HTTP content.
type StorageStatistics struct {
	EvidenceBytes int64
	ReusableBytes int64
	Expired       resourcedeletion.Released
}

func (store *Store) StorageStatistics(
	ctx context.Context,
	now time.Time,
) (StorageStatistics, error) {
	if store == nil || ctx == nil || now.IsZero() {
		return StorageStatistics{}, errors.New("storage statistics request is invalid")
	}
	operation, finish, err := store.operations.begin(ctx)
	if err != nil {
		return StorageStatistics{}, err
	}
	defer finish()

	var result StorageStatistics
	var pageSize, freePages int64
	err = store.database.QueryRowContext(operation, `
SELECT
  COALESCE((SELECT SUM(pgsize) FROM dbstat WHERE name IN (
    'runtime_evidence_bodies', 'runtime_evidence_chunks',
    'runtime_exchange_content_blocks', 'runtime_exchange_content_messages',
    'runtime_exchange_content_transcripts', 'runtime_exchange_contents',
    'runtime_raw_evidence_envelopes'
  )), 0),
  (SELECT page_size FROM pragma_page_size),
  (SELECT freelist_count FROM pragma_freelist_count),
  (SELECT COUNT(*) FROM runtime_exchange_contents WHERE expires_at_unix_ms <= ?),
  (SELECT COUNT(*) FROM runtime_raw_evidence_envelopes WHERE expires_at_unix_ms <= ?)
`, toUnixMillis(now.UTC()), toUnixMillis(now.UTC())).Scan(
		&result.EvidenceBytes, &pageSize, &freePages,
		&result.Expired.Exchanges, &result.Expired.Envelopes,
	)
	if err != nil {
		return StorageStatistics{}, fmt.Errorf("read storage statistics: %w", err)
	}
	if pageSize <= 0 || freePages < 0 ||
		freePages > math.MaxInt64/pageSize {
		return StorageStatistics{}, errors.New("SQLite storage statistics are invalid")
	}
	result.ReusableBytes = pageSize * freePages
	return result, nil
}

// EvidenceArchivePreview performs exact row counts only on the explicit clear
// path. Routine storage snapshots therefore do not scan every evidence index.
func (store *Store) EvidenceArchivePreview(
	ctx context.Context,
) (resourcedeletion.Released, error) {
	if store == nil || ctx == nil {
		return resourcedeletion.Released{}, errors.New("archive preview request is invalid")
	}
	operation, finish, err := store.operations.begin(ctx)
	if err != nil {
		return resourcedeletion.Released{}, err
	}
	defer finish()
	var result resourcedeletion.Released
	err = store.database.QueryRowContext(operation, `
SELECT
  (SELECT COUNT(*) FROM runtime_exchange_contents),
  (SELECT COUNT(*) FROM runtime_raw_evidence_envelopes),
  (SELECT COUNT(*) FROM runtime_activities),
  (SELECT COUNT(*) FROM runtime_connection_events),
  (SELECT COUNT(*) FROM runtime_egress_attempts),
  (SELECT COUNT(*) FROM tool_approvals),
  (SELECT COUNT(*) FROM capture_environment_assignments),
  (SELECT COUNT(*) FROM capture_runs) + (SELECT COUNT(*) FROM manual_captures)
`).Scan(
		&result.Exchanges, &result.Envelopes, &result.Activities,
		&result.Connections, &result.Attempts, &result.Approvals,
		&result.Assignments, &result.Captures,
	)
	if err != nil {
		return resourcedeletion.Released{}, fmt.Errorf("preview evidence archive clear: %w", err)
	}
	return result, nil
}

// CleanupExpired removes only evidence whose own retention deadline passed.
// Both evidence planes share one transaction so a reported failure cannot hide
// a partial cleanup.
func (store *Store) CleanupExpired(
	ctx context.Context,
	now time.Time,
) (resourcedeletion.Released, error) {
	released, _, err := store.cleanupExpired(ctx, now, -1)
	return released, err
}

const expiredCleanupBatchSize = 1000

// MaintainExpired yields the write connection between bounded deletion batches.
// Its caller owns the time budget; cancellation rolls back only the active batch,
// so the next maintenance pass continues instead of repeating a huge transaction.
// Explicit user cleanup retains the all-or-nothing receipt above.
func (store *Store) MaintainExpired(ctx context.Context, now time.Time) error {
	for {
		_, more, err := store.cleanupExpired(ctx, now, expiredCleanupBatchSize)
		if err != nil || !more {
			return err
		}
	}
}

func (store *Store) cleanupExpired(
	ctx context.Context,
	now time.Time,
	limit int,
) (resourcedeletion.Released, bool, error) {
	if store == nil || ctx == nil || now.IsZero() || limit == 0 || limit < -1 {
		return resourcedeletion.Released{}, false, errors.New("storage cleanup request is invalid")
	}
	operation, finish, err := store.operations.begin(ctx)
	if err != nil {
		return resourcedeletion.Released{}, false, err
	}
	defer finish()
	transaction, err := store.database.BeginTx(operation, nil)
	if err != nil {
		return resourcedeletion.Released{}, false, fmt.Errorf("begin expired evidence cleanup: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	exchanges, err := purgeExpiredExchangeContent(
		operation, transaction, toUnixMillis(now.UTC()), limit,
	)
	if err != nil {
		return resourcedeletion.Released{}, false, err
	}
	envelopes, err := purgeExpiredRawEvidence(
		operation, transaction, toUnixMillis(now.UTC()), limit,
	)
	if err != nil {
		return resourcedeletion.Released{}, false, err
	}
	retentionPending, err := applyUsageRetention(operation, transaction, limit)
	if err != nil {
		return resourcedeletion.Released{}, false, err
	}
	result, err := transaction.ExecContext(operation, `DELETE FROM runtime_usage_observations WHERE sequence IN (
		SELECT sequence FROM runtime_usage_observations WHERE expires_at_unix_ms<=? ORDER BY expires_at_unix_ms LIMIT ?
	)`, now.UnixMilli(), limit)
	if err != nil {
		return resourcedeletion.Released{}, false, err
	}
	usage, err := result.RowsAffected()
	if err != nil {
		return resourcedeletion.Released{}, false, err
	}
	if err := transaction.Commit(); err != nil {
		return resourcedeletion.Released{}, false, fmt.Errorf("commit expired evidence cleanup: %w", err)
	}
	return resourcedeletion.Released{
		Exchanges: uint64(exchanges), Envelopes: uint64(envelopes),
	}, retentionPending || limit > 0 && (exchanges == int64(limit) || envelopes == int64(limit) || usage == int64(limit)), nil
}

func purgeExpiredExchangeContent(
	ctx context.Context,
	transaction *sql.Tx,
	deadline int64,
	limit int,
) (int64, error) {
	return deleteExchangeContent(
		ctx, transaction,
		`DELETE FROM runtime_exchange_contents WHERE exchange_id IN (
		 SELECT exchange_id FROM runtime_exchange_contents WHERE expires_at_unix_ms <= ? ORDER BY expires_at_unix_ms LIMIT ?
		)`,
		deadline, limit,
	)
}

func purgeExpiredRawEvidence(
	ctx context.Context,
	transaction *sql.Tx,
	deadline int64,
	limit int,
) (int64, error) {
	return deleteRawEvidence(
		ctx, transaction,
		`DELETE FROM runtime_raw_evidence_envelopes WHERE envelope_id IN (
		 SELECT envelope_id FROM runtime_raw_evidence_envelopes WHERE expires_at_unix_ms <= ? ORDER BY expires_at_unix_ms LIMIT ?
		)`,
		deadline, limit,
	)
}
