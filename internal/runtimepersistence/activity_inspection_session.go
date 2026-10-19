package runtimepersistence

import (
	"errors"
	"fmt"
	"strings"

	"github.com/vibe-agi/vibermate/internal/activity"
)

// Session summaries keep exact association identity and original ownership
// until the canonical request relation. Matched identities are validated in
// bounded indexed batches, never collected into an all-history Go map.
func (r *ActivityInspectionReader) sessionIDs(s *activityInspectionRead, q activity.SummaryScope) (string, []any, error) {
	parts := []string{}
	args := []any{}
	for ordinal := range s.lease.sources {
		parts = append(parts, fmt.Sprintf(`SELECT exchange_id FROM s%d.runtime_exchange_agent_identities WHERE client_kind=? AND session_id=?`, ordinal))
		args = append(args, q.Client, q.SessionID)
	}
	cte := `WITH identity_ids AS (` + strings.Join(parts, ` UNION `) + `)`
	after := ""
	for {
		values := append(append([]any{}, args...), after)
		rows, err := s.tx.QueryContext(s.lease.ctx, cte+` SELECT CASE WHEN length(CAST(exchange_id AS BLOB)) BETWEEN 1 AND 512 THEN exchange_id END FROM identity_ids WHERE exchange_id>? ORDER BY exchange_id LIMIT 256`, values...)
		if err != nil {
			return "", nil, err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
			return "", nil, err
		}
		for _, id := range ids {
			if !validActivityInspectionID(id) {
				return "", nil, activity.ErrInvalidEvent
			}
			if _, err = r.identity(s, id); err != nil && !errors.Is(err, activity.ErrExchangeNotFound) {
				return "", nil, err
			}
		}
		if len(ids) < 256 {
			break
		}
		after = ids[len(ids)-1]
	}
	copies := []string{}
	for ordinal, source := range s.lease.sources {
		if source.Current {
			copies = append(copies, fmt.Sprintf(`SELECT i.exchange_id,i.client_kind,i.session_id,i.recording_scope_kind AS scope_kind,i.recording_scope_id AS scope_id,i.recording_archive_epoch AS epoch,i.recording_capture_generation AS generation,(i.recording_archive_epoch>0) AS known FROM s%d.runtime_exchange_agent_identities i WHERE i.exchange_id IN(SELECT exchange_id FROM identity_ids)`, ordinal))
			continue
		}
		// Only retained parents in this SAME source can prove old unscoped roots.
		cte += fmt.Sprintf(`, owner_rows%d AS (
 SELECT a.subject_id AS exchange_id,CASE WHEN a.capture_run_id<>'' THEN 'managed_run' WHEN a.manual_capture_id<>'' THEN 'manual_capture' ELSE '' END AS scope_kind,CASE WHEN a.capture_run_id<>'' THEN a.capture_run_id ELSE a.manual_capture_id END AS scope_id FROM s%d.runtime_activities a WHERE a.kind IN('exchange.started','exchange.completed') AND a.subject_id IN(SELECT exchange_id FROM identity_ids)
 UNION SELECT exchange_id,scope_kind,scope_id FROM s%d.runtime_exchange_contents WHERE exchange_id IN(SELECT exchange_id FROM identity_ids)
 ), owners%d AS(SELECT exchange_id,min(scope_kind) AS scope_kind,min(scope_id) AS scope_id,count(*) AS roots FROM owner_rows%d GROUP BY exchange_id)`, ordinal, ordinal, ordinal, ordinal, ordinal)
		copies = append(copies, fmt.Sprintf(`SELECT i.exchange_id,i.client_kind,i.session_id,coalesce(o.scope_kind,'') AS scope_kind,coalesce(o.scope_id,'') AS scope_id,1 AS epoch,CASE WHEN o.scope_kind<>'' THEN 1 ELSE 0 END AS generation,(coalesce(o.roots,0)=1) AS known FROM s%d.runtime_exchange_agent_identities i LEFT JOIN owners%d o ON o.exchange_id=i.exchange_id WHERE i.exchange_id IN(SELECT exchange_id FROM identity_ids)`, ordinal, ordinal))
	}
	cte += `, identity_copies AS (` + strings.Join(copies, ` UNION ALL `) + `)`
	visible := fmt.Sprintf(`(x.epoch=%d AND ((x.scope_kind='' AND x.scope_id='' AND x.generation=0) OR EXISTS(SELECT 1 FROM s%d.inspection_mirror_fences f WHERE f.mirror_generation=%d AND f.capture_kind=x.scope_kind AND f.capture_id=x.scope_id AND f.capture_generation=x.generation AND f.deleted=0)))`, s.token.Control.ArchiveEpoch, len(s.lease.sources)-1, s.mirror.Generation)
	cte += `, session_roots AS(SELECT exchange_id,min(client_kind) AS client_kind,min(session_id) AS session_id,min(scope_kind) AS scope_kind,min(scope_id) AS scope_id,min(epoch) AS epoch,min(generation) AS generation FROM identity_copies x WHERE known=1 GROUP BY exchange_id HAVING min(` + visible + `)=1), ids AS(SELECT exchange_id AS subject_id,scope_kind,scope_id,epoch,generation FROM session_roots WHERE client_kind=? AND session_id=?)`
	args = append(args, q.Client, q.SessionID)
	return cte, args, nil
}
func activityIdentityScopeMatch(source InspectionSource) string {
	epoch, generation, kind, id := activityScopeSQL(source, "a.")
	return fmt.Sprintf(`EXISTS(SELECT 1 FROM ids x WHERE x.subject_id=a.subject_id AND x.scope_kind=(%s) AND x.scope_id=(%s) AND x.epoch=(%s) AND x.generation=(%s))`, kind, id, epoch, generation)
}
