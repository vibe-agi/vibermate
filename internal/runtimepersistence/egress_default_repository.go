package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
)

type egressDefaultRepository struct {
	database, reads  *sql.DB
	operations       *operationGate
	reconcileTimeout time.Duration
	committer        transactionCommitter
}

func (s *Store) EgressDefaultRepository() egressprofile.DefaultRepository { return s.egressDefault }

// Both setting and published profile are read from one SQL snapshot.
func loadEgressDefault(ctx context.Context, tx *sql.Tx) (egressprofile.DefaultSetting, error) {
	var revision, count int64
	var id sql.NullString
	var profileRevision sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT revision,egress_id,egress_revision,(SELECT count(*) FROM runtime_egress_default) FROM runtime_egress_default WHERE singleton=1`).Scan(&revision, &id, &profileRevision, &count)
	if err != nil || count != 1 || revision <= 0 || id.Valid != profileRevision.Valid {
		return egressprofile.DefaultSetting{}, errors.Join(egressprofile.ErrDefaultUnavailable, err)
	}
	setting := egressprofile.DefaultSetting{Revision: uint64(revision)}
	if id.Valid {
		parsed, err := egressprofile.NewID(id.String)
		if err != nil || profileRevision.Int64 <= 0 {
			return setting, egressprofile.ErrDefaultUnavailable
		}
		p := egressprofile.Direct()
		if parsed == egressprofile.DirectID {
			if profileRevision.Int64 != 1 {
				return setting, egressprofile.ErrDefaultUnavailable
			}
		} else {
			var exists bool
			p, exists, err = loadEgressProfileRevision(ctx, tx, parsed, egressprofile.Revision(profileRevision.Int64))
			if err != nil || !exists {
				return setting, errors.Join(egressprofile.ErrDefaultUnavailable, err)
			}
		}
		if _, err := egressprofile.RestoreResolved(p, egressprofile.SelectionGlobal, setting.Revision); err != nil {
			return setting, egressprofile.ErrDefaultUnavailable
		}
		setting.Profile = &p
	}
	return setting, nil
}
func (r *egressDefaultRepository) LoadDefault(ctx context.Context) (egressprofile.DefaultSetting, error) {
	permit, err := r.operations.admit(ctx)
	if err != nil {
		return egressprofile.DefaultSetting{}, err
	}
	defer permit.finish()
	return r.readSnapshot(permit.context)
}
func (r *egressDefaultRepository) readSnapshot(ctx context.Context) (egressprofile.DefaultSetting, error) {
	tx, err := r.reads.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return egressprofile.DefaultSetting{}, err
	}
	defer tx.Rollback()
	setting, err := loadEgressDefault(ctx, tx)
	if err != nil {
		return egressprofile.DefaultSetting{}, err
	}
	if err = tx.Commit(); err != nil {
		return egressprofile.DefaultSetting{}, err
	}
	return setting, nil
}
func sameDefault(a, b egressprofile.DefaultSetting) bool {
	return a.Revision == b.Revision && ((a.Profile == nil && b.Profile == nil) || (a.Profile != nil && b.Profile != nil && a.Profile.Equal(*b.Profile)))
}
func (r *egressDefaultRepository) WriteDefault(ctx context.Context, expected uint64, candidate egressprofile.DefaultSetting) (egressprofile.DefaultCommitResult, error) {
	if expected == 0 || expected >= uint64(egressprofile.MaxRevision) || candidate.Revision != expected+1 {
		return egressprofile.DefaultCommitResult{}, egressprofile.ErrInvalidProfile
	}
	if candidate.Profile != nil {
		if _, err := egressprofile.RestoreResolved(*candidate.Profile, egressprofile.SelectionGlobal, candidate.Revision); err != nil {
			return egressprofile.DefaultCommitResult{}, err
		}
		p := *candidate.Profile
		candidate.Profile = &p
	}
	permit, err := r.operations.admit(ctx)
	if err != nil {
		return egressprofile.DefaultCommitResult{}, err
	}
	defer permit.finish()
	tx, err := r.database.BeginTx(permit.context, nil)
	if err != nil {
		return egressprofile.DefaultCommitResult{}, err
	}
	defer tx.Rollback()
	current, err := loadEgressDefault(permit.context, tx)
	if err != nil {
		return egressprofile.DefaultCommitResult{}, err
	}
	if current.Revision != expected {
		return egressprofile.DefaultCommitResult{Outcome: egressprofile.CommitConflict, Setting: current}, nil
	}
	var id, rev any
	if candidate.Profile != nil {
		p := *candidate.Profile
		if p.ID != egressprofile.DirectID {
			stored, exists, err := loadEgressProfileRevision(permit.context, tx, p.ID, p.Revision)
			if err != nil {
				return egressprofile.DefaultCommitResult{}, err
			}
			if !exists || !stored.Equal(p) {
				return egressprofile.DefaultCommitResult{}, egressprofile.ErrInvalidProfile
			}
		}
		id, rev = p.ID.String(), int64(p.Revision)
	}
	result, err := tx.ExecContext(permit.context, `UPDATE runtime_egress_default SET revision=?,egress_id=?,egress_revision=? WHERE singleton=1 AND revision=?`, int64(candidate.Revision), id, rev, int64(expected))
	if err != nil {
		return egressprofile.DefaultCommitResult{}, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return egressprofile.DefaultCommitResult{}, errors.Join(egressprofile.ErrDefaultUnavailable, err)
	}
	commitErr := r.committer.Commit(tx)
	if commitErr == nil {
		return egressprofile.DefaultCommitResult{Outcome: egressprofile.CommitCommitted, Setting: candidate}, nil
	}
	_ = tx.Rollback()
	proof, cancel := context.WithTimeout(permit.ownerContext, r.reconcileTimeout)
	defer cancel()
	stored, readErr := r.readSnapshot(proof)
	if readErr != nil {
		return egressprofile.DefaultCommitResult{Outcome: egressprofile.CommitIndeterminate}, errors.Join(commitErr, readErr)
	}
	if sameDefault(stored, candidate) {
		return egressprofile.DefaultCommitResult{Outcome: egressprofile.CommitCommitted, Setting: stored}, nil
	}
	if sameDefault(stored, current) {
		return egressprofile.DefaultCommitResult{Outcome: egressprofile.CommitNotCommitted, Setting: stored}, commitErr
	}
	return egressprofile.DefaultCommitResult{Outcome: egressprofile.CommitIndeterminate, Setting: stored}, fmt.Errorf("default commit outcome uncertain: %w", commitErr)
}
