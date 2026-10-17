package runtimepersistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// An equal copy published in current must not move an existing Conversation's
// first-publication anchor. Only copies of the selected lifecycle kind supply
// that anchor; timestamps remain actual winning request observation facts.
func (s *activityInspectionRead) exchangeAnchorSQL() (string, string) {
	ordinal := `CASE `
	sequences := []string{}
	for i := range s.lease.sources {
		predicate := `subject_id=c.subject_id AND kind IN('exchange.started','exchange.completed') AND kind=c.kind`
		ordinal += fmt.Sprintf(`WHEN EXISTS(SELECT 1 FROM s%d.runtime_activities WHERE %s) THEN %d `, i, predicate, i)
		sequences = append(sequences, fmt.Sprintf(`(SELECT min(sequence) FROM s%d.runtime_activities WHERE %s)`, i, predicate))
	}
	return ordinal + `ELSE NULL END`, `coalesce(` + strings.Join(append(sequences, "NULL"), ",") + `)`
}
func (r *ActivityInspectionReader) ListConversations(ctx context.Context, q activity.ConversationIndexRequest) (activity.ConversationPage, error) {
	empty := activity.ConversationPage{Items: []activity.ConversationRecord{}}
	if q.Validate() != nil {
		return empty, activity.ErrInvalidEvent
	}
	if q.BeforeFirstSequence != 0 {
		return empty, ErrInspectionCursor
	}
	s, err := r.begin(ctx)
	if err != nil {
		return empty, err
	}
	defer s.close()
	request := q
	request.Cursor = ""
	encoded, _ := json.Marshal(struct {
		Query activity.ConversationIndexRequest
		Stamp recording.InspectionStamp
	}{request, s.token.Control})
	binding := string(encoded)
	var before *inspectionPosition
	if q.Cursor != "" {
		p, err := r.catalog.decodeCursor("activity-conversations", binding, q.Cursor)
		if err != nil {
			return empty, err
		}
		before = &p
	}
	cte, args, err := s.canonicalRelation(activity.PageRequest{CaptureRunID: q.CaptureRunID, ManualCaptureID: q.ManualCaptureID}, `a.subject_id,a.kind,a.conversation_projection_id,a.conversation_display_name,a.conversation_actor,a.occurred_at_unix_ms`)
	if err != nil {
		return empty, err
	}
	ordinal, sequence := s.exchangeAnchorSQL()
	cte += `, anchored AS(SELECT c.*,` + ordinal + ` AS first_ordinal,` + sequence + ` AS first_sequence FROM canonical c), ranked AS(
 SELECT *,row_number() OVER(PARTITION BY conversation_projection_id ORDER BY first_ordinal,first_sequence) AS first_rank,
 row_number() OVER(PARTITION BY conversation_projection_id ORDER BY ordinal DESC,sequence DESC) AS latest_rank,
 row_number() OVER(PARTITION BY conversation_projection_id ORDER BY CASE WHEN trim(conversation_display_name)<>'' AND conversation_display_name<>conversation_actor THEN 0 ELSE 1 END,ordinal DESC,sequence DESC) AS name_rank
 FROM anchored), grouped AS(SELECT conversation_projection_id,count(*) AS turns,min(occurred_at_unix_ms) AS first_at FROM canonical GROUP BY conversation_projection_id)
 SELECT CASE WHEN length(CAST(g.conversation_projection_id AS BLOB)) BETWEEN 1 AND 512 THEN g.conversation_projection_id END,
 f.first_ordinal,f.first_sequence,g.turns,g.first_at,
 CASE WHEN length(CAST(n.conversation_display_name AS BLOB))<=512 THEN n.conversation_display_name END,
 l.ordinal,l.sequence,CASE WHEN length(CAST(l.subject_id AS BLOB)) BETWEEN 1 AND 512 THEN l.subject_id END
 FROM grouped g JOIN ranked f ON f.conversation_projection_id=g.conversation_projection_id AND f.first_rank=1
 JOIN ranked l ON l.conversation_projection_id=g.conversation_projection_id AND l.latest_rank=1
 JOIN ranked n ON n.conversation_projection_id=g.conversation_projection_id AND n.name_rank=1`
	if before != nil {
		cte += ` WHERE (f.first_ordinal<? OR(f.first_ordinal=? AND f.first_sequence<?))`
		args = append(args, before.Ordinal, before.Ordinal, before.Boundary)
	}
	cte += ` ORDER BY f.first_ordinal DESC,f.first_sequence DESC LIMIT ?`
	args = append(args, q.Limit+1)
	rows, err := s.tx.QueryContext(s.lease.ctx, cte, args...)
	if err != nil {
		return empty, err
	}
	type group struct {
		id, name, latest string
		anchor, position inspectionPosition
		turns            int
		first            int64
	}
	groups := []group{}
	for rows.Next() {
		var g group
		if err = rows.Scan(&g.id, &g.anchor.Ordinal, &g.anchor.Boundary, &g.turns, &g.first, &g.name, &g.position.Ordinal, &g.position.Boundary, &g.latest); err != nil {
			break
		}
		if g.turns <= 0 || !validActivityMillis(g.first) || g.anchor.Boundary <= 0 {
			err = ErrInspectionUnavailable
			break
		}
		groups = append(groups, g)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return empty, err
	}
	more := len(groups) > q.Limit
	if more {
		groups = groups[:q.Limit]
	}
	result := activity.ConversationPage{Items: []activity.ConversationRecord{}}
	bytes := 0
	for _, g := range groups {
		latest, err := s.exchange(g.latest)
		if err != nil {
			return empty, err
		}
		if latest.position != g.position || latest.record.Conversation == nil || latest.record.Conversation.ProjectionID != g.id {
			return empty, ErrInspectionChanged
		}
		item := activity.ConversationRecord{Conversation: *latest.record.Conversation, FirstSequence: g.anchor.Boundary, FirstOccurredAt: time.UnixMilli(g.first).UTC(), TurnCount: g.turns, Latest: latest.record}
		item.Conversation.DisplayName = g.name
		if err = item.Validate(); err != nil {
			return empty, err
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return empty, err
		}
		bytes += len(encoded) + activityInspectionPrivateBytes(item.Latest)
		if bytes > 8<<20 {
			return empty, ErrInspectionSourceLimit
		}
		result.Items = append(result.Items, item)
	}
	if more && len(groups) > 0 {
		result.NextCursor, err = r.catalog.encodeCursor("activity-conversations", binding, groups[len(groups)-1].anchor)
		if err != nil {
			return empty, err
		}
	}
	s.endSQL()
	if err = r.Check(s.lease.ctx, s.token); err != nil {
		return empty, err
	}
	return result, nil
}
