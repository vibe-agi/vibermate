package runtimepersistence

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

const toolApprovalColumns = `
	approval_id,
	revision,
	exchange_id,
	access_id,
	plan_revision,
	plan_hash,
	kind,
	subject_refs_json,
	subject_labels_json,
	target_host,
	target_port,
	aggregate_key,
	request_count,
	waiter_count,
	state,
	decision,
	decision_scope,
	decision_reason,
	decision_idempotency_key,
	created_at_unix_ms,
	expires_at_unix_ms,
	resolved_at_unix_ms`

type toolApprovalRepository struct {
	database   *sql.DB
	operations *operationGate
}

var _ toolapproval.Repository = (*toolApprovalRepository)(nil)

func newToolApprovalRepository(
	database *sql.DB,
	operations *operationGate,
) *toolApprovalRepository {
	return &toolApprovalRepository{database: database, operations: operations}
}

func (repository *toolApprovalRepository) Recover(
	ctx context.Context,
	now time.Time,
) (toolapproval.Recovery, error) {
	records, err := repository.CancelPending(ctx, "runtime_recovered", now)
	return toolapproval.Recovery{CanceledPending: len(records)}, err
}

func (repository *toolApprovalRepository) Create(
	ctx context.Context,
	record toolapproval.Record,
) error {
	if err := record.Validate(); err != nil {
		return err
	}
	callIDs, err := json.Marshal(record.SubjectRefs)
	if err != nil {
		return err
	}
	names, err := json.Marshal(record.SubjectLabels)
	if err != nil {
		return err
	}
	// A record with no plan binding stores no binding. Writing a zero hash and
	// a zero revision would record an Access that was never resolved.
	planHash := []byte{}
	if record.PlanRevision != 0 {
		planHash = record.PlanHash[:]
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	_, err = repository.database.ExecContext(
		operation,
		`INSERT INTO tool_approvals (
		     approval_id,
		     revision,
		     exchange_id,
		     access_id,
		     plan_revision,
		     plan_hash,
		     kind,
		     subject_refs_json,
		     subject_labels_json,
		     target_host,
		     target_port,
		     aggregate_key,
		     request_count,
		     waiter_count,
		     state,
		     created_at_unix_ms,
		     expires_at_unix_ms
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
		record.Revision,
		record.ExchangeID,
		record.AccessID.String(),
		record.PlanRevision,
		planHash,
		string(record.Kind),
		callIDs,
		names,
		record.Target.Host,
		int64(record.Target.Port),
		record.AggregateKey,
		int64(record.RequestCount),
		int64(record.WaiterCount),
		string(record.State),
		toUnixMillis(record.CreatedAt),
		toUnixMillis(record.ExpiresAt),
	)
	if err != nil {
		return fmt.Errorf("insert tool approval: %w", err)
	}
	return nil
}

func (repository *toolApprovalRepository) Get(
	ctx context.Context,
	approvalID string,
) (toolapproval.Record, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return toolapproval.Record{}, err
	}
	defer finish()
	return repository.get(operation, repository.database, approvalID)
}

func (repository *toolApprovalRepository) List(
	ctx context.Context,
	request toolapproval.PageRequest,
) ([]toolapproval.Record, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	rows, err := repository.database.QueryContext(
		operation,
		`SELECT `+toolApprovalColumns+`
		 FROM tool_approvals
		 WHERE (? = '' OR state = ?)
		 ORDER BY
		     CASE state WHEN 'pending' THEN 0 ELSE 1 END,
		     created_at_unix_ms DESC,
		     approval_id DESC
		 LIMIT ?`,
		string(request.State),
		string(request.State),
		request.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list tool approvals: %w", err)
	}
	defer rows.Close()
	var records []toolapproval.Record
	for rows.Next() {
		record, err := scanToolApproval(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool approvals: %w", err)
	}
	return records, nil
}

func (repository *toolApprovalRepository) Decide(
	ctx context.Context,
	command toolapproval.DecisionCommand,
	now time.Time,
) (toolapproval.Record, error) {
	if err := command.Validate(); err != nil {
		return toolapproval.Record{}, err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return toolapproval.Record{}, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return toolapproval.Record{}, fmt.Errorf("begin tool approval decision: %w", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	replayed, replayErr := scanToolApproval(transaction.QueryRowContext(
		operation,
		`SELECT `+toolApprovalColumns+`
		 FROM tool_approvals
		 WHERE decision_idempotency_key = ?`,
		command.IdempotencyKey,
	))
	switch {
	case replayErr == nil:
		if replayed.ID != command.ApprovalID ||
			replayed.Decision != command.Decision ||
			replayed.DecisionScope != command.Scope ||
			replayed.DecisionReason != command.ReasonCode {
			return toolapproval.Record{}, toolapproval.ErrRevisionConflict
		}
		return replayed, nil
	case !errors.Is(replayErr, sql.ErrNoRows):
		return toolapproval.Record{}, replayErr
	}
	current, err := repository.get(operation, transaction, command.ApprovalID)
	if err != nil {
		return toolapproval.Record{}, err
	}
	if current.State != toolapproval.StatePending ||
		current.Revision != command.ExpectedRevision ||
		!now.Before(current.ExpiresAt) {
		return current, toolapproval.ErrRevisionConflict
	}
	state := toolapproval.StateDenied
	if command.Decision == toolapproval.DecisionAllowOnce {
		state = toolapproval.StateAllowed
	}
	result, err := transaction.ExecContext(
		operation,
		`UPDATE tool_approvals
		 SET revision = revision + 1,
		     state = ?,
		     decision = ?,
		     decision_scope = ?,
		     decision_reason = ?,
		     decision_idempotency_key = ?,
		     resolved_at_unix_ms = ?
		 WHERE approval_id = ?
		   AND revision = ?
		   AND state = 'pending'`,
		string(state),
		string(command.Decision),
		command.Scope,
		command.ReasonCode,
		command.IdempotencyKey,
		toUnixMillis(now),
		command.ApprovalID,
		command.ExpectedRevision,
	)
	if err != nil {
		return toolapproval.Record{}, fmt.Errorf("decide tool approval: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return toolapproval.Record{}, toolapproval.ErrRevisionConflict
	}
	resolved, err := repository.get(operation, transaction, command.ApprovalID)
	if err != nil {
		return toolapproval.Record{}, err
	}
	// INV-APPROVAL-TERMINAL-ATOMIC: a remembered answer and the rule it
	// creates are one commit. A decision that survived without its rule would
	// ask again; a rule that survived without its decision would answer a
	// question nobody was asked.
	if err := rememberConnectionAnswer(
		operation,
		transaction,
		resolved,
		now,
	); err != nil {
		return toolapproval.Record{}, err
	}
	if err := transaction.Commit(); err != nil {
		return toolapproval.Record{}, fmt.Errorf("commit tool approval decision: %w", err)
	}
	return resolved, nil
}

func (repository *toolApprovalRepository) Cancel(
	ctx context.Context,
	approvalID string,
	reason string,
	now time.Time,
) (toolapproval.Record, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return toolapproval.Record{}, err
	}
	defer finish()
	state := toolapproval.StateCanceled
	if reason == "approval_expired" {
		state = toolapproval.StateExpired
	}
	_, err = repository.database.ExecContext(
		operation,
		`UPDATE tool_approvals
		 SET revision = revision + 1,
		     state = ?,
		     decision_reason = ?,
		     resolved_at_unix_ms = ?
		 WHERE approval_id = ?
		   AND state = 'pending'`,
		string(state),
		reason,
		toUnixMillis(now),
		approvalID,
	)
	if err != nil {
		return toolapproval.Record{}, fmt.Errorf("cancel tool approval: %w", err)
	}
	return repository.get(operation, repository.database, approvalID)
}

func (repository *toolApprovalRepository) CancelPending(
	ctx context.Context,
	reason string,
	now time.Time,
) ([]toolapproval.Record, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return nil, fmt.Errorf("begin pending approval cancellation: %w", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	rows, err := transaction.QueryContext(
		operation,
		`SELECT approval_id
		 FROM tool_approvals
		 WHERE state = 'pending'
		 ORDER BY created_at_unix_ms`,
	)
	if err != nil {
		return nil, fmt.Errorf("list pending tool approvals: %w", err)
	}
	var identifiers []string
	for rows.Next() {
		var identifier string
		if err := rows.Scan(&identifier); err != nil {
			rows.Close()
			return nil, err
		}
		identifiers = append(identifiers, identifier)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if _, err := transaction.ExecContext(
		operation,
		`UPDATE tool_approvals
		 SET revision = revision + 1,
		     state = 'canceled',
		     decision_reason = ?,
		     resolved_at_unix_ms = ?
		 WHERE state = 'pending'`,
		reason,
		toUnixMillis(now),
	); err != nil {
		return nil, fmt.Errorf("cancel pending tool approvals: %w", err)
	}
	records := make([]toolapproval.Record, 0, len(identifiers))
	for _, identifier := range identifiers {
		record, err := repository.get(operation, transaction, identifier)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("commit pending approval cancellation: %w", err)
	}
	return records, nil
}

type toolApprovalQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (repository *toolApprovalRepository) get(
	ctx context.Context,
	query toolApprovalQuery,
	approvalID string,
) (toolapproval.Record, error) {
	record, err := scanToolApproval(query.QueryRowContext(
		ctx,
		`SELECT `+toolApprovalColumns+`
		 FROM tool_approvals
		 WHERE approval_id = ?`,
		approvalID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return toolapproval.Record{}, toolapproval.ErrNotFound
	}
	return record, err
}

type toolApprovalScanner interface {
	Scan(...any) error
}

func scanToolApproval(scanner toolApprovalScanner) (toolapproval.Record, error) {
	var record toolapproval.Record
	var accessID string
	var planRevision int64
	var planHash []byte
	var callIDs []byte
	var names []byte
	var createdAt int64
	var expiresAt int64
	var resolvedAt int64
	var requestCount int64
	var waiterCount int64
	var targetPort int64
	if err := scanner.Scan(
		&record.ID,
		&record.Revision,
		&record.ExchangeID,
		&accessID,
		&planRevision,
		&planHash,
		&record.Kind,
		&callIDs,
		&names,
		&record.Target.Host,
		&targetPort,
		&record.AggregateKey,
		&requestCount,
		&waiterCount,
		&record.State,
		&record.Decision,
		&record.DecisionScope,
		&record.DecisionReason,
		&record.IdempotencyKey,
		&createdAt,
		&expiresAt,
		&resolvedAt,
	); err != nil {
		return toolapproval.Record{}, err
	}
	bound := planRevision != 0
	if bound {
		typedAccessID, err := access.NewAccessID(accessID)
		if err != nil {
			return toolapproval.Record{}, err
		}
		if planRevision < 0 || uint64(planRevision) > uint64(access.MaxRevision) {
			return toolapproval.Record{}, toolapproval.ErrInvalidApproval
		}
		if len(planHash) != len(record.PlanHash) {
			return toolapproval.Record{}, toolapproval.ErrInvalidApproval
		}
		record.AccessID = typedAccessID
		record.PlanRevision = access.Revision(planRevision)
		copy(record.PlanHash[:], planHash)
	} else if accessID != "" || len(planHash) != 0 {
		return toolapproval.Record{}, toolapproval.ErrInvalidApproval
	}
	if requestCount <= 0 || waiterCount <= 0 ||
		requestCount > int64(^uint32(0)) || waiterCount > int64(^uint32(0)) {
		return toolapproval.Record{}, toolapproval.ErrInvalidApproval
	}
	record.RequestCount = uint32(requestCount)
	record.WaiterCount = uint32(waiterCount)
	record.Target.Port = uint16(targetPort)
	if err := decodeStringArray(callIDs, &record.SubjectRefs); err != nil {
		return toolapproval.Record{}, err
	}
	if err := decodeStringArray(names, &record.SubjectLabels); err != nil {
		return toolapproval.Record{}, err
	}
	record.CreatedAt = fromUnixMillis(createdAt)
	record.ExpiresAt = fromUnixMillis(expiresAt)
	if resolvedAt != 0 {
		record.ResolvedAt = fromUnixMillis(resolvedAt)
	}
	if err := record.Validate(); err != nil {
		return toolapproval.Record{}, fmt.Errorf("validate stored tool approval: %w", err)
	}
	return record, nil
}

func decodeStringArray(payload []byte, output *[]string) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("stored string array contains trailing JSON")
	}
	return nil
}

// Join records one more caller waiting on a pending question. The request
// count only ever grows, because it counts what was asked; the waiter count
// tracks who is still listening.
func (repository *toolApprovalRepository) Join(
	ctx context.Context,
	approvalID string,
) (toolapproval.Record, error) {
	return repository.adjustWaiters(
		ctx,
		approvalID,
		`UPDATE tool_approvals
		    SET request_count = MIN(request_count + 1, 4294967295),
		        waiter_count = MIN(waiter_count + 1, request_count + 1)
		  WHERE approval_id = ? AND state = 'pending'`,
	)
}

// Leave records one caller giving up. It never reaches zero: a question with
// no one waiting is canceled rather than left standing with an empty audience.
func (repository *toolApprovalRepository) Leave(
	ctx context.Context,
	approvalID string,
) (toolapproval.Record, error) {
	return repository.adjustWaiters(
		ctx,
		approvalID,
		`UPDATE tool_approvals
		    SET waiter_count = MAX(waiter_count - 1, 1)
		  WHERE approval_id = ? AND state = 'pending'`,
	)
}

func (repository *toolApprovalRepository) adjustWaiters(
	ctx context.Context,
	approvalID string,
	statement string,
) (toolapproval.Record, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return toolapproval.Record{}, err
	}
	defer finish()
	result, err := repository.database.ExecContext(
		operation,
		statement,
		approvalID,
	)
	if err != nil {
		return toolapproval.Record{}, fmt.Errorf("adjust approval waiters: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return toolapproval.Record{}, err
	}
	if affected == 0 {
		return toolapproval.Record{}, toolapproval.ErrNotFound
	}
	return repository.get(operation, repository.database, approvalID)
}
