package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
)

type connectionEventRepository struct {
	database   *sql.DB
	operations *operationGate
}

var _ connectionevent.Repository = (*connectionEventRepository)(nil)

func newConnectionEventRepository(
	database *sql.DB,
	operations *operationGate,
) *connectionEventRepository {
	return &connectionEventRepository{
		database:   database,
		operations: operations,
	}
}

func (repository *connectionEventRepository) Append(
	ctx context.Context,
	event connectionevent.Event,
) (connectionevent.Record, error) {
	if err := event.Validate(); err != nil {
		return connectionevent.Record{}, err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return connectionevent.Record{}, err
	}
	defer finish()
	var endedAt any
	if !event.EndedAt.IsZero() {
		endedAt = toUnixMillis(event.EndedAt)
	}
	result, err := repository.database.ExecContext(
		operation,
		`INSERT INTO runtime_connection_events (
		     connection_id,
		     ingress_id,
		     source_label,
		     source_confidence,
		     access_id,
		     access_name,
		     access_revision,
		     agent_endpoint_id,
		     agent_endpoint_revision,
		     requested_host,
		     observed_sni,
		     route_host,
		     ip,
		     port,
		     decision,
		     rule_id,
		     credential_binding_id,
		     egress_scope,
		     egress_source,
		     egress_rule_id,
		     egress_selector_run_id,
		     egress_proxy_id,
		     egress_policy_revision,
		     decryption,
		     phase,
		     bytes_up,
		     bytes_down,
		     started_at_unix_ms,
		     ended_at_unix_ms,
		     outcome,
		     error_class
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ConnectionID,
		event.IngressID,
		event.SourceLabel,
		string(event.SourceConfidence),
		event.AccessID,
		event.AccessName,
		event.AccessRevision,
		event.AgentEndpointID,
		event.AgentEndpointRevision,
		event.RequestedHost,
		event.ObservedSNI,
		event.RouteHost,
		event.IP,
		event.Port,
		string(event.Decision),
		event.RuleID,
		event.CredentialBindingID,
		string(event.EgressScope),
		string(event.EgressSource),
		event.EgressRuleID,
		event.EgressSelectorRunID,
		event.EgressProxyID,
		event.EgressPolicyRevision,
		string(event.Decryption),
		string(event.Phase),
		event.BytesUp,
		event.BytesDown,
		toUnixMillis(event.StartedAt),
		endedAt,
		string(event.Outcome),
		event.ErrorClass,
	)
	if err != nil {
		return connectionevent.Record{}, fmt.Errorf(
			"append ConnectionEvent: %w",
			err,
		)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return connectionevent.Record{}, fmt.Errorf(
			"read ConnectionEvent sequence: %w",
			err,
		)
	}
	record := connectionevent.Record{Sequence: sequence, Event: event}
	return record, record.Validate()
}

func (repository *connectionEventRepository) List(
	ctx context.Context,
	request connectionevent.PageRequest,
) (connectionevent.Page, error) {
	if err := request.Validate(); err != nil {
		return connectionevent.Page{}, err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return connectionevent.Page{}, err
	}
	defer finish()
	query := connectionEventSelect + `
		 WHERE (? = '' OR ingress_id = ?)
		   AND (? = 0 OR sequence < ?)
		 ORDER BY sequence DESC
		 LIMIT ?`
	if request.LatestPerConnection {
		query = connectionEventSelect + ` AS current
		 WHERE (? = '' OR current.ingress_id = ?)
		   AND current.sequence = (
		       SELECT MAX(latest.sequence)
		       FROM runtime_connection_events AS latest
		       WHERE latest.connection_id = current.connection_id
		   )
		   AND (? = 0 OR current.sequence < ?)
		 ORDER BY current.sequence DESC
		 LIMIT ?`
	}
	rows, err := repository.database.QueryContext(
		operation,
		query,
		request.IngressID,
		request.IngressID,
		request.BeforeSequence,
		request.BeforeSequence,
		request.Limit,
	)
	if err != nil {
		return connectionevent.Page{}, fmt.Errorf(
			"list ConnectionEvents: %w",
			err,
		)
	}
	defer rows.Close()
	page := connectionevent.Page{Items: make([]connectionevent.Record, 0)}
	for rows.Next() {
		record, err := scanConnectionEvent(rows)
		if err != nil {
			return connectionevent.Page{}, err
		}
		page.Items = append(page.Items, record)
	}
	if err := rows.Err(); err != nil {
		return connectionevent.Page{}, fmt.Errorf(
			"iterate ConnectionEvents: %w",
			err,
		)
	}
	if len(page.Items) == request.Limit {
		page.NextCursor, err = connectionevent.Cursor(
			page.Items[len(page.Items)-1].Sequence,
		)
		if err != nil {
			return connectionevent.Page{}, err
		}
	}
	return page, nil
}

func (repository *connectionEventRepository) Timeline(
	ctx context.Context,
	connectionID string,
) (connectionevent.Timeline, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return connectionevent.Timeline{}, err
	}
	defer finish()
	rows, err := repository.database.QueryContext(
		operation,
		connectionEventSelect+`
		 WHERE connection_id = ?
		 ORDER BY sequence ASC
		 LIMIT ?`,
		connectionID,
		connectionevent.MaxTimelineSize+1,
	)
	if err != nil {
		return connectionevent.Timeline{}, fmt.Errorf(
			"read ConnectionEvent timeline: %w",
			err,
		)
	}
	defer rows.Close()
	timeline := connectionevent.Timeline{
		ConnectionID: connectionID,
		Events:       make([]connectionevent.Record, 0),
	}
	for rows.Next() {
		record, err := scanConnectionEvent(rows)
		if err != nil {
			return connectionevent.Timeline{}, err
		}
		timeline.Events = append(timeline.Events, record)
	}
	if err := rows.Err(); err != nil {
		return connectionevent.Timeline{}, fmt.Errorf(
			"iterate ConnectionEvent timeline: %w",
			err,
		)
	}
	if len(timeline.Events) == 0 {
		return connectionevent.Timeline{}, connectionevent.ErrNotFound
	}
	if len(timeline.Events) > connectionevent.MaxTimelineSize {
		return connectionevent.Timeline{}, errors.New(
			"ConnectionEvent timeline exceeds its bound",
		)
	}
	return timeline, nil
}

func (repository *connectionEventRepository) Recover(
	ctx context.Context,
	endedAt time.Time,
) (int, error) {
	if endedAt.IsZero() {
		return 0, connectionevent.ErrInvalidEvent
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer finish()
	result, err := repository.database.ExecContext(
		operation,
		`INSERT INTO runtime_connection_events (
		     connection_id,
		     ingress_id,
		     source_label,
		     source_confidence,
		     access_id,
		     access_name,
		     access_revision,
		     agent_endpoint_id,
		     agent_endpoint_revision,
		     requested_host,
		     observed_sni,
		     route_host,
		     ip,
		     port,
		     decision,
		     rule_id,
		     credential_binding_id,
		     egress_scope,
		     egress_source,
		     egress_rule_id,
		     egress_selector_run_id,
		     egress_proxy_id,
		     egress_policy_revision,
		     decryption,
		     phase,
		     bytes_up,
		     bytes_down,
		     started_at_unix_ms,
		     ended_at_unix_ms,
		     outcome,
		     error_class
		 )
		 SELECT
		     current.connection_id,
		     current.ingress_id,
		     current.source_label,
		     current.source_confidence,
		     current.access_id,
		     current.access_name,
		     current.access_revision,
		     current.agent_endpoint_id,
		     current.agent_endpoint_revision,
		     current.requested_host,
		     current.observed_sni,
		     current.route_host,
		     current.ip,
		     current.port,
		     current.decision,
		     current.rule_id,
		     current.credential_binding_id,
		     current.egress_scope,
		     current.egress_source,
		     current.egress_rule_id,
		     current.egress_selector_run_id,
		     current.egress_proxy_id,
		     current.egress_policy_revision,
		     current.decryption,
		     ?,
		     current.bytes_up,
		     current.bytes_down,
		     current.started_at_unix_ms,
		     CASE
		         WHEN current.started_at_unix_ms > ? THEN current.started_at_unix_ms
		         ELSE ?
		     END,
		     ?,
		     ?
		 FROM runtime_connection_events AS current
		 INNER JOIN (
		     SELECT connection_id, MAX(sequence) AS latest_sequence
		     FROM runtime_connection_events
		     GROUP BY connection_id
		 ) AS latest
		 ON latest.latest_sequence = current.sequence
		 WHERE current.phase NOT IN (?, ?)
		   AND NOT (current.phase = ? AND current.decision = ?)`,
		string(connectionevent.PhaseFailed),
		toUnixMillis(endedAt),
		toUnixMillis(endedAt),
		string(connectionevent.OutcomeFailed),
		connectionevent.RecoveryErrorClass,
		string(connectionevent.PhaseClosed),
		string(connectionevent.PhaseFailed),
		string(connectionevent.PhaseDecided),
		string(connectionevent.DecisionDeny),
	)
	if err != nil {
		return 0, fmt.Errorf("recover ConnectionEvents: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read recovered ConnectionEvent count: %w", err)
	}
	return int(count), nil
}

const connectionEventSelect = `SELECT
    sequence,
    connection_id,
    ingress_id,
	    source_label,
	    source_confidence,
	    access_id,
	    access_name,
	    access_revision,
	    agent_endpoint_id,
	    agent_endpoint_revision,
	    requested_host,
    observed_sni,
    route_host,
    ip,
    port,
    decision,
    rule_id,
    credential_binding_id,
    egress_scope,
    egress_source,
    egress_rule_id,
    egress_selector_run_id,
    egress_proxy_id,
    egress_policy_revision,
    decryption,
    phase,
    bytes_up,
    bytes_down,
    started_at_unix_ms,
    ended_at_unix_ms,
    outcome,
    error_class
FROM runtime_connection_events`

type rowScanner interface {
	Scan(...any) error
}

func scanConnectionEvent(
	row rowScanner,
) (connectionevent.Record, error) {
	var record connectionevent.Record
	var startedAt int64
	var endedAt sql.NullInt64
	err := row.Scan(
		&record.Sequence,
		&record.ConnectionID,
		&record.IngressID,
		&record.SourceLabel,
		&record.SourceConfidence,
		&record.AccessID,
		&record.AccessName,
		&record.AccessRevision,
		&record.AgentEndpointID,
		&record.AgentEndpointRevision,
		&record.RequestedHost,
		&record.ObservedSNI,
		&record.RouteHost,
		&record.IP,
		&record.Port,
		&record.Decision,
		&record.RuleID,
		&record.CredentialBindingID,
		&record.EgressScope,
		&record.EgressSource,
		&record.EgressRuleID,
		&record.EgressSelectorRunID,
		&record.EgressProxyID,
		&record.EgressPolicyRevision,
		&record.Decryption,
		&record.Phase,
		&record.BytesUp,
		&record.BytesDown,
		&startedAt,
		&endedAt,
		&record.Outcome,
		&record.ErrorClass,
	)
	if err != nil {
		return connectionevent.Record{}, fmt.Errorf(
			"scan ConnectionEvent: %w",
			err,
		)
	}
	record.StartedAt = fromUnixMillis(startedAt)
	if endedAt.Valid {
		record.EndedAt = fromUnixMillis(endedAt.Int64)
	}
	if err := record.Validate(); err != nil {
		return connectionevent.Record{}, fmt.Errorf(
			"validate stored ConnectionEvent: %w",
			err,
		)
	}
	return record, nil
}
