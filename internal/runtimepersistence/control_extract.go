package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/environment"
	"hash"
	"os"
	"path/filepath"
	"strings"
)

// SourcePath is an owned, byte-verified private DB/WAL copy, never the original
// or archive. The directory owner separately certifies and preserves those bytes.
// TargetPath must not exist. No public Store or lifecycle writer participates.
type ControlExtraction struct{ SourcePath, TargetPath, LayoutID, ArchiveID string }

// This certifies the required projection only, not optional history readability
// or a backup. The directory owner must additionally certify original artifacts.
type ControlExtractionProof struct {
	Source           SchemaState
	ProjectionDigest [32]byte
	Rows             int64
}

func ExtractControl(ctx context.Context, in ControlExtraction) (proof ControlExtractionProof, result error) {
	if ctx == nil || !roleID(in.LayoutID) || !roleID(in.ArchiveID) || !filepath.IsAbs(in.SourcePath) || !filepath.IsAbs(in.TargetPath) || filepath.Clean(in.SourcePath) != in.SourcePath || filepath.Clean(in.TargetPath) != in.TargetPath || in.SourcePath == in.TargetPath {
		return proof, ErrInvalidDatabasePath
	}
	info, err := os.Lstat(in.SourcePath)
	if err != nil || !info.Mode().IsRegular() {
		return proof, ErrInvalidDatabasePath
	}
	source := sql.OpenDB(newSQLiteReadConnector(in.SourcePath, DefaultBusyTimeout))
	source.SetMaxOpenConns(1)
	defer func() { result = errors.Join(result, source.Close()) }()
	conn, err := source.Conn(ctx)
	if err != nil {
		return proof, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return proof, err
	}
	defer tx.Rollback()
	proof.Source, err = inspectSupportedSchema(ctx, tx)
	if err != nil || (proof.Source.Revision != 1 && proof.Source.Revision != 2 && proof.Source.Revision != 3) {
		return proof, errors.Join(ErrUnsupportedSchema, err)
	}
	if err = extractionStructure(ctx, tx); err != nil {
		return proof, err
	}
	sourceProjections := append([]controlProjection(nil), controlProjections...)
	if publishedNativeRevision(proof.Source.Revision) {
		sourceProjections = append(sourceProjections, nativeControlProjections...)
	}
	for _, p := range sourceProjections {
		if proof.Source.Revision == 1 && p.table == "runtime_egress_default" {
			continue
		}
		if p.table != "capture_runs" && p.table != "manual_captures" {
			if err = extractionIntegrity(ctx, tx, `PRAGMA main.integrity_check(`+quoteMigrationIdentifier(p.table)+`)`); err != nil {
				return proof, fmt.Errorf("required %s: %w", p.table, err)
			}
		}
		if err = extractionNoRows(ctx, tx, `PRAGMA main.foreign_key_check(`+quoteMigrationIdentifier(p.table)+`)`); err != nil {
			return proof, err
		}
	}
	if err = extractionIntegrity(ctx, tx, `PRAGMA main.integrity_check('runtime_usage_retention_caps')`); err != nil {
		return proof, err
	}
	if publishedNativeRevision(proof.Source.Revision) {
		if err = validateExtractionDeliveryConstants(ctx, tx); err != nil {
			return proof, err
		}
	}
	if err = validateExtractedACP(ctx, tx); err != nil {
		return proof, err
	}
	f, err := os.OpenFile(in.TargetPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return proof, err
	}
	if err = f.Close(); err != nil {
		return proof, err
	}
	target := sql.OpenDB(newSQLiteUpgradeConnector(in.TargetPath, DefaultBusyTimeout))
	target.SetMaxOpenConns(1)
	defer func() { result = errors.Join(result, target.Close()) }()
	if _, err = target.ExecContext(ctx, `PRAGMA journal_mode=DELETE`); err != nil {
		return proof, err
	}
	out, err := target.BeginTx(ctx, nil)
	if err != nil {
		return proof, err
	}
	defer out.Rollback()
	if _, err = out.ExecContext(ctx, controlSchemaSQL); err != nil {
		return proof, err
	}
	if _, err = out.ExecContext(ctx, `PRAGMA defer_foreign_keys=ON; DELETE FROM runtime_usage_policy; DELETE FROM runtime_egress_default; DELETE FROM recording_cleanup_state; DELETE FROM recording_denial_coverage`); err != nil {
		return proof, err
	}
	projections := controlExtractionProjections()
	deferred, err := deferExtractionTriggers(ctx, out)
	if err != nil {
		return proof, err
	}
	expected := sha256.New()
	fingerprintPart(expected, 'V', []byte(fmt.Sprintf("control-extraction/v1/source-%d", proof.Source.Revision)))
	for _, p := range projections {
		query, args := extractionQuery(p, proof.Source.Revision, in)
		count, err := streamControlProjection(ctx, tx, out, p, query, args, expected)
		if err != nil {
			return proof, fmt.Errorf("project %s: %w", p.table, err)
		}
		proof.Rows += count
	}
	for _, ddl := range deferred {
		if _, err = out.ExecContext(ctx, ddl); err != nil {
			return proof, err
		}
	}
	if err = validateExtractedControl(ctx, out); err != nil {
		return proof, err
	}
	if err = validateExtractedNative(ctx, out); err != nil {
		return proof, err
	}
	if err = extractionIntegrity(ctx, out, `PRAGMA main.integrity_check`); err != nil {
		return proof, err
	}
	if err = extractionNoRows(ctx, out, `PRAGMA main.foreign_key_check`); err != nil {
		return proof, err
	}
	if err = (&roleSchema{identity: controlSchemaIdentity, revision: 3, ddl: controlSchemaSQL, layout: in.LayoutID}).verify(ctx, out); err != nil {
		return proof, err
	}
	actual := sha256.New()
	fingerprintPart(actual, 'V', []byte(fmt.Sprintf("control-extraction/v1/source-%d", proof.Source.Revision)))
	var actualRows int64
	for _, p := range projections {
		count, err := streamControlProjection(ctx, out, nil, p, `SELECT `+p.columns+` FROM `+p.table+` ORDER BY `+p.key, nil, actual)
		if err != nil {
			return proof, err
		}
		actualRows += count
	}
	if actualRows != proof.Rows || string(expected.Sum(nil)) != string(actual.Sum(nil)) {
		return proof, ErrUnsupportedSchema
	}
	copy(proof.ProjectionDigest[:], actual.Sum(nil))
	if err = out.Commit(); err != nil {
		return proof, err
	}
	if err = tx.Commit(); err != nil {
		return proof, err
	}
	if err = target.Close(); err != nil {
		return proof, err
	}
	f, err = os.Open(in.TargetPath)
	if err != nil {
		return proof, err
	}
	result = errors.Join(f.Sync(), f.Close())
	return proof, result
}

