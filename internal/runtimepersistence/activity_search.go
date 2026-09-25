package runtimepersistence

import (
	"context"
	"fmt"
	"strings"

	"github.com/vibe-agi/vibermate/internal/activity"
)

const searchActivityColumns = `
	 candidate.sequence, candidate.activity_id, candidate.occurred_at_unix_ms,
	 candidate.kind, candidate.environment_id, candidate.environment_revision,
	 candidate.environment_digest, candidate.client_endpoint_id,
	 candidate.client_endpoint_revision, candidate.protocol_plan_id,
	 candidate.protocol_plan_revision, candidate.route_id, candidate.route_revision,
	 candidate.account_id, candidate.account_revision, candidate.credential_epoch,
	 candidate.subject_id, candidate.status, candidate.reason_code,
	 candidate.source_kind, candidate.source_display_name,
	 candidate.source_recognition, candidate.capture_run_id,
	 candidate.manual_capture_id, candidate.connection_id,
	 candidate.conversation_projection_id, candidate.conversation_display_name,
	 candidate.conversation_kind, candidate.conversation_evidence,
	 candidate.conversation_actor, candidate.provider_status,
	 candidate.provider_field, candidate.client_field, candidate.client_path,
	 candidate.transport_evidence_json`

func (repository *activityRepository) SearchExchanges(
	ctx context.Context,
	request activity.SearchRequest,
) (activity.SearchPage, error) {
	if err := request.Validate(); err != nil {
		return activity.SearchPage{}, err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return activity.SearchPage{}, err
	}
	defer finish()
	query := `SELECT ` + searchActivityColumns + `,
		COALESCE(runs.workspace_id, ''),
		COALESCE(runs.workspace_label, ''),
		COALESCE(manuals.display_name, runs.executable_label,
		         candidate.source_display_name),
		contents.manifest_json
	 FROM runtime_activities AS candidate
	 LEFT JOIN capture_runs AS runs
	   ON runs.run_id = candidate.capture_run_id
	 LEFT JOIN manual_captures AS manuals
	   ON manuals.capture_id = candidate.manual_capture_id
	 LEFT JOIN runtime_exchange_contents AS contents
	   ON contents.exchange_id = candidate.subject_id
	  AND contents.expires_at_unix_ms > ?
	 WHERE candidate.kind IN ('exchange.started', 'exchange.completed')
	   AND (candidate.capture_run_id <> '' OR candidate.manual_capture_id <> '')`
	arguments := []any{request.ContentAvailableAt.UnixMilli()}
	filters := request.Query
	if filters.BeforeSequence != 0 {
		query += " AND candidate.sequence < ?"
		arguments = append(arguments, filters.BeforeSequence)
	}
	if filters.EnvironmentID != "" {
		query += " AND candidate.environment_id = ?"
		arguments = append(arguments, filters.EnvironmentID)
	}
	if filters.AccountID != "" {
		query += " AND candidate.account_id = ?"
		arguments = append(arguments, filters.AccountID)
	}
	if filters.Status != "" {
		query += " AND candidate.status = ?"
		arguments = append(arguments, string(filters.Status))
	}
	if filters.Reason != "" {
		query += " AND instr(lower(candidate.reason_code), ?) > 0"
		arguments = append(arguments, strings.ToLower(filters.Reason))
	}
	if !filters.OccurredAtOrAfter.IsZero() {
		query += " AND candidate.occurred_at_unix_ms >= ? AND candidate.occurred_at_unix_ms < ?"
		arguments = append(
			arguments,
			filters.OccurredAtOrAfter.UnixMilli(),
			filters.OccurredBefore.UnixMilli(),
		)
	}
	if filters.Model != "" {
		query += " AND " + searchModelCondition()
		arguments = appendRepeated(arguments, strings.ToLower(filters.Model), 4)
	}
	if filters.Tool != "" {
		query += " AND " + searchToolCondition()
		arguments = append(arguments, strings.ToLower(filters.Tool))
	}
	query += ` AND NOT EXISTS (
		SELECT 1 FROM runtime_activities AS winner
		WHERE winner.kind IN ('exchange.started', 'exchange.completed')
		  AND winner.subject_id = candidate.subject_id
		  AND ((winner.kind = 'exchange.completed' AND candidate.kind = 'exchange.started')
		       OR (winner.kind = candidate.kind AND winner.sequence > candidate.sequence))
	)`
	if filters.Text != "" {
		conditions := []string{
			"candidate.subject_id", "candidate.environment_id", "candidate.account_id",
			"candidate.status", "candidate.reason_code", "candidate.source_display_name",
			"candidate.capture_run_id", "candidate.manual_capture_id",
			"candidate.conversation_projection_id", "candidate.conversation_display_name",
			"COALESCE(runs.workspace_id, '')", "COALESCE(runs.workspace_label, '')",
			"COALESCE(manuals.display_name, runs.executable_label, '')",
		}
		parts := make([]string, 0, len(conditions)+2)
		for _, column := range conditions {
			parts = append(parts, "instr(lower("+column+"), ?) > 0")
		}
		parts = append(parts, searchModelCondition(), searchToolCondition())
		query += " AND (" + strings.Join(parts, " OR ") + ")"
		needle := strings.ToLower(filters.Text)
		arguments = appendRepeated(arguments, needle, len(conditions)+4+1)
	}
	query += " ORDER BY candidate.sequence DESC LIMIT ?"
	arguments = append(arguments, filters.Limit+1)
	rows, err := repository.database.QueryContext(operation, query, arguments...)
	if err != nil {
		return activity.SearchPage{}, fmt.Errorf("search Activities: %w", err)
	}
	defer rows.Close()
	page := activity.SearchPage{Items: make([]activity.SearchHit, 0, filters.Limit)}
	for rows.Next() {
		hit, scanErr := scanActivitySearchHit(rows, filters)
		if scanErr != nil {
			return activity.SearchPage{}, scanErr
		}
		page.Items = append(page.Items, hit)
	}
	if err := rows.Err(); err != nil {
		return activity.SearchPage{}, fmt.Errorf("iterate Activity search: %w", err)
	}
	if len(page.Items) > filters.Limit {
		page.Items = page.Items[:filters.Limit]
		page.NextBeforeSequence = page.Items[len(page.Items)-1].Record.Sequence
	}
	return page, nil
}

