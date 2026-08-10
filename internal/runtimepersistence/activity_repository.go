package runtimepersistence

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/vibe-agi/vibermate/internal/activity"
)

type activityRepository struct {
	database   *sql.DB
	operations *operationGate
}

var _ activity.Repository = (*activityRepository)(nil)

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
		     provider_status,
		     provider_field,
		     client_field,
		     client_path,
		     transport_evidence_json
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
		     provider_status, provider_field, client_field, client_path,
		     transport_evidence_json
		 FROM ranked
		 WHERE exchange_rank = 1
		   AND (? = '' OR capture_run_id = ?)
		   AND (? = '' OR environment_id = ?)
		   AND (? = 0 OR sequence < ?)
		 ORDER BY sequence DESC
		 LIMIT ?`,
		true,
		request.CaptureRunID,
		request.CaptureRunID,
		request.EnvironmentID,
		request.EnvironmentID,
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
	var occurredAt int64
	var diagnosis activity.Diagnosis
	var transportEvidence []byte
	if err := scanner.Scan(
		&record.Sequence,
		&record.ID,
		&occurredAt,
		&record.Kind,
		&record.EnvironmentID,
		&record.EnvironmentRevision,
		&record.EnvironmentDigest,
		&record.ClientEndpointID,
		&record.ClientEndpointRevision,
		&record.ProtocolPlanID,
		&record.ProtocolPlanRevision,
		&record.RouteID,
		&record.RouteRevision,
		&record.AccountID,
		&record.AccountRevision,
		&record.CredentialEpoch,
		&record.SubjectID,
		&record.Status,
		&record.ReasonCode,
		&record.SourceKind,
		&record.SourceDisplayName,
		&record.SourceRecognition,
		&record.CaptureRunID,
		&record.ManualCaptureID,
		&record.ConnectionID,
		&diagnosis.ProviderStatus,
		&diagnosis.ProviderField,
		&diagnosis.ClientField,
		&diagnosis.ClientPath,
		&transportEvidence,
	); err != nil {
		return activity.Record{}, fmt.Errorf("scan Activity: %w", err)
	}
	record.OccurredAt = fromUnixMillis(occurredAt)
	if stored := diagnosis; !stored.Empty() {
		record.Diagnosis = &stored
	}
	var err error
	record.Transport, err = decodeTransportEvidence(transportEvidence)
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
