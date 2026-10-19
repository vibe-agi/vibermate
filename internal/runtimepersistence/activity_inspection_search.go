package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/recording"
)

func inspectedSearchWhere(q activity.SearchQuery, prefix string) ([]string, []any) {
	where := []string{}
	args := []any{}
	for _, f := range []struct{ column, value string }{{"environment_id", q.EnvironmentID}, {"account_id", q.AccountID}, {"status", string(q.Status)}} {
		if f.value != "" {
			where = append(where, prefix+f.column+"=?")
			args = append(args, f.value)
		}
	}
	if q.Reason != "" {
		where = append(where, "instr(lower("+prefix+"reason_code),?)>0")
		args = append(args, strings.ToLower(q.Reason))
	}
	if !q.OccurredAtOrAfter.IsZero() {
		where = append(where, prefix+"occurred_at_unix_ms>=? AND "+prefix+"occurred_at_unix_ms<?")
		args = append(args, q.OccurredAtOrAfter.UnixMilli(), q.OccurredBefore.UnixMilli())
	}
	return where, args
}
func searchManifestSQL(prefix string) string {
	value := prefix + "manifest_json"
	return `CASE WHEN length(` + value + `) BETWEEN 1 AND 33554432 AND json_valid(CAST(` + value + ` AS TEXT)) THEN CAST(` + value + ` AS TEXT) ELSE json('invalid') END`
}
func searchModelSQL(manifest, path string) string {
	value := `json_extract(` + manifest + `,'$.` + path + `')`
	return `CASE WHEN json_type(` + manifest + `,'$.` + path + `')='text' AND length(CAST(` + value + ` AS BLOB)) BETWEEN 1 AND 512 THEN ` + value + ` ELSE json('invalid') END`
}
func searchTerminalSQL(manifest string) string {
	return `CASE WHEN json_type(` + manifest + `,'$.response')='object' THEN 1 WHEN json_type(` + manifest + `,'$.response') IS NULL THEN 0 ELSE json('invalid') END`
}
func searchToolsSQL(manifest string) string {
	value := `json_extract(` + manifest + `,'$.request.tools')`
	return `CASE WHEN json_type(` + manifest + `,'$.request.tools')='null' THEN '[]' WHEN json_type(` + manifest + `,'$.request.tools')='array' AND json_array_length(` + value + `)<=256 AND length(CAST(` + value + ` AS BLOB))<=1048576 AND NOT EXISTS(SELECT 1 FROM json_each(` + value + `) tool WHERE tool.type<>'object' OR json_type(tool.value,'$.name') IS NOT 'text' OR length(CAST(json_extract(tool.value,'$.name') AS BLOB)) NOT BETWEEN 1 AND 256) THEN ` + value + ` ELSE json('invalid') END`
}
func searchMetadataModelCondition() string {
	return `(instr(lower(contents.requested_model),?)>0 OR instr(lower(contents.request_effective_model),?)>0 OR instr(lower(contents.response_effective_model),?)>0 OR instr(lower(contents.reported_model),?)>0)`
}
func searchMetadataToolCondition() string {
	return `EXISTS(SELECT 1 FROM json_each(contents.tools_json) tool WHERE instr(lower(CAST(json_extract(tool.value,'$.name') AS TEXT)),?)>0)`
}

