package runtimepersistence

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

type recordingCleanupRoot struct {
	table, key, where string
	seeds             map[string]string
}

// Scope deletion is decided from each root's own immutable coordinates. Legacy
// Capture coordinates predate epochs and therefore belong only to epoch1.
func cleanupHiddenScope(mirror, kind, id, epoch, generation string) string {
	return `NOT EXISTS(SELECT 1 FROM ` + mirror + `.inspection_mirror_active ma JOIN ` + mirror + `.inspection_mirror_generations mg USING(generation) WHERE ma.singleton=1 AND mg.complete=1 AND ` + epoch + `=mg.archive_epoch AND ((` + kind + `='' AND ` + id + `='' AND ` + generation + `=0) OR EXISTS(SELECT 1 FROM ` + mirror + `.inspection_mirror_fences mf WHERE mf.mirror_generation=mg.generation AND mf.capture_kind=` + kind + ` AND mf.capture_id=` + id + ` AND mf.capture_generation=` + generation + ` AND mf.deleted=0)))`
}
func recordingCleanupRoots(source InspectionSource, mirror string, cutoff time.Time) []recordingCleanupRoot {
	hidden := func(kind, id string) string {
		epoch, generation := "1", `CASE WHEN `+kind+`='' THEN 0 ELSE 1 END`
		if source.Current {
			epoch = "r.recording_archive_epoch"
			generation = "r.recording_capture_generation"
		}
		return cleanupHiddenScope(mirror, kind, id, epoch, generation)
	}
	expired := func(column string) string { return `r.` + column + `<=` + fmt.Sprint(cutoff.UnixMilli()) }
	activityKind := `CASE WHEN r.capture_run_id<>'' THEN 'managed_run' WHEN r.manual_capture_id<>'' THEN 'manual_capture' ELSE '' END`
	activityID := `CASE WHEN r.capture_run_id<>'' THEN r.capture_run_id ELSE r.manual_capture_id END`
	roots := []recordingCleanupRoot{
		{"runtime_activities", "activity_id", hidden(activityKind, activityID), map[string]string{"connection": "r.connection_id", "exchange": `CASE WHEN r.kind IN('exchange.started','exchange.completed') THEN r.subject_id ELSE '' END`}},
		{"runtime_exchange_contents", "exchange_id", `(` + hidden("r.scope_kind", "r.scope_id") + ` OR ` + expired("expires_at_unix_ms") + `)`, map[string]string{"exchange": "r.exchange_id"}},
		{"runtime_raw_evidence_envelopes", "envelope_id", `(` + hidden(`CASE WHEN r.scope_kind='runtime' THEN '' ELSE r.scope_kind END`, "r.scope_id") + ` OR ` + expired("expires_at_unix_ms") + `)`, map[string]string{"exchange": "r.exchange_id", "attempt": "r.attempt_id", "connection": "r.connection_id"}},
	}
	if source.Current {
		roots = append(roots,
			recordingCleanupRoot{"runtime_connection_events", "sequence", hidden("r.recording_scope_kind", "r.recording_scope_id"), map[string]string{"connection": "r.connection_id"}},
			recordingCleanupRoot{"runtime_egress_attempts", "attempt_id", hidden("r.recording_scope_kind", "r.recording_scope_id"), map[string]string{"attempt": "r.attempt_id"}},
			recordingCleanupRoot{"runtime_exchange_agent_identities", "exchange_id", hidden("r.recording_scope_kind", "r.recording_scope_id"), nil},
			recordingCleanupRoot{"runtime_raw_inspection_reveal_audits", "sequence", hidden("r.recording_scope_kind", "r.recording_scope_id"), nil},
			recordingCleanupRoot{"native_tool_contents", "(operation_id,part)", `(` + hidden("r.capture_kind", "r.capture_id") + ` OR ` + expired("expires_at_unix_ms") + `)`, nil},
			recordingCleanupRoot{"capture_observations", "(capture_kind,capture_id,archive_epoch,capture_generation,credential_revision)", cleanupHiddenScope(mirror, "r.capture_kind", "r.capture_id", "r.archive_epoch", "r.capture_generation"), nil},
			recordingCleanupRoot{"acp_snapshots", "(run_id,archive_epoch,capture_generation)", `(` + cleanupHiddenScope(mirror, "'managed_run'", "r.run_id", "r.archive_epoch", "r.capture_generation") + ` OR ` + expired("expires_at_unix_ms") + `)`, nil},
		)

		historyWhere := `(` + hidden("r.capture_kind", "r.capture_id") + ` OR ` + expired("metadata_expires_at_unix_ms") + `)`
		selectedHistory := `SELECT h.operation_id FROM main.native_history_snapshots h WHERE ` + strings.ReplaceAll(historyWhere, "r.", "h.")
		roots = append(roots,
			recordingCleanupRoot{"native_history_latest", "operation_id", `r.operation_id IN(` + selectedHistory + `)`, nil},
			recordingCleanupRoot{"native_history_snapshots", "(operation_id,operation_revision)", historyWhere, nil},
			recordingCleanupRoot{"native_inspection_index", "operation_id", `(` + hidden("r.capture_kind", "r.capture_id") + ` OR (NOT EXISTS(SELECT 1 FROM main.native_history_snapshots h WHERE h.operation_id=r.operation_id) AND NOT EXISTS(SELECT 1 FROM main.native_tool_contents nc WHERE nc.operation_id=r.operation_id)))`, nil},
			recordingCleanupRoot{"runtime_egress_attempts", "attempt_id", `r.parent_kind='client_operation' AND r.parent_id IN(` + selectedHistory + `)`, map[string]string{"attempt": "r.attempt_id"}},
		)
	}
	if !source.Current {
		roots = append(roots, recordingCleanupRoot{"runtime_exchange_agent_identities", "exchange_id", `EXISTS(SELECT 1 FROM ` + mirror + `.recording_denials d WHERE d.kind='exchange' AND d.identity=r.exchange_id)`, nil})
		if source.Database.Revision == 2 {
			nativeWhere := `(` + hidden("r.capture_kind", "r.capture_id") + ` OR (` + expired("metadata_expires_at_unix_ms") + ` AND r.completed_at_unix_ms IS NOT NULL AND NOT EXISTS(SELECT 1 FROM main.native_tool_contents nc WHERE nc.operation_id=r.operation_id AND nc.expires_at_unix_ms>` + fmt.Sprint(cutoff.UnixMilli()) + `)))`
			selected := `SELECT n.operation_id FROM main.native_tool_operations n WHERE ` + strings.ReplaceAll(nativeWhere, "r.", "n.")
			hiddenOwner := `SELECT n.operation_id FROM main.native_tool_operations n WHERE ` + strings.ReplaceAll(hidden("r.capture_kind", "r.capture_id"), "r.", "n.")
			roots = append(roots,
				recordingCleanupRoot{"native_tool_contents", "(operation_id,part)", `(` + expired("expires_at_unix_ms") + ` OR r.operation_id IN(` + hiddenOwner + `))`, nil},
				recordingCleanupRoot{"native_tool_approvals", "approval_id", `r.operation_id IN(` + selected + `)`, nil},
				recordingCleanupRoot{"native_tool_operations", "operation_id", nativeWhere, map[string]string{"connection": "r.connection_id"}},
				recordingCleanupRoot{"runtime_egress_attempts", "attempt_id", `(r.parent_kind='client_operation' AND r.parent_id IN(` + selected + `)) OR r.attempt_id IN(SELECT w.attempt_id FROM main.native_tool_once w WHERE w.operation_id IN(` + selected + `))`, map[string]string{"attempt": "r.attempt_id"}},
			)
		}
	}

	return roots
}

