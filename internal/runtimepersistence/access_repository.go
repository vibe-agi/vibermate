package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
)

type transactionCommitter interface {
	Commit(*sql.Tx) error
}

type sqlTransactionCommitter struct{}

func (sqlTransactionCommitter) Commit(transaction *sql.Tx) error {
	return transaction.Commit()
}

type accessRepository struct {
	database         *sql.DB
	operations       *operationGate
	reconcileTimeout time.Duration
	committer        transactionCommitter
}

var _ access.Repository = (*accessRepository)(nil)

func newAccessRepository(
	database *sql.DB,
	operations *operationGate,
	reconcileTimeout time.Duration,
	committer transactionCommitter,
) *accessRepository {
	return &accessRepository{
		database:         database,
		operations:       operations,
		reconcileTimeout: reconcileTimeout,
		committer:        committer,
	}
}

func (r *accessRepository) LoadAll(ctx context.Context) ([]access.Record, error) {
	permit, err := r.operations.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer permit.finish()

	rows, err := r.database.QueryContext(
		permit.context,
		`SELECT access_id, revision, name, description
		 FROM access_bindings
		 ORDER BY access_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("load Access aggregates: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var records []access.Record
	for rows.Next() {
		record, scanErr := scanAccessRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Access aggregates: %w", err)
	}
	return records, nil
}

func (r *accessRepository) CompareAndSwap(
	ctx context.Context,
	mutation access.Mutation,
) (access.CommitResult, error) {
	if err := mutation.Validate(); err != nil {
		return access.CommitResult{Outcome: access.CommitOutcomeNotCommitted}, err
	}

	permit, err := r.operations.admit(ctx)
	if err != nil {
		return access.CommitResult{Outcome: access.CommitOutcomeNotCommitted}, err
	}
	defer permit.finish()

	transaction, err := r.database.BeginTx(permit.context, nil)
	if err != nil {
		return access.CommitResult{Outcome: access.CommitOutcomeNotCommitted},
			fmt.Errorf("begin Access transaction: %w", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	var result sql.Result
	if mutation.ExpectedRevision == 0 {
		result, err = transaction.ExecContext(
			permit.context,
			`INSERT INTO access_bindings (
			     access_id, revision, name, description
			 )
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(access_id) DO NOTHING`,
			mutation.Candidate.AccessID.String(),
			int64(mutation.Candidate.Revision),
			mutation.Candidate.Binding.Name,
			mutation.Candidate.Binding.Description,
		)
	} else {
		result, err = transaction.ExecContext(
			permit.context,
			`UPDATE access_bindings
			 SET revision = ?, name = ?, description = ?
			 WHERE access_id = ? AND revision = ?`,
			int64(mutation.Candidate.Revision),
			mutation.Candidate.Binding.Name,
			mutation.Candidate.Binding.Description,
			mutation.Candidate.AccessID.String(),
			int64(mutation.ExpectedRevision),
		)
	}
	if err != nil {
		return access.CommitResult{Outcome: access.CommitOutcomeNotCommitted},
			fmt.Errorf("write Access aggregate: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return access.CommitResult{Outcome: access.CommitOutcomeNotCommitted},
			fmt.Errorf("read Access write result: %w", err)
	}
	if affected != 1 {
		current, exists, readErr := loadAccessRecord(
			permit.context,
			transaction,
			mutation.Candidate.AccessID,
		)
		if readErr != nil {
			return access.CommitResult{Outcome: access.CommitOutcomeNotCommitted},
				fmt.Errorf("read Access CAS state: %w", readErr)
		}
		if !exists {
			return access.CommitResult{
				Outcome: access.CommitOutcomeNotConfigured,
			}, nil
		}
		return access.CommitResult{
			Outcome:        access.CommitOutcomeConflict,
			Record:         current,
			ActualRevision: current.Revision,
		}, nil
	}

	commitErr := r.committer.Commit(transaction)
	if commitErr == nil {
		return access.CommitResult{
			Outcome:        access.CommitOutcomeCommitted,
			Record:         mutation.Candidate,
			ActualRevision: mutation.Candidate.Revision,
		}, nil
	}

	// Commit errors can be ambiguous. Release the transaction connection, then
	// reconcile against SQLite while the operation admission remains held.
	_ = transaction.Rollback()
	reconcileContext, cancelReconcile := context.WithTimeout(
		permit.ownerContext,
		r.reconcileTimeout,
	)
	defer cancelReconcile()
	current, exists, reconcileErr := loadAccessRecord(
		reconcileContext,
		r.database,
		mutation.Candidate.AccessID,
	)
	if reconcileErr != nil {
		return access.CommitResult{
				Outcome: access.CommitOutcomeIndeterminate,
			}, errors.Join(
				fmt.Errorf("commit Access transaction: %w", commitErr),
				fmt.Errorf("reconcile Access commit: %w", reconcileErr),
			)
	}
	if exists && current == mutation.Candidate {
		return access.CommitResult{
			Outcome:        access.CommitOutcomeCommitted,
			Record:         current,
			ActualRevision: current.Revision,
		}, nil
	}
	if !exists || current.Revision == mutation.ExpectedRevision {
		return access.CommitResult{
			Outcome:        access.CommitOutcomeNotCommitted,
			Record:         current,
			ActualRevision: current.Revision,
		}, fmt.Errorf("commit Access transaction: %w", commitErr)
	}
	if current.Revision != mutation.Candidate.Revision {
		return access.CommitResult{
				Outcome:        access.CommitOutcomeIndeterminate,
				Record:         current,
				ActualRevision: current.Revision,
			}, fmt.Errorf(
				"commit Access transaction has divergent durable revision: %w",
				commitErr,
			)
	}
	return access.CommitResult{
		Outcome:        access.CommitOutcomeConflict,
		Record:         current,
		ActualRevision: current.Revision,
	}, fmt.Errorf("commit Access transaction: %w", commitErr)
}

type accessRow interface {
	Scan(...any) error
}

type accessRowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadAccessRecord(
	ctx context.Context,
	querier accessRowQuerier,
	accessID access.AccessID,
) (access.Record, bool, error) {
	record, err := scanAccessRecord(querier.QueryRowContext(
		ctx,
		`SELECT access_id, revision, name, description
		 FROM access_bindings
		 WHERE access_id = ?`,
		accessID.String(),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return access.Record{}, false, nil
	}
	if err != nil {
		return access.Record{}, false, err
	}
	return record, true, nil
}

func scanAccessRecord(row accessRow) (access.Record, error) {
	var (
		accessIDText string
		revision     int64
		name         string
		description  string
	)
	if err := row.Scan(&accessIDText, &revision, &name, &description); err != nil {
		return access.Record{}, err
	}
	accessID, err := access.NewAccessID(accessIDText)
	if err != nil {
		return access.Record{}, fmt.Errorf(
			"%w: decode Access ID: %w",
			access.ErrInvalidRepositoryState,
			err,
		)
	}
	if revision <= 0 {
		return access.Record{}, fmt.Errorf(
			"%w: accessId=%q revision=%d",
			access.ErrInvalidRepositoryState,
			accessIDText,
			revision,
		)
	}
	record := access.Record{
		AccessID: accessID,
		Revision: access.Revision(revision),
		Binding: access.Binding{
			Name:        name,
			Description: description,
		},
	}
	if err := record.Validate(); err != nil {
		return access.Record{}, fmt.Errorf(
			"%w: accessId=%q: %w",
			access.ErrInvalidRepositoryState,
			accessIDText,
			err,
		)
	}
	return record, nil
}
