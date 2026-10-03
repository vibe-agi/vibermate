package runtimepersistence

import (
	"context"
	"database/sql"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

func nativeContentPart(part string) bool { return part == "tool_arguments" || part == "tool_result" }

// The authenticated profile producer must suppress its recognized sensitive
// fields before this trusted-Go API. Only authorized tools/call payload parts
// have storage rows; callbacks/auth/proof have no content arm.
func (r *nativeToolRepository) PutContent(ctx context.Context, id nativetool.ID, part string, body []byte, now time.Time) error {
	if !nativeID(id) || !nativeContentPart(part) || len(body) == 0 || len(body) > 16<<20 || !nativeTime(now) {
		return nativetool.ErrInvalid
	}
	return r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		op, err := loadNativeOperation(ctx, tx, id)
		if err != nil {
			return err
		}
		if op.Kind != nativetool.RequestToolsCall || op.Recording.Mode != environment.ContentRecordingFull || (op.State != "started" && op.State != "completed") {
			return nativetool.ErrDenied
		}
		if _, _, _, err := nativeExecutionAuthority(ctx, tx, op, now); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, id).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return nativetool.ErrDenied
		}
		expires := op.CreatedAt.Add(time.Duration(op.Recording.RetentionDays) * 24 * time.Hour)
		if !expires.After(now) {
			return nativetool.ErrDenied
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_contents WHERE operation_id=? AND part=?`, id, part).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return nativetool.ErrConflict
		}
		digest, err := storeEvidenceBody(ctx, tx, body)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO native_tool_contents(operation_id,part,body_digest,expires_at_unix_ms) VALUES(?,?,?,?)`, id, part, digest, expires.UnixMilli())
		return err
	})
}

func (r *nativeToolRepository) ReadContent(ctx context.Context, c captureidentity.Reference, id nativetool.ID, part string, offset, limit int) ([]byte, error) {
	if c.Validate() != nil || !nativeID(id) || !nativeContentPart(part) || offset < 0 || limit <= 0 || limit > 128<<10 {
		return nil, nativetool.ErrInvalid
	}
	var result []byte
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		op, err := loadNativeOperation(ctx, tx, id)
		if err != nil {
			return err
		}
		if op.Capture != c || op.Kind != nativetool.RequestToolsCall || op.Recording.Mode != environment.ContentRecordingFull {
			return nativetool.ErrDenied
		}
		var digest []byte
		var expires int64
		if err := tx.QueryRowContext(ctx, `SELECT body_digest,expires_at_unix_ms FROM native_tool_contents WHERE operation_id=? AND part=?`, id, part).Scan(&digest, &expires); err != nil {
			return nativetool.ErrNotFound
		}
		if len(digest) != 32 || expires != op.CreatedAt.Add(time.Duration(op.Recording.RetentionDays)*24*time.Hour).UnixMilli() {
			return nativetool.ErrInvalid
		}
		if expires <= time.Now().UTC().UnixMilli() {
			return nativetool.ErrDenied
		}
		body, err := loadEvidenceBody(ctx, tx, digest)
		if err != nil {
			return err
		}
		if len(body) > 16<<20 || offset > len(body) {
			return nativetool.ErrInvalid
		}
		end := len(body)
		if limit < end-offset {
			end = offset + limit
		}
		result = append([]byte{}, body[offset:end]...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func deleteNativeContents(ctx context.Context, tx *sql.Tx, where string, args ...any) (int64, error) {
	rows, err := tx.QueryContext(ctx, `DELETE FROM native_tool_contents WHERE `+where+` RETURNING body_digest`, args...)
	if err != nil {
		return 0, err
	}
	var digests []string
	var count int64
	for rows.Next() {
		var digest []byte
		if err := rows.Scan(&digest); err != nil {
			rows.Close()
			return 0, err
		}
		digests = append(digests, string(digest))
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, digest := range uniqueStrings(digests) {
		if err := releaseEvidenceBody(ctx, tx, []byte(digest)); err != nil {
			return 0, err
		}
	}
	return count, nil
}

func purgeExpiredNative(ctx context.Context, tx *sql.Tx, now time.Time, limit int) (resourcedeletion.Released, bool, error) {
	var released resourcedeletion.Released
	content, err := deleteNativeContents(ctx, tx, `(operation_id,part) IN(SELECT operation_id,part FROM native_tool_contents WHERE expires_at_unix_ms<=? ORDER BY expires_at_unix_ms LIMIT ?)`, now.UnixMilli(), limit)
	if err != nil {
		return released, false, err
	}
	// Remove safe metadata only after a terminal; inbound/once identity and the
	// scope barrier survive independently of operation/content retention. Full
	// contents keep the history needed to read them until their own frozen expiry.
	// Deleting expired contents cannot change this selector's eligibility, so the
	// same deterministic bounded set is used for every dependent cleanup below.
	selected := `SELECT o.operation_id FROM native_tool_operations o
	 WHERE o.metadata_expires_at_unix_ms<=? AND o.completed_at_unix_ms IS NOT NULL
	 AND NOT EXISTS(SELECT 1 FROM native_tool_contents c
	   WHERE c.operation_id=o.operation_id AND c.expires_at_unix_ms>?)
	 ORDER BY o.metadata_expires_at_unix_ms,o.operation_id LIMIT ?`
	arguments := []any{now.UnixMilli(), now.UnixMilli(), limit}
	if _, err := deleteNativeContents(ctx, tx, `operation_id IN(`+selected+`)`, arguments...); err != nil {
		return released, false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_approvals WHERE operation_id IN(`+selected+`)`, arguments...).Scan(&released.Approvals); err != nil {
		return released, false, err
	}
	attempts, err := deleteCounted(ctx, tx, `DELETE FROM runtime_egress_attempts WHERE purpose='client_tool_service' AND parent_kind='client_operation' AND parent_id IN(`+selected+`) AND completed_at_unix_ms IS NOT NULL`, arguments...)
	if err != nil {
		return released, false, err
	}
	released.Attempts = attempts
	result, err := tx.ExecContext(ctx, `DELETE FROM native_tool_operations WHERE operation_id IN(`+selected+`)`, arguments...)
	if err != nil {
		return released, false, err
	}
	metadata, err := result.RowsAffected()
	return released, limit > 0 && (content == int64(limit) || metadata == int64(limit)), err
}
