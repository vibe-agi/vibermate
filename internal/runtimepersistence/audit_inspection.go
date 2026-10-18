package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/vibe-agi/vibermate/internal/recording"
)

const auditInspectionBatch = 1024
const auditInspectionMaximumNodes = 65536
const auditInspectionMaximumAssociations = 131072
const auditInspectionMaximumBytes = 8 << 20

type auditInspectionRead struct {
	lease   *inspectionLease
	tx      *sql.Tx
	token   recording.InspectionToken
	mirror  inspectionMirror
	current string
	cancel  context.CancelFunc
}

func (s *auditInspectionRead) endSQL() {
	if s.tx != nil {
		s.tx.Rollback()
		s.tx = nil
	}
}
func (s *auditInspectionRead) close() { s.endSQL(); s.lease.close(); s.cancel() }
func beginAuditInspection(ctx context.Context, c *InspectionCatalog, v recording.InspectionVisibility) (*auditInspectionRead, error) {
	if ctx == nil || c == nil || v == nil {
		return nil, ErrInspectionUnavailable
	}
	work, cancel := context.WithTimeout(ctx, c.timeout)
	snapshot, err := v.ReadInspectionSnapshot(work, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	if !snapshot.Stamp.Valid() || snapshot.Stamp.LayoutID != c.layout {
		cancel()
		return nil, ErrInspectionUnavailable
	}
	lease, err := c.acquire(work)
	if err != nil {
		cancel()
		return nil, err
	}
	s := &auditInspectionRead{lease: lease, token: recording.InspectionToken{Control: snapshot.Stamp, Sources: lease.version}, current: fmt.Sprintf("s%d", len(lease.sources)-1), cancel: cancel}
	s.tx, err = lease.conn.BeginTx(lease.ctx, &sql.TxOptions{ReadOnly: true})
	if err == nil {
		s.mirror, err = readInspectionMirror(lease.ctx, s.tx, s.current, snapshot.Stamp)
	}
	if err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}
func (s *auditInspectionRead) allows(scope recording.Scope) (bool, error) {
	if scope == (recording.Scope{}) {
		return false, nil
	}
	if !validOriginalRecordingScope(scope) {
		return false, ErrInspectionUnavailable
	}
	if scope.Fence.ArchiveEpoch != s.token.Control.ArchiveEpoch {
		return false, nil
	}
	if scope.Capture.Kind == "" {
		return true, nil
	}
	var allowed bool
	err := s.tx.QueryRowContext(s.lease.ctx, `SELECT EXISTS(SELECT 1 FROM `+s.current+`.inspection_mirror_fences WHERE mirror_generation=? AND capture_kind=? AND capture_id=? AND capture_generation=? AND deleted=0)`, s.mirror.Generation, scope.Capture.Kind, scope.Capture.ID, scope.Fence.CaptureGeneration).Scan(&allowed)
	return allowed, err
}

type legacyAuditProof struct{ known, denied bool }

// Legacy audits do not carry a trusted scope. This source-local closure keeps
// only bounded node/association relations in SQLite and returns one scalar
// verdict per requested identity, never a copied Capture map. Ingress strings
// and purpose labels are deliberately absent from the ownership roots.
func (s *auditInspectionRead) legacyProofs(ordinal int, kind string, ids []string) (map[string]legacyAuditProof, error) {
	result := map[string]legacyAuditProof{}
	if len(ids) == 0 {
		return result, nil
	}
	if len(ids) > auditInspectionBatch || (kind != "attempt" && kind != "connection") || ordinal < 0 || ordinal >= len(s.lease.sources) || s.lease.sources[ordinal].Current {
		return nil, ErrInspectionSourceLimit
	}
	alias := fmt.Sprintf("s%d", ordinal)
	audit := alias + `.runtime_egress_attempts`
	values := make([]string, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		values[i] = "(?)"
		args = append(args, id)
	}
	bounded := func(column string) string {
		return `CASE WHEN typeof(` + column + `)='text' AND length(CAST(` + column + ` AS BLOB)) BETWEEN 1 AND 512 THEN ` + column + ` END`
	}
	cte := `WITH RECURSIVE requested(id) AS(VALUES ` + strings.Join(values, ",") + `), nodes(kind,id) AS(SELECT '` + kind + `',id FROM requested
 UNION SELECT 'connection',` + bounded("a.connection_id") + ` FROM nodes n JOIN ` + audit + ` a ON n.kind='attempt' AND a.attempt_id=n.id WHERE a.connection_id<>''
 UNION SELECT 'exchange',` + bounded("a.parent_exchange_id") + ` FROM nodes n JOIN ` + audit + ` a ON n.kind='attempt' AND a.attempt_id=n.id WHERE a.parent_exchange_id<>''
 UNION SELECT 'attempt',` + bounded("a.attempt_id") + ` FROM nodes n JOIN ` + audit + ` a ON n.kind='connection' AND a.connection_id=n.id
 UNION SELECT 'attempt',` + bounded("a.attempt_id") + ` FROM nodes n JOIN ` + audit + ` a ON n.kind='exchange' AND a.parent_exchange_id=n.id WHERE a.parent_exchange_id<>''
 LIMIT ` + fmt.Sprint(auditInspectionMaximumNodes+1) + `)`
	var nodes, invalid int
	if err := s.tx.QueryRowContext(s.lease.ctx, cte+` SELECT count(*),coalesce(max(id IS NULL),0) FROM nodes`, args...).Scan(&nodes, &invalid); err != nil {
		return nil, err
	}
	if nodes > auditInspectionMaximumNodes {
		return nil, fmt.Errorf("%w: legacy audit graph exceeds %d nodes", ErrInspectionSourceLimit, auditInspectionMaximumNodes)
	}
	if invalid != 0 {
		return nil, ErrInspectionUnavailable
	}
	// A corrupt/misclassified linked attempt cannot supply an ownership edge or
	// manufacture runtime-global provenance. Validate its actual frozen fields.
	rows, err := s.tx.QueryContext(s.lease.ctx, cte+` `+inspectionEgressSelect(alias, s.lease.sources[ordinal])+` WHERE attempt_id IN(SELECT id FROM nodes WHERE kind='attempt')`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		if _, err = scanEgressAttempt(rows); err != nil {
			break
		}
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	activityScope := `CASE WHEN a.capture_run_id<>'' THEN 'managed_run' ELSE 'manual_capture' END,CASE WHEN a.capture_run_id<>'' THEN a.capture_run_id ELSE a.manual_capture_id END,(a.capture_run_id<>'' AND a.manual_capture_id<>'')`
	roots := []string{
		`SELECT n.kind,n.id,` + activityScope + ` FROM nodes n JOIN ` + alias + `.runtime_activities a ON n.kind='connection' AND a.connection_id=n.id WHERE a.capture_run_id<>'' OR a.manual_capture_id<>''`,
		`SELECT n.kind,n.id,` + activityScope + ` FROM nodes n JOIN ` + alias + `.runtime_activities a ON n.kind='exchange' AND a.subject_id=n.id WHERE a.kind IN('exchange.started','exchange.completed') AND (a.capture_run_id<>'' OR a.manual_capture_id<>'')`,
		`SELECT n.kind,n.id,c.scope_kind,c.scope_id,0 FROM nodes n JOIN ` + alias + `.runtime_exchange_contents c ON n.kind='exchange' AND c.exchange_id=n.id WHERE c.scope_kind<>'' OR c.scope_id<>''`,
	}
	for _, link := range []struct{ kind, column string }{{"attempt", "attempt_id"}, {"connection", "connection_id"}, {"exchange", "exchange_id"}} {
		roots = append(roots, `SELECT n.kind,n.id,r.scope_kind,r.scope_id,0 FROM nodes n JOIN `+alias+`.runtime_raw_evidence_envelopes r ON n.kind='`+link.kind+`' AND r.`+link.column+`=n.id WHERE r.scope_kind<>'runtime' OR r.scope_id<>''`)
	}
	if publishedNativeRevision(s.lease.sources[ordinal].Database.Revision) {
		roots = append(roots, `SELECT n.kind,n.id,w.capture_kind,w.capture_id,(length(w.replay_key)<>32 OR length(w.intent_digest)<>32 OR length(w.operation_id)<>32 OR w.operation_id GLOB '*[^0-9a-f]*' OR w.claimed_at_unix_ms<=0 OR w.authority_revision<=0) FROM nodes n JOIN `+alias+`.native_tool_once w ON n.kind='attempt' AND w.attempt_id=n.id`)
	}
	cte += `, roots(node_kind,node_id,capture_kind,capture_id,bad) AS(` + strings.Join(roots, ` UNION ALL `) + `), owners(node_kind,node_id,capture_kind,capture_id,bad) AS(
 SELECT node_kind,node_id,capture_kind,capture_id,bad OR typeof(capture_kind)<>'text' OR capture_kind NOT IN('managed_run','manual_capture') OR typeof(capture_id)<>'text' OR length(CAST(capture_id AS BLOB)) NOT BETWEEN 1 AND 128 OR capture_id GLOB '*[^a-zA-Z0-9_.:-]*' FROM roots
 UNION SELECT 'connection',a.connection_id,o.capture_kind,o.capture_id,o.bad FROM owners o JOIN ` + audit + ` a ON o.node_kind='attempt' AND a.attempt_id=o.node_id WHERE a.connection_id<>''
 UNION SELECT 'exchange',a.parent_exchange_id,o.capture_kind,o.capture_id,o.bad FROM owners o JOIN ` + audit + ` a ON o.node_kind='attempt' AND a.attempt_id=o.node_id WHERE a.parent_exchange_id<>''
 UNION SELECT 'attempt',a.attempt_id,o.capture_kind,o.capture_id,o.bad FROM owners o JOIN ` + audit + ` a ON o.node_kind='connection' AND a.connection_id=o.node_id
 UNION SELECT 'attempt',a.attempt_id,o.capture_kind,o.capture_id,o.bad FROM owners o JOIN ` + audit + ` a ON o.node_kind='exchange' AND a.parent_exchange_id=o.node_id WHERE a.parent_exchange_id<>''
 LIMIT ` + fmt.Sprint(auditInspectionMaximumAssociations+1) + `)`
	query := cte + ` SELECT r.id,count(o.node_id),coalesce(max(o.bad),0),coalesce(max(?<>1 OR NOT EXISTS(SELECT 1 FROM ` + s.current + `.inspection_mirror_fences f WHERE f.mirror_generation=? AND f.capture_kind=o.capture_kind AND f.capture_id=o.capture_id AND f.capture_generation=1 AND f.deleted=0)),0),(SELECT count(*) FROM owners) FROM requested r LEFT JOIN owners o ON o.node_kind='` + kind + `' AND o.node_id=r.id GROUP BY r.id`
	args = append(args, s.token.Control.ArchiveEpoch, s.mirror.Generation)
	rows, err = s.tx.QueryContext(s.lease.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var known, bad, denied, total int
		if err = rows.Scan(&id, &known, &bad, &denied, &total); err != nil {
			break
		}
		if total > auditInspectionMaximumAssociations {
			err = fmt.Errorf("%w: legacy audit graph exceeds %d ownership associations", ErrInspectionSourceLimit, auditInspectionMaximumAssociations)
			break
		}
		if bad != 0 {
			err = ErrInspectionUnavailable
			break
		}
		result[id] = legacyAuditProof{known: known > 0, denied: known > 0 && denied > 0}
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return result, nil
}

// The projection is a closed scalar column list, never an optional body read.
func inspectionAuditSelect(base, table, alias string, defaults map[string]string) string {
	columns := strings.Split(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(base, "SELECT"), "FROM "+table)), ",")
	for i, name := range columns {
		name = strings.TrimSpace(name)
		expression := name
		if replacement, found := defaults[name]; found {
			expression = replacement
		}
		columns[i] = `CASE WHEN length(CAST(` + expression + ` AS BLOB))<=4096 THEN ` + expression + ` END AS ` + name
	}
	return `SELECT ` + strings.Join(columns, ",") + ` FROM ` + alias + `.` + table
}
func inspectionEgressSelect(alias string, source InspectionSource) string {
	defaults := map[string]string{}
	if !source.Current && source.Database.Revision == 1 {
		defaults = map[string]string{"selection_source": "''", "default_revision": "0", "byte_counts_known": "1"}
	}
	return inspectionAuditSelect(egressAttemptSelect, "runtime_egress_attempts", alias, defaults)
}
func inspectionConnectionSelect(alias string, source InspectionSource) string {
	defaults := map[string]string{}
	if !source.Current && source.Database.Revision == 1 {
		defaults["inspection_purpose"] = "''"
	}
	return inspectionAuditSelect(connectionEventSelect, "runtime_connection_events", alias, defaults)
}
