package workspacedefault

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

type Manager struct {
	repository   Repository
	environments EnvironmentReader
	clock        Clock
	writes       sync.Mutex
}

var _ Controller = (*Manager)(nil)

func New(repository Repository, environments EnvironmentReader, clock Clock) (*Manager, error) {
	if repository == nil || environments == nil || clock == nil {
		return nil, errors.New("Workspace Environment default dependencies are incomplete")
	}
	return &Manager{repository: repository, environments: environments, clock: clock}, nil
}

func (manager *Manager) Get(ctx context.Context, key Key) (Record, bool, error) {
	if manager == nil || ctx == nil || key.Validate() != nil {
		return Record{}, false, ErrInvalidDefault
	}
	return manager.repository.Load(ctx, key)
}

func (manager *Manager) Resolve(ctx context.Context, scope workspaceidentity.Scope) (Record, bool, error) {
	if scope.Validate() != nil {
		return Record{}, false, ErrInvalidDefault
	}
	key := Key{MachineID: scope.MachineID(), WorkspaceID: scope.WorkspaceID()}
	record, exists, err := manager.Get(ctx, key)
	if err != nil || !exists {
		return record, exists, err
	}
	if err := manager.requireActive(ctx, record.EnvironmentID); err != nil {
		return Record{}, false, err
	}
	return record, true, nil
}

func (manager *Manager) Set(ctx context.Context, command SetCommand) (Record, error) {
	if manager == nil || ctx == nil || command.Key.Validate() != nil ||
		command.ExpectedRevision >= MaxRevision {
		return Record{}, ErrInvalidDefault
	}
	if err := manager.requireActive(ctx, command.EnvironmentID); err != nil {
		return Record{}, err
	}
	manager.writes.Lock()
	defer manager.writes.Unlock()
	current, exists, err := manager.repository.Load(ctx, command.Key)
	if err != nil {
		return Record{}, err
	}
	actual := uint64(0)
	if exists {
		actual = current.Revision
	}
	if actual != command.ExpectedRevision {
		return Record{}, ErrRevisionConflict
	}
	candidate := Record{
		Key: command.Key, EnvironmentID: command.EnvironmentID,
		Revision:  actual + 1,
		UpdatedAt: manager.clock.Now().UTC().Truncate(time.Millisecond),
	}
	result, writeErr := manager.repository.Write(ctx, actual, candidate)
	return finishWrite(candidate, result, writeErr)
}

func (manager *Manager) Clear(ctx context.Context, command ClearCommand) error {
	if manager == nil || ctx == nil || command.Key.Validate() != nil ||
		command.ExpectedRevision == 0 || command.ExpectedRevision > MaxRevision {
		return ErrInvalidDefault
	}
	manager.writes.Lock()
	defer manager.writes.Unlock()
	result, err := manager.repository.Delete(ctx, command.Key, command.ExpectedRevision)
	switch result.Outcome {
	case CommitCommitted:
		if err == nil && result.Deleted {
			return nil
		}
	case CommitConflict:
		return ErrRevisionConflict
	case CommitIndeterminate:
		return errors.Join(ErrCommitUnknown, err)
	case CommitNotCommitted:
		if result.Actual == 0 {
			return ErrDefaultNotFound
		}
	}
	return errors.Join(ErrWriteNotCommitted, err)
}

func (manager *Manager) requireActive(ctx context.Context, id environment.EnvironmentID) error {
	if id == "" || id == environment.SystemTransparentID {
		return ErrEnvironmentNotActive
	}
	snapshot, err := manager.environments.Get(ctx, id)
	if err != nil || snapshot.SystemOwned() || snapshot.Aggregate().State != environment.StateActive {
		return errors.Join(ErrEnvironmentNotActive, err)
	}
	return nil
}

func finishWrite(candidate Record, result CommitResult, err error) (Record, error) {
	switch result.Outcome {
	case CommitCommitted:
		if err == nil && result.Record == candidate {
			return result.Record, nil
		}
	case CommitConflict:
		return Record{}, ErrRevisionConflict
	case CommitIndeterminate:
		return Record{}, errors.Join(ErrCommitUnknown, err)
	case CommitNotCommitted:
	}
	return Record{}, errors.Join(ErrWriteNotCommitted, err)
}
