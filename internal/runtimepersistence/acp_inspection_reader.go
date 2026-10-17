package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// ACPInspectionReader reads retained snapshots only. Registration/connection
// markers remain on the independent control repository.
type ACPInspectionReader struct {
	catalog    *InspectionCatalog
	visibility recording.InspectionVisibility
}

func NewACPInspectionReader(c *InspectionCatalog, v recording.InspectionVisibility) *ACPInspectionReader {
	return &ACPInspectionReader{c, v}
}
func (r *ACPInspectionReader) Begin(ctx context.Context) (recording.InspectionToken, error) {
	return NewInspectionBoundary(r.visibility, r.catalog).Begin(ctx)
}
func (r *ACPInspectionReader) Check(ctx context.Context, t recording.InspectionToken) error {
	return NewInspectionBoundary(r.visibility, r.catalog).Check(ctx, t)
}
func (r *ACPInspectionReader) Read(ctx context.Context, id string, now int64) (acpobservation.Record, error) {
	empty := acpobservation.Record{}
	capture, err := captureidentity.New(captureidentity.KindManagedRun, id)
	if err != nil || ctx == nil || now <= 0 {
		return empty, acpobservation.ErrInvalid
	}
	if r == nil || r.catalog == nil || r.visibility == nil {
		return empty, ErrInspectionUnavailable
	}
	work, cancel := context.WithTimeout(ctx, r.catalog.timeout)
	defer cancel()
	snapshot, err := r.visibility.ReadInspectionSnapshot(work, []captureidentity.Reference{capture})
	if err != nil {
		return empty, err
	}
	if !snapshot.Stamp.Valid() || snapshot.Stamp.LayoutID != r.catalog.layout {
		return empty, ErrInspectionUnavailable
	}
	fence, present := snapshot.Scopes[capture]
	if !present || fence.Deleted || fence.Generation == 0 {
		return empty, acpobservation.ErrNotFound
	}
	scope := recording.Scope{Capture: capture, Fence: recording.Fence{ArchiveEpoch: snapshot.Stamp.ArchiveEpoch, CaptureGeneration: fence.Generation}}
	lease, err := r.catalog.acquire(work)
	if err != nil {
		return empty, err
	}
	defer lease.close()
	token := recording.InspectionToken{Control: snapshot.Stamp, Sources: lease.version}
	tx, err := lease.conn.BeginTx(lease.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	type candidate struct {
		record   acpobservation.Record
		revision int64
		final    bool
	}
	var best *candidate
	for ordinal, source := range lease.sources {
		if !source.Current && (scope.Fence.ArchiveEpoch != 1 || scope.Fence.CaptureGeneration != 1) {
			continue
		}
		c := candidate{record: acpobservation.Record{RunID: id, Scope: scope}}
		var payload []byte
		var final int
		if source.Current {
			query := fmt.Sprintf(`SELECT recording,retention_days,expires_at_unix_ms,revision,final,CASE WHEN expires_at_unix_ms>? AND length(snapshot)<=? THEN snapshot END FROM s%d.acp_snapshots WHERE run_id=? AND archive_epoch=? AND capture_generation=?`, ordinal)
			err = tx.QueryRowContext(lease.ctx, query, now, acpobservation.MaxSnapshotBytes, id, scope.Fence.ArchiveEpoch, scope.Fence.CaptureGeneration).Scan(&c.record.Policy.Mode, &c.record.Policy.RetentionDays, &c.record.ExpiresAtMillis, &c.revision, &final, &payload)
		} else {
			var policy string
			query := fmt.Sprintf(`SELECT CASE WHEN length(CAST(policy AS BLOB))<=4096 THEN policy END,expires_at_unix_ms,revision,final,CASE WHEN expires_at_unix_ms>? AND length(snapshot)<=? THEN snapshot END FROM s%d.acp_observations WHERE run_id=?`, ordinal)
			err = tx.QueryRowContext(lease.ctx, query, now, acpobservation.MaxSnapshotBytes, id).Scan(&policy, &c.record.ExpiresAtMillis, &c.revision, &final, &payload)
			if err == nil && json.Unmarshal([]byte(policy), &c.record.Policy) != nil {
				return empty, acpobservation.ErrInvalid
			}
		}
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return empty, err
		}
		if c.revision <= 0 || final < 0 || final > 1 || c.record.ExpiresAtMillis <= 0 || c.record.Policy.Validate() != nil {
			return empty, acpobservation.ErrInvalid
		}
		c.final = final == 1
		c.record.Expired = c.record.ExpiresAtMillis <= now
		// Released retention legitimately replaces expired snapshot bytes with NULL.
		// Do not load or manufacture content for an expired metadata-only marker.
		if !c.record.Expired {
			if json.Unmarshal(payload, &c.record.Snapshot) != nil || c.record.Snapshot.ValidatePolicy(c.record.Policy) != nil || c.record.Snapshot.Revision != uint64(c.revision) || c.record.Snapshot.Final != c.final {
				return empty, acpobservation.ErrInvalid
			}
		}
		if best != nil {
			if best.record.Policy != c.record.Policy || best.record.ExpiresAtMillis != c.record.ExpiresAtMillis {
				return empty, acpobservation.ErrConflict
			}
			if best.revision == c.revision {
				if best.final != c.final || !reflect.DeepEqual(best.record.Snapshot, c.record.Snapshot) {
					return empty, acpobservation.ErrConflict
				}
			} else if (best.revision < c.revision && best.final) || (c.revision < best.revision && c.final) {
				return empty, acpobservation.ErrConflict
			}
		}
		if best == nil || c.revision >= best.revision {
			best = &c
		}
	}
	if err = tx.Rollback(); err != nil {
		return empty, err
	}
	if err = r.Check(lease.ctx, token); err != nil {
		return empty, err
	}
	if best == nil {
		return empty, acpobservation.ErrNotFound
	}
	return best.record, nil
}

var _ acpobservation.InspectionReader = (*ACPInspectionReader)(nil)