func (r *ActivityInspectionReader) searchRelation(s *activityInspectionRead, q activity.SearchQuery, now time.Time) (string, []any, error) {
	cte, args := activitySearchCandidateIDs(s, q, now)
	columns := `a.subject_id,a.kind,a.status,a.reason_code,a.occurred_at_unix_ms,a.environment_id,a.environment_revision,a.environment_digest,a.client_endpoint_id,a.client_endpoint_revision,a.protocol_plan_id,a.protocol_plan_revision,a.route_id,a.route_revision,a.account_id,a.source_display_name,a.capture_run_id,a.manual_capture_id,a.conversation_projection_id,a.conversation_display_name,{{workspace_id}} AS workspace_id,{{workspace_label}} AS workspace_label,coalesce(nullif({{capture_label}},''),a.source_display_name) AS capture_label,{{epoch}} AS root_epoch,{{generation}} AS root_generation,{{scope_kind}} AS root_kind,{{scope_id}} AS root_id`
	var err error
	cte, args, err = s.canonicalFromIDs(activity.PageRequest{}, columns, cte, args, false)
	if err != nil {
		return "", nil, err
	}
	live := []string{}
	for ordinal := range s.lease.sources {
		live = append(live, fmt.Sprintf(`SELECT exchange_id FROM s%d.runtime_exchange_contents WHERE exchange_id IN(SELECT subject_id FROM ids) AND expires_at_unix_ms>?`, ordinal))
		args = append(args, now.UnixMilli())
	}
	cte += `, live_content_ids AS (` + strings.Join(live, ` UNION `) + `)`
	if err = r.compareSearchContents(s, cte, args); err != nil {
		return "", nil, err
	}
	roots := []string{}
	for ordinal := range s.lease.sources {
		manifest := searchManifestSQL("c.")
		roots = append(roots, fmt.Sprintf(`SELECT %d AS ordinal,c.exchange_id,%s AS terminal FROM s%d.runtime_exchange_contents c WHERE c.exchange_id IN(SELECT exchange_id FROM live_content_ids)`, ordinal, searchTerminalSQL(manifest), ordinal))
	}
	cte += `, content_roots AS (` + strings.Join(roots, ` UNION ALL `) + `), content_ranked AS(SELECT *,row_number() OVER(PARTITION BY exchange_id ORDER BY terminal DESC,ordinal DESC) AS rank FROM content_roots)`
	metadata := []string{}
	for ordinal, source := range s.lease.sources {
		manifest := searchManifestSQL("c.")
		epoch, generation := "1", `CASE WHEN c.scope_kind='' THEN 0 ELSE 1 END`
		if source.Current {
			epoch, generation = "c.recording_archive_epoch", "c.recording_capture_generation"
		}
		visible := fmt.Sprintf(`(%s=%d AND ((c.scope_kind='' AND c.scope_id='' AND %s=0) OR EXISTS(SELECT 1 FROM s%d.inspection_mirror_fences f WHERE f.mirror_generation=%d AND f.capture_kind=c.scope_kind AND f.capture_id=c.scope_id AND f.capture_generation=(%s) AND f.deleted=0)))`, epoch, s.token.Control.ArchiveEpoch, generation, len(s.lease.sources)-1, s.mirror.Generation, generation)
		binding := []string{`json_extract(` + manifest + `,'$.exchangeId') IS c.exchange_id`, `coalesce(json_extract(` + manifest + `,'$.parent.captureRunId'),'') IS candidate.capture_run_id`, `coalesce(json_extract(` + manifest + `,'$.parent.manualCaptureId'),'') IS candidate.manual_capture_id`, epoch + ` IS candidate.root_epoch`, generation + ` IS candidate.root_generation`, `c.scope_kind IS candidate.root_kind`, `c.scope_id IS candidate.root_id`}
		for _, field := range []struct{ json, column string }{{"environmentId", "environment_id"}, {"environmentRevision", "environment_revision"}, {"environmentDigest", "environment_digest"}, {"clientEndpointId", "client_endpoint_id"}, {"clientEndpointRevision", "client_endpoint_revision"}, {"protocolPlanId", "protocol_plan_id"}, {"protocolPlanRevision", "protocol_plan_revision"}, {"routeId", "route_id"}, {"routeRevision", "route_revision"}} {
			binding = append(binding, `json_extract(`+manifest+`,'$.frozen.`+field.json+`') IS candidate.`+field.column)
		}
		metadata = append(metadata, fmt.Sprintf(`SELECT c.exchange_id,%d AS content_ordinal,c.expires_at_unix_ms,%s AS requested_model,%s AS request_effective_model,CASE WHEN w.terminal=1 THEN %s ELSE '' END AS response_effective_model,CASE WHEN w.terminal=1 THEN %s ELSE '' END AS reported_model,%s AS tools_json,CASE WHEN %s THEN 0 ELSE 1 END AS invalid_binding
 FROM s%d.runtime_exchange_contents c JOIN content_ranked w ON w.ordinal=%d AND w.exchange_id=c.exchange_id AND w.rank=1 JOIN canonical candidate ON candidate.subject_id=c.exchange_id WHERE c.expires_at_unix_ms>? AND %s`, ordinal, searchModelSQL(manifest, "request.requestedModel"), searchModelSQL(manifest, "request.effectiveModel"), searchModelSQL(manifest, "response.effectiveModel"), searchModelSQL(manifest, "response.reportedModel"), searchToolsSQL(manifest), strings.Join(binding, " AND "), ordinal, ordinal, visible))
		args = append(args, now.UnixMilli())
	}
	cte += `, metadata AS (` + strings.Join(metadata, ` UNION ALL `) + `)`
	var invalid bool
	if err = s.tx.QueryRowContext(s.lease.ctx, cte+` SELECT EXISTS(SELECT 1 FROM metadata WHERE invalid_binding=1)`, args...).Scan(&invalid); err != nil {
		return "", nil, err
	}
	if invalid {
		return "", nil, exchangecontent.ErrInvalidEvidence
	}
	return cte, args, nil
}
func (r *ActivityInspectionReader) compareSearchContents(s *activityInspectionRead, cte string, args []any) error {
	overlaps := []string{}
	for i := range s.lease.sources {
		for j := i + 1; j < len(s.lease.sources); j++ {
			overlaps = append(overlaps, fmt.Sprintf(`SELECT a.exchange_id FROM s%d.runtime_exchange_contents a JOIN s%d.runtime_exchange_contents b ON b.exchange_id=a.exchange_id WHERE a.exchange_id IN(SELECT exchange_id FROM live_content_ids)`, i, j))
		}
	}
	if len(overlaps) == 0 {
		return nil
	}
	prefix := cte + `, overlaps AS (` + strings.Join(overlaps, ` UNION `) + `)`
	after := ""
	for {
		values := append(append([]any{}, args...), after, after)
		rows, err := s.tx.QueryContext(s.lease.ctx, prefix+` SELECT CASE WHEN length(CAST(exchange_id AS BLOB)) BETWEEN 1 AND 512 THEN exchange_id END FROM overlaps WHERE (?='' OR exchange_id>?) ORDER BY exchange_id LIMIT 256`, values...)
		if err != nil {
			return err
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
			return err
		}
		for _, id := range ids {
			view := &contentInspectionRead{lease: s.lease, tx: s.tx, token: s.token, mirror: s.mirror}
			if _, err = r.content.selectContent(view, id, nil); err != nil && !errors.Is(err, exchangecontent.ErrNotFound) {
				return err
			}
		}
		if len(ids) < 256 {
			return nil
		}
		after = ids[len(ids)-1]
	}
}
func (r *ActivityInspectionReader) Search(ctx context.Context, q activity.SearchQuery) (resultValue activity.SearchPage, resultErr error) {
	if r != nil {
		defer func(caller context.Context) { resultErr = r.catalog.classifyReadCancellation(caller, resultErr) }(ctx)
	}
	empty := activity.SearchPage{Items: []activity.SearchHit{}}
	if q.Validate() != nil {
		return empty, activity.ErrInvalidEvent
	}
	if q.BeforeSequence != 0 {
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
		Query activity.SearchQuery
		Stamp recording.InspectionStamp
	}{request, s.token.Control})
	binding := string(encoded)
	var before *inspectionPosition
	if q.Cursor != "" {
		p, err := r.catalog.decodeCursor("activity-search", binding, q.Cursor)
		if err != nil {
			return empty, err
		}
		before = &p
	}
	now := r.content.now()
	cte, args, err := r.searchRelation(s, q, now)
	if err != nil {
		return empty, err
	}
	where, values := inspectedSearchWhere(q, "candidate.")
	args = append(args, values...)
	if q.Model != "" {
		where = append(where, searchMetadataModelCondition())
		args = appendRepeated(args, strings.ToLower(q.Model), 4)
	}
	if q.Tool != "" {
		where = append(where, searchMetadataToolCondition())
		args = append(args, strings.ToLower(q.Tool))
	}
	if q.Text != "" {
		fields := []string{"subject_id", "environment_id", "account_id", "status", "reason_code", "source_display_name", "capture_run_id", "manual_capture_id", "conversation_projection_id", "conversation_display_name", "workspace_id", "workspace_label", "capture_label"}
		matches := []string{}
		for _, field := range fields {
			matches = append(matches, `instr(lower(candidate.`+field+`),?)>0`)
		}
		matches = append(matches, searchMetadataModelCondition(), searchMetadataToolCondition())
		where = append(where, "("+strings.Join(matches, " OR ")+")")
		args = appendRepeated(args, strings.ToLower(q.Text), len(fields)+5)
	}
	if before != nil {
		where = append(where, `(candidate.ordinal<? OR(candidate.ordinal=? AND candidate.sequence<?))`)
		args = append(args, before.Ordinal, before.Ordinal, before.Boundary)
	}
	statement := cte + ` SELECT candidate.ordinal,candidate.sequence,CASE WHEN length(CAST(candidate.subject_id AS BLOB)) BETWEEN 1 AND 512 THEN candidate.subject_id END,contents.content_ordinal,contents.expires_at_unix_ms FROM canonical candidate LEFT JOIN metadata contents ON contents.exchange_id=candidate.subject_id WHERE ` + strings.Join(where, " AND ") + ` ORDER BY candidate.ordinal DESC,candidate.sequence DESC LIMIT ?`
	args = append(args, q.Limit+1)
	rows, err := s.tx.QueryContext(s.lease.ctx, statement, args...)
	if err != nil {
		return empty, err
	}
	type found struct {
		position        inspectionPosition
		id              string
		content, expiry sql.NullInt64
	}
	hits := []found{}
	for rows.Next() {
		var f found
		if err = rows.Scan(&f.position.Ordinal, &f.position.Boundary, &f.id, &f.content, &f.expiry); err != nil {
			break
		}
		hits = append(hits, f)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return empty, err
	}
	more := len(hits) > q.Limit
	if more {
		hits = hits[:q.Limit]
	}
	result := activity.SearchPage{Items: []activity.SearchHit{}}
	bytes := 0
	for _, f := range hits {
		value, err := s.exchange(f.id)
		if err != nil {
			return empty, err
		}
		if value.position != f.position {
			return empty, ErrInspectionChanged
		}
		context := activity.SearchContext{WorkspaceID: value.record.WorkspaceID, WorkspaceLabel: value.record.WorkspaceLabel, CaptureLabel: value.record.CaptureLabel, ToolNames: []string{}}
		if context.CaptureLabel == "" {
			context.CaptureLabel = value.record.SourceDisplayName
		}
		if f.content.Valid {
			view := &contentInspectionRead{lease: s.lease, tx: s.tx, token: s.token, mirror: s.mirror}
			root, err := r.content.selectContent(view, f.id, nil)
			if errors.Is(err, exchangecontent.ErrNotFound) {
				return empty, ErrInspectionChanged
			}
			if err != nil {
				return empty, err
			}
			if int64(root.ordinal) != f.content.Int64 || root.reference.manifest.ExpiresAt.UnixMilli() != f.expiry.Int64 {
				return empty, ErrInspectionChanged
			}
			manifest := root.reference.manifest
			context.ContentAvailable = true
			context.RequestedModel = manifest.Request.RequestedModel
			context.EffectiveModel = manifest.Request.EffectiveModel
			if manifest.Response != nil {
				context.EffectiveModel = manifest.Response.EffectiveModel
				context.ReportedModel = manifest.Response.ReportedModel
			}
			seen := map[string]bool{}
			for _, tool := range manifest.Request.Tools {
				if !seen[tool.Name] {
					seen[tool.Name] = true
					context.ToolNames = append(context.ToolNames, tool.Name)
				}
			}
			if result.InspectionExpiresAt.IsZero() || manifest.ExpiresAt.Before(result.InspectionExpiresAt) {
				result.InspectionExpiresAt = manifest.ExpiresAt
			}
		}
		hit := activity.SearchHit{Record: value.record, Context: context, Matches: searchMatches(q, value.record, context)}
		if err = hit.Validate(); err != nil {
			return empty, err
		}
		data, err := json.Marshal(hit)
		if err != nil {
			return empty, err
		}
		bytes += len(data) + activityInspectionPrivateBytes(hit.Record)
		if bytes > 8<<20 {
			return empty, ErrInspectionSourceLimit
		}
		result.Items = append(result.Items, hit)
	}
	if more && len(hits) > 0 {
		result.NextCursor, err = r.catalog.encodeCursor("activity-search", binding, hits[len(hits)-1].position)
		if err != nil {
			return empty, err
		}
	}
	s.endSQL()
	if err = r.Check(s.lease.ctx, s.token); err != nil {
		return empty, err
	}
	if !result.InspectionExpiresAt.IsZero() && !result.InspectionExpiresAt.After(r.content.now()) {
		return empty, ErrInspectionChanged
	}
	return result, nil
}

