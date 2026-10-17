package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/recording"
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
	    proxy_revision, account_id, account_settings_revision, selection_source, default_revision,
	    reused_transport,
	    started_at_unix_ms,
	    completed_at_unix_ms,
	    outcome,
	    error_class,
	    bytes_out,
	    bytes_in, byte_counts_known
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
	return appendEgressAttempt(operation, repository.database, attempt)
}

type egressAttemptWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Shared by native Start's single writer transaction and the public audit writer.
func appendEgressAttempt(operation context.Context, writer egressAttemptWriter, attempt egressaudit.Attempt) (egressaudit.Record, error) {
	reused := 0
	if attempt.ReusedTransport() {
		reused = 1
	}
	result, err := writer.ExecContext(
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
		     proxy_revision, account_id, account_settings_revision, selection_source, default_revision,
		     reused_transport,
		     started_at_unix_ms
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
		attempt.Decision().ProxyRevision, attempt.Decision().AccountID, attempt.Decision().AccountSettingsRevision,
		string(attempt.Decision().SelectionSource), attempt.Decision().DefaultRevision,
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
	return completeEgressAttempt(operation, repository.database, attempt)
}

func completeEgressAttempt(operation context.Context, writer egressAttemptWriter, attempt egressaudit.Attempt) (egressaudit.Record, error) {
	if !attempt.Terminal() {
		return egressaudit.Record{}, errors.New("EgressAttempt has not reached a terminal")
	}
	result, err := writer.ExecContext(
		operation,
		`UPDATE runtime_egress_attempts
		    SET completed_at_unix_ms = ?,
		        outcome = ?,
		        error_class = ?,
		        bytes_out = ?,
		        bytes_in = ?, byte_counts_known = ?
		  WHERE attempt_id = ?
		    AND outcome = ''
		    AND completed_at_unix_ms IS NULL
		    AND error_class = ''
		    AND bytes_out = 0
		    AND bytes_in = 0`,
		toUnixMillis(attempt.CompletedAt()),
		string(attempt.Outcome()),
		attempt.ErrorClass(),
		attempt.BytesOut(),
		attempt.BytesIn(),
		attempt.ByteCountsKnown(),
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
		// A retry after an ambiguous commit finds its own terminal already
		// stored. Only that exact terminal is success; anything else is an
		// absent attempt or a conflicting terminal.
		var completedAt sql.NullInt64
		var outcome, errorClass string
		var bytesOut, bytesIn int64
		var known bool
		err := writer.QueryRowContext(
			operation,
			`SELECT completed_at_unix_ms, outcome, error_class, bytes_out, bytes_in, byte_counts_known
			   FROM runtime_egress_attempts WHERE attempt_id = ?`,
			attempt.ID(),
		).Scan(&completedAt, &outcome, &errorClass, &bytesOut, &bytesIn, &known)
		if err != nil || !completedAt.Valid ||
			completedAt.Int64 != toUnixMillis(attempt.CompletedAt()) ||
			outcome != string(attempt.Outcome()) ||
			errorClass != attempt.ErrorClass() ||
			bytesOut != attempt.BytesOut() || bytesIn != attempt.BytesIn() || known != attempt.ByteCountsKnown() {
			return egressaudit.Record{}, errors.New(
				"EgressAttempt is absent or already has another terminal",
			)
		}
	}
	return egressaudit.Record{Attempt: attempt}, nil
}

// Recover terminalizes every attempt that was durably appended but not
// completed by the previous daemon incarnation. ProductRuntime calls it while
// it exclusively owns the database and before constructing any transport, so
// one transaction is both the recovery boundary and the generation boundary.
func (repository *egressAttemptRepository) Recover(
	ctx context.Context,
	completedAt time.Time,
) (int, error) {
	if completedAt.IsZero() {
		return 0, errors.New("EgressAttempt recovery time is required")
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer finish()

	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return 0, fmt.Errorf("begin EgressAttempt recovery: %w", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	// Reconstruct every durable row through the domain constructors before
	// writing anything. That rejects partial terminals as well as complete
	// terminals with impossible timestamps, outcomes, error classes, or byte
	// evidence. Recovery must never preserve invalid prior evidence by merely
	// skipping it, and the shared transaction keeps rejection atomic for the
	// otherwise eligible rows.
	rows, err := transaction.QueryContext(
		operation,
		egressAttemptSelect+" ORDER BY sequence ASC",
	)
	if err != nil {
		return 0, fmt.Errorf("inspect EgressAttempt recovery state: %w", err)
	}
	var validationErr error
	for rows.Next() {
		if _, scanErr := scanEgressAttempt(rows); scanErr != nil {
			validationErr = scanErr
			break
		}
	}
	if validationErr == nil {
		validationErr = rows.Err()
	}
	closeErr := rows.Close()
	if validationErr != nil {
		return 0, fmt.Errorf(
			"validate EgressAttempt recovery state: %w",
			validationErr,
		)
	}
	if closeErr != nil {
		return 0, fmt.Errorf(
			"close EgressAttempt recovery inspection: %w",
			closeErr,
		)
	}

	recoveryMillis := toUnixMillis(completedAt.UTC())
	result, err := transaction.ExecContext(
		operation,
		`UPDATE runtime_egress_attempts
		    SET completed_at_unix_ms = CASE
		            WHEN started_at_unix_ms > ? THEN started_at_unix_ms
		            ELSE ?
		        END,
		        outcome = ?,
		        error_class = CASE WHEN purpose='client_tool_service' THEN 'execution_uncertain' ELSE ? END,
		        byte_counts_known = CASE WHEN purpose='client_tool_service' THEN 0 ELSE byte_counts_known END
		  WHERE outcome = ''
		    AND completed_at_unix_ms IS NULL`,
		recoveryMillis,
		recoveryMillis,
		string(egressaudit.OutcomeFailed),
		egressaudit.RecoveryErrorClass,
	)
	if err != nil {
		return 0, fmt.Errorf("recover EgressAttempts: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read recovered EgressAttempt count: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return 0, fmt.Errorf("commit EgressAttempt recovery: %w", err)
	}
	return int(count), nil
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

	conditions := make([]string, 0, 5)
	arguments := make([]any, 0, 6)
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
	if normalized.ExchangeID != "" {
		conditions = append(conditions, "parent_exchange_id = ?")
		arguments = append(arguments, normalized.ExchangeID)
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

func scanEgressAttempt(rows interface{ Scan(...any) error }) (egressaudit.Record, error) {
	var (
		sequence                int64
		attemptID               string
		connectionID            string
		purpose                 string
		payloadClass            string
		parentKind              string
		parentID                string
		parentXID               string
		callerKind              string
		callerID                string
		targetOrigin            string
		policyID                string
		policyRev               int64
		authority               string
		ruleID                  string
		proxyID                 string
		proxyRevision           uint64
		accountID               string
		accountSettingsRevision uint64
		selectionSource         string
		defaultRevision         uint64
		reused                  int64
		startedAt               int64
		completedAt             sql.NullInt64
		outcome                 string
		errorClass              string
		bytesOut                int64
		bytesIn                 int64
		known                   int64
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
		&proxyRevision, &accountID, &accountSettingsRevision,
		&selectionSource, &defaultRevision,
		&reused,
		&startedAt,
		&completedAt,
		&outcome,
		&errorClass,
		&bytesOut,
		&bytesIn,
		&known,
	); err != nil {
		return egressaudit.Record{}, fmt.Errorf(
			"scan EgressAttempt: %w",
			err,
		)
	}
	if (reused != 0 && reused != 1) || (known != 0 && known != 1) {
		return egressaudit.Record{}, errors.New(
			"restore EgressAttempt: reused transport flag is invalid",
		)
	}
	if policyRev <= 0 {
		return egressaudit.Record{}, errors.New(
			"restore EgressAttempt: policy revision is invalid",
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
			PolicyID:                policyID,
			PolicyRevision:          uint64(policyRev),
			Authority:               egressaudit.PolicyAuthorityKind(authority),
			RuleID:                  ruleID,
			ProxyID:                 proxyID,
			ProxyRevision:           proxyRevision,
			AccountID:               accountID,
			AccountSettingsRevision: accountSettingsRevision,
			SelectionSource:         egressprofile.SelectionSource(selectionSource), DefaultRevision: defaultRevision,
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
	if (outcome == "") != !completedAt.Valid {
		return egressaudit.Record{}, errors.New(
			"restore EgressAttempt: terminal evidence is partial",
		)
	}
	if outcome == "" && (errorClass != "" || bytesOut != 0 || bytesIn != 0 || known != 1) {
		return egressaudit.Record{}, errors.New(
			"restore EgressAttempt: nonterminal evidence is not empty",
		)
	}
	if outcome != "" {
		attempt, err = attempt.Finish(egressaudit.TerminalInput{
			Outcome:              egressaudit.Outcome(outcome),
			ErrorClass:           errorClass,
			BytesOut:             bytesOut,
			BytesIn:              bytesIn,
			CompletedAt:          fromUnixMillis(completedAt.Int64),
			UnobservedByteCounts: known == 0,
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

// PutAttempt retains a complete snapshot without requiring an earlier start.
// A late start is harmless; immutable coordinates and terminal facts cannot
// overwrite conflicting evidence. Native control transactions remain separate.
func (repository *egressAttemptRepository) PutAttempt(ctx context.Context, attempt egressaudit.Attempt) error {
	return repository.putAttempt(ctx, recording.Scope{}, attempt)
}
func (repository *egressAttemptRepository) PutScopedAttempt(ctx context.Context, scope recording.Scope, attempt egressaudit.Attempt) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	return repository.putAttempt(ctx, scope, attempt)
}
func (repository *egressAttemptRepository) putAttempt(ctx context.Context, scope recording.Scope, attempt egressaudit.Attempt) error {
	if attempt.Purpose() == egressaudit.PurposeClientToolService {
		return errors.New("native execution requires its control transaction")
	}
	input := normalizedEgressStart(attempt)
	validated, err := egressaudit.New(input)
	if err != nil {
		return err
	}
	if attempt.Terminal() {
		if _, err := validated.Finish(normalizedEgressTerminal(attempt)); err != nil {
			return err
		}
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	tx, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	priorScope, scopeErr := readRecordingScope(operation, tx, "runtime_egress_attempts", "attempt_id", attempt.ID())
	if scopeErr != nil && !errors.Is(scopeErr, sql.ErrNoRows) {
		return scopeErr
	}
	if scopeErr == nil && priorScope != scope {
		return errors.New("egress recording scope conflicts")
	}
	existing, err := scanEgressAttempt(tx.QueryRowContext(operation, egressAttemptSelect+" WHERE attempt_id = ?", attempt.ID()))
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = appendEgressAttempt(operation, tx, validated); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if normalizedEgressStart(existing.Attempt) != input {
		return errors.New("EgressAttempt immutable coordinates conflict")
	} else if existing.Attempt.Terminal() {
		if attempt.Terminal() && normalizedEgressTerminal(existing.Attempt) != normalizedEgressTerminal(attempt) {
			return errors.New("EgressAttempt terminal conflicts")
		}
		return tx.Commit()
	}
	if _, err = tx.ExecContext(operation, `UPDATE runtime_egress_attempts SET recording_scope_kind=?,recording_scope_id=?,recording_archive_epoch=?,recording_capture_generation=? WHERE attempt_id=?`, scope.Capture.Kind, scope.Capture.ID, scope.Fence.ArchiveEpoch, scope.Fence.CaptureGeneration, attempt.ID()); err != nil {
		return err
	}
	if attempt.Terminal() {
		if _, err = completeEgressAttempt(operation, tx, attempt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func normalizedEgressStart(a egressaudit.Attempt) egressaudit.NewInput {
	return egressaudit.NewInput{
		ID: a.ID(), ConnectionID: a.ConnectionID(),
		Purpose: a.Purpose(), PayloadClass: a.PayloadClass(), Parent: a.Parent(),
		Caller: a.Caller(), CallerID: a.CallerID(), TargetOrigin: a.TargetOrigin(),
		Decision: a.Decision(), ReusedTransport: a.ReusedTransport(),
		StartedAt: fromUnixMillis(toUnixMillis(a.StartedAt())),
	}
}

var _ egressaudit.AttemptStore = (*egressAttemptRepository)(nil)

func normalizedEgressTerminal(a egressaudit.Attempt) egressaudit.TerminalInput {
	return egressaudit.TerminalInput{
		Outcome: a.Outcome(), ErrorClass: a.ErrorClass(),
		BytesOut: a.BytesOut(), BytesIn: a.BytesIn(),
		CompletedAt:          fromUnixMillis(toUnixMillis(a.CompletedAt())),
		UnobservedByteCounts: !a.ByteCountsKnown(),
	}
}
