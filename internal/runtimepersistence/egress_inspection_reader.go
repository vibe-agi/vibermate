package runtimepersistence

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type EgressInspectionReader struct {
	catalog    *InspectionCatalog
	visibility recording.InspectionVisibility
}

func NewEgressInspectionReader(c *InspectionCatalog, v recording.InspectionVisibility) *EgressInspectionReader {
	return &EgressInspectionReader{c, v}
}
func (r *EgressInspectionReader) Begin(ctx context.Context) (recording.InspectionToken, error) {
	return NewInspectionBoundary(r.visibility, r.catalog).Begin(ctx)
}
func (r *EgressInspectionReader) Check(ctx context.Context, t recording.InspectionToken) error {
	return NewInspectionBoundary(r.visibility, r.catalog).Check(ctx, t)
}

type egressInspectionCandidate struct {
	id       string
	position inspectionPosition
}

func egressInspectionFilter(q egressaudit.PageRequest) (string, []any) {
	where := "1=1"
	args := []any{}
	for _, v := range []struct{ column, value string }{{"connection_id", q.ConnectionID}, {"parent_exchange_id", q.ExchangeID}, {"parent_kind", string(q.ParentKind)}, {"parent_id", q.ParentID}, {"purpose", string(q.Purpose)}} {
		if v.value != "" {
			where += ` AND ` + v.column + `=?`
			args = append(args, v.value)
		}
	}
	return where, args
}
func (s *auditInspectionRead) egressCandidates(q egressaudit.PageRequest, p *inspectionPosition, limit int) ([]egressInspectionCandidate, bool, error) {
	ids, publications := []string{}, []string{}
	args := []any{}
	where, filters := egressInspectionFilter(q)
	for i := range s.lease.sources {
		table := fmt.Sprintf("s%d.runtime_egress_attempts", i)
		ids = append(ids, `SELECT attempt_id FROM `+table+` WHERE `+where+` AND NOT EXISTS(SELECT 1 FROM `+s.current+`.recording_denials d WHERE (d.kind='attempt' AND d.identity=attempt_id) OR (d.kind='connection' AND d.identity=connection_id) OR (d.kind='exchange' AND d.identity=parent_exchange_id))`)
		args = append(args, filters...)
		publications = append(publications, fmt.Sprintf(`SELECT %d AS ordinal,sequence,attempt_id,(outcome<>'') AS terminal FROM %s WHERE attempt_id IN(SELECT attempt_id FROM ids)`, i, table))
	}
	query := `WITH ids AS(` + strings.Join(ids, ` UNION `) + `), publications AS(` + strings.Join(publications, ` UNION ALL `) + `), ranked AS(SELECT *,row_number() OVER(PARTITION BY attempt_id ORDER BY terminal DESC,ordinal DESC) AS rank FROM publications) SELECT ordinal,sequence,CASE WHEN length(CAST(attempt_id AS BLOB)) BETWEEN 1 AND 512 THEN attempt_id END FROM ranked WHERE rank=1`
	if p != nil {
		query += ` AND (ordinal<? OR (ordinal=? AND sequence<?))`
		args = append(args, p.Ordinal, p.Ordinal, p.Boundary)
	}
	query += ` ORDER BY ordinal DESC,sequence DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.tx.QueryContext(s.lease.ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := []egressInspectionCandidate{}
	more := false
	for rows.Next() {
		if len(result) == limit {
			more = true
			break
		}
		var c egressInspectionCandidate
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
func egressInspectionMatches(a egressaudit.Attempt, q egressaudit.PageRequest) bool {
	return (q.ConnectionID == "" || a.ConnectionID() == q.ConnectionID) && (q.ExchangeID == "" || a.Parent().ExchangeID == q.ExchangeID) && (q.ParentKind == "" || (a.Parent().Kind == q.ParentKind && a.Parent().ID == q.ParentID)) && (q.Purpose == "" || a.Purpose() == q.Purpose)
}
func (r *EgressInspectionReader) List(ctx context.Context, request egressaudit.PageRequest) (result egressaudit.Page, resultErr error) {
	if r != nil {
		defer func(caller context.Context) { resultErr = r.catalog.classifyReadCancellation(caller, resultErr) }(ctx)
	}
	empty := egressaudit.Page{Items: []egressaudit.Record{}}
	q, err := request.Normalized()
	if err != nil {
		return empty, err
	}
	for _, value := range []string{q.ConnectionID, q.ExchangeID, q.ParentID, string(q.ParentKind)} {
		if len(value) > 512 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
			return empty, fmt.Errorf("invalid egress inspection filter")
		}
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
	binding.AfterCursor = ""
	binding.Limit = 0
	encoded, err := json.Marshal(struct {
		Query   egressaudit.PageRequest
		Control recording.InspectionStamp
	}{binding, s.token.Control})
	if err != nil {
		return empty, err
	}
	query := string(encoded)
	var boundary *inspectionPosition
	if q.AfterCursor != "" {
		p, err := r.catalog.decodeCursor("egress-page", query, q.AfterCursor)
		if err != nil {
			return empty, err
		}
		boundary = &p
	}
	batch := max(q.Limit, 64)
	candidates, more, err := s.egressCandidates(q, boundary, batch)
	if err != nil {
		return empty, err
	}
	type entry struct {
		candidate                                            egressInspectionCandidate
		start                                                egressaudit.NewInput
		terminal                                             egressaudit.TerminalInput
		initialized, hasTerminal, selected, allowed, blocked bool
		record                                               egressaudit.Record
	}
	entries := make(map[string]*entry, len(candidates))
	values := make([]string, len(candidates))
	args := make([]any, 0, len(candidates))
	for i, c := range candidates {
		entries[c.id] = &entry{candidate: c}
		values[i] = "?"
		args = append(args, c.id)
	}
	retained := 0
	for ordinal, source := range s.lease.sources {
		if len(candidates) == 0 {
			break
		}
		alias := fmt.Sprintf("s%d", ordinal)
		statement := inspectionEgressSelect(alias, source)
		if source.Current {
			statement = strings.Replace(statement, " FROM ", ",CASE WHEN length(CAST(recording_scope_kind AS BLOB))<=32 THEN recording_scope_kind END,CASE WHEN length(CAST(recording_scope_id AS BLOB))<=128 THEN recording_scope_id END,recording_archive_epoch,recording_capture_generation FROM ", 1)
		}
		statement += ` WHERE attempt_id IN(` + strings.Join(values, ",") + `)`
		rows, err := s.tx.QueryContext(s.lease.ctx, statement, args...)
		if err != nil {
			return empty, err
		}
		sourceIDs := []string{}
		type scoped struct {
			id    string
			scope recording.Scope
		}
		scopes := []scoped{}
		for rows.Next() {
			var scope recording.Scope
			extra := []any{}
			if source.Current {
				extra = []any{&scope.Capture.Kind, &scope.Capture.ID, &scope.Fence.ArchiveEpoch, &scope.Fence.CaptureGeneration}
			}
			record, scanErr := scanEgressAttempt(rows, extra...)
			if scanErr != nil {
				err = scanErr
				break
			}
			value := entries[record.Attempt.ID()]
			if value == nil {
				err = ErrInspectionUnavailable
				break
			}
			start := normalizedEgressStart(record.Attempt)
			if value.initialized && value.start != start {
				err = fmt.Errorf("%w: conflicting egress immutable identity", ErrInspectionUnavailable)
				break
			}
			if record.Attempt.Terminal() {
				terminal := normalizedEgressTerminal(record.Attempt)
				if value.hasTerminal && value.terminal != terminal {
					err = fmt.Errorf("%w: conflicting egress terminal", ErrInspectionUnavailable)
					break
				}
				value.terminal = terminal
				value.hasTerminal = true
			}
			if !value.initialized {
				value.start = start
				value.initialized = true
				data, _ := json.Marshal(egressaudit.ViewOf(record))
				retained += len(data) + int(unsafe.Sizeof(start))
			}
			if value.candidate.position.Ordinal == uint32(ordinal) {
				if record.Sequence != value.candidate.position.Boundary {
					err = ErrInspectionChanged
					break
				}
				value.selected = true
				value.record = record
				data, _ := json.Marshal(egressaudit.ViewOf(record))
				retained += len(data) + int(unsafe.Sizeof(record))
			}
			if retained > auditInspectionMaximumBytes {
				err = ErrInspectionSourceLimit
				break
			}
			sourceIDs = append(sourceIDs, record.Attempt.ID())
			if source.Current {
				scopes = append(scopes, scoped{record.Attempt.ID(), scope})
			}
		}
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = rows.Err()
		}
		if err != nil {
			return empty, err
		}
		if source.Current {
			for _, v := range scopes {
				value := entries[v.id]
				allowed, err := s.allows(v.scope)
				if err != nil {
					return empty, err
				}
				if v.scope.Capture.Kind != "" && !allowed {
					value.blocked = true
				}
				if value.candidate.position.Ordinal == uint32(ordinal) {
					value.allowed = allowed
				}
			}
		} else {
			proofs, err := s.legacyProofs(ordinal, "attempt", sourceIDs)
			if err != nil {
				return empty, err
			}
			for _, id := range sourceIDs {
				value := entries[id]
				proof := proofs[id]
				value.blocked = value.blocked || proof.denied
				if value.candidate.position.Ordinal == uint32(ordinal) {
					global := value.record.Attempt.Parent().Kind == egressaudit.ParentRuntimeAction && value.record.Attempt.Parent().ExchangeID == "" && value.record.Attempt.ConnectionID() == ""
					value.allowed = (proof.known && !proof.denied) || (!proof.known && global && s.token.Control.ArchiveEpoch == 1)
				}
			}
		}
	}
	page := empty
	var consumed *inspectionPosition
	for i, c := range candidates {
		value := entries[c.id]
		if !value.selected {
			return empty, ErrInspectionUnavailable
		}
		position := c.position
		consumed = &position
		if value.allowed && !value.blocked && egressInspectionMatches(value.record.Attempt, q) {
			page.Items = append(page.Items, value.record)
		}
		if len(page.Items) == q.Limit {
			more = more || i+1 < len(candidates)
			break
		}
	}
	if more && consumed != nil {
		page.NextCursor, err = r.catalog.encodeCursor("egress-page", query, *consumed)
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

var _ egressaudit.InspectionReader = (*EgressInspectionReader)(nil)
