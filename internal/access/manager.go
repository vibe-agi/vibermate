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

// Catalog returns durable editable aggregates, including disabled Accesses
// that are intentionally absent from the active-plan projection.
type AggregateCatalog interface {
	ListAccesses(context.Context) ([]Aggregate, error)
	ReadAccess(context.Context, AccessID) (Aggregate, bool, error)
}

// Manager owns Access recovery, write serialization, and snapshot publication.
type Manager struct {
	repository Repository
	compiler   PlanCompiler
	projection SnapshotProjection
	writer     chan struct{}
	lifecycle  *lifecycleGate
}

var (
	_ AggregateCatalog           = (*Manager)(nil)
	_ Writer                     = (*Manager)(nil)
	_ SnapshotResolver           = (*Manager)(nil)
	_ IngressResolver            = (*Manager)(nil)
	_ DownstreamProtocolResolver = (*Manager)(nil)
	_ LeafIssuanceAdmitter       = (*Manager)(nil)
	_ IngressCatalogReader       = (*Manager)(nil)
	_ ProviderProbeCatalog       = (*Manager)(nil)
	_ ProjectionHealthReader     = (*Manager)(nil)
)

func NewManager(
	ctx context.Context,
	repository Repository,
	compiler PlanCompiler,
	projection SnapshotProjection,
) (*Manager, error) {
	if ctx == nil {
		return nil, errors.New("Access recovery context is nil")
	}
	if repository == nil {
		return nil, errors.New("Access repository is nil")
	}
	if compiler == nil {
		return nil, errors.New("Access plan compiler is nil")
	}
	if projection == nil {
		return nil, errors.New("Access plan projection is nil")
	}

	aggregates, err := repository.LoadAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("recover Access aggregates: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("recover Access aggregates: %w", err)
	}
	snapshots := make([]AccessPlanSnapshot, 0, len(aggregates))
	type withdrawal struct {
		accessID AccessID
		revision Revision
	}
	withdrawals := make([]withdrawal, 0)
	for _, aggregate := range aggregates {
		snapshot, candidate, active, compileErr := compileCandidate(
			compiler,
			aggregate,
		)
		if compileErr != nil {
			return nil, fmt.Errorf(
				"%w: recover accessId=%q: %w",
				ErrInvalidRepositoryState,
				aggregate.Binding.ID.String(),
				compileErr,
			)
		}
		if active {
			snapshots = append(snapshots, snapshot)
		} else {
			withdrawals = append(withdrawals, withdrawal{
				accessID: candidate.Binding.ID,
				revision: candidate.Binding.Revision,
			})
		}
	}
	if err := projection.Restore(snapshots); err != nil {
		return nil, fmt.Errorf("restore Access plan projection: %w", err)
	}
	for _, withdrawn := range withdrawals {
		if err := projection.Withdraw(
			withdrawn.accessID,
			withdrawn.revision,
		); err != nil {
			return nil, fmt.Errorf(
				"%w: restore disabled accessId=%q: %w",
				ErrInvalidRepositoryState,
				withdrawn.accessID.String(),
				err,
			)
		}
	}

	writer := make(chan struct{}, 1)
	writer <- struct{}{}
	return &Manager{
		repository: repository,
		compiler:   compiler,
		projection: projection,
		writer:     writer,
		lifecycle:  newLifecycleGate(),
	}, nil
}

func (m *Manager) ListAccesses(ctx context.Context) ([]Aggregate, error) {
	operationContext, finish, err := m.lifecycle.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	aggregates, err := m.repository.LoadAll(operationContext)
	if err != nil {
		return nil, fmt.Errorf("list Access aggregates: %w", err)
	}
	cloned := make([]Aggregate, len(aggregates))
	for index, aggregate := range aggregates {
		cloned[index] = aggregate.Clone()
	}
	return cloned, nil
}

func (m *Manager) ReadAccess(
	ctx context.Context,
	accessID AccessID,
) (Aggregate, bool, error) {
	operationContext, finish, err := m.lifecycle.begin(ctx)
	if err != nil {
		return Aggregate{}, false, err
	}
	defer finish()
	aggregate, exists, err := m.repository.Load(operationContext, accessID)
	if err != nil {
		return Aggregate{}, false, fmt.Errorf(
			"read Access aggregate accessId=%q: %w",
			accessID.String(),
			err,
		)
	}
	if !exists {
		return Aggregate{}, false, nil
	}
	return aggregate.Clone(), true, nil
}

func (m *Manager) ResolveAccess(accessID AccessID) (AccessPlanSnapshot, error) {
	return m.projection.ResolveAccess(accessID)
}

func (m *Manager) ResolveClientOrigin(origin ClientOrigin) (IngressBinding, error) {
	return m.projection.ResolveClientOrigin(origin)
}

func (m *Manager) ResolveDownstreamProtocols(
	binding IngressBinding,
) ([]ApplicationProtocol, error) {
	return m.projection.ResolveDownstreamProtocols(binding)
}

func (m *Manager) AdmitLeaf(
	intent LeafIssuanceIntent,
) (LeafIssuanceAdmission, error) {
	return m.projection.AdmitLeaf(intent)
}

func (m *Manager) ActiveClientAuthorities() ([]string, error) {
	return m.projection.ActiveClientAuthorities()
}