func activitySearchCandidateIDs(s *activityInspectionRead, q activity.SearchQuery, now time.Time) (string, []any) {
	base := []string{}
	args := []any{}
	for ordinal := range s.lease.sources {
		where, values := inspectedSearchWhere(q, "a.")
		where = append(where, `a.kind IN('exchange.started','exchange.completed')`, `(a.capture_run_id<>'' OR a.manual_capture_id<>'')`)
		base = append(base, fmt.Sprintf(`SELECT a.subject_id FROM s%d.runtime_activities a WHERE `, ordinal)+strings.Join(where, " AND "))
		args = append(args, values...)
	}
	cte := `WITH base_ids AS (` + strings.Join(base, ` UNION `) + `)`
	metadata := func(model, tool, text string) (string, []any) {
		copies := []string{}
		values := []any{}
		for ordinal := range s.lease.sources {
			manifest := searchManifestSQL("c.")
			modelClause := `(instr(lower(` + searchModelSQL(manifest, "request.requestedModel") + `),?)>0 OR instr(lower(` + searchModelSQL(manifest, "request.effectiveModel") + `),?)>0 OR instr(lower(CASE WHEN ` + searchTerminalSQL(manifest) + `=1 THEN ` + searchModelSQL(manifest, "response.effectiveModel") + ` ELSE '' END),?)>0 OR instr(lower(CASE WHEN ` + searchTerminalSQL(manifest) + `=1 THEN ` + searchModelSQL(manifest, "response.reportedModel") + ` ELSE '' END),?)>0)`
			toolClause := `EXISTS(SELECT 1 FROM json_each(` + searchToolsSQL(manifest) + `) tool WHERE instr(lower(CAST(json_extract(tool.value,'$.name') AS TEXT)),?)>0)`
			where := []string{`c.exchange_id IN(SELECT subject_id FROM base_ids)`, `c.expires_at_unix_ms>?`}
			values = append(values, now.UnixMilli())
			if model != "" {
				where = append(where, modelClause)
				values = appendRepeated(values, strings.ToLower(model), 4)
			}
			if tool != "" {
				where = append(where, toolClause)
				values = append(values, strings.ToLower(tool))
			}
			if text != "" {
				where = append(where, `(`+modelClause+` OR `+toolClause+`)`)
				values = appendRepeated(values, strings.ToLower(text), 5)
			}
			copies = append(copies, fmt.Sprintf(`SELECT c.exchange_id FROM s%d.runtime_exchange_contents c WHERE `, ordinal)+strings.Join(where, " AND "))
		}
		return strings.Join(copies, ` UNION `), values
	}
	if q.Model != "" || q.Tool != "" {
		relation, values := metadata(q.Model, q.Tool, "")
		cte += `, metadata_filter_ids AS (` + relation + `)`
		args = append(args, values...)
	}
	if q.Text != "" {
		relation, values := metadata("", "", q.Text)
		cte += `, metadata_text_ids AS (` + relation + `)`
		args = append(args, values...)
	}
	ids := []string{}
	for ordinal, source := range s.lease.sources {
		where := []string{`a.subject_id IN(SELECT subject_id FROM base_ids)`, `a.kind IN('exchange.started','exchange.completed')`}
		if q.Model != "" || q.Tool != "" {
			where = append(where, `a.subject_id IN(SELECT exchange_id FROM metadata_filter_ids)`)
		}
		if q.Text != "" {
			fields := []string{"a.subject_id", "a.environment_id", "a.account_id", "a.status", "a.reason_code", "a.source_display_name", "a.capture_run_id", "a.manual_capture_id", "a.conversation_projection_id", "a.conversation_display_name", activityDisplaySQL(ordinal, source, "workspace_id"), activityDisplaySQL(ordinal, source, "workspace_label"), activityDisplaySQL(ordinal, source, "capture_label")}
			terms := []string{}
			for _, field := range fields {
				terms = append(terms, `instr(lower(`+field+`),?)>0`)
			}
			terms = append(terms, `a.subject_id IN(SELECT exchange_id FROM metadata_text_ids)`)
			where = append(where, "("+strings.Join(terms, " OR ")+")")
			args = appendRepeated(args, strings.ToLower(q.Text), len(fields))
		}
		ids = append(ids, fmt.Sprintf(`SELECT a.subject_id FROM s%d.runtime_activities a WHERE `, ordinal)+strings.Join(where, " AND "))
	}
	return cte + `, ids AS (` + strings.Join(ids, ` UNION `) + `)`, args
}
