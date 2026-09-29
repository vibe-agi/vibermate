package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

// The same repository is shared by every control surface of this Runtime.
func (store *Store) UsageRepository() runtimeusage.Repository { return store }

func (store *Store) UsagePolicy(ctx context.Context) (runtimeusage.CollectionPolicy, error) {
	operation, finish, err := store.operations.begin(ctx)
	if err != nil {
		return runtimeusage.CollectionPolicy{}, err
	}
	defer finish()
	return scanUsagePolicy(store.reads.QueryRowContext(operation, `SELECT enabled,retention_days,revision,collecting_since_unix_ms FROM runtime_usage_policy WHERE singleton=1`))
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
	current, err := scanUsagePolicy(tx.QueryRowContext(operation, `SELECT enabled,retention_days,revision,collecting_since_unix_ms FROM runtime_usage_policy WHERE singleton=1`))
	if err != nil {
		return runtimeusage.CollectionPolicy{}, err
	}
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
	if policy.RetentionDays < current.RetentionDays {
		// The new cap dominates every longer cap over older sequences. One row
		// per duration bounds pending work even if settings change repeatedly.
		if _, err := tx.ExecContext(operation, `DELETE FROM runtime_usage_retention_caps WHERE retention_days>=?`, policy.RetentionDays); err != nil {
			return runtimeusage.CollectionPolicy{}, err
		}
		if _, err := tx.ExecContext(operation, `INSERT INTO runtime_usage_retention_caps(retention_days,through_sequence)
 SELECT ?,sequence FROM runtime_usage_observations ORDER BY sequence DESC LIMIT 1`, policy.RetentionDays); err != nil {
			return runtimeusage.CollectionPolicy{}, err
		}
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

// applyUsageRetention shares the existing maintenance transaction and budget.
// The cap and its progress move atomically with each batch of expiry updates;
// failed or canceled maintenance cannot relax a previously published limit.
func applyUsageRetention(ctx context.Context, tx *sql.Tx, limit int) (bool, error) {
	for {
		var days int
		var after, through int64
		err := tx.QueryRowContext(ctx, `SELECT retention_days,after_sequence,through_sequence
 FROM runtime_usage_retention_caps ORDER BY retention_days LIMIT 1`).Scan(&days, &after, &through)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		var end int64
		if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(sequence),?) FROM (
 SELECT sequence FROM runtime_usage_observations WHERE sequence>? AND sequence<=? ORDER BY sequence LIMIT ?
)`, through, after, through, limit).Scan(&end); err != nil {
			return false, err
		}
		duration := int64(days) * 86400000
		if _, err := tx.ExecContext(ctx, `UPDATE runtime_usage_observations SET expires_at_unix_ms=occurred_at_unix_ms+?
 WHERE sequence>? AND sequence<=? AND expires_at_unix_ms>occurred_at_unix_ms+?`, duration, after, end, duration); err != nil {
			return false, err
		}
		if end == through {
			_, err = tx.ExecContext(ctx, `DELETE FROM runtime_usage_retention_caps WHERE retention_days=?`, days)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE runtime_usage_retention_caps SET after_sequence=? WHERE retention_days=?`, end, days)
		}
		if err != nil {
			return false, err
		}
		if limit > 0 {
			return true, nil // Yield the writer before applying another batch/cap.
		}
	}
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
	if !policy.Enabled || policy.CollectingSince == nil || value.StartedAt.Before(*policy.CollectingSince) {
		return nil
	}
	value.UserID, value.Source = "", "proxy"
	value.Attribution = &runtimeusage.Attribution{}
	if value.CaptureRunID != "" {
		var user, username sql.NullString
		var localUser, machineID, gitJSON string
		err := tx.QueryRowContext(operation, `SELECT runtime_user_id,runtime_username,local_user_label,machine_id,git_json
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
		if err := json.Unmarshal([]byte(gitJSON), &run.Runtime.GitAtLaunch); err != nil {
			return err
		}
		if run.Runtime.GitAtLaunch != nil {
			if err := run.Runtime.GitAtLaunch.Validate(); err != nil {
				return err
			}
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
	if _, err := tx.ExecContext(operation, `INSERT INTO runtime_usage_observations(exchange_id,capture_run_id,manual_capture_id,runtime_user_id,occurred_at_unix_ms,expires_at_unix_ms,observation_json) VALUES(?,?,?,?,?,?,?) ON CONFLICT(exchange_id) DO NOTHING`,
		value.ExchangeID, value.CaptureRunID, value.ManualCaptureID, value.UserID, value.OccurredAt.UnixMilli(), value.OccurredAt.Add(time.Duration(policy.RetentionDays)*24*time.Hour).UnixMilli(), string(data)); err != nil {
		return err
	}
	return tx.Commit()
}
