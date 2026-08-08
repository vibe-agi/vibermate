package runtimepersistence

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/proxyclient"
)

const proxyClientBindingColumns = `
binding_id,
revision,
state,
display_name,
allowed_ingress_scopes_json,
allowed_environment_ids_json,
quota_policy_id,
allowed_grant_kinds,
created_at_unix_ms,
updated_at_unix_ms`

const clientEnrollmentColumns = `
enrollment_id,
binding_id,
binding_revision,
state,
credential_digest,
created_at_unix_ms,
expires_at_unix_ms,
updated_at_unix_ms,
consumed_at_unix_ms,
machine_registration_id`

const proxyClientAuthenticationColumns = `
binding.binding_id,
binding.revision,
binding.state,
binding.display_name,
binding.allowed_ingress_scopes_json,
binding.allowed_environment_ids_json,
binding.quota_policy_id,
binding.allowed_grant_kinds,
binding.created_at_unix_ms,
binding.updated_at_unix_ms,
machine.machine_registration_id,
machine.machine_id,
machine.binding_id,
machine.binding_revision,
machine.revision,
machine.state,
machine.display_name,
machine.created_at_unix_ms,
machine.updated_at_unix_ms,
principal.principal_id,
principal.binding_id,
principal.binding_revision,
principal.machine_registration_id,
principal.credential_revision,
principal.credential_digest,
principal.allowed_grant_kinds,
principal.state,
principal.created_at_unix_ms,
principal.updated_at_unix_ms`

type proxyClientRepository struct {
	database         *sql.DB
	operations       *operationGate
	reconcileTimeout time.Duration
	committer        transactionCommitter
}

var _ proxyclient.Repository = (*proxyClientRepository)(nil)

func newProxyClientRepository(
	database *sql.DB,
	operations *operationGate,
	reconcileTimeout time.Duration,
	committer transactionCommitter,
) *proxyClientRepository {
	return &proxyClientRepository{
		database:         database,
		operations:       operations,
		reconcileTimeout: reconcileTimeout,
		committer:        committer,
	}
}

