package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
)

const captureRunColumns = `
	run_id,
	proxy_capability_hash,
	control_capability_hash,
	cwd,
	executable_label,
	process_id,
	state,
	created_at_unix_ms,
	expires_at_unix_ms,
	updated_at_unix_ms`

type captureRunRepository struct {
	database   *sql.DB
	operations *operationGate
}

var _ capturerun.Repository = (*captureRunRepository)(nil)

func newCaptureRunRepository(
	database *sql.DB,
	operations *operationGate,
) *captureRunRepository {
	return &captureRunRepository{database: database, operations: operations}
}

func (repository *captureRunRepository) Create(
	ctx context.Context,
	record capturerun.DurableRecord,
) error {
	if err := record.Validate(); err != nil {
		return err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	_, err = repository.database.ExecContext(
		operation,
		`INSERT INTO capture_runs (
		     run_id,
		     proxy_capability_hash,
		     control_capability_hash,
		     cwd,
		     executable_label,
		     process_id,
		     state,
		     created_at_unix_ms,
		     expires_at_unix_ms,
		     updated_at_unix_ms
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
		record.ProxyCapabilityHash[:],
		record.ControlCapabilityHash[:],
		record.CWD,
		record.ExecutableLabel,
		record.ProcessID,
		string(record.State),
		toUnixMillis(record.CreatedAt),
		toUnixMillis(record.ExpiresAt),
		toUnixMillis(record.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert CaptureRun: %w", err)
	}
	return nil
}

func (repository *captureRunRepository) AuthorizeProxy(
	ctx context.Context,
	digest capturerun.CapabilityDigest,
	now time.Time,
) (capturerun.DurableRecord, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return capturerun.DurableRecord{}, err
	}
	defer finish()
	record, err := scanCaptureRun(repository.database.QueryRowContext(
		operation,
		`SELECT `+captureRunColumns+`
		 FROM capture_runs
		 WHERE proxy_capability_hash = ?
		   AND state IN ('created', 'attached')
		   AND expires_at_unix_ms > ?`,
		digest[:],
		toUnixMillis(now),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return capturerun.DurableRecord{}, capturerun.ErrCapabilityRejected
	}
	if err != nil {
		return capturerun.DurableRecord{}, fmt.Errorf("authorize CaptureRun proxy: %w", err)
	}
	return record, nil
}

func (repository *captureRunRepository) Attach(
	ctx context.Context,
	runID string,
	digest capturerun.CapabilityDigest,
	processID int,
	now time.Time,
) (capturerun.DurableRecord, error) {
	return repository.updateAndRead(
		ctx,
		runID,
		digest,
		`UPDATE capture_runs
		 SET process_id = ?, state = 'attached', updated_at_unix_ms = ?
		 WHERE run_id = ?
		   AND control_capability_hash = ?
		   AND state IN ('created', 'attached')
		   AND expires_at_unix_ms > ?
		   AND (process_id = 0 OR process_id = ?)`,
		processID,
		toUnixMillis(now),
		runID,
		digest[:],
		toUnixMillis(now),
		processID,
	)
}

func (repository *captureRunRepository) Heartbeat(
	ctx context.Context,
	runID string,
	digest capturerun.CapabilityDigest,
	now time.Time,
	expiresAt time.Time,
) (capturerun.DurableRecord, error) {
	return repository.updateAndRead(
		ctx,
		runID,
		digest,
		`UPDATE capture_runs
		 SET expires_at_unix_ms = ?, updated_at_unix_ms = ?
		 WHERE run_id = ?
		   AND control_capability_hash = ?
		   AND state = 'attached'
		   AND expires_at_unix_ms > ?`,
		toUnixMillis(expiresAt),
		toUnixMillis(now),
		runID,
		digest[:],
		toUnixMillis(now),
	)
}

func (repository *captureRunRepository) Finish(
	ctx context.Context,
	runID string,
	digest capturerun.CapabilityDigest,
	now time.Time,
) error {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	result, err := repository.database.ExecContext(
		operation,
		`UPDATE capture_runs
		 SET state = 'finished', updated_at_unix_ms = ?
		 WHERE run_id = ?
		   AND control_capability_hash = ?
		   AND (
		       state = 'finished' OR
		       (state IN ('created', 'attached') AND expires_at_unix_ms > ?)
		   )`,
		toUnixMillis(now),
		runID,
		digest[:],
		toUnixMillis(now),
	)
	if err != nil {
		return fmt.Errorf("finish CaptureRun: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read CaptureRun finish result: %w", err)
	}
	if affected != 1 {
		return capturerun.ErrCapabilityRejected
	}
	return nil
}

func (repository *captureRunRepository) Recover(
	ctx context.Context,
	now time.Time,
) (capturerun.Recovery, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return capturerun.Recovery{}, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return capturerun.Recovery{}, fmt.Errorf("begin CaptureRun recovery: %w", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	result, err := transaction.ExecContext(
		operation,
		`UPDATE capture_runs
		 SET state = 'expired', updated_at_unix_ms = ?
		 WHERE state IN ('created', 'attached')
		   AND expires_at_unix_ms <= ?`,
		toUnixMillis(now),
		toUnixMillis(now),
	)
	if err != nil {
		return capturerun.Recovery{}, fmt.Errorf("expire recovered CaptureRuns: %w", err)
	}
	expired, err := result.RowsAffected()
	if err != nil {
		return capturerun.Recovery{}, fmt.Errorf("count expired CaptureRuns: %w", err)
	}
	var active int
	if err := transaction.QueryRowContext(
		operation,
		`SELECT COUNT(*)
		 FROM capture_runs
		 WHERE state IN ('created', 'attached')
		   AND expires_at_unix_ms > ?`,
		toUnixMillis(now),
	).Scan(&active); err != nil {
		return capturerun.Recovery{}, fmt.Errorf("count active CaptureRuns: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return capturerun.Recovery{}, fmt.Errorf("commit CaptureRun recovery: %w", err)
	}
	return capturerun.Recovery{
		ExpiredCount: int(expired),
		ActiveCount:  active,
	}, nil
}

func (repository *captureRunRepository) RevokeActive(
	ctx context.Context,
	now time.Time,
) (int, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer finish()
	result, err := repository.database.ExecContext(
		operation,
		`UPDATE capture_runs
		 SET state = 'revoked', updated_at_unix_ms = ?
		 WHERE state IN ('created', 'attached')`,
		toUnixMillis(now),
	)
	if err != nil {
		return 0, fmt.Errorf("revoke active CaptureRuns: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count revoked CaptureRuns: %w", err)
	}
	return int(affected), nil
}

func (repository *captureRunRepository) updateAndRead(
	ctx context.Context,
	runID string,
	_ capturerun.CapabilityDigest,
	statement string,
	arguments ...any,
) (capturerun.DurableRecord, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return capturerun.DurableRecord{}, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return capturerun.DurableRecord{}, fmt.Errorf("begin CaptureRun update: %w", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	result, err := transaction.ExecContext(operation, statement, arguments...)
	if err != nil {
		return capturerun.DurableRecord{}, fmt.Errorf("update CaptureRun: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return capturerun.DurableRecord{}, fmt.Errorf("read CaptureRun update result: %w", err)
	}
	if affected != 1 {
		return capturerun.DurableRecord{}, capturerun.ErrCapabilityRejected
	}
	record, err := scanCaptureRun(transaction.QueryRowContext(
		operation,
		`SELECT `+captureRunColumns+` FROM capture_runs WHERE run_id = ?`,
		runID,
	))
	if err != nil {
		return capturerun.DurableRecord{}, fmt.Errorf("read updated CaptureRun: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return capturerun.DurableRecord{}, fmt.Errorf("commit CaptureRun update: %w", err)
	}
	return record, nil
}

type captureRunScanner interface {
	Scan(...any) error
}

func scanCaptureRun(scanner captureRunScanner) (capturerun.DurableRecord, error) {
	var (
		record                            capturerun.DurableRecord
		proxyHash, controlHash            []byte
		state                             string
		createdAt, expiresAt, updatedAtMS int64
	)
	if err := scanner.Scan(
		&record.ID,
		&proxyHash,
		&controlHash,
		&record.CWD,
		&record.ExecutableLabel,
		&record.ProcessID,
		&state,
		&createdAt,
		&expiresAt,
		&updatedAtMS,
	); err != nil {
		return capturerun.DurableRecord{}, err
	}
	if len(proxyHash) != len(record.ProxyCapabilityHash) ||
		len(controlHash) != len(record.ControlCapabilityHash) {
		return capturerun.DurableRecord{}, errors.New("CaptureRun capability hash length is invalid")
	}
	copy(record.ProxyCapabilityHash[:], proxyHash)
	copy(record.ControlCapabilityHash[:], controlHash)
	record.State = capturerun.State(state)
	record.CreatedAt = fromUnixMillis(createdAt)
	record.ExpiresAt = fromUnixMillis(expiresAt)
	record.UpdatedAt = fromUnixMillis(updatedAtMS)
	if err := record.Validate(); err != nil {
		return capturerun.DurableRecord{}, err
	}
	return record, nil
}
