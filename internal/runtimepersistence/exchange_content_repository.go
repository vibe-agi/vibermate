package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

type exchangeContentRepository struct {
	database   *sql.DB
	operations *operationGate
}

var _ exchangecontent.Repository = (*exchangeContentRepository)(nil)

func newExchangeContentRepository(
	database *sql.DB,
	operations *operationGate,
) *exchangeContentRepository {
	return &exchangeContentRepository{database: database, operations: operations}
}

func (repository *exchangeContentRepository) Put(
	ctx context.Context,
	record exchangecontent.Record,
) error {
	encoded, err := exchangecontent.CanonicalJSON(record)
	if err != nil {
		return err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	_, err = repository.database.ExecContext(
		operation,
		`INSERT INTO runtime_exchange_contents (
		   exchange_id, mode, recorded_at_unix_ms, expires_at_unix_ms, evidence_json
		 ) VALUES (?, ?, ?, ?, ?)`,
		record.ExchangeID,
		string(record.Mode),
		toUnixMillis(record.RecordedAt),
		toUnixMillis(record.ExpiresAt),
		encoded,
	)
	if err != nil {
		return fmt.Errorf("persist Exchange content evidence: %w", err)
	}
	return nil
}

func (repository *exchangeContentRepository) Get(
	ctx context.Context,
	exchangeID string,
	now time.Time,
) (exchangecontent.Record, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return exchangecontent.Record{}, err
	}
	defer finish()
	var mode string
	var recordedMillis, expiresMillis int64
	var encoded []byte
	err = repository.database.QueryRowContext(
		operation,
		`SELECT mode, recorded_at_unix_ms, expires_at_unix_ms, evidence_json
		 FROM runtime_exchange_contents
		 WHERE exchange_id = ? AND expires_at_unix_ms > ?`,
		exchangeID,
		toUnixMillis(now.UTC()),
	).Scan(&mode, &recordedMillis, &expiresMillis, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return exchangecontent.Record{}, exchangecontent.ErrNotFound
	}
	if err != nil {
		return exchangecontent.Record{}, fmt.Errorf("load Exchange content evidence: %w", err)
	}
	record, err := exchangecontent.DecodeCanonicalJSON(encoded)
	if err != nil || record.ExchangeID != exchangeID || string(record.Mode) != mode ||
		toUnixMillis(record.RecordedAt) != recordedMillis ||
		toUnixMillis(record.ExpiresAt) != expiresMillis {
		return exchangecontent.Record{}, exchangecontent.ErrInvalidEvidence
	}
	return record, nil
}

func (repository *exchangeContentRepository) PurgeExpired(
	ctx context.Context,
	now time.Time,
) (uint64, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer finish()
	result, err := repository.database.ExecContext(
		operation,
		`DELETE FROM runtime_exchange_contents WHERE expires_at_unix_ms <= ?`,
		toUnixMillis(now.UTC()),
	)
	if err != nil {
		return 0, fmt.Errorf("purge expired Exchange content evidence: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count < 0 {
		return 0, fmt.Errorf("read purged Exchange content evidence count: %w", err)
	}
	return uint64(count), nil
}
