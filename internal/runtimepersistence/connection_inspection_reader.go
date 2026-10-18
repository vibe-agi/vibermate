package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type ConnectionInspectionReader struct {
	catalog    *InspectionCatalog
	visibility recording.InspectionVisibility
}

func NewConnectionInspectionReader(c *InspectionCatalog, v recording.InspectionVisibility) *ConnectionInspectionReader {
	return &ConnectionInspectionReader{c, v}
}
func (r *ConnectionInspectionReader) Begin(ctx context.Context) (recording.InspectionToken, error) {
	return NewInspectionBoundary(r.visibility, r.catalog).Begin(ctx)
}
func (r *ConnectionInspectionReader) Check(ctx context.Context, t recording.InspectionToken) error {
	return NewInspectionBoundary(r.visibility, r.catalog).Check(ctx, t)
}

type connectionInspectionCandidate struct {
	id       string
	position inspectionPosition
}

func (s *auditInspectionRead) connectionSource(id string) (int, error) {
	var denied bool
	if err := s.tx.QueryRowContext(s.lease.ctx, `SELECT EXISTS(SELECT 1 FROM `+s.current+`.recording_denials WHERE kind='connection' AND identity=?)`, id).Scan(&denied); err != nil {
		return -1, err
	}
	if denied {
		return -1, nil
	}
	winner := -1
	for ordinal := range s.lease.sources {
		var present bool
		if err := s.tx.QueryRowContext(s.lease.ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM s%d.runtime_connection_events WHERE connection_id=?)`, ordinal), id).Scan(&present); err != nil {
			return -1, err
		}
		if present {
			winner = ordinal
		}
	}
	return winner, nil
}
func (s *auditInspectionRead) checkConnectionBoundary(p inspectionPosition) error {
	if int(p.Ordinal) >= len(s.lease.sources) || p.Boundary <= 0 {
		return ErrInspectionCursor
	}
	var id string
	err := s.tx.QueryRowContext(s.lease.ctx, fmt.Sprintf(`SELECT connection_id FROM s%d.runtime_connection_events WHERE sequence=?`, p.Ordinal), p.Boundary).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInspectionChanged
	}
	if err != nil {
		return err
	}
	winner, err := s.connectionSource(id)
	if err != nil {
		return err
	}
	if winner != int(p.Ordinal) {
		return ErrInspectionChanged
	}
	return nil
}
func (s *auditInspectionRead) connectionCandidates(q connectionevent.PageRequest, p *inspectionPosition, limit int) ([]connectionInspectionCandidate, bool, error) {
	ids, publications := []string{}, []string{}
	args := []any{}
	for ordinal := range s.lease.sources {
		table := fmt.Sprintf("s%d.runtime_connection_events", ordinal)
		candidate := `SELECT connection_id FROM ` + table + ` WHERE NOT EXISTS(SELECT 1 FROM ` + s.current + `.recording_denials d WHERE d.kind='connection' AND d.identity=connection_id)`
		if q.IngressID != "" {
			candidate += ` AND ingress_id=?`
			args = append(args, q.IngressID)
		}
		ids = append(ids, candidate)
		publications = append(publications, fmt.Sprintf(`SELECT %d AS ordinal,connection_id,sequence,ingress_id FROM %s WHERE connection_id IN(SELECT connection_id FROM ids)`, ordinal, table))
	}
	statement := `WITH ids AS(` + strings.Join(ids, ` UNION `) + `), publications AS(` + strings.Join(publications, ` UNION ALL `) + `), winners AS(SELECT connection_id,max(ordinal) AS ordinal FROM publications GROUP BY connection_id), selected AS(SELECT p.*,row_number() OVER(PARTITION BY p.connection_id ORDER BY p.sequence DESC) AS latest FROM publications p JOIN winners w ON w.connection_id=p.connection_id AND w.ordinal=p.ordinal) SELECT ordinal,sequence,CASE WHEN length(CAST(connection_id AS BLOB)) BETWEEN 1 AND 512 THEN connection_id END FROM selected WHERE 1=1`
	if q.LatestPerConnection {
		statement += ` AND latest=1`
	}
	if q.IngressID != "" {
		statement += ` AND ingress_id=?`
		args = append(args, q.IngressID)
	}
	if p != nil {
		statement += ` AND (ordinal<? OR (ordinal=? AND sequence<?))`
		args = append(args, p.Ordinal, p.Ordinal, p.Boundary)
	}
	statement += ` ORDER BY ordinal DESC,sequence DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.tx.QueryContext(s.lease.ctx, statement, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := []connectionInspectionCandidate{}
	more := false
	for rows.Next() {
		if len(result) == limit {
			more = true
			break
		}
		var c connectionInspectionCandidate
		if err = rows.Scan(&c.position.Ordinal, &c.position.Boundary, &c.id); err != nil {
			return nil, false, err
		}
		if c.position.Boundary <= 0 || int(c.position.Ordinal) >= len(s.lease.sources) {
			return nil, false, ErrInspectionUnavailable
		}
		result = append(result, c)
	}
	return result, more, rows.Err()
}

// Selection is per whole Connection timeline, not per phase/value. Only the
// original connection coordinates are compared between copies; all occurrences
// from the selected source survive, including identical repeated phases.
func (s *auditInspectionRead) connectionVisibility(canonical map[string]int) (map[string]bool, error) {
	result := map[string]bool{}
	if len(canonical) == 0 {
		return result, nil
	}
	if len(canonical) > auditInspectionBatch {
		return nil, ErrInspectionSourceLimit
	}
	type original struct {
		started int64
		host    string
		port    int
	}
	originals := map[string]original{}
	blocked := map[string]bool{}
	placeholders := make([]string, 0, len(canonical))
	args := make([]any, 0, len(canonical))
	for id := range canonical {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	for ordinal, source := range s.lease.sources {
		scopeColumns, scopeGroups := "", ""
		if source.Current {
			scopeColumns = `,CASE WHEN length(CAST(recording_scope_kind AS BLOB))<=32 THEN recording_scope_kind END AS bounded_scope_kind,CASE WHEN length(CAST(recording_scope_id AS BLOB))<=128 THEN recording_scope_id END AS bounded_scope_id,recording_archive_epoch,recording_capture_generation`
			scopeGroups = `,bounded_scope_kind,bounded_scope_id,recording_archive_epoch,recording_capture_generation`
		}
		statement := fmt.Sprintf(`SELECT connection_id,started_at_unix_ms,CASE WHEN length(CAST(requested_host AS BLOB))<=1024 THEN requested_host END AS bounded_host,port%s FROM s%d.runtime_connection_events WHERE connection_id IN(%s) GROUP BY connection_id,started_at_unix_ms,bounded_host,port%s`, scopeColumns, ordinal, strings.Join(placeholders, ","), scopeGroups)
		rows, err := s.tx.QueryContext(s.lease.ctx, statement, args...)
		if err != nil {
			return nil, err
		}
		ids := []string{}
		seen := map[string]bool{}
		scopes := map[string]recording.Scope{}
		for rows.Next() {
			var id string
			var value original
			var scope recording.Scope
			dest := []any{&id, &value.started, &value.host, &value.port}
			if source.Current {
				dest = append(dest, &scope.Capture.Kind, &scope.Capture.ID, &scope.Fence.ArchiveEpoch, &scope.Fence.CaptureGeneration)
			}
			if err = rows.Scan(dest...); err != nil {
				break
			}
			if seen[id] {
				err = fmt.Errorf("%w: conflicting connection original coordinates", ErrInspectionUnavailable)
				break
			}
			seen[id] = true
			if prior, found := originals[id]; found && prior != value {
				err = fmt.Errorf("%w: conflicting connection identity", ErrInspectionUnavailable)
				break
			}
			originals[id] = value
			ids = append(ids, id)
			if source.Current {
				scopes[id] = scope
			}
		}
		if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		if source.Current {
			for id, scope := range scopes {
				allowed, err := s.allows(scope)
				if err != nil {
					return nil, err
				}
				if scope.Capture.Kind != "" && !allowed {
					blocked[id] = true
				}
				if canonical[id] == ordinal {
					result[id] = allowed
				}
			}
		} else {
			proofs, err := s.legacyProofs(ordinal, "connection", ids)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				p := proofs[id]
				blocked[id] = blocked[id] || p.denied
				if canonical[id] == ordinal {
					result[id] = p.known && !p.denied
				}
			}
		}
	}
	for id := range result {
		result[id] = result[id] && !blocked[id]
	}
	return result, nil
}
func (r *ConnectionInspectionReader) List(ctx context.Context, q connectionevent.PageRequest) (connectionevent.Page, error) {
	empty := connectionevent.Page{Items: []connectionevent.Record{}}
	if q.Validate() != nil {
		return empty, connectionevent.ErrInvalidEvent
	}
	if q.BeforeSequence != 0 {
		return empty, ErrInspectionCursor
	}
	if r == nil {
		return empty, ErrInspectionUnavailable
	}
	s, err := beginAuditInspection(ctx, r.catalog, r.visibility)
	if err != nil {
		return empty, err
	}
	defer s.close()
	binding := q
	binding.Cursor = ""
	binding.Limit = 0
	encoded, err := json.Marshal(struct {
		Query   connectionevent.PageRequest
		Control recording.InspectionStamp
	}{binding, s.token.Control})
	if err != nil {
		return empty, err
	}
	query := string(encoded)
	var boundary *inspectionPosition
	if q.Cursor != "" {
		p, err := r.catalog.decodeCursor("connection-page", query, q.Cursor)
		if err != nil {
			return empty, err
		}
		if err = s.checkConnectionBoundary(p); err != nil {
			return empty, err
		}
		boundary = &p
	}
	candidates, more, err := s.connectionCandidates(q, boundary, max(q.Limit, 64))
	if err != nil {
		return empty, err
	}
	canonical := map[string]int{}
	for _, c := range candidates {
		canonical[c.id] = int(c.position.Ordinal)
	}
	allowed, err := s.connectionVisibility(canonical)
	if err != nil {
		return empty, err
	}
	page := empty
	var consumed *inspectionPosition
	for i, c := range candidates {
		// Validate every consumed selected-source event, including unattributed
		// ones, rather than misclassify malformed data as a successful empty page.
		source := s.lease.sources[c.position.Ordinal]
		statement := inspectionConnectionSelect(fmt.Sprintf("s%d", c.position.Ordinal), source) + ` WHERE sequence=?`
		record, err := scanConnectionEvent(s.tx.QueryRowContext(s.lease.ctx, statement, c.position.Boundary))
		if err != nil {
			return empty, err
		}
		if record.ConnectionID != c.id {
			return empty, ErrInspectionChanged
		}
		position := c.position
		consumed = &position
		if allowed[c.id] {
			page.Items = append(page.Items, record)
		}
		if len(page.Items) == q.Limit {
			more = more || i+1 < len(candidates)
			break
		}
	}
	if more && consumed != nil {
		page.NextCursor, err = r.catalog.encodeCursor("connection-page", query, *consumed)
		if err != nil {
			return empty, err
		}
	}
	s.endSQL()
	if err = r.Check(s.lease.ctx, s.token); err != nil {
		return empty, err
	}
	return page, nil
}
func (r *ConnectionInspectionReader) Timeline(ctx context.Context, id string) (connectionevent.Timeline, error) {
	empty := connectionevent.Timeline{}
	if id == "" || len(id) > connectionevent.MaxIdentityBytes || !utf8.ValidString(id) || strings.TrimSpace(id) != id {
		return empty, connectionevent.ErrInvalidEvent
	}
	if r == nil {
		return empty, ErrInspectionUnavailable
	}
	s, err := beginAuditInspection(ctx, r.catalog, r.visibility)
	if err != nil {
		return empty, err
	}
	defer s.close()
	ordinal, err := s.connectionSource(id)
	if err != nil {
		return empty, err
	}
	if ordinal < 0 {
		return empty, connectionevent.ErrNotFound
	}
	allowed, err := s.connectionVisibility(map[string]int{id: ordinal})
	if err != nil {
		return empty, err
	}
	rows, err := s.tx.QueryContext(s.lease.ctx, inspectionConnectionSelect(fmt.Sprintf("s%d", ordinal), s.lease.sources[ordinal])+` WHERE connection_id=? ORDER BY sequence LIMIT ?`, id, connectionevent.MaxTimelineSize+1)
	if err != nil {
		return empty, err
	}
	timeline := connectionevent.Timeline{ConnectionID: id, Events: []connectionevent.Record{}}
	for rows.Next() {
		var record connectionevent.Record
		record, err = scanConnectionEvent(rows)
		if err != nil {
			break
		}
		if len(timeline.Events) >= connectionevent.MaxTimelineSize {
			err = ErrInspectionSourceLimit
			break
		}
		timeline.Events = append(timeline.Events, record)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return empty, err
	}
	s.endSQL()
	if err = r.Check(s.lease.ctx, s.token); err != nil {
		return empty, err
	}
	if !allowed[id] {
		return empty, connectionevent.ErrNotFound
	}
	return timeline, nil
}

var _ connectionevent.InspectionReader = (*ConnectionInspectionReader)(nil)
