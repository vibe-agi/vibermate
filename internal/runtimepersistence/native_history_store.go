package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
)

const nativeHistoryColumns = `operation_id,operation_revision,capture_kind,capture_id,recording_archive_epoch,recording_capture_generation,environment_id,environment_revision,environment_digest,binding_digest,approval_id,recording,retention_days,rpc_kind,tool_name,target_origin,credential_owner,state,decision_source,terminal_reason,created_at_unix_ms,completed_at_unix_ms,bytes_in,bytes_out,event_kind,event_at_unix_ms,maximum_event_at_unix_ms,metadata_expires_at_unix_ms`

func canonicalNativeHistory(h nativetool.RecordedHistory) nativetool.RecordedHistory {
	p := &h.Observation.Operation
	o := &h.Observation
	p.CreatedAt = p.CreatedAt.UTC().Truncate(time.Millisecond)
	p.MaximumEventAt = p.MaximumEventAt.UTC().Truncate(time.Millisecond)
	p.MetadataExpiresAt = p.MetadataExpiresAt.UTC().Truncate(time.Millisecond)
	if p.CompletedAt != nil {
		v := p.CompletedAt.UTC().Truncate(time.Millisecond)
		p.CompletedAt = &v
	}
	o.EventAt = o.EventAt.UTC().Truncate(time.Millisecond)
	o.MaximumEventAt = o.MaximumEventAt.UTC().Truncate(time.Millisecond)
	o.MetadataExpiresAt = o.MetadataExpiresAt.UTC().Truncate(time.Millisecond)
	return h
}
func validateNativeHistory(h nativetool.RecordedHistory) error { return h.Validate() }