func extractionQuery(p controlProjection, revision int64, in ControlExtraction) (string, []any) {
	if revision == 1 && strings.HasPrefix(p.table, "native_tool_") || p.table == "runtime_usage_retention_rules" {
		return `SELECT ` + strings.TrimSuffix(strings.Repeat("NULL,", len(strings.Split(p.columns, ","))), ",") + ` WHERE 0`, nil
	}
	switch p.table {
	case "native_tool_deliveries":
		return `SELECT a.attempt_id,a.parent_id,a.connection_id,a.payload_class,a.target_origin,a.policy_id,a.policy_revision,a.rule_id,a.proxy_id,a.proxy_revision,a.selection_source,a.default_revision,a.started_at_unix_ms,a.completed_at_unix_ms,a.outcome,a.error_class,a.bytes_out,a.bytes_in,a.byte_counts_known FROM native_tool_once w JOIN native_tool_operations o ON o.operation_id=w.operation_id JOIN runtime_egress_attempts a ON a.attempt_id=w.attempt_id ORDER BY a.attempt_id`, nil
	case "acp_registrations":
		return `SELECT run_id,json_extract(policy,'$.mode'),json_extract(policy,'$.retentionDays'),expires_at_unix_ms,1,1 FROM acp_observations ORDER BY run_id`, nil
	case "runtime_metadata":
		return `SELECT singleton,'vibermate-control',3,initialized_at FROM runtime_metadata ORDER BY singleton`, nil
	case "recording_denial_coverage":
		return `SELECT 1,0,zeroblob(32)`, nil
	case "recording_cleanup_state":
		return `SELECT 1,0,0,1,0,0,0`, nil
	case "runtime_storage_state":
		return `SELECT 1,?,1,1`, []any{in.LayoutID}
	case "runtime_usage_legacy_retention_caps":
		return `SELECT ?,retention_days,after_sequence,through_sequence FROM runtime_usage_retention_caps ORDER BY retention_days`, []any{in.ArchiveID}
	case "recording_capture_fences":
		return `SELECT 'managed_run' AS capture_kind,run_id AS capture_id,1,0 FROM capture_runs UNION ALL SELECT 'manual_capture',capture_id,1,0 FROM manual_captures ORDER BY capture_kind,capture_id`, nil
	case "runtime_egress_default":
		if revision == 1 {
			return `SELECT 1,1,NULL,NULL`, nil
		}
	}
	if p.table == "tool_approvals" {
		columns := strings.Split(p.columns, ",")
		for i, column := range columns {
			switch column {
			case "owner_kind":
				columns[i] = `CASE WHEN kind='tool_intent' THEN 'unknown' ELSE 'shared' END`
			case "owner_capture_kind", "owner_capture_id":
				columns[i] = `''`
			case "owner_archive_epoch", "owner_capture_generation":
				columns[i] = `0`
			}
		}
		return `SELECT ` + strings.Join(columns, ",") + ` FROM tool_approvals ORDER BY approval_id`, nil
	}
	if p.table == "native_tool_operations" {
		columns := strings.Split(p.columns, ",")
		for i, column := range columns {
			switch column {
			case "recording_archive_epoch", "recording_capture_generation":
				columns[i] = "1"
			case "last_event_kind":
				columns[i] = `(SELECT kind FROM native_tool_events e WHERE e.operation_id=o.operation_id AND e.operation_revision=o.revision AND e.operation_revision=(SELECT max(operation_revision) FROM native_tool_events WHERE operation_id=o.operation_id))`
			case "last_event_at_unix_ms":
				columns[i] = `(SELECT occurred_at_unix_ms FROM native_tool_events e WHERE e.operation_id=o.operation_id AND e.operation_revision=o.revision)`
			case "maximum_event_at_unix_ms":
				columns[i] = `(SELECT max(occurred_at_unix_ms) FROM native_tool_events e WHERE e.operation_id=o.operation_id)`
			default:
				columns[i] = "o." + column
			}
		}
		return `SELECT ` + strings.Join(columns, ",") + ` FROM native_tool_operations o ORDER BY o.operation_id`, nil
	}
	columns := strings.Split(p.columns, ",")
	for i, column := range columns {
		if p.table == "provider_accounts" && column == "cyber_access_program" && (revision == 1 || revision == 2) {
			columns[i] = "'standard'"
		}
		if (p.table == "capture_runs" || p.table == "manual_captures") && column == "observation" {
			columns[i] = "'waiting_for_traffic'"
		}
		if p.table == "capture_runs" && column == "first_observed_at_unix_ms" || p.table == "manual_captures" && column == "last_observed_at_unix_ms" {
			columns[i] = "NULL"
		}
	}
	return `SELECT ` + strings.Join(columns, ",") + ` FROM ` + p.table + ` ORDER BY ` + p.key, nil
}

