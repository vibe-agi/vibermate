package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

type providerAccountRepository struct {
	database         *sql.DB
	operations       *operationGate
	reconcileTimeout time.Duration
	committer        transactionCommitter
}

var _ provideraccount.Repository = (*providerAccountRepository)(nil)

type accountWriteKind uint8

const (
	accountWriteIdentity accountWriteKind = iota
	accountWriteAssociations
	accountWriteNote
)

func newProviderAccountRepository(
	database *sql.DB,
	operations *operationGate,
	reconcileTimeout time.Duration,
	committer transactionCommitter,
) *providerAccountRepository {
	return &providerAccountRepository{
		database: database, operations: operations,
		reconcileTimeout: reconcileTimeout, committer: committer,
	}
}

func (repository *providerAccountRepository) LoadAll(
	ctx context.Context,
) ([]provideraccount.Account, error) {
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer permit.finish()
	rows, err := repository.database.QueryContext(
		permit.context,
		`SELECT account_id, display_name, credential_origin, endpoint_associations, association_revision, realm_id, driver_ref,
		        secret_reference, state, revision,
		        created_at_unix_ms, updated_at_unix_ms, note, note_revision
		 FROM provider_accounts ORDER BY account_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list ProviderAccounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	accounts := make([]provideraccount.Account, 0)
	for rows.Next() {
		account, scanErr := scanProviderAccount(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ProviderAccounts: %w", err)
	}
	return accounts, nil
}

func (repository *providerAccountRepository) Load(
	ctx context.Context,
	id provideraccount.ID,
) (provideraccount.Account, bool, error) {
	if _, err := provideraccount.NewID(id.String()); err != nil {
		return provideraccount.Account{}, false, err
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return provideraccount.Account{}, false, err
	}
	defer permit.finish()
	return loadProviderAccount(permit.context, repository.database, id)
}

func (repository *providerAccountRepository) Write(
	ctx context.Context,
	expected uint64,
	candidate provideraccount.Account,
) (provideraccount.CommitResult, error) {
	return repository.write(ctx, expected, candidate, accountWriteIdentity)
}

func (repository *providerAccountRepository) WriteAssociations(ctx context.Context, expected uint64, candidate provideraccount.Account) (provideraccount.CommitResult, error) {
	return repository.write(ctx, expected, candidate, accountWriteAssociations)
}

func (repository *providerAccountRepository) WriteNote(ctx context.Context, expected uint64, candidate provideraccount.Account) (provideraccount.CommitResult, error) {
	return repository.write(ctx, expected, candidate, accountWriteNote)
}

func (repository *providerAccountRepository) write(ctx context.Context, expected uint64, candidate provideraccount.Account, kind accountWriteKind) (provideraccount.CommitResult, error) {
	revision := func(account provideraccount.Account) uint64 {
		if kind == accountWriteAssociations {
			return account.AssociationRevision
		}
		if kind == accountWriteNote {
			return account.NoteRevision
		}
		return account.Revision
	}
	if candidate.Validate() != nil || expected >= provideraccount.MaxRevision ||
		revision(candidate) != expected+1 {
		return provideraccount.CommitResult{Outcome: provideraccount.CommitNotCommitted},
			provideraccount.ErrInvalidAccount
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return provideraccount.CommitResult{Outcome: provideraccount.CommitNotCommitted}, err
	}
	defer permit.finish()
	transaction, err := repository.database.BeginTx(permit.context, nil)
	if err != nil {
		return provideraccount.CommitResult{Outcome: provideraccount.CommitNotCommitted},
			fmt.Errorf("begin ProviderAccount transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	var result sql.Result
	if kind == accountWriteNote {
		result, err = transaction.ExecContext(permit.context,
			`UPDATE provider_accounts SET note = ?, note_revision = ?, updated_at_unix_ms = ? WHERE account_id = ? AND note_revision = ? AND revision = ? AND association_revision = ?`,
			candidate.Note, int64(candidate.NoteRevision), candidate.UpdatedAt.UnixMilli(), candidate.ID.String(), int64(expected), int64(candidate.Revision), int64(candidate.AssociationRevision))
	} else if kind == accountWriteAssociations {
		result, err = transaction.ExecContext(permit.context,
			`UPDATE provider_accounts SET endpoint_associations = ?, association_revision = ?, updated_at_unix_ms = ? WHERE account_id = ? AND association_revision = ? AND revision = ? AND note_revision = ?`,
			candidate.Associations.String(), int64(candidate.AssociationRevision), candidate.UpdatedAt.UnixMilli(), candidate.ID.String(), int64(expected), int64(candidate.Revision), int64(candidate.NoteRevision))
	} else if expected == 0 {
		result, err = transaction.ExecContext(
			permit.context,
			`INSERT INTO provider_accounts(
			   account_id, display_name, credential_origin, endpoint_associations, association_revision, realm_id, driver_ref,
			   secret_reference, state, revision,
			   created_at_unix_ms, updated_at_unix_ms, note, note_revision
			  ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(account_id) DO NOTHING`,
			candidate.ID.String(), candidate.DisplayName, candidate.Origin.String(), candidate.Associations.String(), int64(candidate.AssociationRevision), candidate.RealmID,
			candidate.Driver.String(), candidate.SecretRef.String(), string(candidate.State),
			int64(candidate.Revision), candidate.CreatedAt.UnixMilli(), candidate.UpdatedAt.UnixMilli(), candidate.Note, int64(candidate.NoteRevision),
		)
	} else {
		result, err = transaction.ExecContext(
			permit.context,
			`UPDATE provider_accounts
			 SET display_name = ?, credential_origin = ?, endpoint_associations = ?, association_revision = ?, realm_id = ?, driver_ref = ?, secret_reference = ?,
			     state = ?, revision = ?, updated_at_unix_ms = ?
			 WHERE account_id = ? AND revision = ? AND created_at_unix_ms = ? AND note_revision = ?`,
			candidate.DisplayName, candidate.Origin.String(), candidate.Associations.String(), int64(candidate.AssociationRevision), candidate.RealmID, candidate.Driver.String(),
			candidate.SecretRef.String(), string(candidate.State), int64(candidate.Revision),
			candidate.UpdatedAt.UnixMilli(), candidate.ID.String(), int64(expected),
			candidate.CreatedAt.UnixMilli(), int64(candidate.NoteRevision),
		)
	}
	if err != nil {
		return provideraccount.CommitResult{Outcome: provideraccount.CommitNotCommitted},
			fmt.Errorf("write ProviderAccount: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return provideraccount.CommitResult{Outcome: provideraccount.CommitIndeterminate}, err
	}
	if affected != 1 {
		current, exists, loadErr := loadProviderAccount(permit.context, transaction, candidate.ID)
		if loadErr != nil {
			return provideraccount.CommitResult{Outcome: provideraccount.CommitIndeterminate}, loadErr
		}
		actual := uint64(0)
		if exists {
			actual = revision(current)
		}
		return provideraccount.CommitResult{
			Outcome: provideraccount.CommitConflict, Account: current, Actual: actual,
		}, nil
	}
	commitErr := repository.committer.Commit(transaction)
	if commitErr == nil {
		return provideraccount.CommitResult{
			Outcome: provideraccount.CommitCommitted,
			Account: candidate, Actual: revision(candidate),
		}, nil
	}
	_ = transaction.Rollback()
	reconcileContext, cancel := context.WithTimeout(
		permit.ownerContext,
		repository.reconcileTimeout,
	)
	defer cancel()
	current, exists, reconcileErr := loadProviderAccount(
		reconcileContext,
		repository.database,
		candidate.ID,
	)
	if reconcileErr != nil {
		return provideraccount.CommitResult{Outcome: provideraccount.CommitIndeterminate},
			errors.Join(commitErr, reconcileErr)
	}
	if exists && current == candidate {
		return provideraccount.CommitResult{
			Outcome: provideraccount.CommitCommitted,
			Account: current, Actual: revision(current),
		}, nil
	}
	if (!exists && expected == 0) || (exists && revision(current) == expected) {
		return provideraccount.CommitResult{
			Outcome: provideraccount.CommitNotCommitted,
			Account: current, Actual: revision(current),
		}, commitErr
	}
	return provideraccount.CommitResult{
		Outcome: provideraccount.CommitIndeterminate,
		Account: current, Actual: revision(current),
	}, commitErr
}

func (repository *providerAccountRepository) Delete(
	ctx context.Context,
	id provideraccount.ID,
	expected uint64,
) (provideraccount.CommitResult, error) {
	if _, err := provideraccount.NewID(id.String()); err != nil || expected == 0 ||
		expected > provideraccount.MaxRevision {
		return provideraccount.CommitResult{Outcome: provideraccount.CommitNotCommitted},
			provideraccount.ErrInvalidAccount
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return provideraccount.CommitResult{Outcome: provideraccount.CommitNotCommitted}, err
	}
	defer permit.finish()
	transaction, err := repository.database.BeginTx(permit.context, nil)
	if err != nil {
		return provideraccount.CommitResult{Outcome: provideraccount.CommitNotCommitted}, err
	}
	defer func() { _ = transaction.Rollback() }()
	result, err := transaction.ExecContext(
		permit.context,
		`DELETE FROM provider_accounts WHERE account_id = ? AND revision = ?`,
		id.String(), int64(expected),
	)
	if err != nil {
		return provideraccount.CommitResult{Outcome: provideraccount.CommitNotCommitted},
			fmt.Errorf("delete ProviderAccount: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return provideraccount.CommitResult{Outcome: provideraccount.CommitIndeterminate}, err
	}
	if affected != 1 {
		current, exists, loadErr := loadProviderAccount(permit.context, transaction, id)
		if loadErr != nil {
			return provideraccount.CommitResult{Outcome: provideraccount.CommitIndeterminate}, loadErr
		}
		actual := uint64(0)
		if exists {
			actual = current.Revision
		}
		return provideraccount.CommitResult{
			Outcome: provideraccount.CommitConflict, Account: current, Actual: actual,
		}, nil
	}
	commitErr := repository.committer.Commit(transaction)
	if commitErr == nil {
		return provideraccount.CommitResult{Outcome: provideraccount.CommitCommitted}, nil
	}
	_ = transaction.Rollback()
	reconcileContext, cancel := context.WithTimeout(permit.ownerContext, repository.reconcileTimeout)
	defer cancel()
	current, exists, reconcileErr := loadProviderAccount(reconcileContext, repository.database, id)
	if reconcileErr != nil {
		return provideraccount.CommitResult{Outcome: provideraccount.CommitIndeterminate},
			errors.Join(commitErr, reconcileErr)
	}
	if !exists {
		return provideraccount.CommitResult{Outcome: provideraccount.CommitCommitted}, nil
	}
	if current.Revision == expected {
		return provideraccount.CommitResult{
			Outcome: provideraccount.CommitNotCommitted, Account: current, Actual: current.Revision,
		}, commitErr
	}
	return provideraccount.CommitResult{
		Outcome: provideraccount.CommitIndeterminate, Account: current, Actual: current.Revision,
	}, commitErr
}

type providerAccountRow interface{ Scan(...any) error }

func loadProviderAccount(
	ctx context.Context,
	query interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	id provideraccount.ID,
) (provideraccount.Account, bool, error) {
	account, err := scanProviderAccount(query.QueryRowContext(
		ctx,
		`SELECT account_id, display_name, credential_origin, endpoint_associations, association_revision, realm_id, driver_ref,
		        secret_reference, state, revision,
		        created_at_unix_ms, updated_at_unix_ms, note, note_revision
		 FROM provider_accounts WHERE account_id = ?`,
		id.String(),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return provideraccount.Account{}, false, nil
	}
	if err != nil {
		return provideraccount.Account{}, false, err
	}
	return account, true, nil
}

func scanProviderAccount(row providerAccountRow) (provideraccount.Account, error) {
	var id, displayName, originValue, associationsValue, realmID, driverValue, secretValue, state string
	var associationRevision, noteRevision int64
	var note string
	var revision, createdAt, updatedAt int64
	if err := row.Scan(
		&id, &displayName, &originValue, &associationsValue, &associationRevision, &realmID, &driverValue,
		&secretValue, &state, &revision, &createdAt, &updatedAt, &note, &noteRevision,
	); err != nil {
		return provideraccount.Account{}, err
	}
	parsedID, idErr := provideraccount.NewID(id)
	origin, originErr := originidentity.ParseProviderOrigin(originValue)
	associations, associationErr := provideraccount.ParseEndpointAssociations(associationsValue)
	driver, driverErr := providerauth.NewDriverRef(driverValue)
	secret, secretErr := secretstore.ParseReference(secretValue)
	if idErr != nil || originErr != nil || associationErr != nil || associationRevision <= 0 || driverErr != nil || secretErr != nil ||
		revision <= 0 || createdAt <= 0 || updatedAt < createdAt {
		return provideraccount.Account{}, provideraccount.ErrInvalidAccount
	}
	account := provideraccount.Account{
		ID: parsedID, DisplayName: displayName, Note: note, NoteRevision: uint64(noteRevision), Origin: origin, Associations: associations, AssociationRevision: uint64(associationRevision), RealmID: realmID,
		Driver: driver, SecretRef: secret, State: provideraccount.State(state),
		Revision: uint64(revision), CreatedAt: time.UnixMilli(createdAt).UTC(),
		UpdatedAt: time.UnixMilli(updatedAt).UTC(),
	}
	if err := account.Validate(); err != nil {
		return provideraccount.Account{}, err
	}
	return account, nil
}
