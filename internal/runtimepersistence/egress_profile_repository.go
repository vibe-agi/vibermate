package runtimepersistence

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
)

type egressProfileRepository struct {
	database         *sql.DB
	operations       *operationGate
	reconcileTimeout time.Duration
	committer        transactionCommitter
}

var _ egressprofile.Repository = (*egressProfileRepository)(nil)

func newEgressProfileRepository(
	database *sql.DB,
	operations *operationGate,
	reconcileTimeout time.Duration,
	committer transactionCommitter,
) *egressProfileRepository {
	return &egressProfileRepository{
		database: database, operations: operations,
		reconcileTimeout: reconcileTimeout, committer: committer,
	}
}

func (repository *egressProfileRepository) Write(
	ctx context.Context,
	expected egressprofile.Revision,
	candidate egressprofile.ProfileRevision,
) (egressprofile.CommitResult, error) {
	if candidate.Validate() != nil || expected >= egressprofile.MaxRevision ||
		candidate.Revision != expected+1 || candidate.ID == egressprofile.DirectID {
		return egressprofile.CommitResult{}, egressprofile.ErrInvalidProfile
	}
	policy, err := json.Marshal(candidate.Policy)
	if err != nil {
		return egressprofile.CommitResult{}, egressprofile.ErrInvalidProfile
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return egressprofile.CommitResult{}, err
	}
	defer permit.finish()
	transaction, err := repository.database.BeginTx(permit.context, nil)
	if err != nil {
		return egressprofile.CommitResult{}, fmt.Errorf("begin egress profile publish: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(
		permit.context,
		`INSERT INTO egress_profile_heads(egress_id, current_revision)
		 VALUES (?, 0) ON CONFLICT(egress_id) DO NOTHING`,
		candidate.ID.String(),
	); err != nil {
		return egressprofile.CommitResult{}, fmt.Errorf("create egress profile head: %w", err)
	}
	result, err := transaction.ExecContext(
		permit.context,
		`UPDATE egress_profile_heads SET current_revision = ?
		 WHERE egress_id = ? AND current_revision = ?`,
		int64(candidate.Revision), candidate.ID.String(), int64(expected),
	)
	if err != nil {
		return egressprofile.CommitResult{}, fmt.Errorf("advance egress profile: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return egressprofile.CommitResult{}, err
	}
	if affected != 1 {
		actual, _, _ := loadEgressProfileHead(permit.context, transaction, candidate.ID)
		return egressprofile.CommitResult{
			Outcome: egressprofile.CommitConflict, ActualRevision: actual,
		}, nil
	}
	if _, err := transaction.ExecContext(
		permit.context,
		`INSERT INTO egress_profile_revisions(
		   egress_id, revision, display_name, policy_json, published_at_unix_ms
		 ) VALUES (?, ?, ?, ?, ?)`,
		candidate.ID.String(), int64(candidate.Revision), candidate.DisplayName,
		policy, toUnixMillis(candidate.PublishedAt),
	); err != nil {
		return egressprofile.CommitResult{}, fmt.Errorf("write egress profile revision: %w", err)
	}
	commitErr := repository.committer.Commit(transaction)
	if commitErr == nil {
		return egressprofile.CommitResult{
			Outcome: egressprofile.CommitCommitted, Revision: candidate,
		}, nil
	}
	_ = transaction.Rollback()
	reconcileContext, cancel := context.WithTimeout(
		permit.ownerContext,
		repository.reconcileTimeout,
	)
	defer cancel()
	stored, exists, readErr := loadEgressProfileRevision(
		reconcileContext, repository.database, candidate.ID, candidate.Revision,
	)
	actual, headExists, headErr := loadEgressProfileHead(
		reconcileContext, repository.database, candidate.ID,
	)
	if readErr != nil || headErr != nil {
		return egressprofile.CommitResult{Outcome: egressprofile.CommitIndeterminate},
			errors.Join(commitErr, readErr, headErr)
	}
	if exists && headExists && actual == candidate.Revision && stored.Equal(candidate) {
		return egressprofile.CommitResult{
			Outcome: egressprofile.CommitCommitted, Revision: stored,
			ActualRevision: actual,
		}, nil
	}
	if !exists && ((!headExists && expected == 0) || (headExists && actual == expected)) {
		return egressprofile.CommitResult{
			Outcome: egressprofile.CommitNotCommitted, ActualRevision: actual,
		}, commitErr
	}
	return egressprofile.CommitResult{
		Outcome: egressprofile.CommitIndeterminate, Revision: stored,
		ActualRevision: actual,
	}, commitErr
}

func (repository *egressProfileRepository) LoadRevision(
	ctx context.Context,
	id egressprofile.ID,
	revision egressprofile.Revision,
) (egressprofile.ProfileRevision, bool, error) {
	parsed, err := egressprofile.NewID(id.String())
	if err != nil || parsed != id || id == egressprofile.DirectID || revision == 0 ||
		revision > egressprofile.MaxRevision {
		return egressprofile.ProfileRevision{}, false, egressprofile.ErrInvalidProfile
	}
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return egressprofile.ProfileRevision{}, false, err
	}
	defer permit.finish()
	return loadEgressProfileRevision(permit.context, repository.database, id, revision)
}

func (repository *egressProfileRepository) LoadCurrent(
	ctx context.Context,
) ([]egressprofile.ProfileRevision, error) {
	permit, err := repository.operations.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer permit.finish()
	rows, err := repository.database.QueryContext(
		permit.context,
		`SELECT revisions.egress_id, revisions.revision, revisions.display_name,
		        revisions.policy_json, revisions.published_at_unix_ms
		 FROM egress_profile_heads AS heads
		 JOIN egress_profile_revisions AS revisions
		   ON revisions.egress_id = heads.egress_id
		  AND revisions.revision = heads.current_revision
		 ORDER BY revisions.egress_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list egress profiles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	profiles := []egressprofile.ProfileRevision{}
	for rows.Next() {
		profile, scanErr := scanEgressProfileRevision(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return profiles, nil
}

func loadEgressProfileRevision(
	ctx context.Context,
	query interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	id egressprofile.ID,
	revision egressprofile.Revision,
) (egressprofile.ProfileRevision, bool, error) {
	profile, err := scanEgressProfileRevision(query.QueryRowContext(
		ctx,
		`SELECT egress_id, revision, display_name, policy_json, published_at_unix_ms
		 FROM egress_profile_revisions WHERE egress_id = ? AND revision = ?`,
		id.String(), int64(revision),
	))
	if err == sql.ErrNoRows {
		return egressprofile.ProfileRevision{}, false, nil
	}
	if err != nil {
		return egressprofile.ProfileRevision{}, false,
			fmt.Errorf("read egress profile revision: %w", err)
	}
	if profile.ID != id || profile.Revision != revision {
		return egressprofile.ProfileRevision{}, false, egressprofile.ErrInvalidProfile
	}
	return profile, true, nil
}

func loadEgressProfileHead(
	ctx context.Context,
	query interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	id egressprofile.ID,
) (egressprofile.Revision, bool, error) {
	var revision int64
	err := query.QueryRowContext(
		ctx,
		`SELECT current_revision FROM egress_profile_heads WHERE egress_id = ?`,
		id.String(),
	).Scan(&revision)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read egress profile head: %w", err)
	}
	if revision < 0 || uint64(revision) > uint64(egressprofile.MaxRevision) {
		return 0, false, egressprofile.ErrInvalidProfile
	}
	return egressprofile.Revision(revision), true, nil
}

type egressProfileScanner interface{ Scan(...any) error }

func scanEgressProfileRevision(scanner egressProfileScanner) (egressprofile.ProfileRevision, error) {
	var id, displayName string
	var revision, publishedAt int64
	var policyJSON []byte
	if err := scanner.Scan(&id, &revision, &displayName, &policyJSON, &publishedAt); err != nil {
		return egressprofile.ProfileRevision{}, err
	}
	var policy egressnetwork.Policy
	decoder := json.NewDecoder(bytes.NewReader(policyJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return egressprofile.ProfileRevision{}, egressprofile.ErrInvalidProfile
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return egressprofile.ProfileRevision{}, egressprofile.ErrInvalidProfile
	}
	profile := egressprofile.ProfileRevision{
		ID: egressprofile.ID(id), Revision: egressprofile.Revision(revision),
		DisplayName: displayName, Policy: policy, PublishedAt: fromUnixMillis(publishedAt),
	}
	if revision <= 0 || profile.Validate() != nil {
		return egressprofile.ProfileRevision{}, egressprofile.ErrInvalidProfile
	}
	return profile, nil
}
