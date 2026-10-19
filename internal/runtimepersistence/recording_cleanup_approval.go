package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

// A legacy copy can have an older decision/revision after control extraction.
// Those mutable facts do not change the immutable question or Exchange binding.
func sameCleanupApproval(a, b toolapproval.Record) bool {
	a.Revision, b.Revision = 0, 0
	a.RequestCount, b.RequestCount = 0, 0
	a.WaiterCount, b.WaiterCount = 0, 0
	a.State, b.State = "", ""
	a.Decision, b.Decision = "", ""
	a.DecisionScope, b.DecisionScope = "", ""
	a.DecisionReason, b.DecisionReason = "", ""
	a.IdempotencyKey, b.IdempotencyKey = "", ""
	a.ResolvedAt, b.ResolvedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

// ClassifyLegacyApproval uses every declared source under exclusive maintenance.
// Positive proof is source-local Activity/semantic ownership, never Raw or a
// denied audit closure. False means unknown: it is not permission to delete.
// All history SQL is closed on return, before the caller's required CAS action.
func (s *ObservationStore) ClassifyLegacyApproval(ctx context.Context, control *ControlStore, m *InspectionMaintenance, candidate toolapproval.Record) (owner recording.Scope, proven bool, result error) {
	if candidate.Validate() != nil || candidate.Kind != toolapproval.KindToolIntent || candidate.OwnerKind != toolapproval.OwnershipUnknown {
		return owner, false, toolapproval.ErrInvalidApproval
	}
	if err := s.PublishInspectionMirror(ctx, control); err != nil {
		return owner, false, err
	}
	sources, err := m.cleanupInventory()
	if err != nil {
		return owner, false, err
	}
	var veto bool
	var rawOwner recording.Scope
	var rawSeen bool
	perKind := map[activity.Kind]activity.Record{}
	for _, source := range sources {
		err = s.withCleanupSource(ctx, m, source, func(conn *sql.Conn, mirror string) error {
			tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return err
			}
			defer tx.Rollback()
			var denied bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+mirror+`.recording_denials WHERE kind='exchange' AND identity=?)`, candidate.ExchangeID).Scan(&denied); err != nil {
				return err
			}
			if denied {
				veto = true
				return nil
			}
			present := false
			if !source.Current {
				copy, err := scanToolApproval(tx.QueryRowContext(ctx, `SELECT `+cleanupApprovalColumns(false)+` FROM main.tool_approvals WHERE approval_id=?`, candidate.ID))
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if err == nil {
					present = true
					if !sameCleanupApproval(candidate, copy) {
						veto = true
					}
				}
			}
			scope, positive, err := cleanupExchangeScope(ctx, tx, source, candidate.ExchangeID, s.store.exchangeContents.limits, perKind)
			if err != nil {
				return err
			}
			if present && !positive {
				veto = true
			}
			if positive {
				if proven && scope != owner {
					veto = true
				}
				owner = scope
				proven = true
			}
			// Raw is only a conflict veto. Stream one bounded metadata row at a time;
			// payload bytes are never loaded to decide required authority ownership.
			columns := strings.Split(rawEvidenceColumns, ",")
			for i, column := range columns {
				column = strings.TrimSpace(column)
				if column == "payload_metadata" {
					columns[i] = `NULL`
				} else {
					columns[i] = `CASE WHEN length(CAST(` + column + ` AS BLOB))<=32768 THEN ` + column + ` END`
				}
			}
			epoch, generation := "1", `CASE WHEN scope_kind='runtime' THEN 0 ELSE 1 END`
			if source.Current {
				epoch, generation = "recording_archive_epoch", "recording_capture_generation"
			}
			rows, err := tx.QueryContext(ctx, `SELECT `+strings.Join(columns, ",")+`,`+epoch+`,`+generation+` FROM main.runtime_raw_evidence_envelopes WHERE exchange_id=?`, candidate.ExchangeID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var fence recording.Fence
				raw, _, err := scanRawEvidence(rows, &fence.ArchiveEpoch, &fence.CaptureGeneration)
				if err != nil {
					return err
				}
				raw.RecordingFence = fence
				if err = raw.ValidateMetadata(); err != nil {
					return err
				}
				rawScope := raw.RecordingScope()
				if rawSeen && rawOwner != rawScope {
					veto = true
				}
				rawOwner = rawScope
				rawSeen = true
			}
			return rows.Err()
		})
		if err != nil {
			return recording.Scope{}, false, err
		}
	}
	if veto || !proven || owner.Capture.Kind == "" || rawSeen && rawOwner != owner {
		return recording.Scope{}, false, nil
	}
	return owner, true, nil
}

