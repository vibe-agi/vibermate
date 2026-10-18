package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

// InspectRecordingStorage reports physical copies for administration, never
// logical Usage totals or payload content. It uses the complete source catalog
// and privacy mirror; attachment exhaustion explicitly refuses the snapshot.
func (c *InspectionCatalog) InspectRecordingStorage(ctx context.Context, visibility recording.InspectionVisibility, now time.Time, archivePreview bool) (statistics StorageStatistics, archive resourcedeletion.Released, result error) {
	if ctx == nil || visibility == nil || now.IsZero() {
		return statistics, archive, ErrInspectionUnavailable
	}
	snapshot, err := visibility.ReadInspectionSnapshot(ctx, nil)
	if err != nil {
		return statistics, archive, err
	}
	lease, err := c.acquire(ctx)
	if err != nil {
		return statistics, archive, err
	}
	defer lease.close()
	tx, err := lease.conn.BeginTx(lease.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return statistics, archive, err
	}
	defer tx.Rollback()
	if _, err = readInspectionMirror(lease.ctx, tx, fmt.Sprintf("s%d", len(lease.sources)-1), snapshot.Stamp); err != nil {
		return statistics, archive, err
	}
	for i, source := range lease.sources {
		alias := fmt.Sprintf("s%d", i)
		var bytes, pageSize, freePages int64
		if err = tx.QueryRowContext(lease.ctx, `SELECT coalesce(sum(pgsize),0) FROM dbstat(?) WHERE name IN('runtime_evidence_bodies','runtime_evidence_chunks','runtime_exchange_content_blocks','runtime_exchange_content_messages','runtime_exchange_content_transcripts','runtime_exchange_contents','runtime_raw_evidence_envelopes','native_tool_contents','native_tool_operations','native_tool_events','native_history_snapshots','native_history_latest','native_inspection_index','acp_snapshots','acp_observations','capture_observations')`, alias).Scan(&bytes); err != nil {
			return statistics, archive, err
		}
		if err = tx.QueryRowContext(lease.ctx, `PRAGMA `+alias+`.page_size`).Scan(&pageSize); err != nil {
			return statistics, archive, err
		}
		if err = tx.QueryRowContext(lease.ctx, `PRAGMA `+alias+`.freelist_count`).Scan(&freePages); err != nil {
			return statistics, archive, err
		}
		if pageSize <= 0 || freePages < 0 || freePages > math.MaxInt64/pageSize || bytes < 0 || statistics.EvidenceBytes > math.MaxInt64-bytes || statistics.ReusableBytes > math.MaxInt64-freePages*pageSize {
			return statistics, archive, ErrInspectionUnavailable
		}
		statistics.EvidenceBytes += bytes
		statistics.ReusableBytes += freePages * pageSize
		if archivePreview {
			approvals, assignments, captures := "0", "0", `(SELECT count(*) FROM `+alias+`.capture_observations)`
			if !source.Current {
				approvals = `(SELECT count(*) FROM ` + alias + `.tool_approvals)`
				if publishedNativeRevision(source.Database.Revision) {
					approvals += `+(SELECT count(*) FROM ` + alias + `.native_tool_approvals)`
				}
				assignments = `(SELECT count(*) FROM ` + alias + `.capture_environment_assignments)`
				captures = `(SELECT count(*) FROM ` + alias + `.capture_runs)+(SELECT count(*) FROM ` + alias + `.manual_captures)`
			}
			var counts resourcedeletion.Released
			err = tx.QueryRowContext(lease.ctx, `SELECT (SELECT count(*) FROM `+alias+`.runtime_exchange_contents),(SELECT count(*) FROM `+alias+`.runtime_raw_evidence_envelopes),(SELECT count(*) FROM `+alias+`.runtime_activities),(SELECT count(*) FROM `+alias+`.runtime_connection_events),(SELECT count(*) FROM `+alias+`.runtime_egress_attempts),`+approvals+`,`+assignments+`,`+captures).Scan(&counts.Exchanges, &counts.Envelopes, &counts.Activities, &counts.Connections, &counts.Attempts, &counts.Approvals, &counts.Assignments, &counts.Captures)
			if err != nil {
				return statistics, archive, err
			}
			archive = archive.Add(counts)
		} else {
			var exchanges, envelopes uint64
			err = tx.QueryRowContext(lease.ctx, `SELECT (SELECT count(*) FROM `+alias+`.runtime_exchange_contents WHERE expires_at_unix_ms<=?),(SELECT count(*) FROM `+alias+`.runtime_raw_evidence_envelopes WHERE expires_at_unix_ms<=?)`, now.UnixMilli(), now.UnixMilli()).Scan(&exchanges, &envelopes)
			if err != nil {
				return statistics, archive, err
			}
			statistics.Expired.Exchanges += exchanges
			statistics.Expired.Envelopes += envelopes
		}
	}
	if err = tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return statistics, archive, err
	}
	lease.close()
	if err = c.Check(lease.version); err != nil {
		return statistics, archive, err
	}
	if err = visibility.RecheckInspectionStamp(ctx, snapshot.Stamp); err != nil {
		return statistics, archive, err
	}
	// Routine preview intentionally counts direct semantic/Raw expiry only;
	// dependent audit closure, Usage and unresolved ownership are not fake zeros.
	statistics.Expired.CountsIncomplete = true
	statistics.Expired.PrivacyMetadataRetained = true
	archive.PrivacyMetadataRetained = true
	return statistics, archive, nil
}
func (s *ControlStore) RecordingArchivePreview(ctx context.Context) (released resourcedeletion.Released, result error) {
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return released, err
	}
	defer finish()
	err = s.store.reads.QueryRowContext(operation, `SELECT (SELECT count(*) FROM tool_approvals)+(SELECT count(*) FROM native_tool_approvals),(SELECT count(*) FROM capture_environment_assignments),(SELECT count(*) FROM capture_runs)+(SELECT count(*) FROM manual_captures)`).Scan(&released.Approvals, &released.Assignments, &released.Captures)
	released.PrivacyMetadataRetained = true
	return released, err
}
