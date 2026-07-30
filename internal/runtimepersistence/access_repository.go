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

func (r *accessRepository) LoadAll(ctx context.Context) ([]access.Aggregate, error) {
	permit, err := r.operations.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer permit.finish()

	rows, err := r.database.QueryContext(
		permit.context,
		`SELECT
		     binding.access_id,
		     binding.revision,
		     binding.name,
		     binding.description,
		     plan.format_version,
		     plan.payload_json,
		     origin.client_origin,
		     origin.endpoint_authority,
		     origin.agent_endpoint_id
		 FROM access_bindings AS binding
		 LEFT JOIN access_plan_aggregates AS plan
		   ON plan.access_id = binding.access_id
		 LEFT JOIN access_client_origins AS origin
		   ON origin.access_id = binding.access_id
		 ORDER BY binding.access_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("load Access aggregates: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var aggregates []access.Aggregate
	for rows.Next() {
		aggregate, scanErr := scanAccessAggregate(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		aggregates = append(aggregates, aggregate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Access aggregates: %w", err)
	}
	return aggregates, nil
}

func (r *accessRepository) CompareAndSwap(
	ctx context.Context,
	mutation access.Mutation,
) (access.CommitResult, error) {
	if err := mutation.Validate(); err != nil {
		return access.CommitResult{Outcome: access.CommitOutcomeNotCommitted}, err
	}
	encoded, err := encodeAccessAggregatePayload(mutation.Candidate)
	if err != nil {
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

	candidate := mutation.Candidate
	var result sql.Result
	if mutation.ExpectedRevision == 0 {
		result, err = transaction.ExecContext(
			permit.context,
			`INSERT INTO access_bindings (
			     access_id, revision, name, description
			 )
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(access_id) DO NOTHING`,
			candidate.Binding.ID.String(),
			int64(candidate.Binding.Revision),
			candidate.Binding.Name,
			candidate.Binding.Description,
		)
	} else {
		result, err = transaction.ExecContext(
			permit.context,
			`UPDATE access_bindings
			 SET revision = ?, name = ?, description = ?
			 WHERE access_id = ? AND revision = ?`,
			int64(candidate.Binding.Revision),
			candidate.Binding.Name,
			candidate.Binding.Description,
			candidate.Binding.ID.String(),
			int64(mutation.ExpectedRevision),
		)
	}
	if err != nil {
		return access.CommitResult{Outcome: access.CommitOutcomeNotCommitted},
			fmt.Errorf("write Access aggregate root: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return access.CommitResult{Outcome: access.CommitOutcomeNotCommitted},
			fmt.Errorf("read Access write result: %w", err)
	}
	if affected != 1 {
		current, exists, readErr := loadAccessAggregate(
			permit.context,
			transaction,
			candidate.Binding.ID,
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
			Aggregate:      current,
			ActualRevision: current.Binding.Revision,
		}, nil
	}

	if _, err := transaction.ExecContext(
		permit.context,
		`INSERT INTO access_plan_aggregates (
		     access_id, format_version, payload_json
		 )
		 VALUES (?, ?, ?)
		 ON CONFLICT(access_id) DO UPDATE SET
		   format_version = excluded.format_version,
		   payload_json = excluded.payload_json`,
		candidate.Binding.ID.String(),
		accessAggregateFormatVersion,
		encoded,
	); err != nil {
		return access.CommitResult{Outcome: access.CommitOutcomeNotCommitted},
			fmt.Errorf("write Access aggregate payload: %w", err)
	}
	if _, err := transaction.ExecContext(
		permit.context,
		`INSERT INTO access_client_origins (
		     access_id, client_origin, endpoint_authority, agent_endpoint_id
		 )
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(access_id) DO UPDATE SET
		   client_origin = excluded.client_origin,
		   endpoint_authority = excluded.endpoint_authority,
		   agent_endpoint_id = excluded.agent_endpoint_id`,
		candidate.Binding.ID.String(),
		candidate.AgentEndpoint.ClientOrigin.String(),
		candidate.AgentEndpoint.ClientOrigin.EndpointAuthority(),
		candidate.AgentEndpoint.ID.String(),
	); err != nil {
		return access.CommitResult{Outcome: access.CommitOutcomeNotCommitted},
			fmt.Errorf("write Access ClientOrigin identity: %w", err)
	}

	commitErr := r.committer.Commit(transaction)
	if commitErr == nil {
		return access.CommitResult{
			Outcome:        access.CommitOutcomeCommitted,
			Aggregate:      candidate.Clone(),
			ActualRevision: candidate.Binding.Revision,
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
	current, exists, reconcileErr := loadAccessAggregate(
		reconcileContext,
		r.database,
		candidate.Binding.ID,
	)
	if reconcileErr != nil {
		return access.CommitResult{
				Outcome: access.CommitOutcomeIndeterminate,
			}, errors.Join(
				fmt.Errorf("commit Access transaction: %w", commitErr),
				fmt.Errorf("reconcile Access commit: %w", reconcileErr),
			)
	}
	if exists && current.Equal(candidate) {
		return access.CommitResult{
			Outcome:        access.CommitOutcomeCommitted,
			Aggregate:      current,
			ActualRevision: current.Binding.Revision,
		}, nil
	}
	if !exists || current.Binding.Revision == mutation.ExpectedRevision {
		result := access.CommitResult{
			Outcome: access.CommitOutcomeNotCommitted,
		}
		if exists {
			result.Aggregate = current
			result.ActualRevision = current.Binding.Revision
		}
		return result, fmt.Errorf("commit Access transaction: %w", commitErr)
	}
	return access.CommitResult{
			Outcome:        access.CommitOutcomeIndeterminate,
			Aggregate:      current,
			ActualRevision: current.Binding.Revision,
		}, fmt.Errorf(
			"commit Access transaction has divergent durable state: %w",
			commitErr,
		)
}

type accessRow interface {
	Scan(...any) error
}

type accessRowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadAccessAggregate(
	ctx context.Context,
	querier accessRowQuerier,
	accessID access.AccessID,
) (access.Aggregate, bool, error) {
	aggregate, err := scanAccessAggregate(querier.QueryRowContext(
		ctx,
		`SELECT
		     binding.access_id,
		     binding.revision,
		     binding.name,
		     binding.description,
		     plan.format_version,
		     plan.payload_json,
		     origin.client_origin,
		     origin.endpoint_authority,
		     origin.agent_endpoint_id
		 FROM access_bindings AS binding
		 LEFT JOIN access_plan_aggregates AS plan
		   ON plan.access_id = binding.access_id
		 LEFT JOIN access_client_origins AS origin
		   ON origin.access_id = binding.access_id
		 WHERE binding.access_id = ?`,
		accessID.String(),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return access.Aggregate{}, false, nil
	}
	if err != nil {
		return access.Aggregate{}, false, err
	}
	return aggregate, true, nil
}

func scanAccessAggregate(row accessRow) (access.Aggregate, error) {
	var (
		accessIDText string
		revision     int64
		name         string
		description  string
		format       sql.NullInt64
		encoded      []byte
		clientOrigin sql.NullString
		authority    sql.NullString
		endpointID   sql.NullString
	)
	if err := row.Scan(
		&accessIDText,
		&revision,
		&name,
		&description,
		&format,
		&encoded,
		&clientOrigin,
		&authority,
		&endpointID,
	); err != nil {
		return access.Aggregate{}, err
	}
	if !format.Valid || len(encoded) == 0 {
		return access.Aggregate{}, fmt.Errorf(
			"%w: accessId=%q has no executable plan payload",
			access.ErrInvalidRepositoryState,
			accessIDText,
		)
	}
	aggregate, err := decodeAccessAggregate(
		accessIDText,
		revision,
		name,
		description,
		format.Int64,
		encoded,
	)
	if err != nil {
		return access.Aggregate{}, err
	}
	if !clientOrigin.Valid ||
		!endpointID.Valid ||
		clientOrigin.String != aggregate.AgentEndpoint.ClientOrigin.String() ||
		authority.String != aggregate.AgentEndpoint.ClientOrigin.EndpointAuthority() ||
		endpointID.String != aggregate.AgentEndpoint.ID.String() {
		return access.Aggregate{}, fmt.Errorf(
			"%w: accessId=%q ClientOrigin identity does not match aggregate payload",
			access.ErrInvalidRepositoryState,
			accessIDText,
		)
	}
	return aggregate, nil
}
