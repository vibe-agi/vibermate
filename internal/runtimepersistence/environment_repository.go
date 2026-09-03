package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

const environmentAggregateFormatVersion int64 = 1

type environmentRepository struct {
	database         *sql.DB
	operations       *operationGate
	reconcileTimeout time.Duration
	committer        transactionCommitter
}

var _ environment.Repository = (*environmentRepository)(nil)

func newEnvironmentRepository(database *sql.DB, operations *operationGate, reconcileTimeout time.Duration, committer transactionCommitter) *environmentRepository {
	return &environmentRepository{database: database, operations: operations, reconcileTimeout: reconcileTimeout, committer: committer}
}

func (repository *environmentRepository) LoadAllActive(ctx context.Context) ([]environment.Environment, error) {
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer permit.finish()
	rows, err := repository.database.QueryContext(permit.context,
		`SELECT counters.active_revision, revisions.environment_id, revisions.revision,
		        revisions.name, revisions.state, revisions.format_version,
		        revisions.payload_json, revisions.candidate_digest
		 FROM environment_revision_counters AS counters
		 LEFT JOIN environment_revisions AS revisions
		   ON revisions.environment_id = counters.environment_id
		  AND revisions.revision = counters.active_revision
		 ORDER BY counters.environment_id`)
	if err != nil {
		return nil, fmt.Errorf("load Environment aggregates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []environment.Environment
	for rows.Next() {
		aggregate, _, exists, scanErr := scanEnvironmentAggregateState(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if exists {
			result = append(result, aggregate)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Environment aggregates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close Environment aggregate rows: %w", err)
	}
	return result, nil
}

func (repository *environmentRepository) LoadActive(ctx context.Context, id environment.EnvironmentID) (environment.Environment, bool, error) {
	if _, err := environment.NewEnvironmentID(id.String()); err != nil {
		return environment.Environment{}, false, err
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return environment.Environment{}, false, err
	}
	defer permit.finish()
	aggregate, _, exists, err := loadEnvironmentAggregate(permit.context, repository.database, id)
	return aggregate, exists, err
}

func (repository *environmentRepository) LoadRevision(ctx context.Context, id environment.EnvironmentID, revision environment.Revision) (environment.Environment, bool, error) {
	if _, err := environment.NewEnvironmentID(id.String()); err != nil || revision == 0 {
		if err != nil {
			return environment.Environment{}, false, err
		}
		return environment.Environment{}, false, environment.ErrInvalidEnvironment
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return environment.Environment{}, false, err
	}
	defer permit.finish()
	return loadEnvironmentRevision(permit.context, repository.database, id, revision)
}

func (repository *environmentRepository) LoadDraft(ctx context.Context, id environment.EnvironmentID) (environment.Draft, bool, error) {
	if _, err := environment.NewEnvironmentID(id.String()); err != nil {
		return environment.Draft{}, false, err
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return environment.Draft{}, false, err
	}
	defer permit.finish()
	return loadEnvironmentDraft(permit.context, repository.database, id)
}

func (repository *environmentRepository) SaveDraft(ctx context.Context, mutation environment.DraftMutation) (environment.Draft, error) {
	encoded, digest, err := encodeEnvironmentCandidate(mutation.Candidate)
	if err != nil {
		return environment.Draft{}, err
	}
	if mutation.EnvironmentID != mutation.Candidate.ID || mutation.Candidate.Revision != mutation.ExpectedBaseRevision+1 || digest != mutation.CandidateDigest {
		return environment.Draft{}, environment.ErrInvalidTransition
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return environment.Draft{}, err
	}
	defer permit.finish()
	transaction, err := repository.database.BeginTx(permit.context, nil)
	if err != nil {
		return environment.Draft{}, fmt.Errorf("begin Environment draft transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(permit.context,
		`INSERT INTO environment_revision_counters(environment_id, active_revision, draft_revision)
		 VALUES (?, 0, 0) ON CONFLICT(environment_id) DO NOTHING`, mutation.EnvironmentID.String()); err != nil {
		return environment.Draft{}, fmt.Errorf("create Environment revision counter: %w", err)
	}
	result, err := transaction.ExecContext(permit.context,
		`UPDATE environment_revision_counters
		 SET draft_revision = draft_revision + 1
		 WHERE environment_id = ? AND active_revision = ?
		   AND draft_revision < 9223372036854775807
		   AND ((? = 0 AND NOT EXISTS (
		          SELECT 1 FROM environment_drafts WHERE environment_id = ?
		        )) OR (? <> 0 AND draft_revision = ? AND EXISTS (
		          SELECT 1 FROM environment_drafts
		          WHERE environment_id = ? AND draft_revision = ?
		        )))`,
		mutation.EnvironmentID.String(), int64(mutation.ExpectedBaseRevision),
		int64(mutation.ExpectedDraftRevision), mutation.EnvironmentID.String(),
		int64(mutation.ExpectedDraftRevision), int64(mutation.ExpectedDraftRevision),
		mutation.EnvironmentID.String(), int64(mutation.ExpectedDraftRevision))
	if err != nil {
		return environment.Draft{}, fmt.Errorf("advance Environment draft revision: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return environment.Draft{}, environment.ErrRevisionConflict
	}
	var allocatedDraftRevision int64
	if err := transaction.QueryRowContext(permit.context,
		`SELECT draft_revision FROM environment_revision_counters WHERE environment_id = ?`,
		mutation.EnvironmentID.String()).Scan(&allocatedDraftRevision); err != nil {
		return environment.Draft{}, fmt.Errorf("read allocated Environment draft revision: %w", err)
	}
	nextDraftRevision := environment.Revision(allocatedDraftRevision)
	if _, err := transaction.ExecContext(permit.context,
		`INSERT INTO environment_drafts(environment_id, base_revision, draft_revision, candidate_revision,
		 format_version, payload_json, candidate_digest, updated_at_unix_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(environment_id) DO UPDATE SET
		 base_revision=excluded.base_revision, draft_revision=excluded.draft_revision,
		 candidate_revision=excluded.candidate_revision, format_version=excluded.format_version,
		 payload_json=excluded.payload_json, candidate_digest=excluded.candidate_digest,
		 updated_at_unix_ms=excluded.updated_at_unix_ms`,
		mutation.EnvironmentID.String(), int64(mutation.ExpectedBaseRevision), int64(nextDraftRevision),
		int64(mutation.Candidate.Revision), environmentAggregateFormatVersion, encoded, digest[:], time.Now().UTC().UnixMilli()); err != nil {
		return environment.Draft{}, fmt.Errorf("write Environment draft: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return environment.Draft{}, fmt.Errorf("commit Environment draft: %w", err)
	}
	return environment.Draft{EnvironmentID: mutation.EnvironmentID, BaseRevision: mutation.ExpectedBaseRevision,
		Revision: nextDraftRevision, Candidate: mutation.Candidate.Clone(), CandidateDigest: digest}, nil
}

func (repository *environmentRepository) PublishDraft(ctx context.Context, mutation environment.PublishMutation) (environment.CommitResult, error) {
	encoded, digest, err := encodeEnvironmentCandidate(mutation.Candidate)
	if err != nil {
		return environment.CommitResult{Outcome: environment.CommitOutcomeNotCommitted}, err
	}
	if mutation.EnvironmentID != mutation.Candidate.ID || mutation.Candidate.Revision != mutation.ExpectedBaseRevision+1 || digest != mutation.CandidateDigest {
		return environment.CommitResult{Outcome: environment.CommitOutcomeNotCommitted}, environment.ErrInvalidTransition
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return environment.CommitResult{Outcome: environment.CommitOutcomeNotCommitted}, err
	}
	defer permit.finish()
	transaction, err := repository.database.BeginTx(permit.context, nil)
	if err != nil {
		return environment.CommitResult{Outcome: environment.CommitOutcomeNotCommitted}, fmt.Errorf("begin Environment publish transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	result, err := transaction.ExecContext(permit.context,
		`UPDATE environment_revision_counters
		 SET active_revision = ?
		 WHERE environment_id = ? AND active_revision = ? AND draft_revision = ?
		   AND EXISTS (SELECT 1 FROM environment_drafts
		     WHERE environment_id = ? AND base_revision = ? AND draft_revision = ?
		       AND candidate_revision = ? AND candidate_digest = ?)`,
		int64(mutation.Candidate.Revision), mutation.EnvironmentID.String(), int64(mutation.ExpectedBaseRevision),
		int64(mutation.DraftRevision), mutation.EnvironmentID.String(), int64(mutation.ExpectedBaseRevision),
		int64(mutation.DraftRevision), int64(mutation.Candidate.Revision), digest[:])
	if err != nil {
		return environment.CommitResult{Outcome: environment.CommitOutcomeNotCommitted}, fmt.Errorf("advance Environment active revision: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return environment.CommitResult{Outcome: environment.CommitOutcomeNotCommitted}, err
	}
	if affected != 1 {
		actual := currentEnvironmentRevision(permit.context, transaction, mutation.EnvironmentID)
		return environment.CommitResult{Outcome: environment.CommitOutcomeConflict, ActualRevision: actual}, nil
	}
	if _, err := transaction.ExecContext(permit.context,
		`INSERT INTO environment_revisions(environment_id, revision, name, state, format_version,
		 payload_json, candidate_digest, published_at_unix_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		mutation.EnvironmentID.String(), int64(mutation.Candidate.Revision), mutation.Candidate.Name,
		string(mutation.Candidate.State), environmentAggregateFormatVersion, encoded, digest[:], time.Now().UTC().UnixMilli()); err != nil {
		return environment.CommitResult{Outcome: environment.CommitOutcomeNotCommitted}, fmt.Errorf("write Environment aggregate: %w", err)
	}
	if result, err = transaction.ExecContext(permit.context,
		`DELETE FROM environment_drafts WHERE environment_id = ? AND draft_revision = ? AND candidate_digest = ?`,
		mutation.EnvironmentID.String(), int64(mutation.DraftRevision), digest[:]); err != nil {
		return environment.CommitResult{Outcome: environment.CommitOutcomeNotCommitted}, fmt.Errorf("consume Environment draft: %w", err)
	}
	deleted, _ := result.RowsAffected()
	if deleted != 1 {
		return environment.CommitResult{Outcome: environment.CommitOutcomeNotCommitted}, environment.ErrPreviewStale
	}
	commitErr := repository.committer.Commit(transaction)
	if commitErr == nil {
		return committedEnvironment(mutation.Candidate), nil
	}
	_ = transaction.Rollback()
	reconcileContext, cancel := context.WithTimeout(permit.ownerContext, repository.reconcileTimeout)
	defer cancel()
	current, currentDigest, exists, reconcileErr := loadEnvironmentAggregate(reconcileContext, repository.database, mutation.EnvironmentID)
	if reconcileErr != nil {
		return environment.CommitResult{Outcome: environment.CommitOutcomeIndeterminate}, errors.Join(commitErr, reconcileErr)
	}
	_, draftExists, draftErr := loadEnvironmentDraft(reconcileContext, repository.database, mutation.EnvironmentID)
	if draftErr != nil {
		return environment.CommitResult{Outcome: environment.CommitOutcomeIndeterminate}, errors.Join(commitErr, draftErr)
	}
	if exists && currentDigest == digest && current.Revision == mutation.Candidate.Revision && !draftExists {
		return committedEnvironment(current), nil
	}
	if (!exists && mutation.ExpectedBaseRevision == 0 && draftExists) ||
		(exists && current.Revision == mutation.ExpectedBaseRevision && draftExists) {
		return environment.CommitResult{Outcome: environment.CommitOutcomeNotCommitted, Aggregate: current, ActualRevision: current.Revision}, commitErr
	}
	return environment.CommitResult{Outcome: environment.CommitOutcomeIndeterminate, Aggregate: current, ActualRevision: current.Revision}, commitErr
}

func committedEnvironment(aggregate environment.Environment) environment.CommitResult {
	return environment.CommitResult{Outcome: environment.CommitOutcomeCommitted, Aggregate: aggregate.Clone(), ActualRevision: aggregate.Revision}
}

func encodeEnvironmentCandidate(candidate environment.Environment) ([]byte, environment.CandidateDigest, error) {
	encoded, err := environment.CanonicalJSON(candidate)
	if err != nil {
		return nil, environment.CandidateDigest{}, err
	}
	digest, err := environment.Digest(candidate)
	return encoded, digest, err
}

type environmentRow interface{ Scan(...any) error }

func scanEnvironmentAggregateState(row environmentRow) (environment.Environment, environment.CandidateDigest, bool, error) {
	var activeRevision int64
	var id, name, state sql.NullString
	var revision, format sql.NullInt64
	var encoded, digestBytes []byte
	if err := row.Scan(&activeRevision, &id, &revision, &name, &state, &format, &encoded, &digestBytes); err != nil {
		return environment.Environment{}, environment.CandidateDigest{}, false, err
	}
	if activeRevision < 0 {
		return environment.Environment{}, environment.CandidateDigest{}, false, environment.ErrInvalidRepositoryState
	}
	if activeRevision == 0 {
		if id.Valid || revision.Valid || name.Valid || state.Valid || format.Valid || encoded != nil || digestBytes != nil {
			return environment.Environment{}, environment.CandidateDigest{}, false, environment.ErrInvalidRepositoryState
		}
		return environment.Environment{}, environment.CandidateDigest{}, false, nil
	}
	if !id.Valid || !revision.Valid || !name.Valid || !state.Valid || !format.Valid || revision.Int64 != activeRevision {
		return environment.Environment{}, environment.CandidateDigest{}, false, environment.ErrInvalidRepositoryState
	}
	aggregate, digest, err := validateEnvironmentAggregateFields(
		id.String, revision.Int64, name.String, state.String, format.Int64, encoded, digestBytes,
	)
	return aggregate, digest, err == nil, err
}

func validateEnvironmentAggregateFields(id string, revision int64, name, state string, format int64, encoded, digestBytes []byte) (environment.Environment, environment.CandidateDigest, error) {
	if format != environmentAggregateFormatVersion || revision <= 0 || len(digestBytes) != 32 {
		return environment.Environment{}, environment.CandidateDigest{}, environment.ErrInvalidRepositoryState
	}
	aggregate, err := environment.DecodeCanonicalJSON(encoded)
	if err != nil {
		return environment.Environment{}, environment.CandidateDigest{}, err
	}
	parsedID, err := environment.NewEnvironmentID(id)
	if err != nil || aggregate.ID != parsedID || aggregate.Revision != environment.Revision(revision) || aggregate.Name != name || string(aggregate.State) != state {
		return environment.Environment{}, environment.CandidateDigest{}, environment.ErrInvalidRepositoryState
	}
	var digest environment.CandidateDigest
	copy(digest[:], digestBytes)
	calculated, err := environment.Digest(aggregate)
	if err != nil || calculated != digest {
		return environment.Environment{}, environment.CandidateDigest{}, environment.ErrInvalidRepositoryState
	}
	return aggregate, digest, nil
}

func loadEnvironmentAggregate(ctx context.Context, querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id environment.EnvironmentID) (environment.Environment, environment.CandidateDigest, bool, error) {
	aggregate, digest, exists, err := scanEnvironmentAggregateState(querier.QueryRowContext(ctx,
		`SELECT counters.active_revision, revisions.environment_id, revisions.revision,
		        revisions.name, revisions.state, revisions.format_version,
		        revisions.payload_json, revisions.candidate_digest
		 FROM environment_revision_counters AS counters
		 LEFT JOIN environment_revisions AS revisions
		   ON revisions.environment_id = counters.environment_id
		  AND revisions.revision = counters.active_revision
		 WHERE counters.environment_id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return environment.Environment{}, environment.CandidateDigest{}, false, nil
	}
	if err != nil {
		return environment.Environment{}, environment.CandidateDigest{}, false, fmt.Errorf("load Environment aggregate: %w", err)
	}
	return aggregate, digest, exists, nil
}

func loadEnvironmentRevision(ctx context.Context, querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id environment.EnvironmentID, revision environment.Revision) (environment.Environment, bool, error) {
	var storedID, name, state string
	var storedRevision, format int64
	var encoded, digestBytes []byte
	err := querier.QueryRowContext(ctx,
		`SELECT environment_id, revision, name, state, format_version, payload_json, candidate_digest
		 FROM environment_revisions WHERE environment_id = ? AND revision = ?`,
		id.String(), int64(revision)).Scan(
		&storedID, &storedRevision, &name, &state, &format, &encoded, &digestBytes,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return environment.Environment{}, false, nil
	}
	if err != nil {
		return environment.Environment{}, false, fmt.Errorf("load Environment revision: %w", err)
	}
	aggregate, _, err := validateEnvironmentAggregateFields(
		storedID, storedRevision, name, state, format, encoded, digestBytes,
	)
	if err != nil || aggregate.ID != id || aggregate.Revision != revision {
		if err != nil {
			return environment.Environment{}, false, err
		}
		return environment.Environment{}, false, environment.ErrInvalidRepositoryState
	}
	return aggregate, true, nil
}

func loadEnvironmentDraft(ctx context.Context, querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id environment.EnvironmentID) (environment.Draft, bool, error) {
	var base, revision, candidateRevision, format, activeAuthority, draftAuthority int64
	var encoded, digestBytes []byte
	err := querier.QueryRowContext(ctx,
		`SELECT drafts.base_revision, drafts.draft_revision, drafts.candidate_revision,
		        drafts.format_version, drafts.payload_json, drafts.candidate_digest,
		        counters.active_revision, counters.draft_revision
		 FROM environment_drafts AS drafts
		 JOIN environment_revision_counters AS counters
		   ON counters.environment_id = drafts.environment_id
		 WHERE drafts.environment_id = ?`, id.String()).Scan(
		&base, &revision, &candidateRevision, &format, &encoded, &digestBytes,
		&activeAuthority, &draftAuthority,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return environment.Draft{}, false, nil
	}
	if err != nil {
		return environment.Draft{}, false, fmt.Errorf("load Environment draft: %w", err)
	}
	if base < 0 || revision <= 0 || candidateRevision != base+1 ||
		base != activeAuthority || revision != draftAuthority ||
		format != environmentAggregateFormatVersion || len(digestBytes) != 32 {
		return environment.Draft{}, false, environment.ErrInvalidRepositoryState
	}
	candidate, err := environment.DecodeCanonicalJSON(encoded)
	if err != nil || candidate.ID != id || candidate.Revision != environment.Revision(candidateRevision) {
		return environment.Draft{}, false, environment.ErrInvalidRepositoryState
	}
	var digest environment.CandidateDigest
	copy(digest[:], digestBytes)
	calculated, err := environment.Digest(candidate)
	if err != nil || calculated != digest {
		return environment.Draft{}, false, environment.ErrInvalidRepositoryState
	}
	return environment.Draft{EnvironmentID: id, BaseRevision: environment.Revision(base), Revision: environment.Revision(revision), Candidate: candidate, CandidateDigest: digest}, true, nil
}

func currentEnvironmentRevision(ctx context.Context, transaction *sql.Tx, id environment.EnvironmentID) environment.Revision {
	var revision int64
	if err := transaction.QueryRowContext(ctx, `SELECT active_revision FROM environment_revision_counters WHERE environment_id = ?`, id.String()).Scan(&revision); err != nil || revision < 0 {
		return 0
	}
	return environment.Revision(revision)
}

// Retire clears the active revision pointer and drops the working draft.
//
// It deliberately leaves environment_revisions alone. Those rows are what a
// frozen Exchange resolves when a user opens the exact Environment a Turn ran
// under, and environment_revisions carries a foreign key to the counter row, so
// removing the counter would be rejected anyway. Retirement is therefore the
// whole of deletion at this layer: LoadAllActive joins on the active revision,
// so a retired Environment leaves every live listing at once.
func (repository *environmentRepository) Retire(
	ctx context.Context,
	id environment.EnvironmentID,
) (bool, error) {
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return false, err
	}
	defer permit.finish()
	transaction, err := repository.database.BeginTx(permit.context, nil)
	if err != nil {
		return false, fmt.Errorf("begin Environment retirement transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	result, err := transaction.ExecContext(
		permit.context,
		`UPDATE environment_revision_counters
		 SET active_revision = 0, draft_revision = 0
		 WHERE environment_id = ? AND active_revision <> 0`,
		id.String(),
	)
	if err != nil {
		return false, fmt.Errorf("retire Environment: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected != 1 {
		return false, nil
	}
	if _, err := transaction.ExecContext(
		permit.context,
		`DELETE FROM environment_drafts WHERE environment_id = ?`,
		id.String(),
	); err != nil {
		return false, fmt.Errorf("drop retired Environment draft: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return false, fmt.Errorf("commit Environment retirement: %w", err)
	}
	return true, nil
}
