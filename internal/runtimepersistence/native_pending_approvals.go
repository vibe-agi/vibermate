package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func (r *nativeToolRepository) ListPendingApprovals(ctx context.Context, in nativetool.PendingApprovalRequest) (nativetool.PendingApprovalPage, error) {
	if in.Capture.Validate() != nil || in.Limit < 1 || in.Limit > 199 || in.AfterCreatedAt.IsZero() != (in.AfterID == "") || !in.AfterCreatedAt.IsZero() && (!nativeTime(in.AfterCreatedAt) || !nativeID(in.AfterID)) {
		return nativetool.PendingApprovalPage{}, nativetool.ErrInvalid
	}
	var page nativetool.PendingApprovalPage
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		after := int64(0)
		if !in.AfterCreatedAt.IsZero() {
			after = in.AfterCreatedAt.UnixMilli()
		}
		rows, err := tx.QueryContext(ctx, `SELECT a.approval_id FROM native_tool_approvals a JOIN native_tool_operations o ON o.operation_id=a.operation_id WHERE o.capture_kind=? AND o.capture_id=? AND a.state='pending' AND (a.created_at_unix_ms>? OR (a.created_at_unix_ms=? AND a.approval_id>?)) ORDER BY a.created_at_unix_ms,a.approval_id LIMIT ?`, in.Capture.Kind, in.Capture.ID, after, after, in.AfterID, in.Limit+1)
		if err != nil {
			return err
		}
		page, err = loadNativePendingPage(ctx, tx, rows, in.Limit, &in.Capture)
		return err
	})
	if err != nil {
		return nativetool.PendingApprovalPage{}, err
	}
	return page, nil
}

// ListAllPendingApprovals pages every Capture's waiting questions, oldest
// first: the longest-blocked agent leads. It reads on the screen-read pool, so
// listing never queues behind, or holds up, the writer that forwards traffic.
func (r *nativeToolRepository) ListAllPendingApprovals(ctx context.Context, in nativetool.AllPendingApprovalRequest) (nativetool.PendingApprovalPage, error) {
	if in.Limit < 1 || in.Limit > 199 || !nativeTime(in.ObservedAt) || in.AfterCreatedAt.IsZero() != (in.AfterID == "") || !in.AfterCreatedAt.IsZero() && (!nativeTime(in.AfterCreatedAt) || !nativeID(in.AfterID)) {
		return nativetool.PendingApprovalPage{}, nativetool.ErrInvalid
	}
	var page nativetool.PendingApprovalPage
	err := r.readTransaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		after := int64(0)
		if !in.AfterCreatedAt.IsZero() {
			after = in.AfterCreatedAt.UnixMilli()
		}
		// native_tool_approvals_state_created serves this range. An expired
		// question can stay pending until something answers it; skipping it
		// here keeps the oldest-first pages from filling with dead questions.
		rows, err := tx.QueryContext(ctx, `SELECT approval_id FROM native_tool_approvals WHERE state='pending' AND expires_at_unix_ms>? AND (created_at_unix_ms>? OR (created_at_unix_ms=? AND approval_id>?)) ORDER BY created_at_unix_ms,approval_id LIMIT ?`, in.ObservedAt.UnixMilli(), after, after, in.AfterID, in.Limit+1)
		if err != nil {
			return err
		}
		page, err = loadNativePendingPage(ctx, tx, rows, in.Limit, nil)
		return err
	})
	if err != nil {
		return nativetool.PendingApprovalPage{}, err
	}
	return page, nil
}

// loadNativePendingPage closes rows. When capture is set, every item must
// belong to it.
func loadNativePendingPage(ctx context.Context, tx *sql.Tx, rows *sql.Rows, limit int, capture *captureidentity.Reference) (nativetool.PendingApprovalPage, error) {
	page := nativetool.PendingApprovalPage{Items: []nativetool.PendingApprovalItem{}}
	ids := make([]nativetool.ID, 0, limit+1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return page, err
		}
		parsed, err := nativetool.NewID(id)
		if err != nil {
			rows.Close()
			return page, nativetool.ErrInvalid
		}
		ids = append(ids, parsed)
	}
	err := rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	for _, id := range ids {
		a, err := loadNativeApproval(ctx, tx, id)
		if err != nil {
			return page, err
		}
		op, err := loadNativeOperation(ctx, tx, a.OperationID)
		if err != nil {
			return page, err
		}
		if a.State != "pending" || capture != nil && op.Capture != *capture || op.ApprovalID == nil || *op.ApprovalID != a.ID || op.BindingDigest == nil || *op.BindingDigest != a.BindingDigest {
			return page, nativetool.ErrInvalid
		}
		page.Items = append(page.Items, nativetool.PendingApprovalItem{Approval: a, Operation: op})
	}
	if more {
		last := page.Items[len(page.Items)-1].Approval
		page.NextCreatedAt = last.CreatedAt
		page.NextID = last.ID
	}
	return page, nil
}

// readTransaction gives fn one read-only snapshot on the read pool, admitted
// like every other Store operation. Without a read pool it uses the writer.
func (r *nativeToolRepository) readTransaction(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	if r.reads == nil {
		return r.transaction(ctx, fn)
	}
	permit, err := r.operations.admit(ctx)
	if err != nil {
		return errors.Join(nativetool.ErrUnavailable, err)
	}
	defer permit.finish()
	tx, err := r.reads.BeginTx(permit.context, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return errors.Join(nativetool.ErrUnavailable, err, context.Cause(permit.context))
	}
	defer tx.Rollback()
	if err = fn(permit.context, tx); err != nil {
		err = errors.Join(err, context.Cause(permit.context))
		for _, known := range []error{nativetool.ErrInvalid, nativetool.ErrNotFound, nativetool.ErrUnavailable} {
			if errors.Is(err, known) {
				return err
			}
		}
		return errors.Join(nativetool.ErrUnavailable, err)
	}
	return nil
}
