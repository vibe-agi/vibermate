package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

const egressAttemptSelect = `SELECT
	    sequence,
	    attempt_id,
	    connection_id,
	    purpose,
	    payload_class,
	    parent_kind,
	    parent_id,
	    parent_exchange_id,
	    caller_kind,
	    caller_id,
	    target_origin,
	    policy_id,
	    policy_revision,
	    policy_authority,
	    rule_id,
	    proxy_id,
	    reused_transport,
	    started_at_unix_ms,
	    completed_at_unix_ms,
	    outcome,
	    error_class,
	    bytes_out,
	    bytes_in
	 FROM runtime_egress_attempts`

type egressAttemptRepository struct {
	database   *sql.DB
	operations *operationGate
}

var _ egressaudit.Repository = (*egressAttemptRepository)(nil)

func newEgressAttemptRepository(
	database *sql.DB,
	operations *operationGate,
) *egressAttemptRepository {
	return &egressAttemptRepository{
		database:   database,
		operations: operations,
	}
}

// Append writes one outbound attempt exactly once. A duplicate identity is a
// caller defect rather than a retry, so the unique constraint surfaces it
// instead of silently replacing an earlier destination.
func (repository *egressAttemptRepository) Append(
	ctx context.Context,
	attempt egressaudit.Attempt,
) (egressaudit.Record, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return egressaudit.Record{}, err
	}
	defer finish()
	reused := 0
	if attempt.ReusedTransport() {
		reused = 1
	}
	result, err := repository.database.ExecContext(
		operation,
		`INSERT INTO runtime_egress_attempts (
		     attempt_id,
		     connection_id,
		     purpose,
		     payload_class,
		     parent_kind,
		     parent_id,
		     parent_exchange_id,
		     caller_kind,
		     caller_id,
		     target_origin,
		     policy_id,
		     policy_revision,
		     policy_authority,
		     rule_id,
		     proxy_id,
		     reused_transport,
		     started_at_unix_ms
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.ID(),
		attempt.ConnectionID(),
		string(attempt.Purpose()),
		string(attempt.PayloadClass()),
		string(attempt.Parent().Kind),
		attempt.Parent().ID,
		attempt.Parent().ExchangeID,
		string(attempt.Caller()),
		attempt.CallerID(),
		attempt.TargetOrigin(),
		attempt.Decision().PolicyID,
		int64(attempt.Decision().PolicyRevision),
		string(attempt.Decision().Authority),
		attempt.Decision().RuleID,
		attempt.Decision().ProxyID,
		reused,
		toUnixMillis(attempt.StartedAt()),
	)
	if err != nil {
		return egressaudit.Record{}, fmt.Errorf(
			"append EgressAttempt: %w",
			err,
		)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return egressaudit.Record{}, fmt.Errorf(
			"read EgressAttempt sequence: %w",
			err,
		)
	}
	return egressaudit.Record{Sequence: sequence, Attempt: attempt}, nil
}

// Complete records the terminal exactly once. The WHERE clause refuses a
// second terminal, so a late writer cannot rewrite an outcome.
func (repository *egressAttemptRepository) Complete(
	ctx context.Context,
	attempt egressaudit.Attempt,
) (egressaudit.Record, error) {
	if !attempt.Terminal() {
		return egressaudit.Record{}, errors.New(
			"EgressAttempt has not reached a terminal",
		)
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return egressaudit.Record{}, err
	}
	defer finish()
	result, err := repository.database.ExecContext(
		operation,
		`UPDATE runtime_egress_attempts
		    SET completed_at_unix_ms = ?,
		        outcome = ?,
		        error_class = ?,
		        bytes_out = ?,
		        bytes_in = ?
		  WHERE attempt_id = ? AND outcome = ''`,
		toUnixMillis(attempt.CompletedAt()),
		string(attempt.Outcome()),
		attempt.ErrorClass(),
		attempt.BytesOut(),
		attempt.BytesIn(),
		attempt.ID(),
	)
	if err != nil {
		return egressaudit.Record{}, fmt.Errorf(
			"complete EgressAttempt: %w",
			err,
		)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return egressaudit.Record{}, fmt.Errorf(
			"read EgressAttempt completion: %w",
			err,
		)
	}
	if affected != 1 {
		return egressaudit.Record{}, errors.New(
			"EgressAttempt is absent or already terminal",
		)
	}
	return egressaudit.Record{Attempt: attempt}, nil
}

func (repository *egressAttemptRepository) List(
	ctx context.Context,
	request egressaudit.PageRequest,
) (egressaudit.Page, error) {
	normalized, err := request.Normalized()
	if err != nil {
		return egressaudit.Page{}, err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return egressaudit.Page{}, err
	}
	defer finish()

	conditions := make([]string, 0, 4)
	arguments := make([]any, 0, 5)
	if normalized.AfterCursor != "" {
		sequence, cursorErr := egressaudit.ParseCursor(normalized.AfterCursor)
		if cursorErr != nil {
			return egressaudit.Page{}, cursorErr
		}
		conditions = append(conditions, "sequence < ?")
		arguments = append(arguments, sequence)
	}
	if normalized.ConnectionID != "" {
		conditions = append(conditions, "connection_id = ?")
		arguments = append(arguments, normalized.ConnectionID)
	}
	if normalized.ParentID != "" {
		conditions = append(conditions, "parent_kind = ?", "parent_id = ?")
		arguments = append(
			arguments,
			string(normalized.ParentKind),
			normalized.ParentID,
		)
	}
	if normalized.Purpose != "" {
		conditions = append(conditions, "purpose = ?")
		arguments = append(arguments, string(normalized.Purpose))
	}
	query := egressAttemptSelect
	if len(conditions) != 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY sequence DESC LIMIT ?"
	arguments = append(arguments, normalized.Limit)

	rows, err := repository.database.QueryContext(operation, query, arguments...)
	if err != nil {
		return egressaudit.Page{}, fmt.Errorf("read EgressAttempts: %w", err)
	}
	defer rows.Close()
	page := egressaudit.Page{
		Items: make([]egressaudit.Record, 0, normalized.Limit),
	}
	for rows.Next() {
		record, scanErr := scanEgressAttempt(rows)
		if scanErr != nil {
			return egressaudit.Page{}, scanErr
		}
		page.Items = append(page.Items, record)
	}
	if err := rows.Err(); err != nil {
		return egressaudit.Page{}, fmt.Errorf(
			"iterate EgressAttempts: %w",
			err,
		)
	}
	if len(page.Items) == normalized.Limit {
		page.NextCursor, err = egressaudit.Cursor(
			page.Items[len(page.Items)-1].Sequence,
		)
		if err != nil {
			return egressaudit.Page{}, err
		}
	}
	return page, nil
}

func scanEgressAttempt(rows *sql.Rows) (egressaudit.Record, error) {
	var (
		sequence     int64
		attemptID    string
		connectionID string
		purpose      string
		payloadClass string
		parentKind   string
		parentID     string
		parentXID    string
		callerKind   string
		callerID     string
		targetOrigin string
		policyID     string
		policyRev    int64
		authority    string
		ruleID       string
		proxyID      string
		reused       int64
		startedAt    int64
		completedAt  sql.NullInt64
		outcome      string
		errorClass   string
		bytesOut     int64
		bytesIn      int64
	)
	if err := rows.Scan(
		&sequence,
		&attemptID,
		&connectionID,
		&purpose,
		&payloadClass,
		&parentKind,
		&parentID,
		&parentXID,
		&callerKind,
		&callerID,
		&targetOrigin,
		&policyID,
		&policyRev,
		&authority,
		&ruleID,
		&proxyID,
		&reused,
		&startedAt,
		&completedAt,
		&outcome,
		&errorClass,
		&bytesOut,
		&bytesIn,
	); err != nil {
		return egressaudit.Record{}, fmt.Errorf(
			"scan EgressAttempt: %w",
			err,
		)
	}
	attempt, err := egressaudit.New(egressaudit.NewInput{
		ID:           attemptID,
		ConnectionID: connectionID,
		Purpose:      egressaudit.EgressPurpose(purpose),
		PayloadClass: egressaudit.PayloadClass(payloadClass),
		Parent: egressaudit.ParentRef{
			Kind:       egressaudit.ParentKind(parentKind),
			ID:         parentID,
			ExchangeID: parentXID,
		},
		Caller:       egressaudit.CallerKind(callerKind),
		CallerID:     callerID,
		TargetOrigin: targetOrigin,
		Decision: egressaudit.DecisionRef{
			PolicyID:       policyID,
			PolicyRevision: uint64(policyRev),
			Authority:      egressaudit.PolicyAuthorityKind(authority),
			RuleID:         ruleID,
			ProxyID:        proxyID,
		},
		ReusedTransport: reused == 1,
		StartedAt:       fromUnixMillis(startedAt),
	})
	if err != nil {
		return egressaudit.Record{}, fmt.Errorf(
			"restore EgressAttempt: %w",
			err,
		)
	}
	if outcome != "" && completedAt.Valid {
		attempt, err = attempt.Finish(egressaudit.TerminalInput{
			Outcome:     egressaudit.Outcome(outcome),
			ErrorClass:  errorClass,
			BytesOut:    bytesOut,
			BytesIn:     bytesIn,
			CompletedAt: fromUnixMillis(completedAt.Int64),
		})
		if err != nil {
			return egressaudit.Record{}, fmt.Errorf(
				"restore EgressAttempt terminal: %w",
				err,
			)
		}
	}
	return egressaudit.Record{Sequence: sequence, Attempt: attempt}, nil
}