func cleanupExchangeScope(ctx context.Context, tx *sql.Tx, source InspectionSource, id string, limits *exchangecontent.SourceLimits, perKind map[activity.Kind]activity.Record) (scope recording.Scope, proven bool, result error) {
	columns := strings.ReplaceAll(activityInspectionColumns(0, source), "s0.", "main.")
	rows, err := tx.QueryContext(ctx, `SELECT `+columns+` FROM main.runtime_activities a WHERE a.subject_id=? AND a.kind IN('exchange.started','exchange.completed') LIMIT 3`, id)
	if err != nil {
		return scope, false, err
	}
	kinds := map[activity.Kind]bool{}
	for rows.Next() {
		var record activity.Record
		var fence recording.Fence
		state := activityScanState{record: &record}
		if err = rows.Scan(append(state.targets(), &fence.ArchiveEpoch, &fence.CaptureGeneration)...); err != nil {
			break
		}
		record, err = state.finish()
		if err != nil {
			break
		}
		candidate := activityScopeOf(record, fence)
		if kinds[record.Kind] || proven && candidate != scope {
			err = activity.ErrInvalidEvent
			break
		}
		for _, previous := range perKind {
			if !sameExchangeIdentity(previous, record) {
				err = activity.ErrInvalidEvent
				break
			}
		}
		if err != nil {
			break
		}
		if previous, ok := perKind[record.Kind]; ok && !sameInspectedActivity(previous, record, false) {
			err = activity.ErrInvalidEvent
			break
		}
		perKind[record.Kind] = record
		kinds[record.Kind] = true
		scope = candidate
		proven = true
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return recording.Scope{}, false, err
	}
	ref, err := loadStoredContentReferenceWithin(ctx, tx, id, time.UnixMilli(0).UTC(), limits)
	if errors.Is(err, exchangecontent.ErrNotFound) {
		return scope, proven, nil
	}
	if err != nil {
		return recording.Scope{}, false, err
	}
	kind, owner := storedContentScope(ref.manifest.Parent)
	candidate := recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.Kind(kind), ID: owner}, Fence: recording.Fence{ArchiveEpoch: 1}}
	if kind != "" {
		candidate.Fence.CaptureGeneration = 1
	}
	if source.Current {
		if err = tx.QueryRowContext(ctx, `SELECT recording_archive_epoch,recording_capture_generation FROM main.runtime_exchange_contents WHERE exchange_id=?`, id).Scan(&candidate.Fence.ArchiveEpoch, &candidate.Fence.CaptureGeneration); err != nil {
			return recording.Scope{}, false, err
		}
	}
	if candidate.Validate() != nil || proven && candidate != scope {
		return recording.Scope{}, false, activity.ErrInvalidEvent
	}
	return candidate, true, nil
}