func (m *Manager) ActiveProviderProbeTargets() ([]ProviderProbeTarget, error) {
	return m.projection.ActiveProviderProbeTargets()
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
	accessID := command.accessID()
	if ctx == nil {
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonInvalidAccess,
			ErrInvalidAccess,
			accessID,
			command.ExpectedRevision,
			0,
			errors.New("Access write context is nil"),
		)
	}
	if err := command.validate(); err != nil {
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonInvalidAccess,
			ErrInvalidAccess,
			accessID,
			command.ExpectedRevision,
			0,
			err,
		)
	}
	if err := ctx.Err(); err != nil {
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonWriteNotCommitted,
			ErrWriteNotCommitted,
			accessID,
			command.ExpectedRevision,
			0,
			err,
		)
	}
	candidatePlan, candidate, active, err := compileCandidate(
		m.compiler,
		command.Aggregate,
	)
	if err != nil {
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonInvalidAccessPlan,
			ErrInvalidAccessPlan,
			accessID,
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
				accessID,
				command.ExpectedRevision,
				0,
				err,
			)
		}
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonAccessRuntimeStopping,
			ErrAccessRuntimeStopping,
			accessID,
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
				accessID,
				command.ExpectedRevision,
				0,
				cause,
			)
		}
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonWriteNotCommitted,
			ErrWriteNotCommitted,
			accessID,
			command.ExpectedRevision,
			0,
			cause,
		)
	}

	if _, resolveErr := m.projection.ResolveAccess(accessID); errors.Is(
		resolveErr,
		ErrProjectionUnavailable,
	) {
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonProjectionUnavailable,
			ErrProjectionUnavailable,
			accessID,
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
			!commitResult.Aggregate.Equal(candidate) ||
			commitResult.ActualRevision != candidate.Binding.Revision {
			m.projection.MarkUnavailable(accessID)
			return WriteResult{
					Outcome:  WriteOutcomeIndeterminate,
					Revision: commitResult.ActualRevision,
				}, newFailure(
					ReasonCommitOutcomeUnknown,
					ErrCommitOutcomeUnknown,
					accessID,
					command.ExpectedRevision,
					commitResult.ActualRevision,
					errors.Join(commitErr, ErrInvalidRepositoryState),
				)
		}
		// Caller cancellation after a known commit is deliberately not checked.
		var publishErr error
		if active {
			publishErr = m.projection.Publish(candidatePlan)
		} else {
			publishErr = m.projection.Withdraw(
				accessID,
				candidate.Binding.Revision,
			)
		}
		if publishErr != nil {
			m.projection.MarkUnavailable(accessID)
			return WriteResult{
					Outcome:  WriteOutcomeCommitted,
					Revision: candidate.Binding.Revision,
				}, newFailure(
					ReasonProjectionUnavailable,
					ErrProjectionUnavailable,
					accessID,
					command.ExpectedRevision,
					candidate.Binding.Revision,
					publishErr,
				)
		}
		return WriteResult{
			Outcome:  WriteOutcomeCommitted,
			Revision: candidate.Binding.Revision,
			PlanHash: candidatePlan.PlanHash(),
		}, nil

	case CommitOutcomeConflict:
		return WriteResult{
				Outcome:  WriteOutcomeNotCommitted,
				Revision: commitResult.ActualRevision,
			}, newFailure(
				ReasonRevisionConflict,
				ErrRevisionConflict,
				accessID,
				command.ExpectedRevision,
				commitResult.ActualRevision,
				commitErr,
			)

	case CommitOutcomeNotConfigured:
		return WriteResult{Outcome: WriteOutcomeNotCommitted}, newFailure(
			ReasonAccessNotConfigured,
			ErrAccessNotConfigured,
			accessID,
			command.ExpectedRevision,
			0,
			commitErr,
		)

	case CommitOutcomeNotCommitted:
		return WriteResult{
				Outcome:  WriteOutcomeNotCommitted,
				Revision: commitResult.ActualRevision,
			}, newFailure(
				ReasonWriteNotCommitted,
				ErrWriteNotCommitted,
				accessID,
				command.ExpectedRevision,
				commitResult.ActualRevision,
				commitErr,
			)

	case CommitOutcomeIndeterminate:
		m.projection.MarkUnavailable(accessID)
		return WriteResult{
				Outcome:  WriteOutcomeIndeterminate,
				Revision: commitResult.ActualRevision,
			}, newFailure(
				ReasonCommitOutcomeUnknown,
				ErrCommitOutcomeUnknown,
				accessID,
				command.ExpectedRevision,
				commitResult.ActualRevision,
				commitErr,
			)

	default:
		m.projection.MarkUnavailable(accessID)
		return WriteResult{
				Outcome:  WriteOutcomeIndeterminate,
				Revision: commitResult.ActualRevision,
			}, newFailure(
				ReasonCommitOutcomeUnknown,
				ErrCommitOutcomeUnknown,
				accessID,
				command.ExpectedRevision,
				commitResult.ActualRevision,
				fmt.Errorf("%w: outcome=%q", ErrInvalidRepositoryState, commitResult.Outcome),
			)
	}
}

func compileCandidate(
	compiler PlanCompiler,
	aggregate Aggregate,
) (AccessPlanSnapshot, Aggregate, bool, error) {
	if aggregate.Binding.Status == AccessStatusDisabled {
		compilable := aggregate.Clone()
		compilable.Binding.Status = AccessStatusEnabled
		plan, err := compiler.Compile(compilable)
		if err != nil {
			return AccessPlanSnapshot{}, Aggregate{}, false, err
		}
		candidate := plan.aggregate.Clone()
		candidate.Binding.Status = AccessStatusDisabled
		return AccessPlanSnapshot{}, candidate, false, nil
	}
	plan, err := compiler.Compile(aggregate)
	if err != nil {
		return AccessPlanSnapshot{}, Aggregate{}, false, err
	}
	return plan, plan.aggregate.Clone(), true, nil
}

// Shutdown rejects new writes, cancels pre-commit work, and drains operations
// through their commit-to-publish boundary.
func (m *Manager) Shutdown(ctx context.Context) error {
	return m.lifecycle.closeAndDrain(ctx)
}
