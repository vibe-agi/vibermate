package runtimepersistence

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
)

type activityRepository struct {
	database   *sql.DB
	operations *operationGate
	identityMu sync.Mutex
}

var _ activity.Repository = (*activityRepository)(nil)
var _ activity.ConversationIdentityRepository = (*activityRepository)(nil)
var _ activity.ConversationProjectionWriter = (*activityRepository)(nil)

func newActivityRepository(
	database *sql.DB,
	operations *operationGate,
) *activityRepository {
	return &activityRepository{database: database, operations: operations}
}

func (repository *activityRepository) Append(
	ctx context.Context,
	record activity.Record,
) (activity.Record, error) {
	candidate := record
	candidate.Sequence = 1
	if err := candidate.Validate(); err != nil {
		return activity.Record{}, err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return activity.Record{}, err
	}
	defer finish()
	transportEvidence, err := encodeTransportEvidence(record.Transport)
	if err != nil {
		return activity.Record{}, err
	}
	// A record with nothing to diagnose stores empty columns rather than a
	// structure that says nothing.
	var diagnosis activity.Diagnosis
	if record.Diagnosis != nil {
		diagnosis = *record.Diagnosis
	}
	result, err := repository.database.ExecContext(
		operation,
		`INSERT INTO runtime_activities (
		     activity_id,
		     occurred_at_unix_ms,
		     kind,
		     environment_id,
		     environment_revision,
		     environment_digest,
		     client_endpoint_id,
		     client_endpoint_revision,
		     protocol_plan_id,
		     protocol_plan_revision,
		     route_id,
		     route_revision,
		     account_id,
		     account_revision,
		     credential_epoch,
		     subject_id,
		     status,
		     reason_code,
		     source_kind,
		     source_display_name,
		     source_recognition,
		     capture_run_id,
		     manual_capture_id,
		     connection_id,
		     conversation_projection_id,
		     conversation_display_name,
		     conversation_kind,
		     conversation_evidence,
		     conversation_actor,
		     provider_status,
		     provider_field,
		     client_field,
		     client_path,
		     transport_evidence_json
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
		toUnixMillis(record.OccurredAt),
		string(record.Kind),
		record.EnvironmentID,
		record.EnvironmentRevision,
		record.EnvironmentDigest,
		record.ClientEndpointID,
		record.ClientEndpointRevision,
		record.ProtocolPlanID,
		record.ProtocolPlanRevision,
		record.RouteID,
		record.RouteRevision,
		record.AccountID,
		record.AccountRevision,
		record.CredentialEpoch,
		record.SubjectID,
		string(record.Status),
		record.ReasonCode,
		string(record.SourceKind),
		record.SourceDisplayName,
		string(record.SourceRecognition),
		record.CaptureRunID,
		record.ManualCaptureID,
		record.ConnectionID,
		conversationProjectionID(record.Conversation),
		conversationDisplayName(record.Conversation),
		conversationKind(record.Conversation),
		conversationEvidence(record.Conversation),
		conversationActor(record.Conversation),
		diagnosis.ProviderStatus,
		diagnosis.ProviderField,
		diagnosis.ClientField,
		diagnosis.ClientPath,
		transportEvidence,
	)
	if err != nil {
		return activity.Record{}, fmt.Errorf("append Activity: %w", err)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return activity.Record{}, fmt.Errorf("read Activity sequence: %w", err)
	}
	record.Sequence = sequence
	return record, record.Validate()
}

func (repository *activityRepository) List(
	ctx context.Context,
	request activity.PageRequest,
) (activity.Page, error) {
	return repository.list(
		ctx,
		request,
		`SELECT
		     sequence,
		     activity_id,
		     occurred_at_unix_ms,
		     kind,
		     environment_id,
		     environment_revision,
		     environment_digest,
		     client_endpoint_id,
		     client_endpoint_revision,
		     protocol_plan_id,
		     protocol_plan_revision,
		     route_id,
		     route_revision,
		     account_id,
		     account_revision,
		     credential_epoch,
		     subject_id,
		     status,
		     reason_code,
		     source_kind,
		     source_display_name,
		     source_recognition,
		     capture_run_id,
		     manual_capture_id,
		     connection_id,
		     conversation_projection_id,
		     conversation_display_name,
		     conversation_kind,
		     conversation_evidence,
		     conversation_actor,
		     provider_status,
		     provider_field,
		     client_field,
		     client_path,
		     transport_evidence_json
		 FROM runtime_activities
		 WHERE (? = 0 OR sequence < ?)
		 ORDER BY sequence DESC
		 LIMIT ?`,
		false,
		request.BeforeSequence,
		request.BeforeSequence,
		request.Limit,
	)
}

func (repository *activityRepository) ListExchanges(
	ctx context.Context,
	request activity.PageRequest,
) (activity.Page, error) {
	if err := request.Validate(); err != nil {
		return activity.Page{}, err
	}
	return repository.list(
		ctx,
		request,
		`WITH ranked AS (
		   SELECT
		     sequence,
		     activity_id,
		     occurred_at_unix_ms,
		     kind,
		     environment_id,
		     environment_revision,
		     environment_digest,
		     client_endpoint_id,
		     client_endpoint_revision,
		     protocol_plan_id,
		     protocol_plan_revision,
		     route_id,
		     route_revision,
		     account_id,
		     account_revision,
		     credential_epoch,
		     subject_id,
		     status,
		     reason_code,
		     source_kind,
		     source_display_name,
		     source_recognition,
		     capture_run_id,
		     manual_capture_id,
		     connection_id,
		     conversation_projection_id,
		     conversation_display_name,
		     conversation_kind,
		     conversation_evidence,
		     conversation_actor,
		     provider_status,
		     provider_field,
		     client_field,
		     client_path,
		     transport_evidence_json,
		     ROW_NUMBER() OVER (
		       PARTITION BY subject_id
		       ORDER BY CASE kind WHEN 'exchange.completed' THEN 0 ELSE 1 END,
		                sequence DESC
		     ) AS exchange_rank
		   FROM runtime_activities
		  WHERE kind IN ('exchange.started', 'exchange.completed')
		 )
		 SELECT
		     sequence, activity_id, occurred_at_unix_ms, kind,
		     environment_id, environment_revision, environment_digest,
		     client_endpoint_id, client_endpoint_revision,
		     protocol_plan_id, protocol_plan_revision,
		     route_id, route_revision,
		     account_id, account_revision, credential_epoch,
		     subject_id, status, reason_code,
		     source_kind, source_display_name, source_recognition,
		     capture_run_id, manual_capture_id, connection_id,
		     conversation_projection_id, conversation_display_name,
		     conversation_kind, conversation_evidence, conversation_actor,
		     provider_status, provider_field, client_field, client_path,
		     transport_evidence_json
		 FROM ranked
		 WHERE exchange_rank = 1
		   AND (? = '' OR capture_run_id = ?)
		   AND (? = '' OR manual_capture_id = ?)
		   AND (? = '' OR environment_id = ?)
		   AND (? = '' OR conversation_projection_id = ?)
		   AND (? = 0 OR sequence < ?)
		 ORDER BY sequence DESC
		 LIMIT ?`,
		true,
		request.CaptureRunID,
		request.CaptureRunID,
		request.ManualCaptureID,
		request.ManualCaptureID,
		request.EnvironmentID,
		request.EnvironmentID,
		request.ConversationProjectionID,
		request.ConversationProjectionID,
		request.BeforeSequence,
		request.BeforeSequence,
		request.Limit+1,
	)
}

func (repository *activityRepository) GetExchange(
	ctx context.Context,
	exchangeID string,
) (activity.Record, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return activity.Record{}, err
	}
	defer finish()
	rows, err := repository.database.QueryContext(
		operation,
		`SELECT
		     sequence,
		     activity_id,
		     occurred_at_unix_ms,
		     kind,
		     environment_id,
		     environment_revision,
		     environment_digest,
		     client_endpoint_id,
		     client_endpoint_revision,
		     protocol_plan_id,
		     protocol_plan_revision,
		     route_id,
		     route_revision,
		     account_id,
		     account_revision,
		     credential_epoch,
		     subject_id,
		     status,
		     reason_code,
		     source_kind,
		     source_display_name,
		     source_recognition,
		     capture_run_id,
		     manual_capture_id,
		     connection_id,
		     conversation_projection_id,
		     conversation_display_name,
		     conversation_kind,
		     conversation_evidence,
		     conversation_actor,
		     provider_status,
		     provider_field,
		     client_field,
		     client_path,
		     transport_evidence_json
		 FROM runtime_activities
		 WHERE kind IN ('exchange.started', 'exchange.completed')
		   AND subject_id = ?
		 ORDER BY CASE kind WHEN 'exchange.completed' THEN 0 ELSE 1 END,
		          sequence DESC
		 LIMIT 3`,
		exchangeID,
	)
	if err != nil {
		return activity.Record{}, fmt.Errorf("read Activity Exchange: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return activity.Record{}, fmt.Errorf("read Activity Exchange: %w", err)
		}
		return activity.Record{}, activity.ErrExchangeNotFound
	}
	record, err := scanActivityRecord(rows)
	if err != nil {
		return activity.Record{}, err
	}
	seen := map[activity.Kind]struct{}{record.Kind: {}}
	for rows.Next() {
		candidate, scanErr := scanActivityRecord(rows)
		if scanErr != nil {
			return activity.Record{}, scanErr
		}
		if _, duplicate := seen[candidate.Kind]; duplicate {
			return activity.Record{}, errors.New(
				"Activity Exchange has duplicate lifecycle records",
			)
		}
		seen[candidate.Kind] = struct{}{}
		if !sameExchangeIdentity(record, candidate) {
			return activity.Record{}, errors.New(
				"Activity Exchange lifecycle evidence changed identity",
			)
		}
	}
	if err := rows.Err(); err != nil {
		return activity.Record{}, fmt.Errorf("read Activity Exchange: %w", err)
	}
	return record, nil
}

func (repository *activityRepository) PutConversationIdentity(
	ctx context.Context,
	exchangeID string,
	identity agentconversation.ClientIdentity,
) error {
	identity = identity.Clone()
	identity.ObservedAt = identity.ObservedAt.UTC().Truncate(time.Millisecond)
	if identity.Validate() != nil || exchangeID == "" {
		return activity.ErrInvalidEvent
	}
	repository.identityMu.Lock()
	defer repository.identityMu.Unlock()
	protocolIDs, err := json.Marshal(identity.ProtocolIDs)
	if err != nil {
		return fmt.Errorf("encode Exchange Agent protocol IDs: %w", err)
	}
	attributes, err := json.Marshal(identity.Attributes)
	if err != nil {
		return fmt.Errorf("encode Exchange Agent attributes: %w", err)
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	result, err := repository.database.ExecContext(
		operation,
		`INSERT INTO runtime_exchange_agent_identities (
		   exchange_id, client_kind, session_id, session_resumable,
		   actor_id, actor_label, actor_type, actor_is_subagent,
		   provider_response_id, provider_message_id,
		   protocol_ids_json, attributes_json,
		   evidence_source, confidence, observed_at_unix_ms
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(exchange_id) DO NOTHING`,
		exchangeID,
		identity.Client,
		identity.SessionID,
		identity.SessionResumable,
		identity.ActorID,
		identity.ActorLabel,
		identity.ActorType,
		identity.ActorIsSubagent,
		identity.ProviderResponseID,
		identity.ProviderMessageID,
		string(protocolIDs),
		string(attributes),
		identity.Source,
		identity.Confidence,
		toUnixMillis(identity.ObservedAt),
	)
	if err != nil {
		return fmt.Errorf("persist Exchange Agent identity: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Exchange Agent identity result: %w", err)
	}
	if affected == 1 {
		return nil
	}
	stored, err := repository.getConversationIdentity(operation, exchangeID)
	if err != nil {
		return err
	}
	merged, changed, mergeErr := agentconversation.MergeClientIdentity(stored, identity)
	if mergeErr != nil {
		return fmt.Errorf(
			"%w: Exchange Agent identity changed: %v",
			activity.ErrInvalidEvent,
			mergeErr,
		)
	}
	if !changed {
		return nil
	}
	protocolIDs, err = json.Marshal(merged.ProtocolIDs)
	if err != nil {
		return fmt.Errorf("encode merged Exchange Agent protocol IDs: %w", err)
	}
	attributes, err = json.Marshal(merged.Attributes)
	if err != nil {
		return fmt.Errorf("encode merged Exchange Agent attributes: %w", err)
	}
	result, err = repository.database.ExecContext(
		operation,
		`UPDATE runtime_exchange_agent_identities
		 SET client_kind = ?, session_id = ?, session_resumable = ?,
		     actor_id = ?, actor_label = ?, actor_type = ?, actor_is_subagent = ?,
		     provider_response_id = ?, provider_message_id = ?,
		     protocol_ids_json = ?, attributes_json = ?,
		     evidence_source = ?, confidence = ?, observed_at_unix_ms = ?
		 WHERE exchange_id = ?`,
		merged.Client,
		merged.SessionID,
		merged.SessionResumable,
		merged.ActorID,
		merged.ActorLabel,
		merged.ActorType,
		merged.ActorIsSubagent,
		merged.ProviderResponseID,
		merged.ProviderMessageID,
		string(protocolIDs),
		string(attributes),
		merged.Source,
		merged.Confidence,
		toUnixMillis(merged.ObservedAt),
		exchangeID,
	)
	if err != nil {
		return fmt.Errorf("deepen Exchange Agent identity: %w", err)
	}
	affected, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deepened Exchange Agent identity result: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf(
			"%w: deepened Exchange Agent identity affected=%d",
			activity.ErrInvalidEvent,
			affected,
		)
	}
	return nil
}

func (repository *activityRepository) GetConversationIdentity(
	ctx context.Context,
	exchangeID string,
) (agentconversation.ClientIdentity, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return agentconversation.ClientIdentity{}, err
	}
	defer finish()
	return repository.getConversationIdentity(operation, exchangeID)
}

func (repository *activityRepository) getConversationIdentity(
	ctx context.Context,
	exchangeID string,
) (agentconversation.ClientIdentity, error) {
	var identity agentconversation.ClientIdentity
	var observedAt int64
	var protocolIDs, attributes string
	err := repository.database.QueryRowContext(
		ctx,
		`SELECT client_kind, session_id, session_resumable,
		        actor_id, actor_label, actor_type, actor_is_subagent,
		        provider_response_id, provider_message_id,
		        protocol_ids_json, attributes_json,
		        evidence_source, confidence, observed_at_unix_ms
		 FROM runtime_exchange_agent_identities
		 WHERE exchange_id = ?`,
		exchangeID,
	).Scan(
		&identity.Client,
		&identity.SessionID,
		&identity.SessionResumable,
		&identity.ActorID,
		&identity.ActorLabel,
		&identity.ActorType,
		&identity.ActorIsSubagent,
		&identity.ProviderResponseID,
		&identity.ProviderMessageID,
		&protocolIDs,
		&attributes,
		&identity.Source,
		&identity.Confidence,
		&observedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return agentconversation.ClientIdentity{}, activity.ErrExchangeNotFound
	}
	if err != nil {
		return agentconversation.ClientIdentity{}, fmt.Errorf(
			"read Exchange Agent identity: %w", err,
		)
	}
	identity.ObservedAt = fromUnixMillis(observedAt)
	if err := json.Unmarshal([]byte(protocolIDs), &identity.ProtocolIDs); err != nil {
		return agentconversation.ClientIdentity{}, fmt.Errorf(
			"decode Exchange Agent protocol IDs: %w", err,
		)
	}
	if err := json.Unmarshal([]byte(attributes), &identity.Attributes); err != nil {
		return agentconversation.ClientIdentity{}, fmt.Errorf(
			"decode Exchange Agent attributes: %w", err,
		)
	}
	if identity.Validate() != nil {
		return agentconversation.ClientIdentity{}, fmt.Errorf(
			"%w: stored Exchange Agent identity is invalid", activity.ErrInvalidEvent,
		)
	}
	return identity.Clone(), nil
}

func (repository *activityRepository) ReprojectConversation(
	ctx context.Context,
	exchangeID string,
	conversation agentconversation.Ref,
) error {
	if exchangeID == "" || conversation.Validate() != nil ||
		conversation.Kind == agentconversation.KindPendingExchange {
		return activity.ErrInvalidEvent
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	result, err := repository.database.ExecContext(
		operation,
		`UPDATE runtime_activities
		 SET conversation_projection_id = ?,
		     conversation_display_name = ?,
		     conversation_kind = ?,
		     conversation_evidence = ?,
		     conversation_actor = ?
		 WHERE sequence = (
		   SELECT sequence
		   FROM runtime_activities
		   WHERE subject_id = ?
		     AND kind IN ('exchange.started', 'exchange.completed')
		   ORDER BY CASE kind
		     WHEN 'exchange.completed' THEN 0
		     ELSE 1
		   END
		   LIMIT 1
		 )`,
		conversation.ProjectionID,
		conversation.DisplayName,
		conversation.Kind,
		conversation.Evidence,
		conversation.Actor,
		exchangeID,
	)
	if err != nil {
		return fmt.Errorf("reproject Conversation: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Conversation reproject result: %w", err)
	}
	if affected != 1 {
		return activity.ErrExchangeNotFound
	}
	return nil
}

func (repository *activityRepository) ListConversations(
	ctx context.Context,
	request activity.ConversationIndexRequest,
) (activity.ConversationPage, error) {
	if err := request.Validate(); err != nil {
		return activity.ConversationPage{}, err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return activity.ConversationPage{}, err
	}
	defer finish()
	rows, err := repository.database.QueryContext(
		operation,
		`WITH indexed_all AS (
		   SELECT terminal.*
		   FROM runtime_activities AS terminal
		   WHERE terminal.kind = 'exchange.completed'
		   UNION ALL
		   SELECT started.*
		   FROM runtime_activities AS started
		   WHERE started.kind = 'exchange.started'
		     AND NOT EXISTS (
		       SELECT 1
		       FROM runtime_activities AS terminal
		       WHERE terminal.kind = 'exchange.completed'
		         AND terminal.subject_id = started.subject_id
		     )
		 ), indexed AS (
		   SELECT *
		   FROM indexed_all
		   WHERE (? = '' OR capture_run_id = ?)
		     AND (? = '' OR manual_capture_id = ?)
		 ), named AS (
		   SELECT conversation_projection_id,
		          conversation_display_name,
		          ROW_NUMBER() OVER (
		            PARTITION BY conversation_projection_id
		            ORDER BY CASE
		              WHEN trim(conversation_display_name) <> ''
		               AND conversation_display_name <> conversation_actor THEN 0
		              ELSE 1
		            END,
		            sequence DESC
		          ) AS display_name_rank
		   FROM indexed
		 ), preferred_names AS (
		   SELECT conversation_projection_id,
		          conversation_display_name
		   FROM named
		   WHERE display_name_rank = 1
		 ), grouped AS (
		   SELECT conversation_projection_id,
		          MIN(sequence) AS first_sequence,
		          MIN(occurred_at_unix_ms) AS first_occurred_at_unix_ms,
		          COUNT(*) AS turn_count,
		          MAX(sequence) AS latest_sequence
		   FROM indexed
		   GROUP BY conversation_projection_id
		 ), selected AS (
		   SELECT grouped.first_sequence,
		          grouped.first_occurred_at_unix_ms,
		          grouped.turn_count,
		          preferred_names.conversation_display_name AS preferred_display_name,
		          indexed.*
		   FROM grouped
		   JOIN indexed ON indexed.sequence = grouped.latest_sequence
		   JOIN preferred_names USING (conversation_projection_id)
		   WHERE (? = 0 OR grouped.first_sequence < ?)
		   ORDER BY grouped.first_sequence DESC,
		            lower(preferred_display_name) ASC,
		            indexed.conversation_projection_id ASC
		   LIMIT ?
		 )
		 SELECT first_sequence, first_occurred_at_unix_ms, turn_count,
		        preferred_display_name,
		        sequence, activity_id, occurred_at_unix_ms, kind,
		        environment_id, environment_revision, environment_digest,
		        client_endpoint_id, client_endpoint_revision,
		        protocol_plan_id, protocol_plan_revision,
		        route_id, route_revision,
		        account_id, account_revision, credential_epoch,
		        subject_id, status, reason_code,
		        source_kind, source_display_name, source_recognition,
		        capture_run_id, manual_capture_id, connection_id,
		        conversation_projection_id, conversation_display_name,
		        conversation_kind, conversation_evidence, conversation_actor,
		        provider_status, provider_field, client_field, client_path,
		        transport_evidence_json
		 FROM selected
		 ORDER BY first_sequence DESC,
		          lower(preferred_display_name) ASC,
		          conversation_projection_id ASC`,
		request.CaptureRunID,
		request.CaptureRunID,
		request.ManualCaptureID,
		request.ManualCaptureID,
		request.BeforeFirstSequence,
		request.BeforeFirstSequence,
		request.Limit+1,
	)
	if err != nil {
		return activity.ConversationPage{}, fmt.Errorf("list Conversations: %w", err)
	}
	defer rows.Close()
	page := activity.ConversationPage{Items: make([]activity.ConversationRecord, 0)}
	for rows.Next() {
		var firstOccurredAt int64
		var preferredDisplayName string
		var item activity.ConversationRecord
		state := activityScanState{record: &item.Latest}
		targets := []any{
			&item.FirstSequence,
			&firstOccurredAt,
			&item.TurnCount,
			&preferredDisplayName,
		}
		targets = append(targets, state.targets()...)
		if err := rows.Scan(targets...); err != nil {
			return activity.ConversationPage{}, fmt.Errorf("scan Conversation: %w", err)
		}
		latest, err := state.finish()
		if err != nil {
			return activity.ConversationPage{}, err
		}
		item.Latest = latest
		item.FirstOccurredAt = fromUnixMillis(firstOccurredAt)
		if item.Latest.Conversation == nil {
			return activity.ConversationPage{}, errors.New("Conversation has no reference")
		}
		item.Conversation = *item.Latest.Conversation
		// Identity evidence can deepen after a Conversation has started. Preserve
		// the latest Activity as immutable evidence while the derived directory
		// projection keeps the best observed operator-facing label.
		item.Conversation.DisplayName = preferredDisplayName
		if err := item.Validate(); err != nil {
			return activity.ConversationPage{}, fmt.Errorf("validate Conversation: %w", err)
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return activity.ConversationPage{}, fmt.Errorf("iterate Conversations: %w", err)
	}
	if len(page.Items) > request.Limit {
		page.Items = page.Items[:request.Limit]
		page.NextBeforeFirstSequence = page.Items[len(page.Items)-1].FirstSequence
	}
	return page, nil
}

func sameExchangeIdentity(left, right activity.Record) bool {
	return left.SubjectID == right.SubjectID &&
		left.EnvironmentID == right.EnvironmentID &&
		left.EnvironmentRevision == right.EnvironmentRevision &&
		left.EnvironmentDigest == right.EnvironmentDigest &&
		left.ClientEndpointID == right.ClientEndpointID &&
		left.ClientEndpointRevision == right.ClientEndpointRevision &&
		left.ProtocolPlanID == right.ProtocolPlanID &&
		left.ProtocolPlanRevision == right.ProtocolPlanRevision &&
		left.RouteID == right.RouteID &&
		left.RouteRevision == right.RouteRevision &&
		left.SourceKind == right.SourceKind &&
		left.SourceDisplayName == right.SourceDisplayName &&
		left.SourceRecognition == right.SourceRecognition &&
		left.CaptureRunID == right.CaptureRunID &&
		left.ManualCaptureID == right.ManualCaptureID &&
		left.ConnectionID == right.ConnectionID
}

func (repository *activityRepository) list(
	ctx context.Context,
	request activity.PageRequest,
	query string,
	lookAhead bool,
	arguments ...any,
) (activity.Page, error) {
	if err := request.Validate(); err != nil {
		return activity.Page{}, err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return activity.Page{}, err
	}
	defer finish()
	rows, err := repository.database.QueryContext(
		operation,
		query,
		arguments...,
	)
	if err != nil {
		return activity.Page{}, fmt.Errorf("list Activities: %w", err)
	}
	defer rows.Close()
	page := activity.Page{Items: make([]activity.Record, 0)}
	for rows.Next() {
		record, err := scanActivityRecord(rows)
		if err != nil {
			return activity.Page{}, err
		}
		page.Items = append(page.Items, record)
	}
	if err := rows.Err(); err != nil {
		return activity.Page{}, fmt.Errorf("iterate Activities: %w", err)
	}
	if lookAhead && len(page.Items) > request.Limit {
		page.Items = page.Items[:request.Limit]
		page.NextBeforeSequence = page.Items[len(page.Items)-1].Sequence
	} else if !lookAhead && len(page.Items) == request.Limit {
		page.NextBeforeSequence = page.Items[len(page.Items)-1].Sequence
	}
	return page, nil
}

type activityScanner interface {
	Scan(...any) error
}

func scanActivityRecord(scanner activityScanner) (activity.Record, error) {
	var record activity.Record
	state := activityScanState{record: &record}
	if err := scanner.Scan(state.targets()...); err != nil {
		return activity.Record{}, fmt.Errorf("scan Activity: %w", err)
	}
	return state.finish()
}

type activityScanState struct {
	record            *activity.Record
	occurredAt        int64
	diagnosis         activity.Diagnosis
	conversation      agentconversation.Ref
	transportEvidence []byte
}

func (state *activityScanState) targets() []any {
	record := state.record
	return []any{
		&record.Sequence, &record.ID, &state.occurredAt, &record.Kind,
		&record.EnvironmentID, &record.EnvironmentRevision, &record.EnvironmentDigest,
		&record.ClientEndpointID, &record.ClientEndpointRevision,
		&record.ProtocolPlanID, &record.ProtocolPlanRevision,
		&record.RouteID, &record.RouteRevision,
		&record.AccountID, &record.AccountRevision, &record.CredentialEpoch,
		&record.SubjectID, &record.Status, &record.ReasonCode,
		&record.SourceKind, &record.SourceDisplayName, &record.SourceRecognition,
		&record.CaptureRunID, &record.ManualCaptureID, &record.ConnectionID,
		&state.conversation.ProjectionID, &state.conversation.DisplayName,
		&state.conversation.Kind, &state.conversation.Evidence,
		&state.conversation.Actor,
		&state.diagnosis.ProviderStatus, &state.diagnosis.ProviderField,
		&state.diagnosis.ClientField, &state.diagnosis.ClientPath,
		&state.transportEvidence,
	}
}

func (state *activityScanState) finish() (activity.Record, error) {
	record := *state.record
	record.OccurredAt = fromUnixMillis(state.occurredAt)
	if state.conversation.ProjectionID != "" {
		conversation := state.conversation
		record.Conversation = &conversation
	}
	if stored := state.diagnosis; !stored.Empty() {
		record.Diagnosis = &stored
	}
	var err error
	record.Transport, err = decodeTransportEvidence(state.transportEvidence)
	if err != nil {
		return activity.Record{}, fmt.Errorf(
			"decode Activity transport evidence: %w",
			err,
		)
	}
	if err := record.Validate(); err != nil {
		return activity.Record{}, fmt.Errorf("validate stored Activity: %w", err)
	}
	return record, nil
}

func conversationProjectionID(ref *agentconversation.Ref) string {
	if ref == nil {
		return ""
	}
	return ref.ProjectionID
}

func conversationDisplayName(ref *agentconversation.Ref) string {
	if ref == nil {
		return ""
	}
	return ref.DisplayName
}

func conversationKind(ref *agentconversation.Ref) agentconversation.Kind {
	if ref == nil {
		return ""
	}
	return ref.Kind
}

func conversationEvidence(ref *agentconversation.Ref) agentconversation.Evidence {
	if ref == nil {
		return ""
	}
	return ref.Evidence
}

func conversationActor(ref *agentconversation.Ref) string {
	if ref == nil {
		return ""
	}
	return ref.Actor
}

func encodeTransportEvidence(
	evidence *activity.TransportEvidence,
) ([]byte, error) {
	if evidence == nil {
		return nil, nil
	}
	if err := evidence.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return nil, fmt.Errorf("encode Activity transport evidence: %w", err)
	}
	return encoded, nil
}

func decodeTransportEvidence(
	encoded []byte,
) (*activity.TransportEvidence, error) {
	if len(encoded) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var evidence activity.TransportEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("transport evidence contains trailing JSON")
		}
		return nil, err
	}
	if err := evidence.Validate(); err != nil {
		return nil, err
	}
	return &evidence, nil
}
