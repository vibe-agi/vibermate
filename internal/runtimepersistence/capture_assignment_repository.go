package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
)

type captureAssignmentRepository struct {
	database         *sql.DB
	operations       *operationGate
	reconcileTimeout time.Duration
	committer        transactionCommitter
}

var _ captureassignment.Repository = (*captureAssignmentRepository)(nil)

func newCaptureAssignmentRepository(database *sql.DB, operations *operationGate, reconcileTimeout time.Duration, committer transactionCommitter) *captureAssignmentRepository {
	return &captureAssignmentRepository{
		database: database, operations: operations,
		reconcileTimeout: reconcileTimeout, committer: committer,
	}
}

func (repository *captureAssignmentRepository) Load(ctx context.Context, reference captureidentity.Reference) (captureassignment.Assignment, bool, error) {
	if reference.Validate() != nil {
		return captureassignment.Assignment{}, false, captureassignment.ErrInvalidAssignment
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return captureassignment.Assignment{}, false, err
	}
	defer permit.finish()
	return loadCaptureAssignment(permit.context, repository.database, reference)
}

func (repository *captureAssignmentRepository) ListByEnvironment(ctx context.Context, environmentID environment.EnvironmentID, limit int) ([]captureassignment.Assignment, error) {
	if _, err := environment.NewEnvironmentID(environmentID.String()); err != nil || limit <= 0 || limit > captureassignment.MaxListLimit {
		return nil, captureassignment.ErrInvalidAssignment
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer permit.finish()
	rows, err := repository.database.QueryContext(permit.context,
		`SELECT capture_kind, capture_id, environment_id, assignment_revision, source,
		        launch_environment_id, launch_environment_revision, launch_environment_digest,
		        protected_authorities_json, managed_authorities_json,
		        launch_authority_digest, updated_at_unix_ms
		 FROM capture_environment_assignments
		 WHERE environment_id = ?
		 ORDER BY capture_kind, capture_id LIMIT ?`, environmentID.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("list Capture Environment assignments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]captureassignment.Assignment, 0)
	for rows.Next() {
		assignment, scanErr := scanCaptureAssignment(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, assignment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Capture Environment assignments: %w", err)
	}
	return result, nil
}

func (repository *captureAssignmentRepository) Write(ctx context.Context, expected captureassignment.Revision, candidate captureassignment.Assignment) (captureassignment.CommitResult, error) {
	if candidate.Validate() != nil || expected >= captureassignment.MaxRevision || candidate.Revision != expected+1 {
		return captureassignment.CommitResult{Outcome: captureassignment.CommitOutcomeNotCommitted}, captureassignment.ErrInvalidAssignment
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return captureassignment.CommitResult{Outcome: captureassignment.CommitOutcomeNotCommitted}, err
	}
	defer permit.finish()
	transaction, err := repository.database.BeginTx(permit.context, nil)
	if err != nil {
		return captureassignment.CommitResult{Outcome: captureassignment.CommitOutcomeNotCommitted}, fmt.Errorf("begin Capture assignment transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	launchEnvironmentID, launchRevision, launchEnvironmentDigest, protectedJSON, managedJSON, launchDigest :=
		captureAssignmentAuthorityValues(candidate.LaunchAuthority)
	var result sql.Result
	if expected == 0 {
		result, err = transaction.ExecContext(permit.context,
			`INSERT INTO capture_environment_assignments(
			   capture_kind, capture_id, environment_id, assignment_revision, source,
			   launch_environment_id, launch_environment_revision, launch_environment_digest,
			   protected_authorities_json, managed_authorities_json,
			   launch_authority_digest, updated_at_unix_ms
			 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(capture_kind, capture_id) DO NOTHING`,
			string(candidate.Capture.Kind), candidate.Capture.ID, candidate.EnvironmentID.String(),
			int64(candidate.Revision), string(candidate.Source), launchEnvironmentID, launchRevision,
			launchEnvironmentDigest, protectedJSON, managedJSON, launchDigest,
			candidate.UpdatedAt.UnixMilli())
	} else {
		result, err = transaction.ExecContext(permit.context,
			`UPDATE capture_environment_assignments
			 SET environment_id = ?, assignment_revision = ?, source = ?, updated_at_unix_ms = ?
			 WHERE capture_kind = ? AND capture_id = ? AND assignment_revision = ?
			   AND launch_environment_id = ? AND launch_environment_revision = ?
			   AND launch_environment_digest = ?
			   AND protected_authorities_json = ? AND managed_authorities_json = ?
			   AND launch_authority_digest = ?`,
			candidate.EnvironmentID.String(), int64(candidate.Revision), string(candidate.Source),
			candidate.UpdatedAt.UnixMilli(), string(candidate.Capture.Kind), candidate.Capture.ID,
			int64(expected), launchEnvironmentID, launchRevision, launchEnvironmentDigest, protectedJSON, managedJSON,
			launchDigest)
	}
	if err != nil {
		return captureassignment.CommitResult{Outcome: captureassignment.CommitOutcomeNotCommitted}, fmt.Errorf("write Capture Environment assignment: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return captureassignment.CommitResult{Outcome: captureassignment.CommitOutcomeNotCommitted}, err
	}
	if affected != 1 {
		current, exists, loadErr := loadCaptureAssignment(permit.context, transaction, candidate.Capture)
		if loadErr != nil {
			return captureassignment.CommitResult{Outcome: captureassignment.CommitOutcomeIndeterminate}, loadErr
		}
		actual := captureassignment.Revision(0)
		if exists {
			actual = current.Revision
		}
		return captureassignment.CommitResult{
			Outcome: captureassignment.CommitOutcomeConflict, Assignment: current, Actual: actual,
		}, nil
	}
	commitErr := repository.committer.Commit(transaction)
	if commitErr == nil {
		return captureassignment.CommitResult{
			Outcome: captureassignment.CommitOutcomeCommitted, Assignment: candidate, Actual: candidate.Revision,
		}, nil
	}
	_ = transaction.Rollback()
	reconcileContext, cancel := context.WithTimeout(permit.ownerContext, repository.reconcileTimeout)
	defer cancel()
	current, exists, reconcileErr := loadCaptureAssignment(reconcileContext, repository.database, candidate.Capture)
	if reconcileErr != nil {
		return captureassignment.CommitResult{Outcome: captureassignment.CommitOutcomeIndeterminate}, errors.Join(commitErr, reconcileErr)
	}
	if exists && current == candidate {
		return captureassignment.CommitResult{
			Outcome: captureassignment.CommitOutcomeCommitted, Assignment: current, Actual: current.Revision,
		}, nil
	}
	if (!exists && expected == 0) || (exists && current.Revision == expected) {
		return captureassignment.CommitResult{
			Outcome: captureassignment.CommitOutcomeNotCommitted, Assignment: current, Actual: current.Revision,
		}, commitErr
	}
	return captureassignment.CommitResult{
		Outcome: captureassignment.CommitOutcomeIndeterminate, Assignment: current, Actual: current.Revision,
	}, commitErr
}

type captureAssignmentRow interface{ Scan(...any) error }

func scanCaptureAssignment(row captureAssignmentRow) (captureassignment.Assignment, error) {
	var kind, id, environmentID, source, launchEnvironmentID string
	var protectedJSON, managedJSON string
	var revision, launchRevision, updatedAt int64
	var launchEnvironmentDigestBytes, launchDigestBytes []byte
	if err := row.Scan(
		&kind, &id, &environmentID, &revision, &source,
		&launchEnvironmentID, &launchRevision, &launchEnvironmentDigestBytes,
		&protectedJSON, &managedJSON, &launchDigestBytes, &updatedAt,
	); err != nil {
		return captureassignment.Assignment{}, err
	}
	reference, err := captureidentity.New(captureidentity.Kind(kind), id)
	if err != nil || revision <= 0 || updatedAt <= 0 {
		return captureassignment.Assignment{}, captureassignment.ErrInvalidAssignment
	}
	parsedEnvironmentID, err := environment.NewEnvironmentID(environmentID)
	if err != nil {
		return captureassignment.Assignment{}, captureassignment.ErrInvalidAssignment
	}
	protected, err := decodeCanonicalAuthorityJSON(protectedJSON)
	if err != nil {
		return captureassignment.Assignment{}, captureassignment.ErrInvalidAssignment
	}
	managed, err := decodeCanonicalAuthorityJSON(managedJSON)
	if err != nil || launchRevision <= 0 ||
		len(launchEnvironmentDigestBytes) != 32 || len(launchDigestBytes) != 32 {
		return captureassignment.Assignment{}, captureassignment.ErrInvalidAssignment
	}
	var launchEnvironmentDigest environment.CandidateDigest
	copy(launchEnvironmentDigest[:], launchEnvironmentDigestBytes)
	var launchDigest environment.LaunchAuthorityDigest
	copy(launchDigest[:], launchDigestBytes)
	parsedLaunchEnvironmentID, err := environment.NewEnvironmentID(launchEnvironmentID)
	if err != nil {
		return captureassignment.Assignment{}, captureassignment.ErrInvalidAssignment
	}
	launchAuthority, err := environment.RestoreLaunchAuthorityBoundary(
		parsedLaunchEnvironmentID, environment.Revision(launchRevision), launchEnvironmentDigest,
		protected, managed, launchDigest,
	)
	if err != nil {
		return captureassignment.Assignment{}, captureassignment.ErrInvalidAssignment
	}
	assignment := captureassignment.Assignment{
		Capture: reference, EnvironmentID: parsedEnvironmentID,
		Revision: captureassignment.Revision(revision), Source: captureassignment.Source(source),
		LaunchAuthority: launchAuthority, UpdatedAt: time.UnixMilli(updatedAt).UTC(),
	}
	if err := assignment.Validate(); err != nil {
		return captureassignment.Assignment{}, err
	}
	return assignment, nil
}

func loadCaptureAssignment(ctx context.Context, querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, reference captureidentity.Reference) (captureassignment.Assignment, bool, error) {
	assignment, err := scanCaptureAssignment(querier.QueryRowContext(ctx,
		`SELECT capture_kind, capture_id, environment_id, assignment_revision, source,
		        launch_environment_id, launch_environment_revision, launch_environment_digest,
		        protected_authorities_json, managed_authorities_json,
		        launch_authority_digest, updated_at_unix_ms
		 FROM capture_environment_assignments
		 WHERE capture_kind = ? AND capture_id = ?`, string(reference.Kind), reference.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return captureassignment.Assignment{}, false, nil
	}
	if err != nil {
		return captureassignment.Assignment{}, false, fmt.Errorf("load Capture Environment assignment: %w", err)
	}
	return assignment, true, nil
}

func captureAssignmentAuthorityValues(boundary environment.LaunchAuthorityBoundary) (
	string,
	int64,
	[]byte,
	string,
	string,
	[]byte,
) {
	protected, _ := json.Marshal(boundary.ProtectedAuthorities())
	managed, _ := json.Marshal(boundary.ManagedCredentialAuthorities())
	environmentDigest := boundary.InitialEnvironmentDigest()
	boundaryDigest := boundary.Digest()
	return boundary.InitialEnvironmentID().String(), int64(boundary.InitialEnvironmentRevision()),
		append([]byte(nil), environmentDigest[:]...), string(protected), string(managed),
		append([]byte(nil), boundaryDigest[:]...)
}

func decodeCanonicalAuthorityJSON(encoded string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil || values == nil {
		return nil, captureassignment.ErrInvalidAssignment
	}
	reencoded, err := json.Marshal(values)
	if err != nil || string(reencoded) != encoded {
		return nil, captureassignment.ErrInvalidAssignment
	}
	return values, nil
}