func searchModelCondition() string {
	return `(instr(lower(CAST(json_extract(CAST(contents.manifest_json AS TEXT),
		'$.request.requestedModel') AS TEXT)), ?) > 0
	 OR instr(lower(CAST(json_extract(CAST(contents.manifest_json AS TEXT),
		'$.request.effectiveModel') AS TEXT)), ?) > 0
	 OR instr(lower(CAST(json_extract(CAST(contents.manifest_json AS TEXT),
		'$.response.effectiveModel') AS TEXT)), ?) > 0
	 OR instr(lower(CAST(json_extract(CAST(contents.manifest_json AS TEXT),
		'$.response.reportedModel') AS TEXT)), ?) > 0)`
}

func searchToolCondition() string {
	return `EXISTS (
		SELECT 1
		FROM json_each(CAST(contents.manifest_json AS TEXT), '$.request.tools') AS tool
		WHERE instr(lower(CAST(json_extract(tool.value, '$.name') AS TEXT)), ?) > 0
	)`
}

func appendRepeated(values []any, value string, count int) []any {
	for range count {
		values = append(values, value)
	}
	return values
}

func scanActivitySearchHit(
	scanner activityScanner,
	query activity.SearchQuery,
) (activity.SearchHit, error) {
	var record activity.Record
	state := activityScanState{record: &record}
	var context activity.SearchContext
	var manifestJSON []byte
	targets := append(
		state.targets(),
		&context.WorkspaceID,
		&context.WorkspaceLabel,
		&context.CaptureLabel,
		&manifestJSON,
	)
	if err := scanner.Scan(targets...); err != nil {
		return activity.SearchHit{}, fmt.Errorf("scan Activity search: %w", err)
	}
	stored, err := state.finish()
	if err != nil {
		return activity.SearchHit{}, err
	}
	if len(manifestJSON) != 0 {
		manifest, decodeErr := decodeStoredContentManifest(manifestJSON)
		if decodeErr != nil {
			return activity.SearchHit{}, fmt.Errorf("decode Activity search manifest: %w", decodeErr)
		}
		if manifest.ExchangeID != stored.SubjectID {
			return activity.SearchHit{}, fmt.Errorf(
				"%w: Activity search manifest identity changed",
				activity.ErrInvalidEvent,
			)
		}
		context.ContentAvailable = true
		context.RequestedModel = manifest.Request.RequestedModel
		context.EffectiveModel = manifest.Request.EffectiveModel
		if manifest.Response != nil {
			context.EffectiveModel = manifest.Response.EffectiveModel
			context.ReportedModel = manifest.Response.ReportedModel
		}
		seen := make(map[string]struct{}, len(manifest.Request.Tools))
		for _, tool := range manifest.Request.Tools {
			if _, duplicate := seen[tool.Name]; duplicate {
				continue
			}
			seen[tool.Name] = struct{}{}
			context.ToolNames = append(context.ToolNames, tool.Name)
		}
	}
	if context.ToolNames == nil {
		context.ToolNames = []string{}
	}
	hit := activity.SearchHit{
		Record: stored, Context: context,
		Matches: searchMatches(query, stored, context),
	}
	if err := hit.Validate(); err != nil {
		return activity.SearchHit{}, fmt.Errorf("validate Activity search hit: %w", err)
	}
	return hit, nil
}