func (repository *proxyClientRepository) CreateBinding(
	ctx context.Context,
	record proxyclient.BindingRecord,
) error {
	if err := record.Validate(); err != nil || record.Revision != 1 ||
		record.State != proxyclient.BindingActive {
		return errors.Join(proxyclient.ErrInvalidRecord, err)
	}
	ingress, environments, grants, err := encodeBindingPolicy(record.Policy)
	if err != nil {
		return err
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	result, err := repository.database.ExecContext(
		operation,
		`INSERT INTO proxy_client_bindings (
		     binding_id,
		     revision,
		     state,
		     display_name,
		     allowed_ingress_scopes_json,
		     allowed_environment_ids_json,
		     quota_policy_id,
		     allowed_grant_kinds,
		     created_at_unix_ms,
		     updated_at_unix_ms
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT DO NOTHING`,
		record.ID.String(),
		int64(record.Revision),
		string(record.State),
		record.DisplayName,
		ingress,
		environments,
		record.Policy.QuotaPolicyID(),
		grants,
		toUnixMillis(record.CreatedAt),
		toUnixMillis(record.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert ProxyClientBinding: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read ProxyClientBinding insert result: %w", err)
	}
	if affected != 1 {
		return proxyclient.ErrStateConflict
	}
	return nil
}

func (repository *proxyClientRepository) CreateEnrollment(
	ctx context.Context,
	record proxyclient.EnrollmentRecord,
) error {
	if err := record.Validate(); err != nil || record.State != proxyclient.EnrollmentActive {
		return errors.Join(proxyclient.ErrInvalidRecord, err)
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return err
	}
	defer permit.finish()
	transaction, err := repository.database.BeginTx(permit.context, nil)
	if err != nil {
		return fmt.Errorf("begin client enrollment creation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	binding, err := loadProxyClientBinding(permit.context, transaction, record.BindingID)
	if errors.Is(err, sql.ErrNoRows) {
		return proxyclient.ErrBindingNotFound
	}
	if err != nil {
		return fmt.Errorf("read enrollment ProxyClientBinding: %w", err)
	}
	if binding.State != proxyclient.BindingActive {
		return proxyclient.ErrBindingInactive
	}
	if binding.Revision != record.BindingRevision {
		return proxyclient.ErrBindingConflict
	}
	result, err := transaction.ExecContext(
		permit.context,
		`INSERT INTO client_enrollments (
		     enrollment_id,
		     binding_id,
		     binding_revision,
		     state,
		     credential_digest,
		     created_at_unix_ms,
		     expires_at_unix_ms,
		     updated_at_unix_ms
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT DO NOTHING`,
		record.ID.String(),
		record.BindingID.String(),
		int64(record.BindingRevision),
		string(record.State),
		record.CredentialDigest[:],
		toUnixMillis(record.CreatedAt),
		toUnixMillis(record.ExpiresAt),
		toUnixMillis(record.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert client enrollment: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read client enrollment insert result: %w", err)
	}
	if affected != 1 {
		return proxyclient.ErrStateConflict
	}
	commitErr := repository.committer.Commit(transaction)
	if commitErr == nil {
		return nil
	}
	_ = transaction.Rollback()
	reconcileContext, cancel := context.WithTimeout(
		permit.ownerContext,
		repository.reconcileTimeout,
	)
	defer cancel()
	current, reconcileErr := loadClientEnrollment(
		reconcileContext,
		repository.database,
		record.ID,
	)
	if reconcileErr == nil && enrollmentRecordsEqual(current, record) {
		return nil
	}
	if errors.Is(reconcileErr, sql.ErrNoRows) {
		return fmt.Errorf("commit client enrollment creation: %w", commitErr)
	}
	return errors.Join(
		proxyclient.ErrCommitIndeterminate,
		fmt.Errorf("commit client enrollment creation: %w", commitErr),
		proxyClientOptionalError("reconcile client enrollment creation", reconcileErr),
	)
}

func (repository *proxyClientRepository) CompleteEnrollment(
	ctx context.Context,
	candidate proxyclient.CompletionCandidate,
) (proxyclient.CompletionResult, error) {
	if err := candidate.Validate(); err != nil {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, err
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, err
	}
	defer permit.finish()
	transaction, err := repository.database.BeginTx(permit.context, nil)
	if err != nil {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, fmt.Errorf("begin client enrollment completion: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	enrollment, err := loadClientEnrollment(
		permit.context,
		transaction,
		candidate.EnrollmentID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, proxyclient.ErrEnrollmentRejected
	}
	if err != nil {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, fmt.Errorf("read client enrollment: %w", err)
	}
	if subtle.ConstantTimeCompare(
		enrollment.CredentialDigest[:],
		candidate.EnrollmentDigest[:],
	) != 1 {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, proxyclient.ErrEnrollmentRejected
	}
	switch enrollment.State {
	case proxyclient.EnrollmentConsumed:
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, proxyclient.ErrEnrollmentConsumed
	case proxyclient.EnrollmentExpired:
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, proxyclient.ErrEnrollmentExpired
	case proxyclient.EnrollmentRevoked:
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, proxyclient.ErrEnrollmentRejected
	}
	if !candidate.CompletedAt.Before(enrollment.ExpiresAt) {
		if _, err := transaction.ExecContext(
			permit.context,
			`UPDATE client_enrollments
			 SET state = 'expired', updated_at_unix_ms = ?
			 WHERE enrollment_id = ? AND state = 'active'`,
			toUnixMillis(candidate.CompletedAt),
			enrollment.ID.String(),
		); err != nil {
			return proxyclient.CompletionResult{
				Outcome: proxyclient.CompletionNotCommitted,
			}, fmt.Errorf("expire client enrollment: %w", err)
		}
		if err := repository.committer.Commit(transaction); err != nil {
			return proxyclient.CompletionResult{
				Outcome: proxyclient.CompletionIndeterminate,
			}, errors.Join(proxyclient.ErrEnrollmentExpired, err)
		}
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, proxyclient.ErrEnrollmentExpired
	}
	binding, err := loadProxyClientBinding(
		permit.context,
		transaction,
		enrollment.BindingID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, proxyclient.ErrBindingNotFound
	}
	if err != nil {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, fmt.Errorf("read enrollment binding: %w", err)
	}
	if binding.State != proxyclient.BindingActive {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, proxyclient.ErrBindingInactive
	}
	if binding.Revision != enrollment.BindingRevision {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, proxyclient.ErrBindingConflict
	}
	record := authenticationCandidate(binding, candidate)
	if err := record.Validate(); err != nil {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, err
	}
	inserted, err := insertMachineRegistration(permit.context, transaction, record.Machine)
	if err != nil {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, err
	}
	if !inserted {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, proxyclient.ErrStateConflict
	}
	inserted, err = insertEnrolledPrincipal(permit.context, transaction, record.Principal)
	if err != nil {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, err
	}
	if !inserted {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, proxyclient.ErrStateConflict
	}
	result, err := transaction.ExecContext(
		permit.context,
		`UPDATE client_enrollments
		 SET state = 'consumed',
		     updated_at_unix_ms = ?,
		     consumed_at_unix_ms = ?,
		     machine_registration_id = ?
		 WHERE enrollment_id = ?
		   AND state = 'active'
		   AND binding_id = ?
		   AND binding_revision = ?`,
		toUnixMillis(candidate.CompletedAt),
		toUnixMillis(candidate.CompletedAt),
		record.Machine.ID.String(),
		enrollment.ID.String(),
		enrollment.BindingID.String(),
		int64(enrollment.BindingRevision),
	)
	if err != nil {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, fmt.Errorf("consume client enrollment: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, fmt.Errorf("read client enrollment consumption result: %w", err)
	}
	if affected != 1 {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, proxyclient.ErrEnrollmentConsumed
	}
	commitErr := repository.committer.Commit(transaction)
	if commitErr == nil {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionCommitted,
			Record:  record,
		}, nil
	}
	_ = transaction.Rollback()
	reconcileContext, cancel := context.WithTimeout(
		permit.ownerContext,
		repository.reconcileTimeout,
	)
	defer cancel()
	current, reconcileErr := loadAuthenticationByEnrollment(
		reconcileContext,
		repository.database,
		candidate.EnrollmentID,
	)
	if reconcileErr == nil && authenticationMatchesCandidate(current, candidate) {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionCommitted,
			Record:  current,
		}, nil
	}
	currentEnrollment, enrollmentErr := loadClientEnrollment(
		reconcileContext,
		repository.database,
		candidate.EnrollmentID,
	)
	if enrollmentErr == nil &&
		currentEnrollment.State == proxyclient.EnrollmentActive &&
		subtle.ConstantTimeCompare(
			currentEnrollment.CredentialDigest[:],
			candidate.EnrollmentDigest[:],
		) == 1 {
		return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionNotCommitted,
		}, fmt.Errorf("commit client enrollment completion: %w", commitErr)
	}
	return proxyclient.CompletionResult{
			Outcome: proxyclient.CompletionIndeterminate,
		}, errors.Join(
			proxyclient.ErrCommitIndeterminate,
			fmt.Errorf("commit client enrollment completion: %w", commitErr),
			proxyClientOptionalError("reconcile enrollment completion", reconcileErr),
			proxyClientOptionalError("reconcile enrollment state", enrollmentErr),
		)
}

func (repository *proxyClientRepository) Authenticate(
	ctx context.Context,
	digest proxyclient.ControlDigest,
) (proxyclient.AuthenticationRecord, error) {
	if !digest.Valid() {
		return proxyclient.AuthenticationRecord{}, proxyclient.ErrControlRejected
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return proxyclient.AuthenticationRecord{}, err
	}
	defer finish()
	record, err := loadAuthenticationByDigest(operation, repository.database, digest)
	if errors.Is(err, sql.ErrNoRows) {
		return proxyclient.AuthenticationRecord{}, proxyclient.ErrControlRejected
	}
	if err != nil {
		return proxyclient.AuthenticationRecord{}, fmt.Errorf(
			"read enrolled control principal: %w",
			err,
		)
	}
	if subtle.ConstantTimeCompare(record.Principal.CredentialDigest[:], digest[:]) != 1 ||
		record.Validate() != nil {
		return proxyclient.AuthenticationRecord{}, proxyclient.ErrControlRejected
	}
	return record, nil
}

func (repository *proxyClientRepository) RevokeBinding(
	ctx context.Context,
	id proxyclient.BindingID,
	expected proxyclient.Revision,
	now time.Time,
) (proxyclient.BindingRecord, error) {
	if !id.Valid() || !expected.Valid() || now.IsZero() || uint64(expected) == proxyclient.MaxRevision {
		return proxyclient.BindingRecord{}, proxyclient.ErrInvalidCommand
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return proxyclient.BindingRecord{}, err
	}
	defer permit.finish()
	transaction, err := repository.database.BeginTx(permit.context, nil)
	if err != nil {
		return proxyclient.BindingRecord{}, fmt.Errorf("begin binding revocation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	current, err := loadProxyClientBinding(permit.context, transaction, id)
	if errors.Is(err, sql.ErrNoRows) {
		return proxyclient.BindingRecord{}, proxyclient.ErrBindingNotFound
	}
	if err != nil {
		return proxyclient.BindingRecord{}, fmt.Errorf("read binding for revocation: %w", err)
	}
	if current.State == proxyclient.BindingRevoked {
		return current, nil
	}
	if current.Revision != expected {
		return proxyclient.BindingRecord{}, proxyclient.ErrBindingConflict
	}
	nextRevision := expected + 1
	result, err := transaction.ExecContext(
		permit.context,
		`UPDATE proxy_client_bindings
		 SET state = 'revoked', revision = ?, updated_at_unix_ms = ?
		 WHERE binding_id = ? AND state = 'active' AND revision = ?`,
		int64(nextRevision),
		toUnixMillis(now),
		id.String(),
		int64(expected),
	)
	if err != nil {
		return proxyclient.BindingRecord{}, fmt.Errorf("revoke ProxyClientBinding: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return proxyclient.BindingRecord{}, fmt.Errorf(
			"read ProxyClientBinding revocation result: %w",
			err,
		)
	}
	if affected != 1 {
		return proxyclient.BindingRecord{}, proxyclient.ErrBindingConflict
	}
	for _, statement := range []string{
		`UPDATE client_enrollments
		 SET state = 'revoked', updated_at_unix_ms = ?
		 WHERE binding_id = ? AND state = 'active'`,
		`UPDATE machine_registrations
		 SET state = 'revoked', updated_at_unix_ms = ?
		 WHERE binding_id = ? AND state = 'active'`,
		`UPDATE enrolled_control_principals
		 SET state = 'revoked', updated_at_unix_ms = ?
		 WHERE binding_id = ? AND state = 'active'`,
	} {
		if _, err := transaction.ExecContext(
			permit.context,
			statement,
			toUnixMillis(now),
			id.String(),
		); err != nil {
			return proxyclient.BindingRecord{}, fmt.Errorf(
				"cascade ProxyClientBinding revocation: %w",
				err,
			)
		}
	}
	updated, err := loadProxyClientBinding(permit.context, transaction, id)
	if err != nil {
		return proxyclient.BindingRecord{}, fmt.Errorf("read revoked binding: %w", err)
	}
	commitErr := repository.committer.Commit(transaction)
	if commitErr == nil {
		return updated, nil
	}
	_ = transaction.Rollback()
	reconcileContext, cancel := context.WithTimeout(
		permit.ownerContext,
		repository.reconcileTimeout,
	)
	defer cancel()
	reconciled, reconcileErr := loadProxyClientBinding(
		reconcileContext,
		repository.database,
		id,
	)
	if reconcileErr == nil && reconciled.State == proxyclient.BindingRevoked &&
		reconciled.Revision == nextRevision {
		return reconciled, nil
	}
	return proxyclient.BindingRecord{}, errors.Join(
		proxyclient.ErrCommitIndeterminate,
		fmt.Errorf("commit ProxyClientBinding revocation: %w", commitErr),
		proxyClientOptionalError("reconcile ProxyClientBinding revocation", reconcileErr),
	)
}

func authenticationCandidate(
	binding proxyclient.BindingRecord,
	candidate proxyclient.CompletionCandidate,
) proxyclient.AuthenticationRecord {
	machine := candidate.Machine
	machine.BindingID = binding.ID
	machine.BindingRevision = binding.Revision
	principal := candidate.Principal
	principal.BindingID = binding.ID
	principal.BindingRevision = binding.Revision
	principal.AllowedGrantKinds = binding.Policy.AllowedGrantKinds()
	return proxyclient.AuthenticationRecord{
		Binding:   binding,
		Machine:   machine,
		Principal: principal,
	}
}

func insertMachineRegistration(
	ctx context.Context,
	transaction *sql.Tx,
	record proxyclient.MachineRecord,
) (bool, error) {
	result, err := transaction.ExecContext(
		ctx,
		`INSERT INTO machine_registrations (
		     machine_registration_id,
		     machine_id,
		     binding_id,
		     binding_revision,
		     revision,
		     state,
		     display_name,
		     created_at_unix_ms,
		     updated_at_unix_ms
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT DO NOTHING`,
		record.ID.String(),
		record.MachineID.String(),
		record.BindingID.String(),
		int64(record.BindingRevision),
		int64(record.Revision),
		string(record.State),
		record.DisplayName,
		toUnixMillis(record.CreatedAt),
		toUnixMillis(record.UpdatedAt),
	)
	if err != nil {
		return false, fmt.Errorf("insert MachineRegistration: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read MachineRegistration insert result: %w", err)
	}
	return affected == 1, nil
}

func insertEnrolledPrincipal(
	ctx context.Context,
	transaction *sql.Tx,
	record proxyclient.PrincipalRecord,
) (bool, error) {
	grants, err := encodeGrantKinds(record.AllowedGrantKinds)
	if err != nil {
		return false, err
	}
	result, err := transaction.ExecContext(
		ctx,
		`INSERT INTO enrolled_control_principals (
		     principal_id,
		     binding_id,
		     binding_revision,
		     machine_registration_id,
		     credential_revision,
		     credential_digest,
		     allowed_grant_kinds,
		     state,
		     created_at_unix_ms,
		     updated_at_unix_ms
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT DO NOTHING`,
		record.ID.String(),
		record.BindingID.String(),
		int64(record.BindingRevision),
		record.MachineRegistrationID.String(),
		int64(record.CredentialRevision),
		record.CredentialDigest[:],
		grants,
		string(record.State),
		toUnixMillis(record.CreatedAt),
		toUnixMillis(record.UpdatedAt),
	)
	if err != nil {
		return false, fmt.Errorf("insert enrolled ControlPrincipal: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read enrolled ControlPrincipal insert result: %w", err)
	}
	return affected == 1, nil
}

type proxyClientRow interface {
	Scan(...any) error
}

type proxyClientRowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadProxyClientBinding(
	ctx context.Context,
	querier proxyClientRowQuerier,
	id proxyclient.BindingID,
) (proxyclient.BindingRecord, error) {
	return scanProxyClientBinding(querier.QueryRowContext(
		ctx,
		`SELECT `+proxyClientBindingColumns+`
		 FROM proxy_client_bindings WHERE binding_id = ?`,
		id.String(),
	))
}

func scanProxyClientBinding(row proxyClientRow) (proxyclient.BindingRecord, error) {
	var (
		bindingIDText    string
		revision         int64
		state            string
		displayName      string
		ingressJSON      []byte
		environmentsJSON []byte
		quotaPolicyID    string
		grantBits        int64
		createdAt        int64
		updatedAt        int64
	)
	if err := row.Scan(
		&bindingIDText,
		&revision,
		&state,
		&displayName,
		&ingressJSON,
		&environmentsJSON,
		&quotaPolicyID,
		&grantBits,
		&createdAt,
		&updatedAt,
	); err != nil {
		return proxyclient.BindingRecord{}, err
	}
	var ingressScopes []string
	var environmentIDs []environment.EnvironmentID
	if err := json.Unmarshal(ingressJSON, &ingressScopes); err != nil {
		return proxyclient.BindingRecord{}, fmt.Errorf("decode binding ingress scopes: %w", err)
	}
	if err := json.Unmarshal(environmentsJSON, &environmentIDs); err != nil {
		return proxyclient.BindingRecord{}, fmt.Errorf("decode binding Environment IDs: %w", err)
	}
	grantKinds, err := decodeGrantKinds(grantBits)
	if err != nil {
		return proxyclient.BindingRecord{}, err
	}
	policy, err := proxyclient.NewBindingPolicy(
		ingressScopes,
		environmentIDs,
		quotaPolicyID,
		grantKinds,
	)
	if err != nil ||
		!slices.Equal(ingressScopes, policy.AllowedIngressScopes()) ||
		!slices.Equal(environmentIDs, policy.AllowedEnvironmentIDs()) {
		return proxyclient.BindingRecord{}, errors.Join(proxyclient.ErrInvalidRecord, err)
	}
	bindingID, err := proxyclient.ParseBindingID(bindingIDText)
	if err != nil {
		return proxyclient.BindingRecord{}, errors.Join(proxyclient.ErrInvalidRecord, err)
	}
	record := proxyclient.BindingRecord{
		ID:          bindingID,
		Revision:    proxyclient.Revision(revision),
		State:       proxyclient.BindingState(state),
		DisplayName: displayName,
		Policy:      policy,
		CreatedAt:   fromUnixMillis(createdAt),
		UpdatedAt:   fromUnixMillis(updatedAt),
	}
	if err := record.Validate(); err != nil {
		return proxyclient.BindingRecord{}, err
	}
	return record, nil
}

func loadClientEnrollment(
	ctx context.Context,
	querier proxyClientRowQuerier,
	id proxyclient.EnrollmentID,
) (proxyclient.EnrollmentRecord, error) {
	return scanClientEnrollment(querier.QueryRowContext(
		ctx,
		`SELECT `+clientEnrollmentColumns+`
		 FROM client_enrollments WHERE enrollment_id = ?`,
		id.String(),
	))
}

func scanClientEnrollment(row proxyClientRow) (proxyclient.EnrollmentRecord, error) {
	var (
		enrollmentIDText string
		bindingIDText    string
		bindingRevision  int64
		state            string
		digest           []byte
		createdAt        int64
		expiresAt        int64
		updatedAt        int64
		consumedAt       sql.NullInt64
		machineIDText    sql.NullString
	)
	if err := row.Scan(
		&enrollmentIDText,
		&bindingIDText,
		&bindingRevision,
		&state,
		&digest,
		&createdAt,
		&expiresAt,
		&updatedAt,
		&consumedAt,
		&machineIDText,
	); err != nil {
		return proxyclient.EnrollmentRecord{}, err
	}
	if len(digest) != 32 {
		return proxyclient.EnrollmentRecord{}, proxyclient.ErrInvalidRecord
	}
	enrollmentID, err := proxyclient.ParseEnrollmentID(enrollmentIDText)
	if err != nil {
		return proxyclient.EnrollmentRecord{}, errors.Join(proxyclient.ErrInvalidRecord, err)
	}
	bindingID, err := proxyclient.ParseBindingID(bindingIDText)
	if err != nil {
		return proxyclient.EnrollmentRecord{}, errors.Join(proxyclient.ErrInvalidRecord, err)
	}
	record := proxyclient.EnrollmentRecord{
		ID:              enrollmentID,
		BindingID:       bindingID,
		BindingRevision: proxyclient.Revision(bindingRevision),
		State:           proxyclient.EnrollmentState(state),
		CreatedAt:       fromUnixMillis(createdAt),
		ExpiresAt:       fromUnixMillis(expiresAt),
		UpdatedAt:       fromUnixMillis(updatedAt),
	}
	copy(record.CredentialDigest[:], digest)
	if consumedAt.Valid {
		record.ConsumedAt = fromUnixMillis(consumedAt.Int64)
	}
	if machineIDText.Valid {
		record.MachineRegistrationID, err = proxyclient.ParseMachineRegistrationID(
			machineIDText.String,
		)
		if err != nil {
			return proxyclient.EnrollmentRecord{}, errors.Join(proxyclient.ErrInvalidRecord, err)
		}
	}
	if err := record.Validate(); err != nil {
		return proxyclient.EnrollmentRecord{}, err
	}
	return record, nil
}

func loadAuthenticationByDigest(
	ctx context.Context,
	querier proxyClientRowQuerier,
	digest proxyclient.ControlDigest,
) (proxyclient.AuthenticationRecord, error) {
	return scanProxyClientAuthentication(querier.QueryRowContext(
		ctx,
		`SELECT `+proxyClientAuthenticationColumns+`
		 FROM enrolled_control_principals AS principal
		 JOIN machine_registrations AS machine
		   ON machine.machine_registration_id = principal.machine_registration_id
		 JOIN proxy_client_bindings AS binding
		   ON binding.binding_id = principal.binding_id
		 WHERE principal.credential_digest = ?`,
		digest[:],
	))
}

func loadAuthenticationByEnrollment(
	ctx context.Context,
	querier proxyClientRowQuerier,
	id proxyclient.EnrollmentID,
) (proxyclient.AuthenticationRecord, error) {
	return scanProxyClientAuthentication(querier.QueryRowContext(
		ctx,
		`SELECT `+proxyClientAuthenticationColumns+`
		 FROM client_enrollments AS enrollment
		 JOIN machine_registrations AS machine
		   ON machine.machine_registration_id = enrollment.machine_registration_id
		 JOIN enrolled_control_principals AS principal
		   ON principal.machine_registration_id = machine.machine_registration_id
		 JOIN proxy_client_bindings AS binding
		   ON binding.binding_id = principal.binding_id
		 WHERE enrollment.enrollment_id = ? AND enrollment.state = 'consumed'`,
		id.String(),
	))
}

func scanProxyClientAuthentication(row proxyClientRow) (proxyclient.AuthenticationRecord, error) {
	var (
		bindingIDText       string
		bindingRevision     int64
		bindingState        string
		bindingDisplayName  string
		ingressJSON         []byte
		environmentsJSON    []byte
		quotaPolicyID       string
		bindingGrantBits    int64
		bindingCreatedAt    int64
		bindingUpdatedAt    int64
		machineRecordIDText string
		machineIDText       string
		machineBindingText  string
		machineBindingRev   int64
		machineRevision     int64
		machineState        string
		machineDisplayName  string
		machineCreatedAt    int64
		machineUpdatedAt    int64
		principalIDText     string
		principalBinding    string
		principalBindingRev int64
		principalMachineID  string
		credentialRevision  int64
		credentialDigest    []byte
		principalGrantBits  int64
		principalState      string
		principalCreatedAt  int64
		principalUpdatedAt  int64
	)
	if err := row.Scan(
		&bindingIDText,
		&bindingRevision,
		&bindingState,
		&bindingDisplayName,
		&ingressJSON,
		&environmentsJSON,
		&quotaPolicyID,
		&bindingGrantBits,
		&bindingCreatedAt,
		&bindingUpdatedAt,
		&machineRecordIDText,
		&machineIDText,
		&machineBindingText,
		&machineBindingRev,
		&machineRevision,
		&machineState,
		&machineDisplayName,
		&machineCreatedAt,
		&machineUpdatedAt,
		&principalIDText,
		&principalBinding,
		&principalBindingRev,
		&principalMachineID,
		&credentialRevision,
		&credentialDigest,
		&principalGrantBits,
		&principalState,
		&principalCreatedAt,
		&principalUpdatedAt,
	); err != nil {
		return proxyclient.AuthenticationRecord{}, err
	}
	binding, err := scanProxyClientBinding(authenticationBindingRow{
		values: []any{
			bindingIDText,
			bindingRevision,
			bindingState,
			bindingDisplayName,
			ingressJSON,
			environmentsJSON,
			quotaPolicyID,
			bindingGrantBits,
			bindingCreatedAt,
			bindingUpdatedAt,
		},
	})
	if err != nil {
		return proxyclient.AuthenticationRecord{}, err
	}
	machineRegistrationID, err := proxyclient.ParseMachineRegistrationID(
		machineRecordIDText,
	)
	if err != nil {
		return proxyclient.AuthenticationRecord{}, err
	}
	machineID, err := proxyclient.ParseMachineID(machineIDText)
	if err != nil {
		return proxyclient.AuthenticationRecord{}, err
	}
	machineBindingID, err := proxyclient.ParseBindingID(machineBindingText)
	if err != nil {
		return proxyclient.AuthenticationRecord{}, err
	}
	machine := proxyclient.MachineRecord{
		ID:              machineRegistrationID,
		MachineID:       machineID,
		BindingID:       machineBindingID,
		BindingRevision: proxyclient.Revision(machineBindingRev),
		Revision:        proxyclient.Revision(machineRevision),
		State:           proxyclient.MachineState(machineState),
		DisplayName:     machineDisplayName,
		CreatedAt:       fromUnixMillis(machineCreatedAt),
		UpdatedAt:       fromUnixMillis(machineUpdatedAt),
	}
	if err := machine.Validate(); err != nil {
		return proxyclient.AuthenticationRecord{}, err
	}
	principalID, err := proxyclient.ParsePrincipalID(principalIDText)
	if err != nil {
		return proxyclient.AuthenticationRecord{}, err
	}
	principalBindingID, err := proxyclient.ParseBindingID(principalBinding)
	if err != nil {
		return proxyclient.AuthenticationRecord{}, err
	}
	principalMachine, err := proxyclient.ParseMachineRegistrationID(principalMachineID)
	if err != nil {
		return proxyclient.AuthenticationRecord{}, err
	}
	grantKinds, err := decodeGrantKinds(principalGrantBits)
	if err != nil || len(credentialDigest) != 32 {
		return proxyclient.AuthenticationRecord{}, errors.Join(proxyclient.ErrInvalidRecord, err)
	}
	principal := proxyclient.PrincipalRecord{
		ID:                    principalID,
		BindingID:             principalBindingID,
		BindingRevision:       proxyclient.Revision(principalBindingRev),
		MachineRegistrationID: principalMachine,
		CredentialRevision:    proxyclient.Revision(credentialRevision),
		AllowedGrantKinds:     grantKinds,
		State:                 proxyclient.PrincipalState(principalState),
		CreatedAt:             fromUnixMillis(principalCreatedAt),
		UpdatedAt:             fromUnixMillis(principalUpdatedAt),
	}
	copy(principal.CredentialDigest[:], credentialDigest)
	if err := principal.Validate(); err != nil {
		return proxyclient.AuthenticationRecord{}, err
	}
	return proxyclient.AuthenticationRecord{
		Binding:   binding,
		Machine:   machine,
		Principal: principal,
	}, nil
}

// authenticationBindingRow reuses the one binding decoder without creating a
// second policy parsing path. Values are copied into the destinations in the
// same order as proxyClientBindingColumns.
type authenticationBindingRow struct{ values []any }

func (row authenticationBindingRow) Scan(destinations ...any) error {
	if len(destinations) != len(row.values) {
		return errors.New("authentication binding row shape is invalid")
	}
	for index, destination := range destinations {
		switch target := destination.(type) {
		case *string:
			*target = row.values[index].(string)
		case *int64:
			*target = row.values[index].(int64)
		case *[]byte:
			*target = slices.Clone(row.values[index].([]byte))
		default:
			return errors.New("authentication binding destination is invalid")
		}
	}
	return nil
}

func encodeBindingPolicy(policy proxyclient.BindingPolicy) ([]byte, []byte, int64, error) {
	if !policy.Valid() {
		return nil, nil, 0, proxyclient.ErrInvalidRecord
	}
	ingress, err := json.Marshal(policy.AllowedIngressScopes())
	if err != nil {
		return nil, nil, 0, fmt.Errorf("encode binding ingress scopes: %w", err)
	}
	environments, err := json.Marshal(policy.AllowedEnvironmentIDs())
	if err != nil {
		return nil, nil, 0, fmt.Errorf("encode binding Environment IDs: %w", err)
	}
	grants, err := encodeGrantKinds(policy.AllowedGrantKinds())
	return ingress, environments, grants, err
}

func encodeGrantKinds(kinds []controlprincipal.GrantKind) (int64, error) {
	var bits int64
	for _, kind := range kinds {
		switch kind {
		case controlprincipal.GrantCaptureRun:
			bits |= 1
		case controlprincipal.GrantManualCapture:
			bits |= 2
		default:
			return 0, proxyclient.ErrInvalidRecord
		}
	}
	if bits == 0 || len(kinds) != len(decodeGrantKindsUnchecked(bits)) {
		return 0, proxyclient.ErrInvalidRecord
	}
	return bits, nil
}

func decodeGrantKinds(bits int64) ([]controlprincipal.GrantKind, error) {
	if bits < 1 || bits > 3 {
		return nil, proxyclient.ErrInvalidRecord
	}
	return decodeGrantKindsUnchecked(bits), nil
}

func decodeGrantKindsUnchecked(bits int64) []controlprincipal.GrantKind {
	var kinds []controlprincipal.GrantKind
	if bits&1 != 0 {
		kinds = append(kinds, controlprincipal.GrantCaptureRun)
	}
	if bits&2 != 0 {
		kinds = append(kinds, controlprincipal.GrantManualCapture)
	}
	return kinds
}

func enrollmentRecordsEqual(left, right proxyclient.EnrollmentRecord) bool {
	return left.ID == right.ID && left.BindingID == right.BindingID &&
		left.BindingRevision == right.BindingRevision && left.State == right.State &&
		subtle.ConstantTimeCompare(
			left.CredentialDigest[:],
			right.CredentialDigest[:],
		) == 1 &&
		left.CreatedAt.Equal(right.CreatedAt) && left.ExpiresAt.Equal(right.ExpiresAt) &&
		left.UpdatedAt.Equal(right.UpdatedAt) && left.ConsumedAt.Equal(right.ConsumedAt) &&
		left.MachineRegistrationID == right.MachineRegistrationID
}

func authenticationMatchesCandidate(
	record proxyclient.AuthenticationRecord,
	candidate proxyclient.CompletionCandidate,
) bool {
	return record.Validate() == nil &&
		record.Machine.ID == candidate.Machine.ID &&
		record.Machine.MachineID == candidate.Machine.MachineID &&
		record.Machine.DisplayName == candidate.Machine.DisplayName &&
		record.Principal.ID == candidate.Principal.ID &&
		record.Principal.MachineRegistrationID == candidate.Principal.MachineRegistrationID &&
		record.Principal.CredentialRevision == candidate.Principal.CredentialRevision &&
		subtle.ConstantTimeCompare(
			record.Principal.CredentialDigest[:],
			candidate.Principal.CredentialDigest[:],
		) == 1
}

func proxyClientOptionalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
