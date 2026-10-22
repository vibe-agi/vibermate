package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

var ErrRecordingCheckpointBusy = errors.New("recording checkpoint remains busy")

// cleanupInventory is available only to the actual catalog maintenance owner.
// It includes every declared source, independently of the read ATTACH limit.
func (m *InspectionMaintenance) cleanupInventory() ([]InspectionSource, error) {
	if m == nil || m.catalog == nil {
		return nil, ErrInspectionUnavailable
	}
	c := m.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.maintenance != m || c.active != 0 || c.version.Generation != m.generation {
		return nil, ErrInspectionChanged
	}
	return slices.Clone(c.sources), nil
}

// withCleanupSource borrows the existing current writer, or opens just one
// existing legacy source. It never opens/replaces a second current writer.
// Callers release all SQL before certifying a bounded selection in control.
func (s *ObservationStore) withCleanupSource(ctx context.Context, m *InspectionMaintenance, source InspectionSource, work func(*sql.Conn, string) error) (result error) {
	sources, err := m.cleanupInventory()
	if err != nil {
		return err
	}
	if !slices.Contains(sources, source) || filepath.Join(m.catalog.root, sources[len(sources)-1].RelativePath) != s.store.databasePath {
		return ErrInspectionUnavailable
	}
	path := filepath.Join(m.catalog.root, source.RelativePath)
	physical, err := filepath.EvalSymlinks(path)
	if err != nil || physical != path {
		return ErrInspectionUnavailable
	}
	if _, err = inspectionSourceFiles(path, source.Current); err != nil {
		return err
	}
	db := s.store.database
	if !source.Current {
		db = sql.OpenDB(newSQLiteUpgradeConnector(path, 50*time.Millisecond))
		db.SetMaxOpenConns(1)
		defer func() { result = errors.Join(result, db.Close()) }()
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, conn.Close()) }()
	if source.Current {
		defer func() {
			_, err := conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA synchronous=NORMAL`)
			result = errors.Join(result, err)
		}()
	}
	if err = inspectAttachedObservation(ctx, conn, "main", m.catalog.layout, source); err != nil {
		return err
	}
	mirror := "main"
	if !source.Current {
		u := url.URL{Scheme: "file", Path: s.store.databasePath}
		q := u.Query()
		q.Set("mode", "ro")
		u.RawQuery = q.Encode()
		if _, err = conn.ExecContext(ctx, `ATTACH DATABASE ? AS cleanup_current`, u.String()); err != nil {
			return err
		}
		mirror = "cleanup_current"
	}
	var before, after int64
	if err = conn.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&before); err != nil {
		return err
	}
	result = work(conn, mirror)
	if err = conn.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&after); err != nil {
		return errors.Join(result, err)
	}
	if after != before {
		m.catalog.mu.Lock()
		if m.mutated == nil {
			m.mutated = make(map[string]bool)
		}
		m.mutated[source.ID] = true
		m.catalog.mu.Unlock()
	}
	return result
}

// RecordingSourceMutated reports actual SQL work under this exclusive lease.
// It is not a crash-recovery proof: resumed claims must compact every source.
func (m *InspectionMaintenance) RecordingSourceMutated(index int) (bool, error) {
	sources, err := m.cleanupInventory()
	if err != nil {
		return false, err
	}
	if index < 0 || index >= len(sources) {
		return false, ErrInspectionSourceLimit
	}
	m.catalog.mu.Lock()
	defer m.catalog.mu.Unlock()
	return m.mutated[sources[index].ID], nil
}

// PurgeExpiredRecordingUsage is only the Usage portion of a full maintenance
// pass. It does not acknowledge the reliable obligation or physical erasure.
// Every live copy/cap remains untouched. Any expired/deleted source copy first
// creates certified whole-identity denial, then all declared copies are purged.
func (s *ObservationStore) PurgeExpiredRecordingUsage(ctx context.Context, control *ControlStore, m *InspectionMaintenance, cutoff time.Time) (removed uint64, result error) {
	if ctx == nil || control == nil || cutoff.IsZero() {
		return 0, ErrInspectionUnavailable
	}
	sources, err := m.cleanupInventory()
	if err != nil {
		return 0, err
	}
	if err = s.PublishInspectionMirror(ctx, control); err != nil {
		return 0, err
	}
	for _, source := range sources {
		after := ""
		for {
			var ids []string
			err = s.withCleanupSource(ctx, m, source, func(conn *sql.Conn, mirror string) error {
				query, args := cleanupUsageSelection(source, mirror, cutoff, after)
				rows, err := conn.QueryContext(ctx, query, args...)
				if err != nil {
					return err
				}
				defer rows.Close()
				for rows.Next() {
					var id string
					if err = rows.Scan(&id); err != nil {
						return err
					}
					ids = append(ids, id)
				}
				return rows.Err()
			})
			if err != nil {
				return removed, err
			}
			if len(ids) == 0 {
				break
			}
			if err = s.validateCleanupUsageCopies(ctx, m, ids); err != nil {
				return removed, err
			}
			// No source transaction/connection remains leased across this required write.
			if err = s.DenyRecordingIdentities(ctx, control, "usage", ids); err != nil {
				return removed, err
			}
			after = ids[len(ids)-1]
		}
	}
	// Certification is complete for every source before any source loses a cap-
	// bearing root. On restart an uncertified tail is certified before deletion.
	if err = s.CertifyRecordingDenialTail(ctx, control); err != nil {
		return removed, err
	}
	if err = s.PublishInspectionMirror(ctx, control); err != nil {
		return removed, err
	}
	for _, source := range sources {
		for {
			var count int64
			err = s.withCleanupSource(ctx, m, source, func(conn *sql.Conn, mirror string) error {
				if _, err := conn.ExecContext(ctx, `PRAGMA synchronous=FULL`); err != nil {
					return err
				}
				tx, err := conn.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				r, err := tx.ExecContext(ctx, `DELETE FROM main.runtime_usage_observations WHERE exchange_id IN(SELECT u.exchange_id FROM main.runtime_usage_observations u JOIN `+mirror+`.recording_denials d ON d.kind='usage' AND d.identity=u.exchange_id ORDER BY u.exchange_id LIMIT 256)`)
				if err != nil {
					return err
				}
				count, err = r.RowsAffected()
				if err != nil {
					return err
				}
				return tx.Commit()
			})
			if err != nil {
				return removed, err
			}
			removed += uint64(count)
			if count < 256 {
				break
			}
		}
	}
	return removed, nil
}

// CompactRecordingSource proves actual SQLite completion for one declared
// source. A busy checkpoint is pending work, even when SQLite returned no error.
// This is deliberately separate from visibility/SQL deletion and its counts.
func (s *ObservationStore) CompactRecordingSource(ctx context.Context, m *InspectionMaintenance, index int) error {
	return s.verifyRecordingSource(ctx, m, index, true)
}

// VerifyUnchangedRecordingSource retains every certification prerequisite but
// avoids a full database rewrite when this fresh owner made no SQL changes.
func (s *ObservationStore) VerifyUnchangedRecordingSource(ctx context.Context, m *InspectionMaintenance, index int) error {
	changed, err := m.RecordingSourceMutated(index)
	if err != nil {
		return err
	}
	if changed {
		return ErrInspectionUnavailable
	}
	return s.verifyRecordingSource(ctx, m, index, false)
}

func (s *ObservationStore) verifyRecordingSource(ctx context.Context, m *InspectionMaintenance, index int, compact bool) error {
	sources, err := m.cleanupInventory()
	if err != nil {
		return err
	}
	if index < 0 || index >= len(sources) {
		return ErrInspectionSourceLimit
	}
	source := sources[index]
	return s.withCleanupSource(ctx, m, source, func(conn *sql.Conn, _ string) error {
		checkpoint := func() error {
			var busy, log, done int
			if err := conn.QueryRowContext(ctx, `PRAGMA main.wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &done); err != nil {
				return err
			}
			if busy != 0 || (log >= 0 && (log != 0 || done != 0)) {
				return errors.Join(ErrInspectionUnavailable, ErrRecordingCheckpointBusy)
			}
			return nil
		}
		if err := checkpoint(); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `PRAGMA main.synchronous=FULL`); err != nil {
			return err
		}
		if compact {
			if _, err := conn.ExecContext(ctx, `VACUUM main`); err != nil {
				return err
			}
		}
		if err := checkpoint(); err != nil {
			return err
		}
		var integrity string
		if err := conn.QueryRowContext(ctx, `PRAGMA main.integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
			return errors.Join(ErrInspectionUnavailable, err)
		}
		rows, err := conn.QueryContext(ctx, `PRAGMA main.foreign_key_check`)
		if err != nil {
			return err
		}
		bad := rows.Next()
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil || bad {
			return errors.Join(ErrInspectionUnavailable, err)
		}
		path := filepath.Join(m.catalog.root, source.RelativePath)
		for _, suffix := range []string{"-wal", "-journal"} {
			info, err := os.Lstat(path + suffix)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
				return errors.Join(ErrInspectionUnavailable, err)
			}
		}
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		if err = errors.Join(file.Sync(), file.Close()); err != nil {
			return err
		}
		directory, err := os.Open(filepath.Dir(path))
		if err != nil {
			return err
		}
		return errors.Join(directory.Sync(), directory.Close())
	})
}

// Only selected logical identities need pairwise equality before whole-ID
// removal. Canonical bounded DTO hashes keep memory independent of all copies.
func (s *ObservationStore) validateCleanupUsageCopies(ctx context.Context, m *InspectionMaintenance, ids []string) error {
	if len(ids) > 256 {
		return ErrInspectionSourceLimit
	}
	sources, err := m.cleanupInventory()
	if err != nil {
		return err
	}
	type immutable struct {
		digest            [32]byte
		kind, scope       string
		epoch, generation int64
	}
	seen := make(map[string]immutable, len(ids))
	args := make([]any, len(ids))
	slots := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		slots[i] = "?"
	}
	for _, source := range sources {
		if err = s.withCleanupSource(ctx, m, source, func(conn *sql.Conn, _ string) error {
			rows, err := conn.QueryContext(ctx, `SELECT CASE WHEN length(CAST(observation_json AS BLOB))<=65536 THEN observation_json END,exchange_id,capture_run_id,manual_capture_id,runtime_user_id,occurred_at_unix_ms,`+usageInspectionScope(source.Current, "")+` FROM runtime_usage_observations WHERE exchange_id IN(`+strings.Join(slots, ",")+`)`, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var raw, id, capture, manual, user string
				var at int64
				var identity immutable
				if err = rows.Scan(&raw, &id, &capture, &manual, &user, &at, &identity.kind, &identity.scope, &identity.epoch, &identity.generation); err != nil {
					return err
				}
				var value runtimeusage.Observation
				if json.Unmarshal([]byte(raw), &value) != nil || value.Validate() != nil || value.ExchangeID != id || value.CaptureRunID != capture || value.ManualCaptureID != manual || string(value.UserID) != user || value.OccurredAt.UnixMilli() != at {
					return ErrInspectionUnavailable
				}
				value.StartedAt = value.StartedAt.UTC()
				value.OccurredAt = value.OccurredAt.UTC()
				canonical, err := json.Marshal(value)
				if err != nil {
					return err
				}
				identity.digest = sha256.Sum256(canonical)
				if previous, ok := seen[id]; ok && previous != identity {
					return fmt.Errorf("%w: conflicting Usage Exchange identity", ErrInspectionUnavailable)
				}
				seen[id] = identity
			}
			return rows.Err()
		}); err != nil {
			return err
		}
	}
	return nil
}

// cleanupUsageSelection is shared by read-only routine eligibility and the
// destructive owner so retention caps cannot drift between those decisions.
func cleanupUsageSelection(source InspectionSource, mirror string, cutoff time.Time, after string) (string, []any) {
	revision, epoch, kind, id, generation := "0", "1", `CASE WHEN u.capture_run_id<>'' THEN 'managed_run' WHEN u.manual_capture_id<>'' THEN 'manual_capture' ELSE '' END`, `CASE WHEN u.capture_run_id<>'' THEN u.capture_run_id ELSE u.manual_capture_id END`, `CASE WHEN u.capture_run_id<>'' OR u.manual_capture_id<>'' THEN 1 ELSE 0 END`
	if source.Current {
		revision = "u.policy_revision"
		epoch = "u.recording_archive_epoch"
		kind = "u.recording_scope_kind"
		id = "u.recording_scope_id"
		generation = "u.recording_capture_generation"
	}
	cap := `min(u.expires_at_unix_ms,coalesce((SELECT min(CASE WHEN u.occurred_at_unix_ms>9223372036854775807-c.retention_days*86400000 THEN 9223372036854775807 ELSE u.occurred_at_unix_ms+c.retention_days*86400000 END) FROM ` + mirror + `.inspection_mirror_usage_caps c WHERE c.mirror_generation=g.generation AND ` + revision + `<=c.through_policy_revision),9223372036854775807)`
	args := []any{}
	if !source.Current {
		cap += `,coalesce((SELECT min(CASE WHEN u.occurred_at_unix_ms>9223372036854775807-c.retention_days*86400000 THEN 9223372036854775807 ELSE u.occurred_at_unix_ms+c.retention_days*86400000 END) FROM ` + mirror + `.inspection_mirror_legacy_caps c WHERE c.mirror_generation=g.generation AND c.archive_id=? AND u.sequence>c.after_sequence AND u.sequence<=c.through_sequence),9223372036854775807),coalesce((SELECT min(CASE WHEN u.occurred_at_unix_ms>9223372036854775807-c.retention_days*86400000 THEN 9223372036854775807 ELSE u.occurred_at_unix_ms+c.retention_days*86400000 END) FROM main.runtime_usage_retention_caps c WHERE u.sequence>c.after_sequence AND u.sequence<=c.through_sequence),9223372036854775807)`
		args = append(args, source.ID)
	}
	cap += `)`
	query := `SELECT CASE WHEN length(CAST(u.exchange_id AS BLOB)) BETWEEN 1 AND 512 THEN u.exchange_id END FROM main.runtime_usage_observations u CROSS JOIN ` + mirror + `.inspection_mirror_active a JOIN ` + mirror + `.inspection_mirror_generations g ON g.generation=a.generation WHERE a.singleton=1 AND g.complete=1 AND (` + cap + `<=? OR ` + epoch + `<>g.archive_epoch OR (` + kind + `<>'' AND NOT EXISTS(SELECT 1 FROM ` + mirror + `.inspection_mirror_fences f WHERE f.mirror_generation=g.generation AND f.capture_kind=` + kind + ` AND f.capture_id=` + id + ` AND f.capture_generation=` + generation + ` AND f.deleted=0))) AND u.exchange_id>? ORDER BY u.exchange_id LIMIT 256`
	args = append(args, cutoff.UnixMilli(), after)
	return query, args
}
