package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/capturerun"
)

type acpRegistrationRepository struct {
	database   *sql.DB
	operations *operationGate
}

func (s *ControlStore) ACPRegistrations() acpobservation.RegistrationRepository {
	return s.store.ACPRegistrations()
}
func (s *Store) ACPRegistrations() acpobservation.RegistrationRepository {
	return &acpRegistrationRepository{s.database, s.operations}
}

const acpRegistrationColumns = `run_id,recording,retention_days,expires_at_unix_ms,archive_epoch,capture_generation`

func scanACPRegistration(row interface{ Scan(...any) error }) (acpobservation.Registration, error) {
	var r acpobservation.Registration
	err := row.Scan(&r.RunID, &r.Policy.Mode, &r.Policy.RetentionDays, &r.ExpiresAtMillis, &r.Fence.ArchiveEpoch, &r.Fence.CaptureGeneration)
	if err == nil && (r.Policy.Validate() != nil || r.Fence.ArchiveEpoch == 0 || r.Fence.CaptureGeneration == 0) {
		err = acpobservation.ErrInvalid
	}
	return r, err
}
func (r *acpRegistrationRepository) Register(ctx context.Context, input acpobservation.Registration, now int64) (acpobservation.Registration, error) {
	if len(input.RunID) == 0 || len(input.RunID) > 128 || input.Policy.Validate() != nil || input.ExpiresAtMillis <= now {
		return acpobservation.Registration{}, acpobservation.ErrInvalid
	}
	operation, finish, err := r.operations.begin(ctx)
	if err != nil {
		return acpobservation.Registration{}, err
	}
	defer finish()
	tx, err := r.database.BeginTx(operation, nil)
	if err != nil {
		return acpobservation.Registration{}, err
	}
	defer tx.Rollback()
	var state string
	err = tx.QueryRowContext(operation, `SELECT state FROM capture_runs WHERE run_id=? AND state IN('created','attached') AND expires_at_unix_ms>? AND `+captureRunIdentityActive, input.RunID, now, now).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return acpobservation.Registration{}, capturerun.ErrCapabilityRejected
	}
	if err != nil {
		return acpobservation.Registration{}, err
	}
	if state != "created" {
		return acpobservation.Registration{}, acpobservation.ErrConflict
	}
	existing, err := scanACPRegistration(tx.QueryRowContext(operation, `SELECT `+acpRegistrationColumns+` FROM acp_registrations WHERE run_id=?`, input.RunID))
	if err == nil {
		if existing.Policy != input.Policy || existing.ExpiresAtMillis != input.ExpiresAtMillis {
			return acpobservation.Registration{}, acpobservation.ErrConflict
		}
		if err = tx.Commit(); err != nil {
			return acpobservation.Registration{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return acpobservation.Registration{}, err
	}
	err = tx.QueryRowContext(operation, `SELECT s.archive_epoch,f.generation FROM runtime_storage_state s,recording_capture_fences f WHERE s.singleton=1 AND f.capture_kind='managed_run' AND f.capture_id=? AND f.deleted=0`, input.RunID).Scan(&input.Fence.ArchiveEpoch, &input.Fence.CaptureGeneration)
	if err != nil {
		return acpobservation.Registration{}, err
	}
	_, err = tx.ExecContext(operation, `INSERT INTO acp_registrations(`+acpRegistrationColumns+`) VALUES(?,?,?,?,?,?)`, input.RunID, input.Policy.Mode, input.Policy.RetentionDays, input.ExpiresAtMillis, input.Fence.ArchiveEpoch, input.Fence.CaptureGeneration)
	if err != nil {
		return acpobservation.Registration{}, err
	}
	if err = tx.Commit(); err != nil {
		return acpobservation.Registration{}, err
	}
	return input, nil
}
func (r *acpRegistrationRepository) Registration(ctx context.Context, id string, now int64) (acpobservation.Registration, error) {
	operation, finish, err := r.operations.begin(ctx)
	if err != nil {
		return acpobservation.Registration{}, err
	}
	defer finish()
	result, err := scanACPRegistration(r.database.QueryRowContext(operation, `SELECT `+acpRegistrationColumns+` FROM acp_registrations WHERE run_id=? AND expires_at_unix_ms>?`, id, now))
	if errors.Is(err, sql.ErrNoRows) {
		err = acpobservation.ErrNotFound
	}
	return result, err
}
func (r *acpRegistrationRepository) Markers(ctx context.Context, ids []string) (map[string]bool, error) {
	if len(ids) > 200 {
		return nil, acpobservation.ErrInvalid
	}
	markers := map[string]bool{}
	if len(ids) == 0 {
		return markers, nil
	}
	operation, finish, err := r.operations.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := r.database.QueryContext(operation, `SELECT run_id FROM acp_registrations WHERE run_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		markers[id] = true
	}
	return markers, rows.Err()
}
