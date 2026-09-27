package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

const usageSchemaSQL = `
CREATE TABLE runtime_usage_schema_metadata(singleton INTEGER PRIMARY KEY CHECK(singleton=1), source_sha256 TEXT NOT NULL) STRICT;
CREATE TABLE runtime_usage_policy(
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
 retention_days INTEGER NOT NULL CHECK(retention_days BETWEEN 1 AND 365),
 revision INTEGER NOT NULL CHECK(revision>0), collecting_since_unix_ms INTEGER
) STRICT;
INSERT INTO runtime_usage_policy VALUES(1,0,90,1,NULL);
CREATE TABLE runtime_usage_observations(
 exchange_id TEXT PRIMARY KEY NOT NULL, capture_run_id TEXT NOT NULL, manual_capture_id TEXT NOT NULL,
 runtime_user_id TEXT NOT NULL, occurred_at_unix_ms INTEGER NOT NULL, expires_at_unix_ms INTEGER NOT NULL,
 observation_json TEXT NOT NULL CHECK(json_valid(observation_json) AND length(observation_json)<=16384)
) STRICT;
CREATE INDEX runtime_usage_period ON runtime_usage_observations(occurred_at_unix_ms, exchange_id);
CREATE INDEX runtime_usage_user_period ON runtime_usage_observations(runtime_user_id, occurred_at_unix_ms, exchange_id);
CREATE INDEX runtime_usage_expiry ON runtime_usage_observations(expires_at_unix_ms);
CREATE INDEX runtime_usage_capture ON runtime_usage_observations(capture_run_id);
CREATE INDEX runtime_usage_manual ON runtime_usage_observations(manual_capture_id);
`

