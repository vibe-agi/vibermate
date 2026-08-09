package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

const manualCaptureColumns = `
	capture_id,
	owner_kind,
	owner_id,
	display_name,
	client_class,
	lifetime,
	state,
	credential_revision,
	proxy_credential_hash,
	observation,
	created_at_unix_ms,
	updated_at_unix_ms,
	expires_at_unix_ms,
	last_observed_at_unix_ms`

type manualCaptureRepository struct {
	database   *sql.DB
	operations *operationGate
}

var _ manualcapture.Repository = (*manualCaptureRepository)(nil)

func newManualCaptureRepository(
	database *sql.DB,
	operations *operationGate,
) *manualCaptureRepository {
	return &manualCaptureRepository{database: database, operations: operations}
}

func (repository *manualCaptureRepository) Create(
	ctx context.Context,
	record manualcapture.DurableRecord,
) error {
	if err := record.Validate(); err != nil {
		return err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	ownerID, _ := record.Owner.ProxyClientBindingID()
	result, err := repository.database.ExecContext(
		operation,
		`INSERT INTO manual_captures (
		     capture_id,
		     owner_kind,
		     owner_id,
		     display_name,
		     client_class,
		     lifetime,
		     state,
		     credential_revision,
		     proxy_credential_hash,
		     observation,
		     created_at_unix_ms,
		     updated_at_unix_ms,
		     expires_at_unix_ms,
		     last_observed_at_unix_ms
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT DO NOTHING`,
		record.ID.String(),
		string(record.Owner.Kind()),
		ownerID,
		record.DisplayName,
		string(record.ClientClass),
		string(record.Lifetime),
		string(record.State),
		int64(record.CredentialRevision),
		record.ProxyCredentialHash[:],
		string(record.Observation),
		toUnixMillis(record.CreatedAt),
		toUnixMillis(record.UpdatedAt),
		nullableUnixMillis(record.ExpiresAt),
		nullableUnixMillis(record.LastObservedAt),
	)
	if err != nil {
		return fmt.Errorf("insert ManualCapture: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read ManualCapture insert result: %w", err)
	}
	if affected != 1 {
		return manualcapture.ErrStateConflict
	}
	return nil
}

func (repository *manualCaptureRepository) Rotate(
	ctx context.Context,
	owner manualcapture.OwnerScope,
	id manualcapture.ID,
	expected manualcapture.CredentialRevision,
	digest manualcapture.CredentialDigest,
	now time.Time,
) (manualcapture.DurableRecord, error) {
	if !owner.Valid() || !id.Valid() || !expected.Valid() || !digest.Valid() || now.IsZero() {
		return manualcapture.DurableRecord{}, manualcapture.ErrInvalidCommand
	}
	if expected == manualcapture.MaxCredentialRevision {
		return manualcapture.DurableRecord{}, manualcapture.ErrRevisionConflict
	}
	return repository.mutate(
		ctx,
		owner,
		id,
		expected,
		now,
		false,
		manualcapture.ErrStateConflict,
		func(operation context.Context, transaction *sql.Tx) (sql.Result, error) {
			return transaction.ExecContext(
				operation,
				`UPDATE OR IGNORE manual_captures
				 SET proxy_credential_hash = ?,
				     credential_revision = credential_revision + 1,
				     observation = 'waiting_for_traffic',
				     last_observed_at_unix_ms = NULL,
				     updated_at_unix_ms = ?
				 WHERE capture_id = ?
				   AND owner_kind = ?
				   AND owner_id = ?
				   AND state = 'active'
				   AND credential_revision = ?
				   AND credential_revision < 9223372036854775807`,
				digest[:],
				toUnixMillis(now),
				id.String(),
				string(owner.Kind()),
				ownerStorageID(owner),
				int64(expected),
			)
		},
	)
}

func (repository *manualCaptureRepository) Revoke(
	ctx context.Context,
	owner manualcapture.OwnerScope,
	id manualcapture.ID,
	expected manualcapture.CredentialRevision,
	now time.Time,
) (manualcapture.DurableRecord, error) {
	if !owner.Valid() || !id.Valid() || !expected.Valid() || now.IsZero() {
		return manualcapture.DurableRecord{}, manualcapture.ErrInvalidCommand
	}
	return repository.mutate(
		ctx,
		owner,
		id,
		expected,
		now,
		true,
		manualcapture.ErrNotActive,
		func(operation context.Context, transaction *sql.Tx) (sql.Result, error) {
			return transaction.ExecContext(
				operation,
				`UPDATE manual_captures
				 SET state = 'revoked', updated_at_unix_ms = ?
				 WHERE capture_id = ?
				   AND owner_kind = ?
				   AND owner_id = ?
				   AND state = 'active'
				   AND credential_revision = ?`,
				toUnixMillis(now),
				id.String(),
				string(owner.Kind()),
				ownerStorageID(owner),
				int64(expected),
			)
		},
	)
}

type manualCaptureMutation func(context.Context, *sql.Tx) (sql.Result, error)

func (repository *manualCaptureRepository) mutate(
	ctx context.Context,
	owner manualcapture.OwnerScope,
	id manualcapture.ID,
	expected manualcapture.CredentialRevision,
	now time.Time,
	revokedIsSuccess bool,
	activeNoChange error,
	mutation manualCaptureMutation,
) (manualcapture.DurableRecord, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return manualcapture.DurableRecord{}, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return manualcapture.DurableRecord{}, fmt.Errorf("begin ManualCapture mutation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := expireOwnedManualCaptures(
		operation,
		transaction,
		now,
		owner,
		id.String(),
	); err != nil {
		return manualcapture.DurableRecord{}, err
	}
	result, err := mutation(operation, transaction)
	if err != nil {
		return manualcapture.DurableRecord{}, fmt.Errorf("mutate ManualCapture: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return manualcapture.DurableRecord{}, fmt.Errorf("read ManualCapture mutation result: %w", err)
	}
	if affected != 1 {
		current, readErr := scanManualCapture(transaction.QueryRowContext(
			operation,
			`SELECT `+manualCaptureColumns+`
			 FROM manual_captures
			 WHERE capture_id = ? AND owner_kind = ? AND owner_id = ?`,
			id.String(),
			string(owner.Kind()),
			ownerStorageID(owner),
		))
		if errors.Is(readErr, sql.ErrNoRows) {
			return manualcapture.DurableRecord{}, manualcapture.ErrNotFound
		}
		if readErr != nil {
			return manualcapture.DurableRecord{}, fmt.Errorf("read rejected ManualCapture mutation: %w", readErr)
		}
		var outcome error
		switch {
		case current.CredentialRevision != expected:
			outcome = manualcapture.ErrRevisionConflict
		case revokedIsSuccess && current.State == manualcapture.StateRevoked:
			outcome = nil
		case current.State == manualcapture.StateActive:
			outcome = activeNoChange
		default:
			outcome = manualcapture.ErrNotActive
		}
		if err := transaction.Commit(); err != nil {
			return manualcapture.DurableRecord{}, fmt.Errorf("commit rejected ManualCapture mutation: %w", err)
		}
		return current, outcome
	}
	record, err := scanManualCapture(transaction.QueryRowContext(
		operation,
		`SELECT `+manualCaptureColumns+` FROM manual_captures WHERE capture_id = ?`,
		id.String(),
	))
	if err != nil {
		return manualcapture.DurableRecord{}, fmt.Errorf("read mutated ManualCapture: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return manualcapture.DurableRecord{}, fmt.Errorf("commit ManualCapture mutation: %w", err)
	}
	return record, nil
}

func (repository *manualCaptureRepository) AuthorizeProxy(
	ctx context.Context,
	digest manualcapture.CredentialDigest,
	now time.Time,
) (manualcapture.DurableRecord, error) {
	if !digest.Valid() || now.IsZero() {
		return manualcapture.DurableRecord{}, manualcapture.ErrCredentialRejected
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return manualcapture.DurableRecord{}, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return manualcapture.DurableRecord{}, fmt.Errorf("begin ManualCapture authorization: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	expireResult, err := transaction.ExecContext(
		operation,
		`UPDATE manual_captures
		 SET state = 'expired', updated_at_unix_ms = ?
		 WHERE proxy_credential_hash = ?
		   AND state = 'active'
		   AND lifetime = 'temporary'
		   AND expires_at_unix_ms <= ?`,
		toUnixMillis(now),
		digest[:],
		toUnixMillis(now),
	)
	if err != nil {
		return manualcapture.DurableRecord{}, fmt.Errorf("expire ManualCapture authorization: %w", err)
	}
	expired, err := expireResult.RowsAffected()
	if err != nil {
		return manualcapture.DurableRecord{}, fmt.Errorf("read ManualCapture expiry result: %w", err)
	}
	result, err := transaction.ExecContext(
		operation,
		`UPDATE manual_captures
		 SET observation = 'observed',
		     last_observed_at_unix_ms = ?,
		     updated_at_unix_ms = ?
		 WHERE proxy_credential_hash = ?
		   AND state = 'active'
		   AND (lifetime = 'until_revoked' OR expires_at_unix_ms > ?)`,
		toUnixMillis(now),
		toUnixMillis(now),
		digest[:],
		toUnixMillis(now),
	)
	if err != nil {
		return manualcapture.DurableRecord{}, fmt.Errorf("observe ManualCapture authorization: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return manualcapture.DurableRecord{}, fmt.Errorf("read ManualCapture authorization result: %w", err)
	}
	if affected != 1 {
		if expired > 0 {
			if err := transaction.Commit(); err != nil {
				return manualcapture.DurableRecord{}, fmt.Errorf("commit ManualCapture expiry: %w", err)
			}
		}
		return manualcapture.DurableRecord{}, manualcapture.ErrCredentialRejected
	}
	record, err := scanManualCapture(transaction.QueryRowContext(
		operation,
		`SELECT `+manualCaptureColumns+`
		 FROM manual_captures WHERE proxy_credential_hash = ?`,
		digest[:],
	))
	if err != nil {
		return manualcapture.DurableRecord{}, fmt.Errorf("read authorized ManualCapture: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return manualcapture.DurableRecord{}, fmt.Errorf("commit ManualCapture authorization: %w", err)
	}
	return record, nil
}

func (repository *manualCaptureRepository) Get(
	ctx context.Context,
	owner manualcapture.OwnerScope,
	id manualcapture.ID,
	now time.Time,
) (manualcapture.DurableRecord, error) {
	if !owner.Valid() || !id.Valid() || now.IsZero() {
		return manualcapture.DurableRecord{}, manualcapture.ErrInvalidCommand
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return manualcapture.DurableRecord{}, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return manualcapture.DurableRecord{}, fmt.Errorf("begin ManualCapture read: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := expireOwnedManualCaptures(
		operation,
		transaction,
		now,
		owner,
		id.String(),
	); err != nil {
		return manualcapture.DurableRecord{}, err
	}
	record, err := scanManualCapture(transaction.QueryRowContext(
		operation,
		`SELECT `+manualCaptureColumns+`
		 FROM manual_captures
		 WHERE capture_id = ? AND owner_kind = ? AND owner_id = ?`,
		id.String(),
		string(owner.Kind()),
		ownerStorageID(owner),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return manualcapture.DurableRecord{}, manualcapture.ErrNotFound
	}
	if err != nil {
		return manualcapture.DurableRecord{}, fmt.Errorf("get ManualCapture: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return manualcapture.DurableRecord{}, fmt.Errorf("commit ManualCapture read: %w", err)
	}
	return record, nil
}

func (repository *manualCaptureRepository) Active(
	ctx context.Context,
	id manualcapture.ID,
	now time.Time,
) (bool, error) {
	if !id.Valid() || now.IsZero() {
		return false, manualcapture.ErrInvalidCommand
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return false, err
	}
	defer finish()
	var state manualcapture.State
	var expiresAtUnixMillis sql.NullInt64
	err = repository.database.QueryRowContext(
		operation,
		`SELECT state, expires_at_unix_ms
		 FROM manual_captures WHERE capture_id = ?`,
		id.String(),
	).Scan(&state, &expiresAtUnixMillis)
	if errors.Is(err, sql.ErrNoRows) {
		return false, manualcapture.ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("read ManualCapture activity: %w", err)
	}
	switch state {
	case manualcapture.StateActive:
		return !expiresAtUnixMillis.Valid ||
			expiresAtUnixMillis.Int64 > toUnixMillis(now), nil
	case manualcapture.StateRevoked, manualcapture.StateExpired:
		return false, nil
	default:
		return false, manualcapture.ErrInvalidRecord
	}
}

func (repository *manualCaptureRepository) List(
	ctx context.Context,
	request manualcapture.PageRequest,
	now time.Time,
) ([]manualcapture.DurableRecord, error) {
	request = request.Normalized()
	if !request.Owner.Valid() || now.IsZero() {
		return nil, manualcapture.ErrInvalidCommand
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return nil, fmt.Errorf("begin ManualCapture list: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := expireOwnedManualCaptures(
		operation,
		transaction,
		now,
		request.Owner,
		"",
	); err != nil {
		return nil, err
	}
	rows, err := transaction.QueryContext(
		operation,
		`SELECT `+manualCaptureColumns+`
		 FROM manual_captures
		 WHERE owner_kind = ? AND owner_id = ?
		 ORDER BY updated_at_unix_ms DESC, capture_id ASC
		 LIMIT ?`,
		string(request.Owner.Kind()),
		ownerStorageID(request.Owner),
		request.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list ManualCaptures: %w", err)
	}
	defer rows.Close()
	records := make([]manualcapture.DurableRecord, 0, request.Limit)
	for rows.Next() {
		record, scanErr := scanManualCapture(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan ManualCapture list: %w", scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ManualCapture list: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("commit ManualCapture list: %w", err)
	}
	return records, nil
}

func (repository *manualCaptureRepository) Recover(
	ctx context.Context,
	now time.Time,
) (manualcapture.Recovery, error) {
	if now.IsZero() {
		return manualcapture.Recovery{}, manualcapture.ErrInvalidCommand
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return manualcapture.Recovery{}, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return manualcapture.Recovery{}, fmt.Errorf("begin ManualCapture recovery: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	result, err := transaction.ExecContext(
		operation,
		`UPDATE manual_captures
		 SET state = 'expired', updated_at_unix_ms = ?
		 WHERE state = 'active'
		   AND lifetime = 'temporary'
		   AND expires_at_unix_ms <= ?`,
		toUnixMillis(now),
		toUnixMillis(now),
	)
	if err != nil {
		return manualcapture.Recovery{}, fmt.Errorf("expire recovered ManualCaptures: %w", err)
	}
	expired, err := result.RowsAffected()
	if err != nil {
		return manualcapture.Recovery{}, fmt.Errorf("count expired ManualCaptures: %w", err)
	}
	var active int
	if err := transaction.QueryRowContext(
		operation,
		`SELECT COUNT(*) FROM manual_captures WHERE state = 'active'`,
	).Scan(&active); err != nil {
		return manualcapture.Recovery{}, fmt.Errorf("count active ManualCaptures: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return manualcapture.Recovery{}, fmt.Errorf("commit ManualCapture recovery: %w", err)
	}
	return manualcapture.Recovery{ExpiredCount: int(expired), ActiveCount: active}, nil
}

func expireOwnedManualCaptures(
	ctx context.Context,
	transaction *sql.Tx,
	now time.Time,
	owner manualcapture.OwnerScope,
	id string,
) error {
	query := `UPDATE manual_captures
	 SET state = 'expired', updated_at_unix_ms = ?
	 WHERE state = 'active'
	   AND lifetime = 'temporary'
	   AND expires_at_unix_ms <= ?
	   AND owner_kind = ?
	   AND owner_id = ?`
	arguments := []any{
		toUnixMillis(now),
		toUnixMillis(now),
		string(owner.Kind()),
		ownerStorageID(owner),
	}
	if id != "" {
		query += ` AND capture_id = ?`
		arguments = append(arguments, id)
	}
	if _, err := transaction.ExecContext(ctx, query, arguments...); err != nil {
		return fmt.Errorf("expire ManualCapture: %w", err)
	}
	return nil
}

type manualCaptureScanner interface {
	Scan(...any) error
}

func scanManualCapture(
	scanner manualCaptureScanner,
) (manualcapture.DurableRecord, error) {
	var (
		record                        manualcapture.DurableRecord
		captureID, ownerKind, ownerID string
		clientClass, lifetime, state  string
		observation                   string
		credentialRevision            int64
		credentialHash                []byte
		createdAt, updatedAt          int64
		expiresAt, lastObservedAt     sql.NullInt64
	)
	if err := scanner.Scan(
		&captureID,
		&ownerKind,
		&ownerID,
		&record.DisplayName,
		&clientClass,
		&lifetime,
		&state,
		&credentialRevision,
		&credentialHash,
		&observation,
		&createdAt,
		&updatedAt,
		&expiresAt,
		&lastObservedAt,
	); err != nil {
		return manualcapture.DurableRecord{}, err
	}
	id, err := manualcapture.ParseID(captureID)
	if err != nil {
		return manualcapture.DurableRecord{}, err
	}
	owner, err := manualcapture.RestoreOwnerScope(
		manualcapture.OwnerKind(ownerKind),
		ownerID,
	)
	if err != nil {
		return manualcapture.DurableRecord{}, err
	}
	if credentialRevision <= 0 || len(credentialHash) != sha256Size {
		return manualcapture.DurableRecord{}, manualcapture.ErrInvalidRecord
	}
	record.ID = id
	record.Owner = owner
	record.ClientClass = manualcapture.ClientClass(clientClass)
	record.Lifetime = manualcapture.Lifetime(lifetime)
	record.State = manualcapture.State(state)
	record.CredentialRevision = manualcapture.CredentialRevision(credentialRevision)
	copy(record.ProxyCredentialHash[:], credentialHash)
	record.Observation = manualcapture.Observation(observation)
	record.CreatedAt = fromUnixMillis(createdAt)
	record.UpdatedAt = fromUnixMillis(updatedAt)
	if expiresAt.Valid {
		record.ExpiresAt = fromUnixMillis(expiresAt.Int64)
	}
	if lastObservedAt.Valid {
		record.LastObservedAt = fromUnixMillis(lastObservedAt.Int64)
	}
	if err := record.Validate(); err != nil {
		return manualcapture.DurableRecord{}, err
	}
	return record, nil
}

func ownerStorageID(owner manualcapture.OwnerScope) string {
	value, _ := owner.ProxyClientBindingID()
	return value
}

func nullableUnixMillis(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return toUnixMillis(value)
}

const sha256Size = 32