// RemoveLegacyApproval is the short required half of maintenance. The caller
// owns Authority publication exclusion and has closed every history lease.
// Exact row recheck prevents a proof selected before a decision/rebinding from
// deleting changed authority. Missing rows count as zero, never a guessed one.
func (r *toolApprovalRepository) RemoveLegacyApproval(ctx context.Context, expected toolapproval.Record, owner recording.Scope) (removed bool, result error) {
	if !r.controlOwnership || expected.Validate() != nil || expected.Kind != toolapproval.KindToolIntent || expected.OwnerKind != toolapproval.OwnershipUnknown || owner.Validate() != nil || owner.Capture.Kind == "" {
		return false, toolapproval.ErrInvalidApproval
	}
	operation, finish, err := r.operations.begin(ctx)
	if err != nil {
		return false, err
	}
	defer finish()
	err = fullSynchronousTransaction(operation, r.database, func(tx *sql.Tx) error {
		var eligible bool
		if err := tx.QueryRowContext(operation, `SELECT s.archive_epoch>? OR EXISTS(SELECT 1 FROM recording_capture_fences f WHERE f.capture_kind=? AND f.capture_id=? AND f.generation=? AND f.deleted=1) FROM runtime_storage_state s WHERE s.singleton=1`, owner.Fence.ArchiveEpoch, owner.Capture.Kind, owner.Capture.ID, owner.Fence.CaptureGeneration).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return toolapproval.ErrRevisionConflict
		}
		current, err := scanToolApproval(tx.QueryRowContext(operation, `SELECT `+r.columns()+` FROM tool_approvals WHERE approval_id=?`, expected.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, expected) {
			return toolapproval.ErrRevisionConflict
		}
		count, err := deleteCounted(operation, tx, `DELETE FROM tool_approvals WHERE approval_id=? AND revision=? AND owner_kind='unknown'`, expected.ID, expected.Revision)
		removed = count == 1
		return err
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}

// PurgeRecordingLegacyApprovals must precede evidence/Usage proof destruction.
// It retains at most 100 bounded approval rows and one source-local proof at a
// time. The actual Authority performs required actions between history leases.
func (s *ObservationStore) PurgeRecordingLegacyApprovals(ctx context.Context, control *ControlStore, m *InspectionMaintenance, authority toolapproval.LegacyApprovalRemover) (released resourcedeletion.Released, result error) {
	if authority == nil {
		return released, ErrInspectionUnavailable
	}
	sources, err := m.cleanupInventory()
	if err != nil {
		return released, err
	}
	snapshot, err := control.ReadInspectionSnapshot(ctx, nil)
	if err != nil {
		return released, err
	}
	// Every frozen monolithic archive belongs to epoch1. A durable explicit
	// whole clear authorizes its complete approval metadata removal in the
	// evidence pass, independently of Capture ownership inference.
	if snapshot.Stamp.ArchiveEpoch > 1 {
		return released, nil
	}
	process := func(candidate toolapproval.Record) error {
		expected, err := control.ToolApprovalRepository().Get(ctx, candidate.ID)
		if err != nil && !errors.Is(err, toolapproval.ErrNotFound) {
			return err
		}
		if err == nil {
			if expected.OwnerKind != toolapproval.OwnershipUnknown || !sameCleanupApproval(expected, candidate) {
				released.ApprovalOwnershipUnknown = true
				released.CountsIncomplete = true
				return nil
			}
			candidate = expected
		}
		owner, proven, err := s.ClassifyLegacyApproval(ctx, control, m, candidate)
		if err != nil {
			return err
		}
		if !proven {
			released.ApprovalOwnershipUnknown = true
			released.CountsIncomplete = true
			return nil
		}
		snapshot, err := control.ReadInspectionSnapshot(ctx, []captureidentity.Reference{owner.Capture})
		if err != nil {
			return err
		}
		if snapshot.Allows(owner) {
			return nil
		}
		removed, err := authority.RemoveLegacyApproval(ctx, candidate, owner)
		if err != nil {
			return err
		}
		if removed {
			released.Approvals++
		}
		for _, source := range sources {
			if source.Current {
				continue
			}
			var n uint64
			err = s.withCleanupSource(ctx, m, source, func(conn *sql.Conn, _ string) error {
				if _, err := conn.ExecContext(ctx, `PRAGMA synchronous=FULL`); err != nil {
					return err
				}
				tx, err := conn.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				copy, err := scanToolApproval(tx.QueryRowContext(ctx, `SELECT `+cleanupApprovalColumns(false)+` FROM main.tool_approvals WHERE approval_id=?`, candidate.ID))
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				if err != nil {
					return err
				}
				if !sameCleanupApproval(candidate, copy) {
					return ErrInspectionChanged
				}
				n, err = deleteCounted(ctx, tx, `DELETE FROM main.tool_approvals WHERE approval_id=? AND kind='tool_intent'`, candidate.ID)
				if err != nil {
					return err
				}
				return tx.Commit()
			})
			if err != nil {
				return err
			}
			released.Approvals += n
		}
		return nil
	}
	// Include unknown required rows even when no archived approval copy remains.
	// A previous interrupted pass can have removed one side already.
	after := ""
	for {
		page, err := control.cleanupApprovalPage(ctx, after)
		if err != nil {
			return released, err
		}
		for _, candidate := range page {
			if err = process(candidate); err != nil {
				return released, err
			}
			after = candidate.ID
		}
		if len(page) < 100 {
			break
		}
	}
	for _, source := range sources {
		if source.Current {
			continue
		}
		after = ""
		for {
			var page []toolapproval.Record
			err = s.withCleanupSource(ctx, m, source, func(conn *sql.Conn, _ string) error {
				var err error
				page, err = readCleanupApprovalPage(ctx, conn, false, after)
				return err
			})
			if err != nil {
				return released, err
			}
			for _, candidate := range page {
				if err = process(candidate); err != nil {
					return released, err
				}
				after = candidate.ID
			}
			if len(page) < 100 {
				break
			}
		}
	}
	return released, nil
}

func cleanupApprovalColumns(control bool) string {
	columns := strings.Split(toolApprovalColumns, ",")
	for i, column := range columns {
		column = strings.TrimSpace(column)
		columns[i] = `CASE WHEN length(CAST(` + column + ` AS BLOB))<=65536 THEN ` + column + ` END`
	}
	if control {
		return strings.Join(columns, ",") + `,` + toolApprovalOwnershipColumns
	}
	return strings.Join(columns, ",") + `,'','','',0,0`
}
func readCleanupApprovalPage(ctx context.Context, q contentReadQueries, control bool, after string) ([]toolapproval.Record, error) {
	where := `kind='tool_intent'`
	if control {
		where += ` AND owner_kind='unknown'`
	}
	rows, err := q.QueryContext(ctx, `SELECT `+cleanupApprovalColumns(control)+` FROM main.tool_approvals WHERE `+where+` AND approval_id>? ORDER BY approval_id LIMIT 100`, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []toolapproval.Record{}
	for rows.Next() {
		record, err := scanToolApproval(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}
func (s *ControlStore) cleanupApprovalPage(ctx context.Context, after string) ([]toolapproval.Record, error) {
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	return readCleanupApprovalPage(operation, s.store.reads, true, after)
}
