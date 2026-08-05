package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
)

func (r *accessRepository) InspectDeletion(
	ctx context.Context,
	accessID access.AccessID,
	observedAt time.Time,
) (access.DeletionInspection, bool, error) {
	validatedID, err := access.NewAccessID(accessID.String())
	if err != nil || observedAt.IsZero() {
		return access.DeletionInspection{}, false, access.ErrInvalidAccess
	}
	permit, err := r.operations.admit(ctx)
	if err != nil {
		return access.DeletionInspection{}, false, err
	}
	defer permit.finish()
	inspection, exists, err := inspectAccessDeletion(
		permit.context,
		r.database,
		validatedID,
		observedAt,
	)
	if err != nil {
		return access.DeletionInspection{}, false, fmt.Errorf(
			"inspect Access deletion accessId=%q: %w",
			validatedID.String(),
			err,
		)
	}
	return inspection, exists, nil
}

func (r *accessRepository) Delete(
	ctx context.Context,
	mutation access.DeleteMutation,
) (access.DeleteResult, error) {
	if err := mutation.Validate(); err != nil {
		return access.DeleteResult{Outcome: access.DeleteOutcomeNotCommitted}, err
	}
	permit, err := r.operations.admit(ctx)
	if err != nil {
		return access.DeleteResult{Outcome: access.DeleteOutcomeNotCommitted}, err
	}
	defer permit.finish()

	transaction, err := r.database.BeginTx(permit.context, nil)
	if err != nil {
		return access.DeleteResult{Outcome: access.DeleteOutcomeNotCommitted},
			fmt.Errorf("begin Access deletion transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	inspection, exists, err := inspectAccessDeletion(
		permit.context,
		transaction,
		mutation.AccessID,
		mutation.DeletedAt,
	)
	if err != nil {
		return access.DeleteResult{Outcome: access.DeleteOutcomeNotCommitted},
			fmt.Errorf("inspect Access deletion transaction: %w", err)
	}
	if !exists {
		retired, revision, retiredErr := loadAccessTombstone(
			permit.context,
			transaction,
			mutation.AccessID,
		)
		if retiredErr != nil {
			return access.DeleteResult{Outcome: access.DeleteOutcomeNotCommitted},
				fmt.Errorf("read Access tombstone: %w", retiredErr)
		}
		if retired {
			return access.DeleteResult{
				Outcome:  access.DeleteOutcomeRetired,
				Revision: revision,
			}, nil
		}
		return access.DeleteResult{Outcome: access.DeleteOutcomeNotConfigured}, nil
	}
	actualRevision := inspection.Aggregate.Binding.Revision
	if actualRevision != mutation.ExpectedRevision {
		return access.DeleteResult{
			Outcome:  access.DeleteOutcomeConflict,
			Revision: actualRevision,
		}, nil
	}
	if inspection.Aggregate.Binding.Status != access.AccessStatusDisabled {
		return access.DeleteResult{
			Outcome:  access.DeleteOutcomeBlocked,
			Revision: actualRevision,
		}, nil
	}
	impact, err := inspection.RepositoryImpactToken()
	if err != nil {
		return access.DeleteResult{Outcome: access.DeleteOutcomeNotCommitted}, err
	}
	if impact != mutation.ExpectedRepositoryImpact {
		return access.DeleteResult{
			Outcome:  access.DeleteOutcomeImpactChanged,
			Revision: actualRevision,
		}, nil
	}
	activeRuns := 0
	for _, reference := range inspection.WorkspaceReferences {
		activeRuns += len(reference.ActiveCaptureRunIDs)
	}
	if activeRuns != 0 || len(inspection.ProxyClientReferences) != 0 ||
		(len(inspection.WorkspaceReferences) != 0 &&
			!mutation.RetireWorkspaceBindings) {
		return access.DeleteResult{
			Outcome:  access.DeleteOutcomeBlocked,
			Revision: actualRevision,
		}, nil
	}
	if mutation.RetireWorkspaceBindings {
		if _, err := transaction.ExecContext(
			permit.context,
			`DELETE FROM workspace_route_bindings WHERE access_id = ?`,
			mutation.AccessID.String(),
		); err != nil {
			return access.DeleteResult{Outcome: access.DeleteOutcomeNotCommitted},
				fmt.Errorf("retire Access workspace routes: %w", err)
		}
	}
	result, err := transaction.ExecContext(
		permit.context,
		`INSERT INTO access_tombstones (
		     access_id, last_revision, name, deleted_at_unix_ms
		 ) VALUES (?, ?, ?, ?)
		 ON CONFLICT(access_id) DO NOTHING`,
		mutation.AccessID.String(),
		int64(actualRevision),
		inspection.Aggregate.Binding.Name,
		toUnixMillis(mutation.DeletedAt),
	)
	if err != nil {
		return access.DeleteResult{Outcome: access.DeleteOutcomeNotCommitted},
			fmt.Errorf("write Access tombstone: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return access.DeleteResult{Outcome: access.DeleteOutcomeIndeterminate},
			fmt.Errorf("Access tombstone write was not unique")
	}
	result, err = transaction.ExecContext(
		permit.context,
		`DELETE FROM access_bindings WHERE access_id = ? AND revision = ?`,
		mutation.AccessID.String(),
		int64(mutation.ExpectedRevision),
	)
	if err != nil {
		return access.DeleteResult{Outcome: access.DeleteOutcomeNotCommitted},
			fmt.Errorf("delete Access aggregate: %w", err)
	}
	affected, err = result.RowsAffected()
	if err != nil || affected != 1 {
		return access.DeleteResult{Outcome: access.DeleteOutcomeIndeterminate},
			fmt.Errorf("Access aggregate changed during deletion")
	}

	commitErr := r.committer.Commit(transaction)
	if commitErr == nil {
		return access.DeleteResult{
			Outcome:  access.DeleteOutcomeCommitted,
			Revision: actualRevision,
		}, nil
	}

	_ = transaction.Rollback()
	reconcileContext, cancelReconcile := context.WithTimeout(
		permit.ownerContext,
		r.reconcileTimeout,
	)
	defer cancelReconcile()
	retired, tombstoneRevision, reconcileErr := loadAccessTombstone(
		reconcileContext,
		r.database,
		mutation.AccessID,
	)
	if reconcileErr != nil {
		return access.DeleteResult{Outcome: access.DeleteOutcomeIndeterminate},
			errors.Join(
				fmt.Errorf("commit Access deletion: %w", commitErr),
				fmt.Errorf("reconcile Access deletion: %w", reconcileErr),
			)
	}
	current, stillExists, loadErr := loadAccessAggregate(
		reconcileContext,
		r.database,
		mutation.AccessID,
	)
	if loadErr != nil {
		return access.DeleteResult{Outcome: access.DeleteOutcomeIndeterminate},
			errors.Join(
				fmt.Errorf("commit Access deletion: %w", commitErr),
				fmt.Errorf("reconcile Access aggregate: %w", loadErr),
			)
	}
	if retired && tombstoneRevision == actualRevision && !stillExists {
		return access.DeleteResult{
			Outcome:  access.DeleteOutcomeCommitted,
			Revision: actualRevision,
		}, nil
	}
	if !retired && stillExists && current.Binding.Revision == actualRevision {
		return access.DeleteResult{
			Outcome:  access.DeleteOutcomeNotCommitted,
			Revision: actualRevision,
		}, fmt.Errorf("commit Access deletion: %w", commitErr)
	}
	return access.DeleteResult{
		Outcome:  access.DeleteOutcomeIndeterminate,
		Revision: actualRevision,
	}, fmt.Errorf("commit Access deletion has divergent durable state: %w", commitErr)
}

type accessDeletionQuerier interface {
	accessRowQuerier
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func inspectAccessDeletion(
	ctx context.Context,
	querier accessDeletionQuerier,
	accessID access.AccessID,
	observedAt time.Time,
) (access.DeletionInspection, bool, error) {
	aggregate, exists, err := loadAccessAggregate(ctx, querier, accessID)
	if err != nil || !exists {
		return access.DeletionInspection{}, exists, err
	}
	rows, err := querier.QueryContext(
		ctx,
		`SELECT
		   binding.binding_id,
		   binding.revision,
		   binding.workspace_label,
		   run.run_id
		 FROM workspace_route_bindings AS binding
		 LEFT JOIN capture_runs AS run
		   ON run.machine_id = binding.machine_id
		  AND run.workspace_id = binding.workspace_id
		  AND run.state IN ('created', 'attached')
		  AND run.expires_at_unix_ms > ?
		 WHERE binding.access_id = ?
		 ORDER BY binding.binding_id, run.run_id`,
		toUnixMillis(observedAt),
		accessID.String(),
	)
	if err != nil {
		return access.DeletionInspection{}, false, err
	}
	defer func() { _ = rows.Close() }()
	references := make([]access.DeletionWorkspaceReference, 0)
	for rows.Next() {
		var (
			bindingID      string
			revision       uint64
			workspaceLabel string
			runID          sql.NullString
		)
		if err := rows.Scan(
			&bindingID,
			&revision,
			&workspaceLabel,
			&runID,
		); err != nil {
			return access.DeletionInspection{}, false, err
		}
		if len(references) == 0 ||
			references[len(references)-1].BindingID != bindingID {
			references = append(references, access.DeletionWorkspaceReference{
				BindingID:      bindingID,
				Revision:       revision,
				WorkspaceLabel: workspaceLabel,
			})
		}
		if runID.Valid {
			last := &references[len(references)-1]
			last.ActiveCaptureRunIDs = append(last.ActiveCaptureRunIDs, runID.String)
		}
	}
	if err := rows.Err(); err != nil {
		return access.DeletionInspection{}, false, err
	}
	if err := rows.Close(); err != nil {
		return access.DeletionInspection{}, false, err
	}
	profileIDs := make(map[string]struct{}, len(aggregate.Profiles))
	for _, profile := range aggregate.Profiles {
		profileIDs[profile.ID.String()] = struct{}{}
	}
	proxyRows, err := querier.QueryContext(
		ctx,
		`SELECT `+proxyClientBindingColumns+`
		 FROM proxy_client_bindings
		 ORDER BY binding_id`,
	)
	if err != nil {
		return access.DeletionInspection{}, false, err
	}
	defer func() { _ = proxyRows.Close() }()
	proxyReferences := make([]access.DeletionProxyClientReference, 0)
	for proxyRows.Next() {
		record, scanErr := scanProxyClientBinding(proxyRows)
		if scanErr != nil {
			return access.DeletionInspection{}, false, scanErr
		}
		for _, profileID := range record.Policy.AllowedProfileIDs() {
			if _, referenced := profileIDs[profileID]; !referenced {
				continue
			}
			proxyReferences = append(
				proxyReferences,
				access.DeletionProxyClientReference{
					BindingID: record.ID.String(),
					Revision:  uint64(record.Revision),
				},
			)
			break
		}
	}
	if err := proxyRows.Err(); err != nil {
		return access.DeletionInspection{}, false, err
	}
	inspection := access.DeletionInspection{
		Aggregate:             aggregate,
		WorkspaceReferences:   references,
		ProxyClientReferences: proxyReferences,
	}
	if err := inspection.Validate(); err != nil {
		return access.DeletionInspection{}, false, err
	}
	return inspection, true, nil
}

func accessIdentityRetired(
	ctx context.Context,
	querier accessRowQuerier,
	accessID access.AccessID,
) (bool, error) {
	retired, _, err := loadAccessTombstone(ctx, querier, accessID)
	return retired, err
}

func loadAccessTombstone(
	ctx context.Context,
	querier accessRowQuerier,
	accessID access.AccessID,
) (bool, access.Revision, error) {
	var revision uint64
	err := querier.QueryRowContext(
		ctx,
		`SELECT last_revision FROM access_tombstones WHERE access_id = ?`,
		accessID.String(),
	).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	if revision == 0 || revision > uint64(access.MaxRevision) {
		return false, 0, access.ErrInvalidRepositoryState
	}
	return true, access.Revision(revision), nil
}