// certifyCleanupQuery consumes bounded scalar identities. The source lease and
// rows are closed before the required prefix certificate advances.
func (s *ObservationStore) certifyCleanupQuery(ctx context.Context, control *ControlStore, m *InspectionMaintenance, source InspectionSource, kind string, query func(string) string) (added bool, result error) {
	for {
		var ids []string
		err := s.withCleanupSource(ctx, m, source, func(conn *sql.Conn, mirror string) error {
			rows, err := conn.QueryContext(ctx, query(mirror))
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					return err
				}
				if !validRecordingDenial(kind, id) {
					return ErrInspectionUnavailable
				}
				ids = append(ids, id)
			}
			return rows.Err()
		})
		if err != nil {
			return added, err
		}
		if len(ids) == 0 {
			return added, nil
		}
		if err = s.DenyRecordingIdentities(ctx, control, kind, ids); err != nil {
			return added, err
		}
		added = true
	}
}

func (s *ObservationStore) certifyRecordingAuditClosure(ctx context.Context, control *ControlStore, m *InspectionMaintenance, cutoff time.Time) error {
	sources, err := m.cleanupInventory()
	if err != nil {
		return err
	}
	// Decode the actual closed Attempt DTO before trusting its graph edges.
	// Streaming validation has fixed memory and runs only under maintenance.
	for _, source := range sources {
		if err := s.withCleanupSource(ctx, m, source, func(conn *sql.Conn, _ string) error {
			rows, err := conn.QueryContext(ctx, inspectionEgressSelect("main", source))
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				if _, err = scanEgressAttempt(rows); err != nil {
					return err
				}
			}
			return rows.Err()
		}); err != nil {
			return err
		}
	}
	// Select every destructible proof root before any source transaction deletes.
	for _, source := range sources {
		if !source.Current && source.Database.Revision == 2 {
			_, err := s.certifyCleanupQuery(ctx, control, m, source, "attempt", func(mirror string) string {
				hidden := cleanupHiddenScope(mirror, "r.capture_kind", "r.capture_id", "1", "1")
				return `SELECT CASE WHEN r.claimed_at_unix_ms>0 AND r.authority_revision>0 THEN r.attempt_id END FROM main.native_tool_once r WHERE ` + hidden + ` AND NOT EXISTS(SELECT 1 FROM ` + mirror + `.recording_denials d WHERE d.kind='attempt' AND d.identity=r.attempt_id) LIMIT 256`
			})
			if err != nil {
				return err
			}
		}

		for rootIndex := range recordingCleanupRoots(source, "main", cutoff) {
			for _, kind := range []string{"attempt", "connection", "exchange"} {
				_, err = s.certifyCleanupQuery(ctx, control, m, source, kind, func(mirror string) string {
					root := recordingCleanupRoots(source, mirror, cutoff)[rootIndex]
					column, ok := root.seeds[kind]
					if !ok {
						return `SELECT '' WHERE 0`
					}
					return `SELECT DISTINCT ` + column + ` FROM main.` + root.table + ` r WHERE (` + root.where + `) AND ` + column + `<>'' AND NOT EXISTS(SELECT 1 FROM ` + mirror + `.recording_denials d WHERE d.kind='` + kind + `' AND d.identity=` + column + `) LIMIT 256`
				})
				if err != nil {
					return err
				}
			}
		}
	}
	// A link in another source can extend either endpoint. Iterate the finite
	// typed relation until no source adds an endpoint, not merely one source pass.
	for {
		changed := false
		for _, source := range sources {
			for _, kind := range []string{"attempt", "connection", "exchange"} {
				column := map[string]string{"attempt": "r.attempt_id", "connection": "r.connection_id", "exchange": "r.parent_exchange_id"}[kind]
				added, err := s.certifyCleanupQuery(ctx, control, m, source, kind, func(mirror string) string {
					linked := `EXISTS(SELECT 1 FROM ` + mirror + `.recording_denials d WHERE (d.kind='attempt' AND d.identity=r.attempt_id) OR (d.kind='connection' AND d.identity=r.connection_id) OR (d.kind='exchange' AND d.identity=r.parent_exchange_id))`
					return `SELECT DISTINCT ` + column + ` FROM main.runtime_egress_attempts r WHERE ` + linked + ` AND ` + column + `<>'' AND NOT EXISTS(SELECT 1 FROM ` + mirror + `.recording_denials d WHERE d.kind='` + kind + `' AND d.identity=` + column + `) LIMIT 256`
				})
				if err != nil {
					return err
				}
				changed = changed || added
			}
		}
		if !changed {
			return nil
		}
	}
}