func initializeUsageSchema(ctx context.Context, database *sql.DB) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	digest := sha256.Sum256([]byte(usageSchemaSQL))
	expected := hex.EncodeToString(digest[:])
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name='runtime_usage_schema_metadata'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := tx.ExecContext(ctx, usageSchemaSQL); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_usage_schema_metadata VALUES(1,?)`, expected); err != nil {
			return err
		}
	} else {
		var actual string
		if err := tx.QueryRowContext(ctx, `SELECT source_sha256 FROM runtime_usage_schema_metadata WHERE singleton=1`).Scan(&actual); err != nil || actual != expected {
			return errors.New("unsupported usage schema extension")
		}
	}
	return tx.Commit()
}

// The same repository is shared by every control surface of this Runtime.
func (store *Store) UsageRepository() runtimeusage.Repository { return store }

func (store *Store) UsagePolicy(ctx context.Context) (runtimeusage.CollectionPolicy, error) {
	operation, finish, err := store.operations.begin(ctx)
	if err != nil {
		return runtimeusage.CollectionPolicy{}, err
	}
	defer finish()
	return scanUsagePolicy(store.database.QueryRowContext(operation, `SELECT enabled,retention_days,revision,collecting_since_unix_ms FROM runtime_usage_policy WHERE singleton=1`))
}

func scanUsagePolicy(row *sql.Row) (runtimeusage.CollectionPolicy, error) {
	var policy runtimeusage.CollectionPolicy
	var since sql.NullInt64
	err := row.Scan(&policy.Enabled, &policy.RetentionDays, &policy.Revision, &since)
	if since.Valid {
		value := time.UnixMilli(since.Int64).UTC()
		policy.CollectingSince = &value
	}
	return policy, err
}

func (store *Store) SetUsagePolicy(ctx context.Context, policy runtimeusage.CollectionPolicy, now time.Time) (runtimeusage.CollectionPolicy, error) {
	if err := policy.Validate(); err != nil {
		return runtimeusage.CollectionPolicy{}, err
	}
	if now.IsZero() {
		return runtimeusage.CollectionPolicy{}, errors.New("usage clock is missing")
	}
	operation, finish, err := store.operations.begin(ctx)
	if err != nil {
		return runtimeusage.CollectionPolicy{}, err
	}
	defer finish()
	tx, err := store.database.BeginTx(operation, nil)
	if err != nil {
		return runtimeusage.CollectionPolicy{}, err
	}
	defer tx.Rollback()
	since := now.Truncate(time.Millisecond)
	if since.Before(now) {
		since = since.Add(time.Millisecond)
	}
	result, err := tx.ExecContext(operation, `UPDATE runtime_usage_policy SET enabled=?,retention_days=?,revision=revision+1,
 collecting_since_unix_ms=CASE WHEN enabled=0 AND ?=1 THEN ? ELSE collecting_since_unix_ms END WHERE singleton=1 AND revision=?`,
		policy.Enabled, policy.RetentionDays, policy.Enabled, since.UnixMilli(), policy.Revision)
	if err != nil {
		return runtimeusage.CollectionPolicy{}, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return runtimeusage.CollectionPolicy{}, runtimeusage.ErrPolicyConflict
	}
	// Shorter retention applies immediately. Lengthening never resurrects or
	// extends records collected under a shorter consented retention period.
	if _, err := tx.ExecContext(operation, `UPDATE runtime_usage_observations SET expires_at_unix_ms=min(expires_at_unix_ms,occurred_at_unix_ms+?)`, int64(policy.RetentionDays)*86400000); err != nil {
		return runtimeusage.CollectionPolicy{}, err
	}
	if _, err := tx.ExecContext(operation, `DELETE FROM runtime_usage_observations WHERE expires_at_unix_ms<=?`, now.UnixMilli()); err != nil {
		return runtimeusage.CollectionPolicy{}, err
	}
	updated, err := scanUsagePolicy(tx.QueryRowContext(operation, `SELECT enabled,retention_days,revision,collecting_since_unix_ms FROM runtime_usage_policy WHERE singleton=1`))
	if err != nil {
		return runtimeusage.CollectionPolicy{}, err
	}
	if err := tx.Commit(); err != nil {
		return runtimeusage.CollectionPolicy{}, err
	}
	return updated, nil
}

func (store *Store) RecordUsage(ctx context.Context, value runtimeusage.Observation) error {
	if err := value.Validate(); err != nil {
		return err
	}
	operation, finish, err := store.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	tx, err := store.database.BeginTx(operation, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	policy, err := scanUsagePolicy(tx.QueryRowContext(operation, `SELECT enabled,retention_days,revision,collecting_since_unix_ms FROM runtime_usage_policy WHERE singleton=1`))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(operation, `DELETE FROM runtime_usage_observations WHERE expires_at_unix_ms<=?`, value.OccurredAt.UnixMilli()); err != nil {
		return err
	}
	if !policy.Enabled || policy.CollectingSince == nil || value.StartedAt.Before(*policy.CollectingSince) {
		return nil
	}
	value.UserID, value.Source = "", "proxy"
	value.Attribution = &runtimeusage.Attribution{}
	if value.CaptureRunID != "" {
		var user, username sql.NullString
		var localUser, machineID, gitJSON string
		err := tx.QueryRowContext(operation, `SELECT runtime_user_id,runtime_username,local_user_label,machine_id,
		 COALESCE((SELECT git_json FROM capture_run_projects p WHERE p.run_id=capture_runs.run_id),'')
		 FROM capture_runs WHERE run_id=?`, value.CaptureRunID).Scan(&user, &username, &localUser, &machineID, &gitJSON)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		} // A deleted Capture must not reappear through a late callback.
		if err != nil {
			return err
		}
		value.UserID, value.Source = runtimeuser.UserID(user.String), "local"
		if value.UserID != "" {
			value.Source = "member"
		}
		run := capturerun.View{RuntimeUserID: value.UserID, RuntimeUsername: username.String, LocalUserLabel: localUser, MachineID: machineID}
		if gitJSON != "" {
			var git capturerun.GitSnapshot
			if err := json.Unmarshal([]byte(gitJSON), &git); err != nil {
				return err
			}
			if err := git.Validate(); err != nil {
				return err
			}
			run.Runtime.GitAtLaunch = &git
		}
		value.Attribution = runtimeusage.CaptureAttribution(run)
	} else if value.ManualCaptureID != "" {
		var exists int
		if err := tx.QueryRowContext(operation, `SELECT count(*) FROM manual_captures WHERE capture_id=?`, value.ManualCaptureID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return nil
		}
		value.Source = "manual"
	}
	// Snapshot display labels, not credentials or user notes, so account/profile
	// renames do not relabel past traffic. IDs remain the grouping key.
	if err := tx.QueryRowContext(operation, `SELECT COALESCE((SELECT name FROM environment_revisions WHERE environment_id=? AND revision=?),'')`, value.EnvironmentID, value.EnvironmentRevision).Scan(&value.EnvironmentName); err != nil {
		return err
	}
	if err := tx.QueryRowContext(operation, `SELECT COALESCE((SELECT display_name FROM provider_accounts WHERE account_id=?),'')`, value.AccountID).Scan(&value.AccountName); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(operation, `INSERT INTO runtime_usage_observations VALUES(?,?,?,?,?,?,?) ON CONFLICT(exchange_id) DO NOTHING`,
		value.ExchangeID, value.CaptureRunID, value.ManualCaptureID, value.UserID, value.OccurredAt.UnixMilli(), value.OccurredAt.Add(time.Duration(policy.RetentionDays)*24*time.Hour).UnixMilli(), string(data)); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) ListUsage(ctx context.Context, query runtimeusage.Query, userID runtimeuser.UserID, now time.Time, limit int) ([]runtimeusage.Observation, bool, error) {
	from, until := query.Bounds()
	if from.IsZero() || !until.After(from) || now.IsZero() || limit < 1 || limit > 100000 || (userID != "" && !userID.Valid()) {
		return nil, false, errors.New("invalid usage query")
	}
	operation, finish, err := store.operations.begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer finish()
	statement := `SELECT observation_json FROM runtime_usage_observations WHERE occurred_at_unix_ms>=? AND occurred_at_unix_ms<? AND expires_at_unix_ms>?`
	if _, err := store.database.ExecContext(operation, `DELETE FROM runtime_usage_observations WHERE expires_at_unix_ms<=?`, now.UnixMilli()); err != nil {
		return nil, false, err
	}
	args := []any{from.UnixMilli(), until.UnixMilli(), now.UnixMilli()}
	// Filter ownership before limiting, so members cannot lose their history to
	// another member's traffic or learn anything about that traffic's volume.
	if userID != "" {
		statement += ` AND runtime_user_id=?`
		args = append(args, userID)
	}
	statement += ` ORDER BY occurred_at_unix_ms DESC,exchange_id LIMIT ?`
	args = append(args, limit+1)
	rows, err := store.database.QueryContext(operation, statement, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := []runtimeusage.Observation{}
	for rows.Next() {
		if len(result) == limit {
			return result, true, nil
		}
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, false, err
		}
		var value runtimeusage.Observation
		if err := json.Unmarshal([]byte(data), &value); err != nil {
			return nil, false, err
		}
		if err := value.Validate(); err != nil {
			return nil, false, err
		}
		result = append(result, value)
	}
	return result, false, rows.Err()
}