func searchMatches(
	query activity.SearchQuery,
	record activity.Record,
	context activity.SearchContext,
) []string {
	matches := make([]string, 0, 8)
	seen := map[string]struct{}{}
	add := func(kind string) {
		if _, exists := seen[kind]; exists {
			return
		}
		seen[kind] = struct{}{}
		matches = append(matches, kind)
	}
	if query.EnvironmentID != "" {
		add("environment")
	}
	if query.AccountID != "" {
		add("account")
	}
	if query.Model != "" {
		add("model")
	}
	if query.Tool != "" {
		add("tool")
	}
	if query.Status != "" {
		add("status")
	}
	if query.Reason != "" {
		add("error")
	}
	if !query.OccurredAtOrAfter.IsZero() {
		add("time")
	}
	needle := strings.ToLower(query.Text)
	if needle == "" {
		return matches
	}
	contains := func(value string) bool {
		return strings.Contains(strings.ToLower(value), needle)
	}
	if contains(context.WorkspaceID) || contains(context.WorkspaceLabel) {
		add("workspace")
	}
	if contains(context.CaptureLabel) || contains(record.CaptureRunID) ||
		contains(record.ManualCaptureID) {
		add("capture")
	}
	if record.Conversation != nil &&
		(contains(record.Conversation.ProjectionID) ||
			contains(record.Conversation.DisplayName)) {
		add("conversation")
	}
	if contains(record.EnvironmentID) {
		add("environment")
	}
	if contains(record.AccountID) {
		add("account")
	}
	if contains(context.RequestedModel) || contains(context.EffectiveModel) ||
		contains(context.ReportedModel) {
		add("model")
	}
	for _, tool := range context.ToolNames {
		if contains(tool) {
			add("tool")
			break
		}
	}
	if contains(string(record.Status)) {
		add("status")
	}
	if contains(record.ReasonCode) {
		add("error")
	}
	if contains(record.SourceDisplayName) {
		add("source")
	}
	if contains(record.SubjectID) {
		add("exchange")
	}
	return matches
}
