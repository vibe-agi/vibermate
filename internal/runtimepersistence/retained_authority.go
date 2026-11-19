package runtimepersistence

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"time"

	"modernc.org/sqlite"

	"github.com/vibe-agi/vibermate/internal/captureauthority"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

func (repository *captureRunRepository) ProxyValidity(ctx context.Context, digest capturerun.CapabilityDigest, now time.Time) (captureauthority.Validity, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return captureauthority.Validity{}, errors.Join(captureauthority.ErrUnavailable, err)
	}
	defer finish()
	var validity captureauthority.Validity
	var expires int64
	var sessionDigest []byte
	err = authorityReads(repository.database, repository.reads).QueryRowContext(operation, `SELECT run_id,
 COALESCE(runtime_user_id, ''),
 (SELECT token_digest FROM runtime_user_login_sessions WHERE session_id = capture_runs.login_session_id),
 MIN(expires_at_unix_ms, COALESCE((SELECT expires_at_unix_ms FROM runtime_user_login_sessions WHERE session_id = capture_runs.login_session_id), expires_at_unix_ms))
 FROM capture_runs WHERE proxy_capability_hash = ? AND state IN ('created', 'attached') AND expires_at_unix_ms > ? AND `+captureRunIdentityActive,
		digest[:], toUnixMillis(now), toUnixMillis(now)).Scan(&validity.Capture.ID, &validity.UserID, &sessionDigest, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return captureauthority.Validity{}, capturerun.ErrCapabilityRejected
	}
	if err != nil {
		// The read failed; nothing was learned about the capability.
		return captureauthority.Validity{}, errors.Join(captureauthority.ErrUnavailable, err)
	}
	validity.Capture.Kind = captureidentity.KindManagedRun
	validity.CredentialRevision = 1
	validity.ExpiresAt = fromUnixMillis(expires)
	copy(validity.SessionDigest[:], sessionDigest)
	return validity, nil
}

func (repository *manualCaptureRepository) ProxyValidity(ctx context.Context, digest manualcapture.CredentialDigest, now time.Time) (captureauthority.Validity, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return captureauthority.Validity{}, errors.Join(captureauthority.ErrUnavailable, err)
	}
	defer finish()
	var validity captureauthority.Validity
	var expires sql.NullInt64
	err = authorityReads(repository.database, repository.reads).QueryRowContext(operation, `SELECT capture_id, credential_revision, expires_at_unix_ms FROM manual_captures
 WHERE proxy_credential_hash = ? AND state = 'active' AND (expires_at_unix_ms IS NULL OR expires_at_unix_ms > ?)`, digest[:], toUnixMillis(now)).Scan(&validity.Capture.ID, &validity.CredentialRevision, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return captureauthority.Validity{}, manualcapture.ErrCredentialRejected
	}
	if err != nil {
		// The read failed; nothing was learned about the credential.
		return captureauthority.Validity{}, errors.Join(captureauthority.ErrUnavailable, err)
	}
	validity.Capture.Kind = captureidentity.KindManualCapture
	if expires.Valid {
		validity.ExpiresAt = fromUnixMillis(expires.Int64)
	}
	return validity, nil
}

// authorityReads is the connection pool for reads that decide whether an agent
// may open or keep a connection. Production stores give these reads their own
// read-only connections, so they never queue behind a write.
func authorityReads(database, reads *sql.DB) *sql.DB {
	if reads != nil {
		return reads
	}
	return database
}

// authorityReadFailure tells a failed read from a refusal. When the database
// itself failed (busy, locked, I/O, closed, timed out) nothing was learned
// about the credential, so the answer is ErrUnavailable and the agent is told
// to retry. A row that was read but cannot be decoded stays a rejection.
func authorityReadFailure(err, rejected error) error {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) || errors.Is(err, sql.ErrConnDone) ||
		errors.Is(err, driver.ErrBadConn) {
		return errors.Join(captureauthority.ErrUnavailable, err)
	}
	return errors.Join(rejected, err)
}
