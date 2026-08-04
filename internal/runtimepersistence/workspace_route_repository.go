package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
	"github.com/vibe-agi/vibermate/internal/workspaceroute"
)

const workspaceRouteColumns = `
    binding_id,
    access_id,
    machine_id,
    workspace_id,
	 machine_registration_revision,
	 workspace_label,
	 workspace_evidence,
    profile_id,
    revision,
    updated_at_unix_ms`

type workspaceRouteRepository struct {
	database   *sql.DB
	operations *operationGate
}

var _ workspaceroute.Repository = (*workspaceRouteRepository)(nil)

func newWorkspaceRouteRepository(
	database *sql.DB,
	operations *operationGate,
) *workspaceRouteRepository {
	return &workspaceRouteRepository{database: database, operations: operations}
}

func (repository *workspaceRouteRepository) ResolveOrCreate(
	ctx context.Context,
	request workspaceroute.CreateRequest,
) (workspaceroute.Record, error) {
	candidate, err := request.Record()
	if err != nil {
		return workspaceroute.Record{}, err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return workspaceroute.Record{}, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return workspaceroute.Record{}, fmt.Errorf("begin workspace route creation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(
		operation,
		`INSERT INTO workspace_route_bindings (
		     binding_id,
		     access_id,
		     machine_id,
		     workspace_id,
		     machine_registration_revision,
		     workspace_label,
		     workspace_evidence,
		     profile_id,
		     revision,
		     updated_at_unix_ms
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
		 ON CONFLICT(access_id, machine_id, workspace_id) DO NOTHING`,
		candidate.ID.String(),
		candidate.AccessID.String(),
		candidate.MachineID.String(),
		candidate.WorkspaceID.String(),
		int64(candidate.MachineRegistrationRevision),
		candidate.WorkspaceLabel,
		string(candidate.WorkspaceEvidence),
		candidate.ProfileID.String(),
		toUnixMillis(candidate.UpdatedAt),
	); err != nil {
		return workspaceroute.Record{}, fmt.Errorf("insert workspace route binding: %w", err)
	}
	record, err := scanWorkspaceRoute(transaction.QueryRowContext(
		operation,
		`SELECT `+workspaceRouteColumns+`
		 FROM workspace_route_bindings
		 WHERE access_id = ? AND machine_id = ? AND workspace_id = ?`,
		candidate.AccessID.String(),
		candidate.MachineID.String(),
		candidate.WorkspaceID.String(),
	))
	if err != nil {
		return workspaceroute.Record{}, fmt.Errorf("read workspace route binding: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return workspaceroute.Record{}, fmt.Errorf("commit workspace route creation: %w", err)
	}
	return record, nil
}

func (repository *workspaceRouteRepository) Get(
	ctx context.Context,
	id workspaceroute.BindingID,
) (workspaceroute.Record, error) {
	if _, err := workspaceroute.ParseBindingID(id.String()); err != nil {
		return workspaceroute.Record{}, err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return workspaceroute.Record{}, err
	}
	defer finish()
	record, err := scanWorkspaceRoute(repository.database.QueryRowContext(
		operation,
		`SELECT `+workspaceRouteColumns+`
		 FROM workspace_route_bindings
		 WHERE binding_id = ?`,
		id.String(),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return workspaceroute.Record{}, workspaceroute.ErrBindingNotFound
	}
	if err != nil {
		return workspaceroute.Record{}, fmt.Errorf("get workspace route binding: %w", err)
	}
	return record, nil
}

func (repository *workspaceRouteRepository) CompareAndSwap(
	ctx context.Context,
	id workspaceroute.BindingID,
	expected workspaceroute.Revision,
	profileID access.EndpointProfileID,
	updatedAt time.Time,
) (workspaceroute.Record, error) {
	if _, err := workspaceroute.ParseBindingID(id.String()); err != nil ||
		!expected.Valid() || profileID.String() == "" || updatedAt.IsZero() {
		return workspaceroute.Record{}, workspaceroute.ErrInvalidBinding
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return workspaceroute.Record{}, err
	}
	defer finish()
	transaction, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return workspaceroute.Record{}, fmt.Errorf("begin workspace route update: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	result, err := transaction.ExecContext(
		operation,
		`UPDATE workspace_route_bindings
		 SET profile_id = ?, revision = revision + 1, updated_at_unix_ms = ?
		 WHERE binding_id = ? AND revision = ? AND revision < 9223372036854775807`,
		profileID.String(),
		toUnixMillis(updatedAt),
		id.String(),
		int64(expected),
	)
	if err != nil {
		return workspaceroute.Record{}, fmt.Errorf("update workspace route binding: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return workspaceroute.Record{}, fmt.Errorf("read workspace route update result: %w", err)
	}
	if affected != 1 {
		return workspaceroute.Record{}, workspaceroute.ErrRevisionConflict
	}
	record, err := scanWorkspaceRoute(transaction.QueryRowContext(
		operation,
		`SELECT `+workspaceRouteColumns+`
		 FROM workspace_route_bindings WHERE binding_id = ?`,
		id.String(),
	))
	if err != nil {
		return workspaceroute.Record{}, fmt.Errorf("read updated workspace route binding: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return workspaceroute.Record{}, fmt.Errorf("commit workspace route update: %w", err)
	}
	return record, nil
}

func (repository *workspaceRouteRepository) List(
	ctx context.Context,
	request workspaceroute.PageRequest,
) ([]workspaceroute.Record, error) {
	request = request.Normalized()
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	rows, err := repository.database.QueryContext(
		operation,
		`SELECT `+workspaceRouteColumns+`
		 FROM workspace_route_bindings
		 ORDER BY updated_at_unix_ms DESC, binding_id ASC
		 LIMIT ?`,
		request.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list workspace route bindings: %w", err)
	}
	defer rows.Close()
	records := make([]workspaceroute.Record, 0)
	for rows.Next() {
		record, scanErr := scanWorkspaceRoute(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan workspace route binding: %w", scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace route bindings: %w", err)
	}
	return records, nil
}

type workspaceRouteScanner interface {
	Scan(...any) error
}

func scanWorkspaceRoute(scanner workspaceRouteScanner) (workspaceroute.Record, error) {
	var bindingID, accessID, machineID, workspaceID, profileID string
	var workspaceLabel, workspaceEvidence string
	var machineRegistrationRevision int64
	var revision int64
	var updatedAtMillis int64
	if err := scanner.Scan(
		&bindingID,
		&accessID,
		&machineID,
		&workspaceID,
		&machineRegistrationRevision,
		&workspaceLabel,
		&workspaceEvidence,
		&profileID,
		&revision,
		&updatedAtMillis,
	); err != nil {
		return workspaceroute.Record{}, err
	}
	parsedBindingID, err := workspaceroute.ParseBindingID(bindingID)
	if err != nil {
		return workspaceroute.Record{}, err
	}
	parsedAccessID, err := access.NewAccessID(accessID)
	if err != nil {
		return workspaceroute.Record{}, err
	}
	parsedMachineID, err := workspaceidentity.ParseMachineID(machineID)
	if err != nil {
		return workspaceroute.Record{}, err
	}
	parsedWorkspaceID, err := workspaceidentity.ParseWorkspaceID(workspaceID)
	if err != nil {
		return workspaceroute.Record{}, err
	}
	parsedProfileID, err := access.NewEndpointProfileID(profileID)
	if err != nil {
		return workspaceroute.Record{}, err
	}
	record := workspaceroute.Record{
		ID:                          parsedBindingID,
		AccessID:                    parsedAccessID,
		MachineID:                   parsedMachineID,
		WorkspaceID:                 parsedWorkspaceID,
		MachineRegistrationRevision: uint64(machineRegistrationRevision),
		WorkspaceLabel:              workspaceLabel,
		WorkspaceEvidence:           workspaceidentity.Evidence(workspaceEvidence),
		ProfileID:                   parsedProfileID,
		Revision:                    workspaceroute.Revision(revision),
		UpdatedAt:                   fromUnixMillis(updatedAtMillis),
	}
	return record, record.Validate()
}
