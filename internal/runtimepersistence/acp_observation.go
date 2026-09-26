package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/environment"
)

//go:embed acp_schema.sql
var acpSchemaSQL string

// Called only after the released base digest has been validated. No destructive
// migration or reinitialization is permitted if either schema is unfamiliar.
func initializeACPSchema(ctx context.Context, database *sql.DB) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	digest := sha256.Sum256([]byte(acpSchemaSQL))
	expected := hex.EncodeToString(digest[:])
	var count int
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name='acp_schema_metadata'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := transaction.ExecContext(ctx, acpSchemaSQL); err != nil {
			return errors.New("ACP schema extension could not be initialized")
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO acp_schema_metadata VALUES (1,1,?)`, expected); err != nil {
			return err
		}
	} else {
		var revision int
		var digest string
		if err := transaction.QueryRowContext(ctx, `SELECT revision,source_sha256 FROM acp_schema_metadata WHERE singleton=1`).Scan(&revision, &digest); err != nil || revision != 1 || digest != expected {
			return errors.New("unsupported ACP schema extension")
		}
	}
	return transaction.Commit()
}

type acpObservationRepository struct {
	database   *sql.DB
	operations *operationGate
}

func (store *Store) ACPObservations() acpobservation.Repository {
	return &acpObservationRepository{database: store.database, operations: store.operations}
}

func (repository *acpObservationRepository) transaction(ctx context.Context, now int64, work func(context.Context, *sql.Tx) error) error {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	tx, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return errors.New("ACP storage transaction unavailable")
	}
	defer tx.Rollback()
	// Keep only the small connection marker after retention expires. It still
	// distinguishes ACP from a managed HTTP capture, without retaining content.
	if _, err = tx.ExecContext(operation, `UPDATE acp_observations SET snapshot=NULL WHERE expires_at_unix_ms<=? AND snapshot IS NOT NULL`, now); err != nil {
		return errors.New("ACP retention unavailable")
	}
	if err = work(operation, tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return errors.New("ACP storage commit unavailable")
	}
	return nil
}

func activeACPParent(ctx context.Context, tx *sql.Tx, runID string, now int64) bool {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM capture_runs WHERE run_id=? AND state IN ('created','attached') AND expires_at_unix_ms>? AND `+captureRunIdentityActive, runID, now, now).Scan(&count)
	return err == nil && count == 1
}

func encodeACPSnapshot(snapshot acpobservation.Snapshot, policy environment.ContentRecordingPolicy) ([]byte, error) {
	if err := snapshot.Validate(policy.Mode == environment.ContentRecordingFull); err != nil {
		return nil, err
	}
	if policy.Mode == environment.ContentRecordingOff && (len(snapshot.Sessions) != 0 || len(snapshot.Prompts) != 0 || snapshot.Agent != (acpobservation.Agent{}) || snapshot.ProtocolVersion != 0 || snapshot.NeedsAuthentication) {
		return nil, acpobservation.ErrInvalid
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) > acpobservation.MaxSnapshotBytes {
		return nil, acpobservation.ErrInvalid
	}
	return encoded, nil
}

func (repository *acpObservationRepository) Create(ctx context.Context, record acpobservation.Record, now int64) error {
	if record.RunID == "" || record.Policy.Validate() != nil || record.ExpiresAtMillis <= now || record.Snapshot.Revision != 1 || record.Snapshot.Final {
		return acpobservation.ErrInvalid
	}
	payload, err := encodeACPSnapshot(record.Snapshot, record.Policy)
	if err != nil {
		return err
	}
	policy, _ := json.Marshal(record.Policy)
	return repository.transaction(ctx, now, func(ctx context.Context, tx *sql.Tx) error {
		if !activeACPParent(ctx, tx, record.RunID, now) {
			return acpobservation.ErrNotFound
		}
		// Attachment can race the earlier control authorization. Choosing ACP
		// must still precede attachment in the same storage transaction.
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM capture_runs WHERE run_id=?`, record.RunID).Scan(&state); err != nil {
			return errors.New("ACP registration boundary unavailable")
		}
		if state != "created" {
			return acpobservation.ErrConflict
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO acp_observations(run_id,policy,expires_at_unix_ms,snapshot,revision,final) VALUES(?,?,?,?,1,0) ON CONFLICT(run_id) DO NOTHING`, record.RunID, string(policy), record.ExpiresAtMillis, payload)
		if err != nil {
			return errors.New("ACP observation creation failed")
		}
		var existing string
		var expiry int64
		if err := tx.QueryRowContext(ctx, `SELECT policy,expires_at_unix_ms FROM acp_observations WHERE run_id=?`, record.RunID).Scan(&existing, &expiry); err != nil {
			return errors.New("ACP observation creation uncertain")
		}
		if existing != string(policy) || expiry != record.ExpiresAtMillis {
			return acpobservation.ErrConflict
		}
		return nil
	})
}

