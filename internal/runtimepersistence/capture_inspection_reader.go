package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type CaptureInspectionReader struct {
	catalog *InspectionCatalog
	control recording.CaptureInspectionControl
}

func NewCaptureInspectionReader(c *InspectionCatalog, control recording.CaptureInspectionControl) *CaptureInspectionReader {
	return &CaptureInspectionReader{catalog: c, control: control}
}
func (r *CaptureInspectionReader) CheckCaptureObservation(ctx context.Context, v recording.CaptureObservationView) error {
	if r == nil || r.catalog == nil || r.control == nil || v.Selection.Stamp.LayoutID != r.catalog.layout {
		return ErrInspectionUnavailable
	}
	if err := r.control.RecheckCaptureInspectionSelection(ctx, v.Selection); err != nil {
		return err
	}
	return r.catalog.Check(v.Sources)
}
func (r *CaptureInspectionReader) ReadCaptureObservation(ctx context.Context, ref captureidentity.Reference) (result recording.CaptureObservationView, err error) {
	if r != nil {
		defer func(caller context.Context) { err = r.catalog.classifyReadCancellation(caller, err) }(ctx)
	}
	if ctx == nil || r == nil || r.catalog == nil || r.control == nil || ref.Validate() != nil {
		return result, ErrInspectionUnavailable
	}
	work, cancel := context.WithTimeout(ctx, r.catalog.timeout)
	defer cancel()
	selection, err := r.control.ReadCaptureInspectionSelection(work, ref)
	if err != nil {
		return result, err
	}
	if !selection.Stamp.Valid() || selection.Stamp.LayoutID != r.catalog.layout || selection.Scope.Capture != ref || selection.Visible && !validCaptureObservation(selection.Scope, selection.CredentialRevision) {
		return result, ErrInspectionUnavailable
	}
	checkContext := work
	result.Selection = selection
	result.Sources = r.catalog.Version()
	if selection.Visible {
		lease, err := r.catalog.acquire(work)
		if err != nil {
			return recording.CaptureObservationView{}, err
		}
		defer lease.close()
		result.Sources = lease.version
		checkContext = lease.ctx
		tx, err := lease.conn.BeginTx(lease.ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return recording.CaptureObservationView{}, err
		}
		defer tx.Rollback()
		for ordinal, source := range lease.sources {
			var first, last sql.NullInt64
			alias := fmt.Sprintf("s%d", ordinal)
			if source.Current {
				err = tx.QueryRowContext(lease.ctx, `SELECT first_observed_at_unix_ms,last_observed_at_unix_ms FROM `+alias+`.capture_observations WHERE capture_kind=? AND capture_id=? AND archive_epoch=? AND capture_generation=? AND credential_revision=?`, ref.Kind, ref.ID, selection.Scope.Fence.ArchiveEpoch, selection.Scope.Fence.CaptureGeneration, selection.CredentialRevision).Scan(&first, &last)
				if err == nil && (!first.Valid || !last.Valid || first.Int64 > last.Int64) {
					return recording.CaptureObservationView{}, ErrInspectionUnavailable
				}
			} else {
				if selection.Scope.Fence != (recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}) {
					continue
				}
				var observation string
				var created, updated int64
				if ref.Kind == captureidentity.KindManagedRun {
					err = tx.QueryRowContext(lease.ctx, `SELECT CASE WHEN length(CAST(observation AS BLOB))<=32 THEN observation END,first_observed_at_unix_ms,created_at_unix_ms FROM `+alias+`.capture_runs WHERE run_id=?`, ref.ID).Scan(&observation, &first, &created)
					if err == nil && (observation != "observed" && observation != "waiting_for_traffic" || (observation == "observed") != first.Valid || first.Valid && first.Int64 < created) {
						return recording.CaptureObservationView{}, ErrInspectionUnavailable
					}
				} else {
					err = tx.QueryRowContext(lease.ctx, `SELECT CASE WHEN length(CAST(observation AS BLOB))<=32 THEN observation END,last_observed_at_unix_ms,created_at_unix_ms,updated_at_unix_ms FROM `+alias+`.manual_captures WHERE capture_id=? AND credential_revision=?`, ref.ID, selection.CredentialRevision).Scan(&observation, &last, &created, &updated)
					if err == nil && (observation != "observed" && observation != "waiting_for_traffic" || (observation == "observed") != last.Valid || last.Valid && (last.Int64 < created || last.Int64 > updated)) {
						return recording.CaptureObservationView{}, ErrInspectionUnavailable
					}
				}
			}
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return recording.CaptureObservationView{}, errors.Join(ErrInspectionUnavailable, err)
			}
			if first.Valid {
				if !validCaptureDisplayMillis(first.Int64) {
					return recording.CaptureObservationView{}, ErrInspectionUnavailable
				}
				at := time.UnixMilli(first.Int64).UTC()
				if result.FirstObservedAt == nil || at.Before(*result.FirstObservedAt) {
					result.FirstObservedAt = &at
				}
			}
			if last.Valid {
				if !validCaptureDisplayMillis(last.Int64) {
					return recording.CaptureObservationView{}, ErrInspectionUnavailable
				}
				at := time.UnixMilli(last.Int64).UTC()
				if result.LastObservedAt == nil || at.After(*result.LastObservedAt) {
					result.LastObservedAt = &at
				}
			}
		}
		if err = tx.Commit(); err != nil {
			return recording.CaptureObservationView{}, err
		}
	}
	if err = r.CheckCaptureObservation(checkContext, result); err != nil {
		return recording.CaptureObservationView{}, err
	}
	return result, nil
}
func validCaptureDisplayMillis(value int64) bool { return value > 0 && value <= 253402300799999 }

var _ recording.CaptureObservationReader = (*CaptureInspectionReader)(nil)
