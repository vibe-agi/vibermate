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
	if store == nil || ctx == nil || now.IsZero() {
		return resourcedeletion.Released{}, errors.New("storage cleanup request is invalid")
	}
	operation, finish, err := store.operations.begin(ctx)
	if err != nil {
		return resourcedeletion.Released{}, err
	}
	defer finish()
	transaction, err := store.database.BeginTx(operation, nil)
	if err != nil {
		return resourcedeletion.Released{}, fmt.Errorf("begin expired evidence cleanup: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	exchanges, err := purgeExpiredExchangeContent(
		operation, transaction, toUnixMillis(now.UTC()),
	)
	if err != nil {
		return resourcedeletion.Released{}, err
	}
	envelopes, err := purgeExpiredRawEvidence(
		operation, transaction, toUnixMillis(now.UTC()),
	)
	if err != nil {
		return resourcedeletion.Released{}, err
	}
	if err := transaction.Commit(); err != nil {
		return resourcedeletion.Released{}, fmt.Errorf("commit expired evidence cleanup: %w", err)
	}
	return resourcedeletion.Released{
		Exchanges: uint64(exchanges), Envelopes: uint64(envelopes),
	}, nil
}

func purgeExpiredExchangeContent(
	ctx context.Context,
	transaction *sql.Tx,
	deadline int64,
) (int64, error) {
	result, err := transaction.ExecContext(
		ctx,
		`DELETE FROM runtime_exchange_contents WHERE expires_at_unix_ms <= ?`,
		deadline,
	)
	if err != nil {
		return 0, fmt.Errorf("purge expired Exchange content evidence: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count < 0 {
		return 0, fmt.Errorf("read purged Exchange content evidence count: %w", err)
	}
	if count > 0 {
		if err := purgeUnreachableContent(ctx, transaction); err != nil {
			return 0, err
		}
	}
	return count, nil
}

func purgeExpiredRawEvidence(
	ctx context.Context,
	transaction *sql.Tx,
	deadline int64,
) (int64, error) {
	count, err := deleteExpiredRawEvidence(ctx, transaction, deadline)
	if err != nil {
		return 0, err
	}
	if count > 0 {
		if err := purgeUnreferencedEvidenceBytes(ctx, transaction); err != nil {
			return 0, err
		}
	}
	return count, nil
}

func deleteExpiredRawEvidence(
	ctx context.Context,
	transaction *sql.Tx,
	deadline int64,
) (int64, error) {
	result, err := transaction.ExecContext(
		ctx,
		`DELETE FROM runtime_raw_evidence_envelopes WHERE expires_at_unix_ms <= ?`,
		deadline,
	)
	if err != nil {
		return 0, fmt.Errorf("purge expired raw evidence: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count < 0 {
		return 0, fmt.Errorf("read purged raw evidence count: %w", err)
	}
	return count, nil
}
