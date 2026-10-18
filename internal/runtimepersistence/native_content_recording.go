package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

const nativeContentColumns = `operation_id,part,capture_kind,capture_id,recording_archive_epoch,recording_capture_generation,environment_id,environment_revision,environment_digest,rpc_kind,tool_name,target_origin,credential_owner,recording,retention_days,created_at_unix_ms,expires_at_unix_ms,observed_at_unix_ms,body_digest`

func nativeContentValues(v nativetool.ContentObservation, digest []byte) []any {
	i := v.Identity
	return []any{i.OperationID, v.Part, i.Scope.Capture.Kind, i.Scope.Capture.ID, i.Scope.Fence.ArchiveEpoch, i.Scope.Fence.CaptureGeneration, i.EnvironmentID, i.EnvironmentRevision, i.EnvironmentDigest[:], i.Kind, i.ToolName, i.TargetOrigin, i.CredentialOwner, i.Recording.Mode, i.Recording.RetentionDays, i.CreatedAt.UnixMilli(), i.ExpiresAt.UnixMilli(), v.ObservedAt.UnixMilli(), digest}
}
func scanNativeContent(row interface{ Scan(...any) error }) (nativetool.ContentObservation, []byte, error) {
	var v nativetool.ContentObservation
	i := &v.Identity
	var environmentDigest, bodyDigest []byte
	var created, expires, observed int64
	err := row.Scan(&i.OperationID, &v.Part, &i.Scope.Capture.Kind, &i.Scope.Capture.ID, &i.Scope.Fence.ArchiveEpoch, &i.Scope.Fence.CaptureGeneration, &i.EnvironmentID, &i.EnvironmentRevision, &environmentDigest, &i.Kind, &i.ToolName, &i.TargetOrigin, &i.CredentialOwner, &i.Recording.Mode, &i.Recording.RetentionDays, &created, &expires, &observed, &bodyDigest)
	if err != nil {
		return v, nil, err
	}
	if len(environmentDigest) != 32 || len(bodyDigest) != 32 {
		return v, nil, nativetool.ErrInvalid
	}
	copy(i.EnvironmentDigest[:], environmentDigest)
	i.CreatedAt = time.UnixMilli(created).UTC()
	i.ExpiresAt = time.UnixMilli(expires).UTC()
	v.ObservedAt = time.UnixMilli(observed).UTC()
	if i.Validate() != nil || !nativeContentPart(string(v.Part)) || v.ObservedAt.Before(i.CreatedAt) {
		return v, nil, nativetool.ErrInvalid
	}
	return v, bodyDigest, nil
}

// PutNativeContent publishes one self-contained immutable envelope/body graph
// with discovery in the same observation transaction. No authority lookup,
// history predecessor, session, lease or control foreign key is involved.
func (s *ObservationStore) PutNativeContent(ctx context.Context, v nativetool.ContentObservation) (writeErr error) {
	defer func() { writeErr = recordingWriteError(writeErr) }()
	if v.Identity.Validate() != nil || !nativeContentPart(string(v.Part)) || len(v.Body) == 0 || len(v.Body) > 16<<20 || v.ObservedAt.Before(v.Identity.CreatedAt) {
		return nativetool.ErrInvalid
	}
	if !v.Identity.ExpiresAt.After(time.Now()) {
		return nativetool.ErrNotFound
	}
	v.Identity.CreatedAt = time.UnixMilli(v.Identity.CreatedAt.UnixMilli()).UTC()
	v.Identity.ExpiresAt = time.UnixMilli(v.Identity.ExpiresAt.UnixMilli()).UTC()
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	tx, err := s.store.database.BeginTx(operation, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	existing, _, err := scanNativeContent(tx.QueryRowContext(operation, `SELECT `+nativeContentColumns+` FROM native_tool_contents WHERE operation_id=? LIMIT 1`, v.Identity.OperationID))
	if err == nil && existing.Identity != v.Identity {
		return nativetool.ErrConflict
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var previous []byte
	err = tx.QueryRowContext(operation, `SELECT body_digest FROM native_tool_contents WHERE operation_id=? AND part=?`, v.Identity.OperationID, v.Part).Scan(&previous)
	if err == nil {
		digest := sha256.Sum256(v.Body)
		if !bytes.Equal(previous, digest[:]) {
			return nativetool.ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	digest, err := storeEvidenceBody(operation, tx, v.Body)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(operation, `INSERT INTO native_tool_contents(`+nativeContentColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, nativeContentValues(v, digest)...)
	if err != nil {
		return err
	}
	if err := publishNativeInspectionIndex(operation, tx, v.Identity.InspectionIdentity(), v.Identity.Scope, 0); err != nil {
		return err
	}
	return tx.Commit()
}

// GetNativeContent is an optional inspection read. Lifecycle remains explicitly
// unknown if no independent history snapshot survived. Source composition and
// durable deletion/visibility overlays are installed by the following tasks.
func (s *ObservationStore) GetNativeContent(ctx context.Context, id nativetool.ID, part nativetool.ContentPart) (nativetool.ContentObservation, error) {
	if !nativeID(id) || !nativeContentPart(string(part)) {
		return nativetool.ContentObservation{}, nativetool.ErrInvalid
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return nativetool.ContentObservation{}, err
	}
	defer finish()
	tx, err := s.store.reads.BeginTx(operation, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nativetool.ContentObservation{}, err
	}
	defer tx.Rollback()
	v, digest, err := scanNativeContent(tx.QueryRowContext(operation, `SELECT `+nativeContentColumns+` FROM native_tool_contents WHERE operation_id=? AND part=? AND expires_at_unix_ms>?`, id, part, time.Now().UnixMilli()))
	if errors.Is(err, sql.ErrNoRows) {
		return v, nativetool.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.Body, err = loadEvidenceBody(operation, tx, digest)
	if err != nil {
		return nativetool.ContentObservation{}, err
	}
	if len(v.Body) == 0 || len(v.Body) > 16<<20 {
		return nativetool.ContentObservation{}, nativetool.ErrInvalid
	}
	if err := tx.Commit(); err != nil {
		return nativetool.ContentObservation{}, err
	}
	return v, nil
}
func (s *ObservationStore) ReadNativeContent(ctx context.Context, c captureidentity.Reference, id nativetool.ID, part string, offset, limit int) ([]byte, error) {
	if c.Validate() != nil || offset < 0 || limit < 1 || limit > 128<<10 {
		return nil, nativetool.ErrInvalid
	}
	v, err := s.GetNativeContent(ctx, id, nativetool.ContentPart(part))
	if err != nil {
		return nil, err
	}
	if v.Identity.Scope.Capture != c {
		return nil, nativetool.ErrDenied
	}
	if offset > len(v.Body) {
		return nil, nativetool.ErrInvalid
	}
	return bytes.Clone(v.Body[offset:min(len(v.Body), offset+limit)]), nil
}

var _ nativetool.ContentWriter = (*ObservationStore)(nil)
