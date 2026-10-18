package runtimepersistence

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
)

type acpSnapshotReader struct{ store *Store }

func (s *ObservationStore) ACPSnapshots() acpobservation.Reader { return s.store.ACPSnapshots() }
func (s *Store) ACPSnapshots() acpobservation.Reader            { return acpSnapshotReader{s} }
func (s *ObservationStore) PutACP(ctx context.Context, r acpobservation.Record) (writeErr error) {
	defer func() { writeErr = recordingWriteError(writeErr) }()
	return s.store.PutACP(ctx, r)
}
func (s *Store) PutACP(ctx context.Context, r acpobservation.Record) (writeErr error) {
	defer func() { writeErr = recordingWriteError(writeErr) }()
	if r.Scope.Validate() != nil || r.Scope.Capture.Kind != captureidentity.KindManagedRun || r.Scope.Capture.ID != r.RunID || r.Snapshot.ValidatePolicy(r.Policy) != nil {
		return acpobservation.ErrInvalid
	}
	if r.ExpiresAtMillis <= time.Now().UnixMilli() {
		return acpobservation.ErrNotFound
	}
	payload, err := encodeACPSnapshot(r.Snapshot, r.Policy)
	if err != nil {
		return err
	}
	operation, finish, err := s.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	tx, err := s.database.BeginTx(operation, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if s.observationOnly {
		if err := checkRecordingReplayAdmission(operation, tx, r.Scope); err != nil {
			return err
		}
	}
	var old []byte
	var revision uint64
	var final bool
	var mode string
	var days uint16
	var expiry int64
	err = tx.QueryRowContext(operation, `SELECT snapshot,revision,final,recording,retention_days,expires_at_unix_ms FROM acp_snapshots WHERE run_id=? AND archive_epoch=? AND capture_generation=?`, r.RunID, r.Scope.Fence.ArchiveEpoch, r.Scope.Fence.CaptureGeneration).Scan(&old, &revision, &final, &mode, &days, &expiry)
	if err == nil {
		if mode != string(r.Policy.Mode) || days != r.Policy.RetentionDays || expiry != r.ExpiresAtMillis {
			return acpobservation.ErrConflict
		}
		if revision == r.Snapshot.Revision && bytes.Equal(old, payload) {
			return nil
		}
		if final || r.Snapshot.Revision <= revision {
			return acpobservation.ErrConflict
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(operation, `INSERT INTO acp_snapshots(run_id,archive_epoch,capture_generation,recording,retention_days,expires_at_unix_ms,snapshot,revision,final) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id,archive_epoch,capture_generation) DO UPDATE SET snapshot=excluded.snapshot,revision=excluded.revision,final=excluded.final`, r.RunID, r.Scope.Fence.ArchiveEpoch, r.Scope.Fence.CaptureGeneration, r.Policy.Mode, r.Policy.RetentionDays, r.ExpiresAtMillis, payload, r.Snapshot.Revision, r.Snapshot.Final)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (reader acpSnapshotReader) Read(ctx context.Context, id string, now int64) (acpobservation.Record, error) {
	operation, finish, err := reader.store.operations.begin(ctx)
	if err != nil {
		return acpobservation.Record{}, err
	}
	defer finish()
	var r acpobservation.Record
	var payload []byte
	err = reader.store.reads.QueryRowContext(operation, `SELECT run_id,archive_epoch,capture_generation,recording,retention_days,expires_at_unix_ms,snapshot FROM acp_snapshots WHERE run_id=? ORDER BY archive_epoch DESC,capture_generation DESC LIMIT 1`, id).Scan(&r.RunID, &r.Scope.Fence.ArchiveEpoch, &r.Scope.Fence.CaptureGeneration, &r.Policy.Mode, &r.Policy.RetentionDays, &r.ExpiresAtMillis, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		err = acpobservation.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.Scope.Capture = captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: r.RunID}
	r.Expired = r.ExpiresAtMillis <= now
	if !r.Expired && (json.Unmarshal(payload, &r.Snapshot) != nil || r.Snapshot.ValidatePolicy(r.Policy) != nil) {
		return acpobservation.Record{}, acpobservation.ErrInvalid
	}
	return r, nil
}