type extractionExec interface {
	schemaQueryer
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func extractionStructure(ctx context.Context, conn extractionExec) error {
	if _, err := conn.ExecContext(ctx, `PRAGMA ignore_check_constraints=ON`); err != nil {
		return err
	}
	var flag int
	if err := conn.QueryRowContext(ctx, `PRAGMA ignore_check_constraints`).Scan(&flag); err != nil || flag != 1 {
		return ErrUnsupportedSchema
	}
	checked := extractionIntegrity(ctx, conn, `PRAGMA main.integrity_check`)
	_, restore := conn.ExecContext(ctx, `PRAGMA ignore_check_constraints=OFF`)
	readback := conn.QueryRowContext(ctx, `PRAGMA ignore_check_constraints`).Scan(&flag)
	if restore != nil || readback != nil || flag != 0 {
		return errors.Join(ErrUnsupportedSchema, checked, restore, readback)
	}
	return checked
}
func extractionIntegrity(ctx context.Context, q schemaQueryer, query string) error {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return err
		}
		if value != "ok" {
			return ErrUnsupportedSchema
		}
		count++
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if count != 1 {
		return ErrUnsupportedSchema
	}
	return nil
}
func extractionNoRows(ctx context.Context, q schemaQueryer, query string) error {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return ErrUnsupportedSchema
	}
	return errors.Join(rows.Err(), rows.Close())
}
func streamControlProjection(ctx context.Context, source schemaQueryer, target *sql.Tx, p controlProjection, query string, args []any, h hash.Hash) (int64, error) {
	fingerprintPart(h, 'T', []byte(p.table))
	fingerprintPart(h, 'C', []byte(p.columns))
	rows, err := source.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := len(strings.Split(p.columns, ","))
	values := make([]any, n)
	scan := make([]any, n)
	for i := range scan {
		scan[i] = &values[i]
	}
	var count int64
	for rows.Next() {
		if err = rows.Scan(scan...); err != nil {
			return 0, err
		}
		h.Write([]byte{'R'})
		for _, v := range values {
			switch v := v.(type) {
			case nil:
				fingerprintPart(h, 'N', nil)
			case int64:
				var b [8]byte
				binary.BigEndian.PutUint64(b[:], uint64(v))
				fingerprintPart(h, 'I', b[:])
			case string:
				fingerprintPart(h, 'S', []byte(v))
			case []byte:
				fingerprintPart(h, 'B', v)
			default:
				return 0, ErrUnsupportedSchema
			}
		}
		if target != nil {
			parameters := append([]any(nil), values...)
			placeholders := make([]string, n)
			for i, value := range values {
				placeholders[i] = "?"
				if blob, ok := value.([]byte); ok {
					placeholders[i] = "CAST(? AS BLOB)"
					if len(blob) == 0 {
						parameters[i] = ""
					}
				}
			}
			insert := `INSERT INTO ` + p.table + ` (` + p.columns + `) VALUES (` + strings.Join(placeholders, ",") + `)`
			if _, err = target.ExecContext(ctx, insert, parameters...); err != nil {
				return 0, err
			}
		}
		count++
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	h.Write([]byte{'E'})
	return count, nil
}

func validateExtractedACP(ctx context.Context, q schemaQueryer) error {
	rows, err := q.QueryContext(ctx, `SELECT run_id,policy,expires_at_unix_ms FROM acp_observations ORDER BY run_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, encoded, expiry any
		if err = rows.Scan(&id, &encoded, &expiry); err != nil {
			return err
		}
		run, ok := id.(string)
		if !ok || !roleID(run) {
			return ErrUnsupportedSchema
		}
		policy, ok := encoded.(string)
		if !ok {
			return ErrUnsupportedSchema
		}
		expires, ok := expiry.(int64)
		if !ok || expires <= 0 {
			return ErrUnsupportedSchema
		}
		var decoded environment.ContentRecordingPolicy
		if err = json.Unmarshal([]byte(policy), &decoded); err != nil || decoded.Validate() != nil {
			return ErrUnsupportedSchema
		}
	}
	return errors.Join(rows.Err(), rows.Close())
}
