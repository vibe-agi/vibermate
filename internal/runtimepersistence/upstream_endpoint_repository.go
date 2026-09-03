package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type upstreamEndpointRepository struct {
	database         *sql.DB
	operations       *operationGate
	reconcileTimeout time.Duration
	committer        transactionCommitter
}

var _ upstreamendpoint.Repository = (*upstreamEndpointRepository)(nil)

func newUpstreamEndpointRepository(
	database *sql.DB,
	operations *operationGate,
	reconcileTimeout time.Duration,
	committer transactionCommitter,
) *upstreamEndpointRepository {
	return &upstreamEndpointRepository{
		database: database, operations: operations,
		reconcileTimeout: reconcileTimeout, committer: committer,
	}
}

func (repository *upstreamEndpointRepository) LoadAll(
	ctx context.Context,
) ([]upstreamendpoint.Endpoint, error) {
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer permit.finish()
	rows, err := repository.database.QueryContext(
		permit.context,
		`SELECT endpoint_id, display_name, origin, realm_id,
		        backend_protocols_json, capabilities_json, drivers_json,
		        state, revision, created_at_unix_ms, updated_at_unix_ms
		 FROM upstream_endpoints ORDER BY endpoint_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list UpstreamEndpoints: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]upstreamendpoint.Endpoint, 0)
	for rows.Next() {
		item, scanErr := scanUpstreamEndpoint(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate UpstreamEndpoints: %w", err)
	}
	return items, nil
}

func (repository *upstreamEndpointRepository) Load(
	ctx context.Context,
	id upstreamendpoint.ID,
) (upstreamendpoint.Endpoint, bool, error) {
	if _, err := upstreamendpoint.NewID(id.String()); err != nil {
		return upstreamendpoint.Endpoint{}, false, err
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return upstreamendpoint.Endpoint{}, false, err
	}
	defer permit.finish()
	return loadUpstreamEndpoint(permit.context, repository.database, id)
}

func (repository *upstreamEndpointRepository) Write(
	ctx context.Context,
	expected uint64,
	candidate upstreamendpoint.Endpoint,
) (upstreamendpoint.CommitResult, error) {
	if candidate.Validate() != nil || expected >= upstreamendpoint.MaxRevision ||
		candidate.Revision != expected+1 {
		return upstreamendpoint.CommitResult{Outcome: upstreamendpoint.CommitNotCommitted},
			upstreamendpoint.ErrInvalidEndpoint
	}
	protocols, capabilities, drivers, err := encodeEndpointCollections(candidate)
	if err != nil {
		return upstreamendpoint.CommitResult{Outcome: upstreamendpoint.CommitNotCommitted}, err
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return upstreamendpoint.CommitResult{Outcome: upstreamendpoint.CommitNotCommitted}, err
	}
	defer permit.finish()
	transaction, err := repository.database.BeginTx(permit.context, nil)
	if err != nil {
		return upstreamendpoint.CommitResult{Outcome: upstreamendpoint.CommitNotCommitted},
			fmt.Errorf("begin UpstreamEndpoint transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	var result sql.Result
	if expected == 0 {
		result, err = transaction.ExecContext(
			permit.context,
			`INSERT INTO upstream_endpoints(
			   endpoint_id, display_name, origin, realm_id,
			   backend_protocols_json, capabilities_json, drivers_json,
			   state, revision, created_at_unix_ms, updated_at_unix_ms
			 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(endpoint_id) DO NOTHING`,
			candidate.ID.String(), candidate.DisplayName, candidate.Origin.String(), candidate.RealmID,
			protocols, capabilities, drivers, string(candidate.State), int64(candidate.Revision),
			candidate.CreatedAt.UnixMilli(), candidate.UpdatedAt.UnixMilli(),
		)
	} else {
		result, err = transaction.ExecContext(
			permit.context,
			`UPDATE upstream_endpoints
			 SET display_name = ?, origin = ?, realm_id = ?, backend_protocols_json = ?,
			     capabilities_json = ?, drivers_json = ?, state = ?, revision = ?,
			     updated_at_unix_ms = ?
			 WHERE endpoint_id = ? AND revision = ? AND created_at_unix_ms = ?`,
			candidate.DisplayName, candidate.Origin.String(), candidate.RealmID, protocols,
			capabilities, drivers, string(candidate.State), int64(candidate.Revision),
			candidate.UpdatedAt.UnixMilli(), candidate.ID.String(), int64(expected),
			candidate.CreatedAt.UnixMilli(),
		)
	}
	if err != nil {
		return upstreamendpoint.CommitResult{Outcome: upstreamendpoint.CommitNotCommitted},
			fmt.Errorf("write UpstreamEndpoint: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return upstreamendpoint.CommitResult{Outcome: upstreamendpoint.CommitIndeterminate}, err
	}
	if affected != 1 {
		current, exists, loadErr := loadUpstreamEndpoint(permit.context, transaction, candidate.ID)
		if loadErr != nil {
			return upstreamendpoint.CommitResult{Outcome: upstreamendpoint.CommitIndeterminate}, loadErr
		}
		actual := uint64(0)
		if exists {
			actual = current.Revision
		}
		return upstreamendpoint.CommitResult{
			Outcome: upstreamendpoint.CommitConflict, Endpoint: current, Actual: actual,
		}, nil
	}
	commitErr := repository.committer.Commit(transaction)
	if commitErr == nil {
		return upstreamendpoint.CommitResult{
			Outcome: upstreamendpoint.CommitCommitted, Endpoint: candidate.Clone(), Actual: candidate.Revision,
		}, nil
	}
	_ = transaction.Rollback()
	reconcileContext, cancel := context.WithTimeout(permit.ownerContext, repository.reconcileTimeout)
	defer cancel()
	current, exists, reconcileErr := loadUpstreamEndpoint(reconcileContext, repository.database, candidate.ID)
	if reconcileErr != nil {
		return upstreamendpoint.CommitResult{Outcome: upstreamendpoint.CommitIndeterminate},
			errors.Join(commitErr, reconcileErr)
	}
	if exists && current.Equal(candidate) {
		return upstreamendpoint.CommitResult{
			Outcome: upstreamendpoint.CommitCommitted, Endpoint: current, Actual: current.Revision,
		}, nil
	}
	if (!exists && expected == 0) || (exists && current.Revision == expected) {
		return upstreamendpoint.CommitResult{
			Outcome: upstreamendpoint.CommitNotCommitted, Endpoint: current, Actual: current.Revision,
		}, commitErr
	}
	return upstreamendpoint.CommitResult{
		Outcome: upstreamendpoint.CommitIndeterminate, Endpoint: current, Actual: current.Revision,
	}, commitErr
}

type upstreamEndpointRow interface{ Scan(...any) error }

func loadUpstreamEndpoint(
	ctx context.Context,
	query interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	id upstreamendpoint.ID,
) (upstreamendpoint.Endpoint, bool, error) {
	item, err := scanUpstreamEndpoint(query.QueryRowContext(
		ctx,
		`SELECT endpoint_id, display_name, origin, realm_id,
		        backend_protocols_json, capabilities_json, drivers_json,
		        state, revision, created_at_unix_ms, updated_at_unix_ms
		 FROM upstream_endpoints WHERE endpoint_id = ?`,
		id.String(),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return upstreamendpoint.Endpoint{}, false, nil
	}
	if err != nil {
		return upstreamendpoint.Endpoint{}, false, err
	}
	return item, true, nil
}

func scanUpstreamEndpoint(row upstreamEndpointRow) (upstreamendpoint.Endpoint, error) {
	var id, displayName, rawOrigin, realmID, state string
	var protocolsJSON, capabilitiesJSON, driversJSON []byte
	var revision, createdAt, updatedAt int64
	if err := row.Scan(
		&id, &displayName, &rawOrigin, &realmID,
		&protocolsJSON, &capabilitiesJSON, &driversJSON,
		&state, &revision, &createdAt, &updatedAt,
	); err != nil {
		return upstreamendpoint.Endpoint{}, err
	}
	parsedID, idErr := upstreamendpoint.NewID(id)
	origin, originErr := originidentity.ParseProviderOrigin(rawOrigin)
	var protocols []string
	var capabilities []protocolspec.ProviderCapability
	var driverValues []string
	protocolErr := json.Unmarshal(protocolsJSON, &protocols)
	capabilityErr := json.Unmarshal(capabilitiesJSON, &capabilities)
	driverErr := json.Unmarshal(driversJSON, &driverValues)
	drivers := make([]providerauth.DriverRef, len(driverValues))
	for index, value := range driverValues {
		driver, err := providerauth.NewDriverRef(value)
		if err != nil {
			driverErr = err
			break
		}
		drivers[index] = driver
	}
	if idErr != nil || originErr != nil || protocolErr != nil || capabilityErr != nil ||
		driverErr != nil || revision <= 0 || createdAt <= 0 || updatedAt < createdAt {
		return upstreamendpoint.Endpoint{}, upstreamendpoint.ErrInvalidEndpoint
	}
	item := upstreamendpoint.Endpoint{
		ID: parsedID, DisplayName: displayName, Origin: origin, RealmID: realmID,
		BackendProtocols: protocols, Capabilities: capabilities, Drivers: drivers,
		State: upstreamendpoint.State(state), Revision: uint64(revision),
		CreatedAt: time.UnixMilli(createdAt).UTC(), UpdatedAt: time.UnixMilli(updatedAt).UTC(),
	}
	if err := item.Validate(); err != nil {
		return upstreamendpoint.Endpoint{}, err
	}
	return item, nil
}

func encodeEndpointCollections(endpoint upstreamendpoint.Endpoint) ([]byte, []byte, []byte, error) {
	protocols, err := json.Marshal(endpoint.BackendProtocols)
	if err != nil {
		return nil, nil, nil, err
	}
	capabilities, err := json.Marshal(endpoint.Capabilities)
	if err != nil {
		return nil, nil, nil, err
	}
	driverValues := make([]string, len(endpoint.Drivers))
	for index, driver := range endpoint.Drivers {
		driverValues[index] = driver.String()
	}
	drivers, err := json.Marshal(driverValues)
	if err != nil {
		return nil, nil, nil, err
	}
	return protocols, capabilities, drivers, nil
}