// PurgeRecordingEvidence is the graph portion of an owned cleanup pass; the
// caller still owes Usage, source compaction/verification and durable receipt.
func (s *ObservationStore) PurgeRecordingEvidence(ctx context.Context, control *ControlStore, m *InspectionMaintenance, cutoff time.Time) (released resourcedeletion.Released, result error) {
	if ctx == nil || control == nil || cutoff.IsZero() {
		return released, ErrInspectionUnavailable
	}
	if err := s.PublishInspectionMirror(ctx, control); err != nil {
		return released, err
	}
	if err := s.certifyRecordingAuditClosure(ctx, control, m, cutoff); err != nil {
		return released, err
	}
	if err := s.PublishInspectionMirror(ctx, control); err != nil {
		return released, err
	}
	sources, err := m.cleanupInventory()
	if err != nil {
		return released, err
	}
	for _, source := range sources {
		for {
			var batch resourcedeletion.Released
			var total int64
			err = s.withCleanupSource(ctx, m, source, func(conn *sql.Conn, mirror string) error {
				if _, err := conn.ExecContext(ctx, `PRAGMA synchronous=FULL`); err != nil {
					return err
				}
				tx, err := conn.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				roots := recordingCleanupRoots(source, mirror, cutoff)
				// Audit identities are purged whole, even when one surviving scope is live.
				roots = append(roots, recordingCleanupRoot{"runtime_egress_attempts", "attempt_id", `EXISTS(SELECT 1 FROM ` + mirror + `.recording_denials d WHERE d.kind='attempt' AND d.identity=r.attempt_id)`, nil}, recordingCleanupRoot{"runtime_connection_events", "sequence", `EXISTS(SELECT 1 FROM ` + mirror + `.recording_denials d WHERE d.kind='connection' AND d.identity=r.connection_id)`, nil})
				for _, root := range roots {
					query := `DELETE FROM ` + root.table + ` WHERE ` + root.key + ` IN(SELECT ` + strings.Trim(root.key, "()") + ` FROM ` + root.table + ` r WHERE ` + root.where + ` LIMIT 100)`
					var n int64
					switch root.table {
					case "runtime_raw_evidence_envelopes":
						n, err = deleteRawEvidenceCompatible(ctx, tx, source.Current || source.Database.Revision == 2, query)
					case "runtime_exchange_contents":
						n, err = deleteExchangeContent(ctx, tx, query)
					case "native_tool_contents":
						n, err = deleteNativeContents(ctx, tx, root.key+` IN(SELECT `+strings.Trim(root.key, "()")+` FROM `+root.table+` r WHERE `+root.where+` LIMIT 100)`)
					default:
						var r sql.Result
						r, err = tx.ExecContext(ctx, query)
						if err == nil {
							n, err = r.RowsAffected()
						}
					}
					if err != nil {
						return fmt.Errorf("purge %s: %w", root.table, err)
					}
					total += n
					switch root.table {
					case "runtime_raw_evidence_envelopes":
						batch.Envelopes += uint64(n)
					case "runtime_exchange_contents":
						batch.Exchanges += uint64(n)
					case "runtime_activities":
						batch.Activities += uint64(n)
					case "runtime_egress_attempts":
						batch.Attempts += uint64(n)
					case "runtime_connection_events":
						batch.Connections += uint64(n)
					case "native_tool_approvals":
						batch.Approvals += uint64(n)
					}
				}
				return tx.Commit()
			})
			if err != nil {
				return released, err
			}
			released = released.Add(batch)
			if total == 0 {
				break
			}
		}
	}
	return released, nil
}
