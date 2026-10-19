package runtimepersistence

import (
	"context"
	"database/sql"

	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func (r *nativeToolRepository) ListPendingApprovals(ctx context.Context, in nativetool.PendingApprovalRequest) (nativetool.PendingApprovalPage, error) {
	if in.Capture.Validate() != nil || in.Limit < 1 || in.Limit > 199 || in.AfterCreatedAt.IsZero() != (in.AfterID == "") || !in.AfterCreatedAt.IsZero() && (!nativeTime(in.AfterCreatedAt) || !nativeID(in.AfterID)) {
		return nativetool.PendingApprovalPage{}, nativetool.ErrInvalid
	}
	page := nativetool.PendingApprovalPage{Items: []nativetool.PendingApprovalItem{}}
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		after := int64(0)
		if !in.AfterCreatedAt.IsZero() {
			after = in.AfterCreatedAt.UnixMilli()
		}
		rows, err := tx.QueryContext(ctx, `SELECT a.approval_id FROM native_tool_approvals a JOIN native_tool_operations o ON o.operation_id=a.operation_id WHERE o.capture_kind=? AND o.capture_id=? AND a.state='pending' AND (a.created_at_unix_ms>? OR (a.created_at_unix_ms=? AND a.approval_id>?)) ORDER BY a.created_at_unix_ms,a.approval_id LIMIT ?`, in.Capture.Kind, in.Capture.ID, after, after, in.AfterID, in.Limit+1)
		if err != nil {
			return err
		}
		ids := make([]nativetool.ID, 0, in.Limit+1)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			parsed, err := nativetool.NewID(id)
			if err != nil {
				rows.Close()
				return nativetool.ErrInvalid
			}
			ids = append(ids, parsed)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		more := len(ids) > in.Limit
		if more {
			ids = ids[:in.Limit]
		}
		for _, id := range ids {
			a, err := loadNativeApproval(ctx, tx, id)
			if err != nil {
				return err
			}
			op, err := loadNativeOperation(ctx, tx, a.OperationID)
			if err != nil {
				return err
			}
			if a.State != "pending" || op.Capture != in.Capture || op.ApprovalID == nil || *op.ApprovalID != a.ID || op.BindingDigest == nil || *op.BindingDigest != a.BindingDigest {
				return nativetool.ErrInvalid
			}
			page.Items = append(page.Items, nativetool.PendingApprovalItem{Approval: a, Operation: op})
		}
		if more {
			last := page.Items[len(page.Items)-1].Approval
			page.NextCreatedAt = last.CreatedAt
			page.NextID = last.ID
		}
		return nil
	})
	if err != nil {
		return nativetool.PendingApprovalPage{}, err
	}
	return page, nil
}
