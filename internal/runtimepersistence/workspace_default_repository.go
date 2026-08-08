package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/workspacedefault"
)

type workspaceDefaultRepository struct {
	database         *sql.DB
	operations       *operationGate
	reconcileTimeout time.Duration
	committer        transactionCommitter
}

var _ workspacedefault.Repository = (*workspaceDefaultRepository)(nil)

func newWorkspaceDefaultRepository(
	database *sql.DB,
	operations *operationGate,
	reconcileTimeout time.Duration,
	committer transactionCommitter,
) *workspaceDefaultRepository {
	return &workspaceDefaultRepository{
		database: database, operations: operations,
		reconcileTimeout: reconcileTimeout, committer: committer,
	}
}

func (repository *workspaceDefaultRepository) Load(
	ctx context.Context,
	key workspacedefault.Key,
) (workspacedefault.Record, bool, error) {
	if key.Validate() != nil {
		return workspacedefault.Record{}, false, workspacedefault.ErrInvalidDefault
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return workspacedefault.Record{}, false, err
	}
	defer permit.finish()
	return loadWorkspaceDefault(permit.context, repository.database, key)
}

func (repository *workspaceDefaultRepository) Write(
	ctx context.Context,
	expected uint64,
	candidate workspacedefault.Record,
) (workspacedefault.CommitResult, error) {
	if candidate.Validate() != nil || expected >= workspacedefault.MaxRevision ||
		candidate.Revision != expected+1 {
		return workspacedefault.CommitResult{Outcome: workspacedefault.CommitNotCommitted},
			workspacedefault.ErrInvalidDefault
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return workspacedefault.CommitResult{Outcome: workspacedefault.CommitNotCommitted}, err
	}
	defer permit.finish()
	transaction, err := repository.database.BeginTx(permit.context, nil)
	if err != nil {
		return workspacedefault.CommitResult{Outcome: workspacedefault.CommitNotCommitted},
			fmt.Errorf("begin Workspace Environment default transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	var result sql.Result
	if expected == 0 {
		result, err = transaction.ExecContext(
			permit.context,
			`INSERT INTO workspace_environment_defaults(
			   machine_id, workspace_id, environment_id, revision, updated_at_unix_ms
			 ) VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(machine_id, workspace_id) DO NOTHING`,
			candidate.Key.MachineID.String(), candidate.Key.WorkspaceID.String(),
			candidate.EnvironmentID.String(), int64(candidate.Revision), candidate.UpdatedAt.UnixMilli(),
		)
	} else {
		result, err = transaction.ExecContext(
			permit.context,
			`UPDATE workspace_environment_defaults
			 SET environment_id = ?, revision = ?, updated_at_unix_ms = ?
			 WHERE machine_id = ? AND workspace_id = ? AND revision = ?`,
			candidate.EnvironmentID.String(), int64(candidate.Revision), candidate.UpdatedAt.UnixMilli(),
			candidate.Key.MachineID.String(), candidate.Key.WorkspaceID.String(), int64(expected),
		)
	}
	if err != nil {
		return workspacedefault.CommitResult{Outcome: workspacedefault.CommitNotCommitted},
			fmt.Errorf("write Workspace Environment default: %w", err)
	}
	return repository.finishMutation(permit, transaction, result, expected, candidate, false)
}

func (repository *workspaceDefaultRepository) Delete(
	ctx context.Context,
	key workspacedefault.Key,
	expected uint64,
) (workspacedefault.CommitResult, error) {
	if key.Validate() != nil || expected == 0 || expected > workspacedefault.MaxRevision {
		return workspacedefault.CommitResult{Outcome: workspacedefault.CommitNotCommitted},
			workspacedefault.ErrInvalidDefault
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return workspacedefault.CommitResult{Outcome: workspacedefault.CommitNotCommitted}, err
	}
	defer permit.finish()
	transaction, err := repository.database.BeginTx(permit.context, nil)
	if err != nil {
		return workspacedefault.CommitResult{Outcome: workspacedefault.CommitNotCommitted}, err
	}
	defer func() { _ = transaction.Rollback() }()
	result, err := transaction.ExecContext(
		permit.context,
		`DELETE FROM workspace_environment_defaults
		 WHERE machine_id = ? AND workspace_id = ? AND revision = ?`,
		key.MachineID.String(), key.WorkspaceID.String(), int64(expected),
	)
	if err != nil {
		return workspacedefault.CommitResult{Outcome: workspacedefault.CommitNotCommitted}, err
	}
	return repository.finishMutation(
		permit, transaction, result, expected,
		workspacedefault.Record{Key: key}, true,
	)
}

func (repository *workspaceDefaultRepository) finishMutation(
	permit *operationPermit,
	transaction *sql.Tx,
	result sql.Result,
	expected uint64,
	candidate workspacedefault.Record,
	deleting bool,
) (workspacedefault.CommitResult, error) {
	affected, err := result.RowsAffected()
	if err != nil {
		return workspacedefault.CommitResult{Outcome: workspacedefault.CommitIndeterminate}, err
	}
	if affected != 1 {
		current, exists, loadErr := loadWorkspaceDefault(permit.context, transaction, candidate.Key)
		if loadErr != nil {
			return workspacedefault.CommitResult{Outcome: workspacedefault.CommitIndeterminate}, loadErr
		}
		actual := uint64(0)
		if exists {
			actual = current.Revision
		}
		return workspacedefault.CommitResult{
			Outcome: workspacedefault.CommitConflict, Record: current, Actual: actual,
		}, nil
	}
	commitErr := repository.committer.Commit(transaction)
	if commitErr == nil {
		return workspacedefault.CommitResult{
			Outcome: workspacedefault.CommitCommitted, Record: candidate,
			Actual: candidate.Revision, Deleted: deleting,
		}, nil
	}
	_ = transaction.Rollback()
	reconcileContext, cancel := context.WithTimeout(permit.ownerContext, repository.reconcileTimeout)
	defer cancel()
	current, exists, reconcileErr := loadWorkspaceDefault(
		reconcileContext, repository.database, candidate.Key,
	)
	if reconcileErr != nil {
		return workspacedefault.CommitResult{Outcome: workspacedefault.CommitIndeterminate},
			errors.Join(commitErr, reconcileErr)
	}
	if deleting && !exists {
		return workspacedefault.CommitResult{
			Outcome: workspacedefault.CommitCommitted, Actual: expected, Deleted: true,
		}, nil
	}
	if !deleting && exists && current == candidate {
		return workspacedefault.CommitResult{
			Outcome: workspacedefault.CommitCommitted, Record: current, Actual: current.Revision,
		}, nil
	}
	if exists && current.Revision == expected {
		return workspacedefault.CommitResult{
			Outcome: workspacedefault.CommitNotCommitted, Record: current, Actual: current.Revision,
		}, commitErr
	}
	if !exists && !deleting && expected == 0 {
		return workspacedefault.CommitResult{Outcome: workspacedefault.CommitNotCommitted}, commitErr
	}
	return workspacedefault.CommitResult{
		Outcome: workspacedefault.CommitIndeterminate, Record: current,
		Actual: current.Revision,
	}, commitErr
}

func loadWorkspaceDefault(
	ctx context.Context,
	query interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	key workspacedefault.Key,
) (workspacedefault.Record, bool, error) {
	var environmentID string
	var revision, updatedAt int64
	err := query.QueryRowContext(
		ctx,
		`SELECT environment_id, revision, updated_at_unix_ms
		 FROM workspace_environment_defaults
		 WHERE machine_id = ? AND workspace_id = ?`,
		key.MachineID.String(), key.WorkspaceID.String(),
	).Scan(&environmentID, &revision, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return workspacedefault.Record{}, false, nil
	}
	if err != nil {
		return workspacedefault.Record{}, false, err
	}
	parsedEnvironment, parseErr := environment.NewEnvironmentID(environmentID)
	if parseErr != nil || revision <= 0 || updatedAt <= 0 {
		return workspacedefault.Record{}, false, workspacedefault.ErrInvalidDefault
	}
	record := workspacedefault.Record{
		Key: key, EnvironmentID: parsedEnvironment, Revision: uint64(revision),
		UpdatedAt: time.UnixMilli(updatedAt).UTC(),
	}
	if err := record.Validate(); err != nil {
		return workspacedefault.Record{}, false, err
	}
	return record, true, nil
}
