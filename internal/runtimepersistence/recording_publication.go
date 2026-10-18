package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

// InspectionExportCursor is a keyset position in one fixed control snapshot.
// A zero cursor begins an export. It contains no history identity or authority.
type InspectionExportCursor struct {
	relation int
	kind, id string
	days     int
}
type InspectionExportPage struct {
	Policy   runtimeusage.CollectionPolicy
	Next     InspectionExportCursor
	Done     bool
	rows     [][]any
	relation int
}

// ExportInspectionPage copies at most 256 rows, then releases its control read
// before the publisher can write history. Every page checks the expected stamp
// in the same SQL snapshot as the page; no count or OFFSET scan is needed.
func (s *ControlStore) ExportInspectionPage(ctx context.Context, expected recording.InspectionStamp, cursor InspectionExportCursor) (page InspectionExportPage, result error) {
	if !expected.Valid() || cursor.relation < 0 || cursor.relation > 2 || len(cursor.kind) > 128 || len(cursor.id) > 128 || cursor.days < 0 || cursor.days > 365 {
		return page, ErrInspectionChanged
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return page, err
	}
	defer finish()
	tx, err := s.store.reads.BeginTx(operation, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	var stamp recording.InspectionStamp
	var denialHash []byte
	if err = tx.QueryRowContext(operation, `SELECT s.layout_id,s.archive_epoch,s.visibility_revision,p.revision,d.identity_count,d.chain_hash FROM runtime_storage_state s JOIN runtime_usage_policy p USING(singleton) JOIN recording_denial_coverage d USING(singleton) WHERE s.singleton=1`).Scan(&stamp.LayoutID, &stamp.ArchiveEpoch, &stamp.VisibilityRevision, &stamp.UsagePolicyRevision, &stamp.DenialCount, &denialHash); err != nil {
		return page, err
	}
	if len(denialHash) != 32 {
		return page, ErrInspectionUnavailable
	}
	copy(stamp.DenialHash[:], denialHash)
	if stamp != expected {
		return page, ErrInspectionChanged
	}
	page.Policy, err = scanUsagePolicy(tx.QueryRowContext(operation, `SELECT enabled,retention_days,revision,collecting_since_unix_ms FROM runtime_usage_policy WHERE singleton=1`))
	if err != nil {
		return page, err
	}
	var query string
	var args []any
	switch cursor.relation {
	case 0:
		query = `SELECT capture_kind,capture_id,generation,deleted FROM recording_capture_fences WHERE (capture_kind,capture_id)>(?,?) ORDER BY capture_kind,capture_id LIMIT 256`
		args = []any{cursor.kind, cursor.id}
	case 1:
		query = `SELECT retention_days,through_policy_revision FROM runtime_usage_retention_rules WHERE retention_days>? ORDER BY retention_days LIMIT 256`
		args = []any{cursor.days}
	case 2:
		query = `SELECT archive_id,retention_days,after_sequence,through_sequence FROM runtime_usage_legacy_retention_caps WHERE (archive_id,retention_days)>(?,?) ORDER BY archive_id,retention_days LIMIT 256`
		args = []any{cursor.id, cursor.days}
	}
	rows, err := tx.QueryContext(operation, query, args...)
	if err != nil {
		return page, err
	}
	page.relation = cursor.relation
	page.Next = cursor
	for rows.Next() {
		switch cursor.relation {
		case 0:
			var kind, id string
			var generation, deleted int64
			if err = rows.Scan(&kind, &id, &generation, &deleted); err == nil {
				page.rows = append(page.rows, []any{kind, id, generation, deleted})
				page.Next.kind, page.Next.id = kind, id
			}
		case 1:
			var days int
			var through int64
			if err = rows.Scan(&days, &through); err == nil {
				page.rows = append(page.rows, []any{days, through})
				page.Next.days = days
			}
		case 2:
			var id string
			var days int
			var after, through int64
			if err = rows.Scan(&id, &days, &after, &through); err == nil {
				page.rows = append(page.rows, []any{id, days, after, through})
				page.Next.id, page.Next.days = id, days
			}
		}
		if err != nil {
			rows.Close()
			return InspectionExportPage{}, err
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return InspectionExportPage{}, err
	}
	if len(page.rows) < 256 {
		page.Next = InspectionExportCursor{relation: cursor.relation + 1}
		page.Done = page.Next.relation == 3
	}
	if err = tx.Commit(); err != nil {
		return InspectionExportPage{}, err
	}
	return page, nil
}

// PublishInspectionMirror is optional owner work. Staging is never active; a
// changed control stamp leaves the old mirror untouched (and readers refuse
// that stale mirror). Restart discards staging and exports a fresh snapshot.
func (s *ObservationStore) PublishInspectionMirror(ctx context.Context, control *ControlStore) error {
	return s.publishInspectionMirror(ctx, control, control.ExportInspectionPage)
}
func (s *ObservationStore) publishInspectionMirror(ctx context.Context, control *ControlStore, export func(context.Context, recording.InspectionStamp, InspectionExportCursor) (InspectionExportPage, error)) error {
	s.mirrorMu.Lock()
	defer s.mirrorMu.Unlock()
	snapshot, err := control.ReadInspectionSnapshot(ctx, nil)
	if err != nil {
		return err
	}
	if err = s.verifyRecordingDenialCoverage(ctx, snapshot.Stamp); err != nil {
		return err
	}
	page, err := export(ctx, snapshot.Stamp, InspectionExportCursor{})
	if err != nil {
		return err
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	// Only the fixed optional publisher writes these relations. Serialize calls
	// on this owner as well, so an abandoned generation cannot be activated.
	tx, err := s.store.database.BeginTx(operation, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(operation, `DELETE FROM inspection_mirror_generations WHERE generation NOT IN(SELECT generation FROM inspection_mirror_active)`); err != nil {
		return err
	}
	var generation int64
	if err = tx.QueryRowContext(operation, `SELECT coalesce(max(generation),0) FROM inspection_mirror_generations`).Scan(&generation); err != nil {
		return err
	}
	if generation == math.MaxInt64 {
		return ErrInspectionUnavailable
	}
	generation++
	var since any
	if page.Policy.CollectingSince != nil {
		since = page.Policy.CollectingSince.UnixMilli()
	}
	st := snapshot.Stamp
	if _, err = tx.ExecContext(operation, `INSERT INTO inspection_mirror_generations VALUES(?,?,?,?,?,?,?,?,0,0,0,0)`, generation, st.LayoutID, st.ArchiveEpoch, st.VisibilityRevision, st.UsagePolicyRevision, page.Policy.Enabled, page.Policy.RetentionDays, since); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	counts := [3]int64{}
	for {
		if len(page.rows) > 256 || page.relation < 0 || page.relation > 2 {
			return ErrInspectionUnavailable
		}
		tx, err = s.store.database.BeginTx(operation, nil)
		if err != nil {
			return err
		}
		for _, row := range page.rows {
			table := []string{"inspection_mirror_fences", "inspection_mirror_usage_caps", "inspection_mirror_legacy_caps"}[page.relation]
			placeholders := []string{"?,?,?,?,?", "?,?,?", "?,?,?,?,?"}[page.relation]
			if _, err = tx.ExecContext(operation, `INSERT INTO `+table+` VALUES(`+placeholders+`)`, append([]any{generation}, row...)...); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		counts[page.relation] += int64(len(page.rows))
		if page.Done {
			break
		}
		page, err = export(operation, st, page.Next)
		if err != nil {
			return err
		}
	}
	// This read owns no observation transaction. A subsequent control mutation
	// is safe: readers compare the entire stamp before and after their snapshot.
	if err = control.RecheckInspectionStamp(operation, st); err != nil {
		return err
	}
	tx, err = s.store.database.BeginTx(operation, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = readRecordingDenialCoverage(operation, tx, "main", st); err != nil {
		return err
	}
	if _, err = tx.ExecContext(operation, `UPDATE inspection_mirror_generations SET fence_count=?,current_cap_count=?,legacy_cap_count=?,complete=1 WHERE generation=?`, counts[0], counts[1], counts[2], generation); err != nil {
		return err
	}
	if _, err = tx.ExecContext(operation, `INSERT INTO inspection_mirror_active VALUES(1,?) ON CONFLICT(singleton) DO UPDATE SET generation=excluded.generation`, generation); err != nil {
		return err
	}
	if err = installRecordingWriterCoverage(operation, tx, st); err != nil {
		return err
	}
	if _, err = tx.ExecContext(operation, `UPDATE recording_admission_state SET enforced=1,verified_data_version=(SELECT data_version FROM pragma_data_version),required_denial_count=?,required_denial_hash=? WHERE singleton=1`, st.DenialCount, st.DenialHash[:]); err != nil {
		return err
	}
	if _, err = tx.ExecContext(operation, `DELETE FROM inspection_mirror_generations WHERE generation<>?`, generation); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("publish inspection mirror: %w", err)
	}
	return nil
}

// PublishedRecordingStamp is used by the exclusive maintenance owner to retain
// exactly the activated mirror coordinates, never a newer control snapshot.
func (s *ObservationStore) PublishedRecordingStamp(ctx context.Context) (stamp recording.InspectionStamp, result error) {
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return stamp, err
	}
	defer finish()
	var hash []byte
	err = s.store.reads.QueryRowContext(operation, `SELECT g.layout_id,g.archive_epoch,g.visibility_revision,g.usage_policy_revision,d.required_denial_count,d.required_denial_hash FROM inspection_mirror_active a JOIN inspection_mirror_generations g USING(generation) CROSS JOIN recording_admission_state d WHERE a.singleton=1 AND d.singleton=1 AND g.complete=1 AND d.enforced=1`).Scan(&stamp.LayoutID, &stamp.ArchiveEpoch, &stamp.VisibilityRevision, &stamp.UsagePolicyRevision, &stamp.DenialCount, &hash)
	if err != nil || len(hash) != 32 {
		return recording.InspectionStamp{}, errors.Join(ErrInspectionUnavailable, err)
	}
	copy(stamp.DenialHash[:], hash)
	if !stamp.Valid() {
		return recording.InspectionStamp{}, ErrInspectionUnavailable
	}
	return stamp, nil
}