func (repository *acpObservationRepository) Save(ctx context.Context, runID string, snapshot acpobservation.Snapshot, now int64) error {
	return repository.transaction(ctx, now, func(ctx context.Context, tx *sql.Tx) error {
		if !activeACPParent(ctx, tx, runID, now) {
			return acpobservation.ErrNotFound
		}
		var storedPolicy string
		var old []byte
		var revision uint64
		var final bool
		err := tx.QueryRowContext(ctx, `SELECT policy,snapshot,revision,final FROM acp_observations WHERE run_id=? AND expires_at_unix_ms>?`, runID, now).Scan(&storedPolicy, &old, &revision, &final)
		if errors.Is(err, sql.ErrNoRows) {
			return acpobservation.ErrNotFound
		}
		if err != nil {
			return errors.New("ACP observation unavailable")
		}
		var policy environment.ContentRecordingPolicy
		if json.Unmarshal([]byte(storedPolicy), &policy) != nil {
			return acpobservation.ErrInvalid
		}
		payload, err := encodeACPSnapshot(snapshot, policy)
		if err != nil {
			return err
		}
		if revision == snapshot.Revision && bytes.Equal(old, payload) {
			return nil
		}
		if final || snapshot.Revision <= revision {
			return acpobservation.ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE acp_observations SET snapshot=?,revision=?,final=? WHERE run_id=?`, payload, snapshot.Revision, snapshot.Final, runID); err != nil {
			return errors.New("ACP observation write failed")
		}
		return nil
	})
}

func (repository *acpObservationRepository) Read(ctx context.Context, runID string, now int64) (acpobservation.Record, error) {
	var record acpobservation.Record
	err := repository.transaction(ctx, now, func(ctx context.Context, tx *sql.Tx) error {
		var policy string
		var snapshot []byte
		err := tx.QueryRowContext(ctx, `SELECT run_id,policy,expires_at_unix_ms,snapshot FROM acp_observations WHERE run_id=?`, runID).Scan(&record.RunID, &policy, &record.ExpiresAtMillis, &snapshot)
		if errors.Is(err, sql.ErrNoRows) {
			return acpobservation.ErrNotFound
		}
		if err != nil {
			return errors.New("ACP observation read failed")
		}
		if json.Unmarshal([]byte(policy), &record.Policy) != nil {
			return acpobservation.ErrInvalid
		}
		record.Expired = record.ExpiresAtMillis <= now
		if !record.Expired && json.Unmarshal(snapshot, &record.Snapshot) != nil {
			return acpobservation.ErrInvalid
		}
		return nil
	})
	return record, err
}

func (repository *acpObservationRepository) Markers(ctx context.Context, runIDs []string, now int64) (map[string]bool, error) {
	markers := map[string]bool{}
	if len(runIDs) == 0 {
		return markers, nil
	}
	if len(runIDs) > 200 {
		return nil, acpobservation.ErrInvalid
	}
	arguments := make([]any, len(runIDs))
	for index, id := range runIDs {
		arguments[index] = id
	}
	err := repository.transaction(ctx, now, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT run_id FROM acp_observations WHERE run_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(arguments)), ",")+`)`, arguments...)
		if err != nil {
			return errors.New("ACP connection index unavailable")
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return errors.New("ACP connection index unreadable")
			}
			markers[id] = true
		}
		return rows.Err()
	})
	return markers, err
}
