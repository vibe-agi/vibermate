package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/recording"
)

type recordingDenialCertificate struct {
	count uint64
	hash  [32]byte
}

func validRecordingDenial(kind, id string) bool {
	return (kind == "usage" || kind == "attempt" || kind == "connection" || kind == "exchange") && len(id) > 0 && len(id) <= 512 && utf8.ValidString(id) && strings.TrimSpace(id) == id
}
func nextRecordingDenialHash(previous [32]byte, kind, id string) [32]byte {
	h := sha256.New()
	h.Write(previous[:])
	var length [8]byte
	for _, part := range []string{kind, id} {
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		h.Write(length[:])
		h.Write([]byte(part))
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Linear, streaming verification bounds memory independently of retained IDs.
// The reliable certificate names a prefix; an uncertified tail is conservative
// negative state only and can never authorize destructive source cleanup.
func readRecordingDenialCoverage(ctx context.Context, tx *sql.Tx, alias string, expected recording.InspectionStamp) (certificate recordingDenialCertificate, result error) {
	defer func() {
		if result != nil && !errors.Is(result, context.Canceled) && !errors.Is(result, context.DeadlineExceeded) {
			result = errors.Join(ErrRecordingPrivacyUnavailable, result)
		}
	}()
	rows, err := tx.QueryContext(ctx, `SELECT sequence,CASE WHEN length(kind)<=16 THEN kind END,CASE WHEN length(CAST(identity AS BLOB))<=512 THEN identity END,CASE WHEN length(chain_hash)=32 THEN chain_hash END FROM `+alias+`.recording_denials ORDER BY sequence`)
	if err != nil {
		return certificate, errors.Join(ErrInspectionUnavailable, err)
	}
	defer rows.Close()
	matched := expected.DenialCount == 0 && expected.DenialHash == ([32]byte{})
	for rows.Next() {
		var sequence uint64
		var kind, id string
		var stored []byte
		if err = rows.Scan(&sequence, &kind, &id, &stored); err != nil {
			return certificate, errors.Join(ErrInspectionUnavailable, err)
		}
		if sequence != certificate.count+1 || !validRecordingDenial(kind, id) || len(stored) != 32 {
			return certificate, ErrInspectionUnavailable
		}
		next := nextRecordingDenialHash(certificate.hash, kind, id)
		if string(stored) != string(next[:]) {
			return certificate, ErrInspectionUnavailable
		}
		certificate = recordingDenialCertificate{sequence, next}
		if sequence == expected.DenialCount {
			matched = next == expected.DenialHash
		}
	}
	if err = rows.Err(); err != nil {
		return certificate, errors.Join(ErrInspectionUnavailable, err)
	}
	if !matched {
		return certificate, ErrInspectionUnavailable
	}
	return certificate, nil
}

func (s *ObservationStore) verifyRecordingDenialCoverage(ctx context.Context, expected recording.InspectionStamp) error {
	tx, err := s.store.reads.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = readRecordingDenialCoverage(ctx, tx, "main", expected); err != nil {
		return err
	}
	return tx.Commit()
}

// DenyRecordingIdentities is a maintenance-only operation under actual source
// exclusion, never an ordinary sink callback. Its <=256 IDs come from retained
// proof roots selected before destruction. The FULL-synced history transaction
// precedes a separate FULL-synced fixed-size control certificate. A crash in the
// gap leaves only conservative denial; source roots must still be retained.
func (s *ObservationStore) DenyRecordingIdentities(ctx context.Context, control *ControlStore, kind string, ids []string) error {
	if len(ids) == 0 || len(ids) > 256 {
		return ErrInspectionSourceLimit
	}
	for _, id := range ids {
		if !validRecordingDenial(kind, id) {
			return ErrInspectionUnavailable
		}
	}
	return s.certifyRecordingIdentities(ctx, control, kind, ids)
}

// CertifyRecordingDenialTail is maintenance-only recovery. A failed required
// CAS may leave a conservative FULL-committed tail that selectors now skip.
// Verify the whole chain and require that tail in control before destroying
// any roots, even when retry selected no new IDs. All history SQL returns
// before the fixed-size reliable CAS; failure is never treated as success.
func (s *ObservationStore) CertifyRecordingDenialTail(ctx context.Context, control *ControlStore) error {
	return s.certifyRecordingIdentities(ctx, control, "", nil)
}

func (s *ObservationStore) certifyRecordingIdentities(ctx context.Context, control *ControlStore, kind string, ids []string) error {
	snapshot, err := control.ReadInspectionSnapshot(ctx, nil)
	if err != nil {
		return err
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	var certificate recordingDenialCertificate
	err = fullSynchronousTransaction(operation, s.store.database, func(tx *sql.Tx) error {
		var err error
		certificate, err = readRecordingDenialCoverage(operation, tx, "main", snapshot.Stamp)
		if err != nil {
			return err
		}
		for _, id := range ids {
			var present bool
			if err = tx.QueryRowContext(operation, `SELECT EXISTS(SELECT 1 FROM recording_denials WHERE kind=? AND identity=?)`, kind, id).Scan(&present); err != nil {
				return err
			}
			if present {
				continue
			}
			if certificate.count == math.MaxInt64 {
				return ErrInspectionUnavailable
			}
			certificate.hash = nextRecordingDenialHash(certificate.hash, kind, id)
			certificate.count++
			if _, err = tx.ExecContext(operation, `INSERT INTO recording_denials VALUES(?,?,?,?)`, certificate.count, kind, id, certificate.hash[:]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// All history SQL has returned before obtaining any required control owner.
	return control.certifyRecordingDenials(ctx, snapshot.Stamp, certificate)
}
func (s *ControlStore) certifyRecordingDenials(ctx context.Context, previous recording.InspectionStamp, next recordingDenialCertificate) error {
	if next.count < previous.DenialCount {
		return ErrInspectionUnavailable
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	return fullSynchronousTransaction(operation, s.store.database, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(operation, `UPDATE recording_denial_coverage SET identity_count=?,chain_hash=? WHERE singleton=1 AND identity_count=? AND chain_hash=?`, next.count, next.hash[:], previous.DenialCount, previous.DenialHash[:])
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrInspectionChanged
		}
		if next.count != previous.DenialCount {
			_, err = tx.ExecContext(operation, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1 WHERE singleton=1`)
		}
		return err
	})
}

func fullSynchronousTransaction(ctx context.Context, db *sql.DB, work func(*sql.Tx) error) (result error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `PRAGMA synchronous=FULL`); err != nil {
		return err
	}
	// Both writer pools have one connection. Restore its normal ordinary-write
	// policy only after real commit/rollback; no other role is attached.
	defer func() {
		_, err := conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA synchronous=NORMAL`)
		result = errors.Join(result, err)
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = work(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("certify recording privacy state: %w", err)
	}
	return nil
}

// installRecordingWriterCoverage binds admission to this exact SQL connection.
// Other connections cannot alter TEMP state. Its mutation-version guard detects
// external SQL changes even if they also downgrade main-file metadata; the
// connector refuses replacement rather than silently losing these guards.
func installRecordingWriterCoverage(ctx context.Context, tx *sql.Tx, expected recording.InspectionStamp) error {
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS recording_writer_coverage(identity_count INTEGER NOT NULL,chain_hash BLOB NOT NULL,data_version INTEGER NOT NULL) STRICT;DELETE FROM temp.recording_writer_coverage`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO temp.recording_writer_coverage SELECT ?,?,data_version FROM pragma_data_version`, expected.DenialCount, expected.DenialHash[:]); err != nil {
		return err
	}
	for _, table := range []string{"runtime_activities", "runtime_connection_events", "runtime_egress_attempts", "runtime_exchange_agent_identities", "runtime_usage_observations", "runtime_raw_evidence_envelopes", "runtime_exchange_contents", "native_history_snapshots", "native_inspection_index", "native_tool_contents", "capture_observations", "acp_snapshots", "runtime_raw_inspection_reveal_audits"} {
		for _, action := range []string{"INSERT", "UPDATE"} {
			query := `CREATE TEMP TRIGGER IF NOT EXISTS recording_owned_` + table + `_` + strings.ToLower(action) + ` BEFORE ` + action + ` ON main.` + table + `
 WHEN NOT EXISTS(SELECT 1 FROM recording_writer_coverage c WHERE c.data_version=(SELECT data_version FROM pragma_data_version)
 AND (c.identity_count=0 OR EXISTS(SELECT 1 FROM recording_denials d WHERE d.sequence=c.identity_count AND d.chain_hash=c.chain_hash)))
 BEGIN SELECT RAISE(ABORT,'recording_scope_discarded'); END`
			if _, err := tx.ExecContext(ctx, query); err != nil {
				return err
			}
		}
	}
	return nil
}

var ErrRecordingPrivacyUnavailable = errors.New("recording privacy coverage is unavailable")

// CheckRecordingWriter is a constant-size check of the exact owned SQL writer.
// It never consults control and cannot repair or adopt unexpected source edits.
func (s *ObservationStore) CheckRecordingWriter(ctx context.Context) error {
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	var valid bool
	err = s.store.database.QueryRowContext(operation, `SELECT EXISTS(SELECT 1 FROM temp.recording_writer_coverage c WHERE c.data_version=(SELECT data_version FROM pragma_data_version) AND (c.identity_count=0 OR EXISTS(SELECT 1 FROM recording_denials d WHERE d.sequence=c.identity_count AND d.chain_hash=c.chain_hash)))`).Scan(&valid)
	if operation.Err() != nil {
		return operation.Err()
	}
	if err != nil || !valid {
		return errors.Join(ErrInspectionUnavailable, ErrRecordingPrivacyUnavailable, err)
	}
	return nil
}