func historyNullable[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}
func historyTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return v.UnixMilli()
}
func nativeHistoryArgs(h nativetool.RecordedHistory) []any {
	o, p := h.Observation, h.Observation.Operation
	var binding any
	if p.BindingDigest != nil {
		binding = p.BindingDigest[:]
	}
	return []any{p.ID, p.Revision, p.Capture.Kind, p.Capture.ID, h.Scope.Fence.ArchiveEpoch, h.Scope.Fence.CaptureGeneration, p.EnvironmentID, p.EnvironmentRevision, p.EnvironmentDigest[:], binding, historyNullable(p.ApprovalID), p.Recording.Mode, p.Recording.RetentionDays, p.Kind, p.ToolName, p.TargetOrigin, p.CredentialOwner, p.State, p.DecisionSource, p.Reason, p.CreatedAt.UnixMilli(), historyTime(p.CompletedAt), historyNullable(p.BytesIn), historyNullable(p.BytesOut), o.EventKind, o.EventAt.UnixMilli(), o.MaximumEventAt.UnixMilli(), o.MetadataExpiresAt.UnixMilli()}
}
func scanNativeHistory(row interface{ Scan(...any) error }) (nativetool.RecordedHistory, error) {
	var h nativetool.RecordedHistory
	p := &h.Observation.Operation
	o := &h.Observation
	var epoch, generation, revision, envRevision, retention, created, eventAt, maximum, expires int64
	var digest, binding []byte
	var approval sql.NullString
	var completed, in, out sql.NullInt64
	err := row.Scan(&p.ID, &revision, &p.Capture.Kind, &p.Capture.ID, &epoch, &generation, &p.EnvironmentID, &envRevision, &digest, &binding, &approval, &p.Recording.Mode, &retention, &p.Kind, &p.ToolName, &p.TargetOrigin, &p.CredentialOwner, &p.State, &p.DecisionSource, &p.Reason, &created, &completed, &in, &out, &o.EventKind, &eventAt, &maximum, &expires)
	if err != nil {
		return h, err
	}
	if epoch <= 0 || generation <= 0 || revision <= 0 || envRevision <= 0 || retention < 0 || retention > 3650 || len(digest) != 32 || binding != nil && len(binding) != 32 {
		return h, nativetool.ErrInvalid
	}
	h.Scope = recording.Scope{Capture: p.Capture, Fence: recording.Fence{ArchiveEpoch: uint64(epoch), CaptureGeneration: uint64(generation)}}
	p.RecordingScope = h.Scope
	p.Revision = uint64(revision)
	p.EnvironmentRevision = environment.Revision(envRevision)
	p.Recording.RetentionDays = uint16(retention)
	copy(p.EnvironmentDigest[:], digest)
	if binding != nil {
		v := nativetool.Digest{}
		copy(v[:], binding)
		p.BindingDigest = &v
	}
	if approval.Valid {
		v := nativetool.ID(approval.String)
		p.ApprovalID = &v
	}
	if completed.Valid {
		v := time.UnixMilli(completed.Int64).UTC()
		p.CompletedAt = &v
	}
	if in.Valid {
		v := in.Int64
		p.BytesIn = &v
	}
	if out.Valid {
		v := out.Int64
		p.BytesOut = &v
	}
	p.CreatedAt = time.UnixMilli(created).UTC()
	o.EventAt = time.UnixMilli(eventAt).UTC()
	o.MaximumEventAt = time.UnixMilli(maximum).UTC()
	o.MetadataExpiresAt = time.UnixMilli(expires).UTC()
	p.MaximumEventAt = o.MaximumEventAt
	p.MetadataExpiresAt = o.MetadataExpiresAt
	return h, validateNativeHistory(h)
}
func (s *ObservationStore) PutNativeHistory(ctx context.Context, h nativetool.RecordedHistory) error {
	h = canonicalNativeHistory(h)
	if err := validateNativeHistory(h); err != nil {
		return err
	}
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
	p := h.Observation.Operation
	existing, err := scanNativeHistory(tx.QueryRowContext(operation, `SELECT `+nativeHistoryColumns+` FROM native_history_snapshots WHERE operation_id=? AND operation_revision=?`, p.ID, p.Revision))
	if err == nil {
		if !reflect.DeepEqual(existing, h) {
			return nativetool.ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var originalExpiry int64
	expiryErr := tx.QueryRowContext(operation, `SELECT metadata_expires_at_unix_ms FROM native_history_snapshots WHERE operation_id=? LIMIT 1`, p.ID).Scan(&originalExpiry)
	if expiryErr == nil && originalExpiry != h.Observation.MetadataExpiresAt.UnixMilli() {
		return nativetool.ErrConflict
	}
	if expiryErr != nil && !errors.Is(expiryErr, sql.ErrNoRows) {
		return expiryErr
	}
	if err := publishNativeInspectionIndex(operation, tx, inspectionIdentity(p), h.Scope, p.Revision); err != nil {
		return err
	}
	if _, err := tx.ExecContext(operation, `INSERT INTO native_history_snapshots(`+nativeHistoryColumns+`) VALUES(`+strings.TrimSuffix(strings.Repeat("?,", 28), ",")+`)`, nativeHistoryArgs(h)...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(operation, `INSERT INTO native_history_latest(operation_id,operation_revision) VALUES(?,?) ON CONFLICT(operation_id) DO UPDATE SET operation_revision=MAX(operation_revision,excluded.operation_revision)`, p.ID, p.Revision); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *ObservationStore) GetNativeHistory(ctx context.Context, id nativetool.ID) (nativetool.RecordedHistory, error) {
	if !nativeID(id) {
		return nativetool.RecordedHistory{}, nativetool.ErrInvalid
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return nativetool.RecordedHistory{}, err
	}
	defer finish()
	h, err := scanNativeHistory(s.store.reads.QueryRowContext(operation, `SELECT `+nativeHistoryColumns+` FROM native_history_snapshots WHERE operation_id=? AND operation_revision=(SELECT operation_revision FROM native_history_latest WHERE operation_id=?) AND metadata_expires_at_unix_ms>?`, id, id, time.Now().UTC().UnixMilli()))
	if errors.Is(err, sql.ErrNoRows) {
		err = nativetool.ErrNotFound
	}
	return h, err
}
func inspectionIdentity(p nativetool.Operation) nativetool.InspectionIdentity {
	return nativetool.InspectionIdentity{OperationID: p.ID, Capture: p.Capture, EnvironmentID: p.EnvironmentID, EnvironmentRevision: p.EnvironmentRevision, EnvironmentDigest: p.EnvironmentDigest, Kind: p.Kind, TargetOrigin: p.TargetOrigin, Recording: p.Recording, CreatedAt: p.CreatedAt}
}

const nativeIndexColumns = `position,operation_id,capture_kind,capture_id,recording_archive_epoch,recording_capture_generation,environment_id,environment_revision,environment_digest,rpc_kind,target_origin,recording,retention_days,created_at_unix_ms,history_revision`

func scanNativeIndex(row interface{ Scan(...any) error }) (nativetool.InspectionIndexEntry, recording.Scope, error) {
	var e nativetool.InspectionIndexEntry
	var scope recording.Scope
	p := &e.Identity
	var digest []byte
	var epoch, generation, revision, retention, created, history int64
	err := row.Scan(&e.Position, &p.OperationID, &p.Capture.Kind, &p.Capture.ID, &epoch, &generation, &p.EnvironmentID, &revision, &digest, &p.Kind, &p.TargetOrigin, &p.Recording.Mode, &retention, &created, &history)
	if err != nil {
		return e, scope, err
	}
	if e.Position <= 0 || epoch <= 0 || generation <= 0 || revision <= 0 || retention < 0 || retention > 3650 || len(digest) != 32 || history < 0 {
		return e, scope, nativetool.ErrInvalid
	}
	copy(p.EnvironmentDigest[:], digest)
	p.EnvironmentRevision = environment.Revision(revision)
	p.Recording.RetentionDays = uint16(retention)
	p.CreatedAt = time.UnixMilli(created).UTC()
	e.HistoryRevision = uint64(history)
	scope = recording.Scope{Capture: p.Capture, Fence: recording.Fence{ArchiveEpoch: uint64(epoch), CaptureGeneration: uint64(generation)}}
	e.Scope = scope
	return e, scope, validateNativeInspectionIdentity(e.Identity, scope)
}

// The caller owns the same observation transaction as its snapshot/body graph.
// revision zero is content-only publication: it cannot invent or lower state.
// No live control lookup is permitted here. Identical retry detection belongs
// to the graph writer before calling this helper (and thus does not republish).
func publishNativeInspectionIndex(ctx context.Context, tx *sql.Tx, p nativetool.InspectionIdentity, scope recording.Scope, revision uint64) error {
	if err := validateNativeInspectionIdentity(p, scope); err != nil {
		return err
	}
	if revision > math.MaxInt64 {
		return nativetool.ErrInvalid
	}
	existing, previous, err := scanNativeIndex(tx.QueryRowContext(ctx, `SELECT `+nativeIndexColumns+` FROM native_inspection_index WHERE operation_id=?`, p.OperationID))
	if err == nil && (existing.Identity != p || previous != scope) {
		return nativetool.ErrConflict
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO native_inspection_index(operation_id,capture_kind,capture_id,recording_archive_epoch,recording_capture_generation,environment_id,environment_revision,environment_digest,rpc_kind,target_origin,recording,retention_days,created_at_unix_ms,history_revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(operation_id) DO UPDATE SET position=excluded.position,history_revision=MAX(history_revision,excluded.history_revision)`, p.OperationID, p.Capture.Kind, p.Capture.ID, scope.Fence.ArchiveEpoch, scope.Fence.CaptureGeneration, p.EnvironmentID, p.EnvironmentRevision, p.EnvironmentDigest[:], p.Kind, p.TargetOrigin, p.Recording.Mode, p.Recording.RetentionDays, p.CreatedAt.UnixMilli(), revision)
	return err
}
func (s *ObservationStore) PageNativeInspection(ctx context.Context, q nativetool.InspectionIndexRequest) (nativetool.InspectionIndexPage, error) {
	page := nativetool.InspectionIndexPage{Items: []nativetool.InspectionIndexEntry{}}
	if q.Capture.Validate() != nil || q.AfterPosition < 0 || q.Limit < 1 || q.Limit > 200 {
		return page, nativetool.ErrInvalid
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return page, err
	}
	defer finish()
	rows, err := s.store.reads.QueryContext(operation, `SELECT `+nativeIndexColumns+` FROM native_inspection_index WHERE capture_kind=? AND capture_id=? AND position>? AND EXISTS(SELECT 1 FROM native_history_snapshots h WHERE h.operation_id=native_inspection_index.operation_id AND h.metadata_expires_at_unix_ms>?) ORDER BY position LIMIT ?`, q.Capture.Kind, q.Capture.ID, q.AfterPosition, time.Now().UTC().UnixMilli(), q.Limit)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		e, scope, err := scanNativeIndex(rows)
		if err != nil || scope.Capture != q.Capture {
			return page, nativetool.ErrInvalid
		}
		page.Items = append(page.Items, e)
		page.NextPosition = e.Position
	}
	return page, rows.Err()
}

var _ nativetool.HistoryWriter = (*ObservationStore)(nil)

func validateNativeInspectionIdentity(p nativetool.InspectionIdentity, scope recording.Scope) error {
	if scope.Validate() != nil || scope.Capture != p.Capture || !nativeID(p.OperationID) || p.EnvironmentRevision == 0 || p.EnvironmentRevision > math.MaxInt64 || p.EnvironmentDigest == (environment.CandidateDigest{}) || p.Kind.Validate() != nil || p.Recording.Validate() != nil || !nativeTime(p.CreatedAt) {
		return nativetool.ErrInvalid
	}
	if _, err := environment.NewEnvironmentID(string(p.EnvironmentID)); err != nil {
		return nativetool.ErrInvalid
	}
	origin, err := originidentity.ParseProviderOrigin(p.TargetOrigin)
	if err != nil || len(p.TargetOrigin) > 1024 || origin.Scheme() != "https" || origin.BasePath() != "" {
		return nativetool.ErrInvalid
	}

	return nil
}
