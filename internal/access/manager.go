package access

import (
	"context"
	"errors"
	"fmt"
)

// Writer performs serialized Access aggregate mutations.
type Writer interface {
	WriteAccess(context.Context, WriteCommand) (WriteResult, error)
}

// Manager owns Access recovery, write serialization, and snapshot publication.
type Manager struct {
	repository Repository
	projection SnapshotProjection
	writer     chan struct{}
	lifecycle  *lifecycleGate
}

var (
	_ Writer                 = (*Manager)(nil)
	_ SnapshotResolver       = (*Manager)(nil)
	_ ProjectionHealthReader = (*Manager)(nil)
)

func NewManager(
	ctx context.Context,
	repository Repository,
	projection SnapshotProjection,
) (*Manager, error) {
	if ctx == nil {
		return nil, errors.New("Access recovery context is nil")
	}
	if repository == nil {
		return nil, errors.New("Access repository is nil")
	}
	if projection == nil {
		return nil, errors.New("Access snapshot projection is nil")
	}

	records, err := repository.LoadAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("recover Access aggregates: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("recover Access aggregates: %w", err)
	}
	snapshots := make([]Snapshot, 0, len(records))
	for _, record := range records {
		snapshot, snapshotErr := record.snapshot()
		if snapshotErr != nil {
			return nil, fmt.Errorf(
				"%w: recover accessId=%q: %w",
				ErrInvalidRepositoryState,
				record.AccessID.String(),
				snapshotErr,
			)
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := projection.Restore(snapshots); err != nil {
		return nil, fmt.Errorf("restore Access snapshot projection: %w", err)
	}

	writer := make(chan struct{}, 1)
	writer <- struct{}{}
	return &Manager{
		repository: repository,
		projection: projection,
		writer:     writer,
		lifecycle:  newLifecycleGate(),
	}, nil
}

func (m *Manager) ResolveAccess(accessID AccessID) (Snapshot, error) {
	return m.projection.ResolveAccess(accessID)
}

func (m *Manager) ProjectionHealth() ProjectionHealth {
	return m.projection.ProjectionHealth()
}

// WriteAccess serializes CAS read through pointer publication. SQLite commits
// before the process-local projection becomes observable.
func (m *Manager) WriteAccess(
	ctx context.Context,
	command WriteCommand,
) (WriteResult, error) {
	if ctx == nil {
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonInvalidAccess,
			ErrInvalidAccess,
			command.AccessID,
			command.ExpectedRevision,
			0,
			errors.New("Access write context is nil"),
		)
	}
	if err := command.validate(); err != nil {
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonInvalidAccess,
			ErrInvalidAccess,
			command.AccessID,
			command.ExpectedRevision,
			0,
			err,
		)
	}

	operationContext, finish, err := m.lifecycle.begin(ctx)
	if err != nil {
		if !errors.Is(err, ErrAccessRuntimeStopping) {
			return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
				ReasonWriteNotCommitted,
				ErrWriteNotCommitted,
				command.AccessID,
				command.ExpectedRevision,
				0,
				err,
			)
		}
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonAccessRuntimeStopping,
			ErrAccessRuntimeStopping,
			command.AccessID,
			command.ExpectedRevision,
			0,
			err,
		)
	}
	defer finish()

	select {
	case <-m.writer:
		defer func() {
			m.writer <- struct{}{}
		}()
	case <-operationContext.Done():
		cause := context.Cause(operationContext)
		if errors.Is(cause, ErrAccessRuntimeStopping) {
			return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
				ReasonAccessRuntimeStopping,
				ErrAccessRuntimeStopping,
				command.AccessID,
				command.ExpectedRevision,
				0,
				cause,
			)
		}
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonWriteNotCommitted,
			ErrWriteNotCommitted,
			command.AccessID,
			command.ExpectedRevision,
			0,
			cause,
		)
	}

	candidate := Record{
		AccessID: command.AccessID,
		Revision: command.ExpectedRevision + 1,
		Binding:  command.Binding,
	}
	snapshot, snapshotErr := candidate.snapshot()
	if snapshotErr != nil {
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonInvalidAccess,
			ErrInvalidAccess,
			command.AccessID,
			command.ExpectedRevision,
			0,
			snapshotErr,
		)
	}
	if _, resolveErr := m.projection.ResolveAccess(command.AccessID); errors.Is(
		resolveErr,
		ErrProjectionUnavailable,
	) {
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonProjectionUnavailable,
			ErrProjectionUnavailable,
			command.AccessID,
			command.ExpectedRevision,
			0,
			resolveErr,
		)
	}
	commitResult, commitErr := m.repository.CompareAndSwap(
		operationContext,
		Mutation{
			ExpectedRevision: command.ExpectedRevision,
			Candidate:        candidate,
		},
	)

	switch commitResult.Outcome {
	case CommitOutcomeCommitted:
		if commitErr != nil ||
			commitResult.Record != candidate ||
			commitResult.ActualRevision != candidate.Revision {
			m.projection.MarkUnavailable(command.AccessID)
			return WriteResult{Outcome: WriteOutcomeIndeterminate}, newFailure(
				ReasonCommitOutcomeUnknown,
				ErrCommitOutcomeUnknown,
				command.AccessID,
				command.ExpectedRevision,
				commitResult.ActualRevision,
				errors.Join(commitErr, ErrInvalidRepositoryState),
			)
		}
		// Caller cancellation after a known commit is deliberately not checked.
		if publishErr := m.projection.Publish(snapshot); publishErr != nil {
			m.projection.MarkUnavailable(command.AccessID)
			return WriteResult{
					Outcome:  WriteOutcomeCommitted,
					Snapshot: snapshot,
				}, newFailure(
					ReasonProjectionUnavailable,
					ErrProjectionUnavailable,
					command.AccessID,
					command.ExpectedRevision,
					candidate.Revision,
					publishErr,
				)
		}
		return WriteResult{
			Outcome:  WriteOutcomeCommitted,
			Snapshot: snapshot,
		}, nil

	case CommitOutcomeConflict:
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonRevisionConflict,
			ErrRevisionConflict,
			command.AccessID,
			command.ExpectedRevision,
			commitResult.ActualRevision,
			commitErr,
		)

	case CommitOutcomeNotConfigured:
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonAccessNotConfigured,
			ErrAccessNotConfigured,
			command.AccessID,
			command.ExpectedRevision,
			0,
			commitErr,
		)

	case CommitOutcomeNotCommitted:
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonWriteNotCommitted,
			ErrWriteNotCommitted,
			command.AccessID,
			command.ExpectedRevision,
			commitResult.ActualRevision,
			commitErr,
		)

	case CommitOutcomeIndeterminate:
		m.projection.MarkUnavailable(command.AccessID)
		return WriteResult{Outcome: WriteOutcomeIndeterminate}, newFailure(
			ReasonCommitOutcomeUnknown,
			ErrCommitOutcomeUnknown,
			command.AccessID,
			command.ExpectedRevision,
			commitResult.ActualRevision,
			commitErr,
		)

	default:
		m.projection.MarkUnavailable(command.AccessID)
		return WriteResult{Outcome: WriteOutcomeIndeterminate}, newFailure(
			ReasonCommitOutcomeUnknown,
			ErrCommitOutcomeUnknown,
			command.AccessID,
			command.ExpectedRevision,
			commitResult.ActualRevision,
			fmt.Errorf("%w: outcome=%q", ErrInvalidRepositoryState, commitResult.Outcome),
		)
	}
}

// Shutdown rejects new writes, cancels pre-commit work, and drains operations
// through their commit-to-publish boundary.
func (m *Manager) Shutdown(ctx context.Context) error {
	return m.lifecycle.closeAndDrain(ctx)
}
