package runtimepersistence

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// RecordingCleanupNeeded can prove a routine pass has no work. It never
// acknowledges an existing claim. A caller must conservatively schedule work
// when this bounded inspection cannot prove absence (including source limits).
func RecordingCleanupNeeded(ctx context.Context, control *ControlStore, catalog *InspectionCatalog, cutoff time.Time) (bool, error) {
	if ctx == nil || control == nil || catalog == nil || cutoff.IsZero() {
		return true, ErrInspectionUnavailable
	}
	work, cancel := context.WithTimeout(ctx, catalog.timeout)
	defer cancel()
	boundary := NewInspectionBoundary(control, catalog)
	token, err := boundary.Begin(work)
	if err != nil {
		return true, err
	}
	// Unknown legacy required approvals may still need ownership inference.
	// Only the destructive owner is allowed to perform that inference/removal.
	approvals, err := control.cleanupApprovalPage(work, "")
	if err != nil || len(approvals) != 0 {
		return true, err
	}
	lease, err := catalog.acquire(work)
	if err != nil {
		return true, err
	}
	defer lease.close()
	if lease.version != token.Sources {
		return true, ErrInspectionChanged
	}
	tx, err := lease.conn.BeginTx(lease.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	mirror := fmt.Sprintf("s%d", len(lease.sources)-1)
	if _, err = readInspectionMirror(lease.ctx, tx, mirror, token.Control); err != nil {
		return true, err
	}
	for ordinal, source := range lease.sources {
		alias := fmt.Sprintf("s%d", ordinal)
		roots := recordingCleanupVerificationRoots(source, mirror, cutoff)
		roots = append(roots, recordingCleanupRoot{table: "runtime_egress_attempts", where: cleanupAuditLinkPredicate(mirror)})
		if !source.Current {
			// Conservatively retain the full approval classifier when any legacy
			// tool-intent row exists; do not approximate its ownership semantics.
			roots = append(roots, recordingCleanupRoot{table: "tool_approvals", where: `r.kind='tool_intent'`})
			if publishedNativeRevision(source.Database.Revision) {
				roots = append(roots, recordingCleanupRoot{table: "native_tool_once", where: cleanupLegacyOncePredicate(mirror)})
			}
		}
		for _, root := range roots {
			var exists bool
			predicate := strings.ReplaceAll(root.where, "main.", alias+".")
			if err = tx.QueryRowContext(lease.ctx, `SELECT EXISTS(SELECT 1 FROM `+alias+`.`+root.table+` r WHERE `+predicate+`)`).Scan(&exists); err != nil || exists {
				return true, err
			}
		}
		query, args := cleanupUsageSelection(source, mirror, cutoff, "")
		query = strings.ReplaceAll(query, "main.", alias+".")
		var exists bool
		if err = tx.QueryRowContext(lease.ctx, `SELECT EXISTS(`+query+`)`, args...).Scan(&exists); err != nil || exists {
			return true, err
		}
	}
	if err = tx.Rollback(); err != nil {
		return true, err
	}
	if err = boundary.Check(lease.ctx, token); err != nil {
		return true, err
	}
	return false, nil
}
