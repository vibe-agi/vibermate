package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

type runtimeUserRepository struct {
	database   *sql.DB
	operations *operationGate
}

var _ runtimeuser.Repository = (*runtimeUserRepository)(nil)

func newRuntimeUserRepository(
	database *sql.DB,
	operations *operationGate,
) *runtimeUserRepository {
	return &runtimeUserRepository{database: database, operations: operations}
}

func (repository *runtimeUserRepository) CreateUser(
	ctx context.Context,
	record runtimeuser.UserRecord,
) error {
	if record.Validate() != nil {
		return runtimeuser.ErrInvalidUser
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	result, err := repository.database.ExecContext(
		operation,
		`INSERT INTO runtime_users (
		     user_id, username, password_hash, state,
		     created_at_unix_ms, updated_at_unix_ms
		 ) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(username) DO NOTHING`,
		string(record.User.ID), record.User.Username, record.PasswordHash,
		string(record.User.State), toUnixMillis(record.User.CreatedAt),
		toUnixMillis(record.User.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert Runtime User: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Runtime User create result: %w", err)
	}
	if affected == 0 {
		return runtimeuser.ErrUsernameConflict
	}
	return nil
}

func (repository *runtimeUserRepository) FindUserByUsername(
	ctx context.Context,
	username string,
) (runtimeuser.UserRecord, bool, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return runtimeuser.UserRecord{}, false, err
	}
	defer finish()
	record, err := scanRuntimeUser(repository.database.QueryRowContext(
		operation,
		`SELECT user_id, username, password_hash, state,
		        created_at_unix_ms, updated_at_unix_ms
		   FROM runtime_users
		  WHERE username = ?`,
		username,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return runtimeuser.UserRecord{}, false, nil
	}
	if err != nil {
		return runtimeuser.UserRecord{}, false, fmt.Errorf("find Runtime User: %w", err)
	}
	return record, true, nil
}

func (repository *runtimeUserRepository) ListUsers(
	ctx context.Context,
) ([]runtimeuser.UserRecord, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	rows, err := repository.database.QueryContext(
		operation,
		`SELECT user_id, username, password_hash, state,
		        created_at_unix_ms, updated_at_unix_ms
		   FROM runtime_users
		  ORDER BY username ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list Runtime Users: %w", err)
	}
	defer rows.Close()
	records := make([]runtimeuser.UserRecord, 0)
	for rows.Next() {
		record, scanErr := scanRuntimeUser(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan Runtime User: %w", scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Runtime Users: %w", err)
	}
	return records, nil
}

func (repository *runtimeUserRepository) SetUserState(
	ctx context.Context,
	id runtimeuser.UserID,
	state runtimeuser.State,
	updatedAt time.Time,
) (runtimeuser.UserRecord, bool, error) {
	if !id.Valid() || (state != runtimeuser.StateActive && state != runtimeuser.StateDisabled) {
		return runtimeuser.UserRecord{}, false, runtimeuser.ErrInvalidUser
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return runtimeuser.UserRecord{}, false, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return runtimeuser.UserRecord{}, false, fmt.Errorf("begin Runtime User state update: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	result, err := transaction.ExecContext(
		operation,
		`UPDATE runtime_users
		    SET state = ?, updated_at_unix_ms = ?
		  WHERE user_id = ?`,
		string(state), toUnixMillis(updatedAt), string(id),
	)
	if err != nil {
		return runtimeuser.UserRecord{}, false, fmt.Errorf("update Runtime User state: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return runtimeuser.UserRecord{}, false, fmt.Errorf("read Runtime User state update: %w", err)
	}
	if affected == 0 {
		return runtimeuser.UserRecord{}, false, nil
	}
	if state == runtimeuser.StateDisabled {
		if _, err := transaction.ExecContext(
			operation,
			`UPDATE runtime_user_login_sessions
			    SET revoked_at_unix_ms = COALESCE(revoked_at_unix_ms, ?)
			  WHERE user_id = ?`,
			toUnixMillis(updatedAt), string(id),
		); err != nil {
			return runtimeuser.UserRecord{}, false,
				fmt.Errorf("revoke disabled Runtime User sessions: %w", err)
		}
	}
	record, err := scanRuntimeUser(transaction.QueryRowContext(
		operation,
		`SELECT user_id, username, password_hash, state,
		        created_at_unix_ms, updated_at_unix_ms
		   FROM runtime_users
		  WHERE user_id = ?`,
		string(id),
	))
	if err != nil {
		return runtimeuser.UserRecord{}, false, fmt.Errorf("read updated Runtime User: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return runtimeuser.UserRecord{}, false, fmt.Errorf("commit Runtime User state update: %w", err)
	}
	return record, true, nil
}

func (repository *runtimeUserRepository) CreateSession(
	ctx context.Context,
	record runtimeuser.SessionRecord,
) error {
	if record.Validate() != nil {
		return runtimeuser.ErrInvalidSession
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	_, err = repository.database.ExecContext(
		operation,
		`INSERT INTO runtime_user_login_sessions (
		     session_id, user_id, token_digest, machine_id, device_name,
		     created_at_unix_ms, expires_at_unix_ms, revoked_at_unix_ms
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, NULL)`,
		record.ID, string(record.UserID), record.TokenDigest[:],
		record.MachineID.String(), record.DeviceName,
		toUnixMillis(record.CreatedAt), toUnixMillis(record.ExpiresAt),
	)
	if err != nil {
		return fmt.Errorf("insert Runtime User Login Session: %w", err)
	}
	return nil
}

func (repository *runtimeUserRepository) FindSession(
	ctx context.Context,
	digest runtimeuser.SessionDigest,
) (runtimeuser.SessionRecord, runtimeuser.UserRecord, bool, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return runtimeuser.SessionRecord{}, runtimeuser.UserRecord{}, false, err
	}
	defer finish()
	var (
		sessionID, userID, machineID, deviceName string
		tokenDigest                              []byte
		createdAt, expiresAt                     int64
		revokedAt                                sql.NullInt64
		username, passwordHash, state            string
		userCreatedAt, userUpdatedAt             int64
	)
	err = repository.database.QueryRowContext(
		operation,
		`SELECT s.session_id, s.user_id, s.token_digest, s.machine_id,
		        s.device_name, s.created_at_unix_ms, s.expires_at_unix_ms,
		        s.revoked_at_unix_ms, u.username, u.password_hash, u.state,
		        u.created_at_unix_ms, u.updated_at_unix_ms
		   FROM runtime_user_login_sessions AS s
		   JOIN runtime_users AS u ON u.user_id = s.user_id
		  WHERE s.token_digest = ?`,
		digest[:],
	).Scan(
		&sessionID, &userID, &tokenDigest, &machineID, &deviceName,
		&createdAt, &expiresAt, &revokedAt, &username, &passwordHash, &state,
		&userCreatedAt, &userUpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return runtimeuser.SessionRecord{}, runtimeuser.UserRecord{}, false, nil
	}
	if err != nil {
		return runtimeuser.SessionRecord{}, runtimeuser.UserRecord{}, false,
			fmt.Errorf("find Runtime User Login Session: %w", err)
	}
	if len(tokenDigest) != len(digest) {
		return runtimeuser.SessionRecord{}, runtimeuser.UserRecord{}, false,
			errors.New("stored Runtime User Login Session digest is invalid")
	}
	var storedDigest runtimeuser.SessionDigest
	copy(storedDigest[:], tokenDigest)
	parsedMachineID, err := workspaceidentity.ParseMachineID(machineID)
	if err != nil {
		return runtimeuser.SessionRecord{}, runtimeuser.UserRecord{}, false, err
	}
	session := runtimeuser.SessionRecord{
		ID: runtimeuser.LoginSessionID(sessionID), UserID: runtimeuser.UserID(userID),
		TokenDigest: storedDigest,
		MachineID:   parsedMachineID, DeviceName: deviceName,
		CreatedAt: fromUnixMillis(createdAt), ExpiresAt: fromUnixMillis(expiresAt),
	}
	if revokedAt.Valid {
		session.RevokedAt = fromUnixMillis(revokedAt.Int64)
	}
	user := runtimeuser.UserRecord{
		User: runtimeuser.User{
			ID: runtimeuser.UserID(userID), Username: username,
			State: runtimeuser.State(state), CreatedAt: fromUnixMillis(userCreatedAt),
			UpdatedAt: fromUnixMillis(userUpdatedAt),
		},
		PasswordHash: passwordHash,
	}
	if session.Validate() != nil || user.Validate() != nil {
		return runtimeuser.SessionRecord{}, runtimeuser.UserRecord{}, false,
			errors.New("stored Runtime User Login Session is invalid")
	}
	return session, user, true, nil
}

func (repository *runtimeUserRepository) RevokeSession(
	ctx context.Context,
	digest runtimeuser.SessionDigest,
	revokedAt time.Time,
) (bool, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return false, err
	}
	defer finish()
	result, err := repository.database.ExecContext(
		operation,
		`UPDATE runtime_user_login_sessions
		    SET revoked_at_unix_ms = ?
		  WHERE token_digest = ? AND revoked_at_unix_ms IS NULL`,
		toUnixMillis(revokedAt), digest[:],
	)
	if err != nil {
		return false, fmt.Errorf("revoke Runtime User Login Session: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read Runtime User Login Session revoke result: %w", err)
	}
	return affected == 1, nil
}

type runtimeUserScanner interface{ Scan(...any) error }

func scanRuntimeUser(scanner runtimeUserScanner) (runtimeuser.UserRecord, error) {
	var userID, username, passwordHash, state string
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&userID, &username, &passwordHash, &state, &createdAt, &updatedAt,
	); err != nil {
		return runtimeuser.UserRecord{}, err
	}
	record := runtimeuser.UserRecord{
		User: runtimeuser.User{
			ID: runtimeuser.UserID(userID), Username: username,
			State: runtimeuser.State(state), CreatedAt: fromUnixMillis(createdAt),
			UpdatedAt: fromUnixMillis(updatedAt),
		},
		PasswordHash: passwordHash,
	}
	if record.Validate() != nil {
		return runtimeuser.UserRecord{}, errors.New("stored Runtime User is invalid")
	}
	return record, nil
}
