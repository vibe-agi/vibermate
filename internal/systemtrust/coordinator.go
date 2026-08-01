package systemtrust

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	DefaultCommandTimeout        = 15 * time.Second
	DefaultReconciliationTimeout = 5 * time.Second
)

type CoordinatorOptions struct {
	OwnerContext          context.Context
	CommandTimeout        time.Duration
	ReconciliationTimeout time.Duration
}

func DefaultCoordinatorOptions(ownerContext context.Context) CoordinatorOptions {
	return CoordinatorOptions{
		OwnerContext:          ownerContext,
		CommandTimeout:        DefaultCommandTimeout,
		ReconciliationTimeout: DefaultReconciliationTimeout,
	}
}

func (options CoordinatorOptions) valid() bool {
	return options.OwnerContext != nil && options.CommandTimeout > 0 &&
		options.ReconciliationTimeout > 0
}

// Coordinator owns fail-fast mutation admission and post-command
// reconciliation. It has no production composition in this slice.
type Coordinator struct {
	source  CurrentPublicRootSource
	adapter *MacOSAdapter
	options CoordinatorOptions

	ownerContext context.Context
	cancelOwner  context.CancelCauseFunc
	stopOwner    func() bool

	mu             sync.Mutex
	closed         bool
	drained        bool
	activePlans    int
	activeMutation bool
	activeCancel   context.CancelCauseFunc
	changed        chan struct{}
}

func NewCoordinator(
	source CurrentPublicRootSource,
	adapter *MacOSAdapter,
	options CoordinatorOptions,
) (*Coordinator, error) {
	if source == nil || adapter == nil || adapter.executor == nil ||
		!options.valid() {
		return nil, ErrInvalidOperation
	}
	ownerContext, cancelOwner := context.WithCancelCause(options.OwnerContext)
	coordinator := &Coordinator{
		source:       source,
		adapter:      adapter,
		options:      options,
		ownerContext: ownerContext,
		cancelOwner:  cancelOwner,
		changed:      make(chan struct{}),
	}
	coordinator.stopOwner = context.AfterFunc(ownerContext, func() {
		coordinator.closeAdmission(context.Cause(ownerContext))
	})
	return coordinator, nil
}

func (coordinator *Coordinator) Plan(
	ctx context.Context,
	operation Operation,
) (ChangePlan, error) {
	if coordinator == nil || ctx == nil || !operation.valid() {
		return ChangePlan{}, ErrInvalidOperation
	}
	finish, err := coordinator.beginPlan()
	if err != nil {
		return ChangePlan{}, err
	}
	defer finish()
	operationContext, cancel, stopCaller := linkedContext(
		coordinator.ownerContext,
		ctx,
	)
	defer cancel(nil)
	defer stopCaller()
	if err := operationContext.Err(); err != nil {
		return ChangePlan{}, context.Cause(operationContext)
	}
	root, err := coordinator.source.currentPublicRoot(operationContext)
	if err != nil || !root.valid() {
		return ChangePlan{}, errors.Join(ErrCurrentRootInvalid, err)
	}
	observation, err := coordinator.inspect(operationContext, root)
	if ctx.Err() != nil {
		return ChangePlan{}, context.Cause(ctx)
	}
	if operationContext.Err() != nil {
		return ChangePlan{}, context.Cause(operationContext)
	}
	if err != nil || !observation.usable() {
		return ChangePlan{}, errors.Join(ErrObservationUnknown, err)
	}
	return buildPlan(operation, root, observation)
}

func (coordinator *Coordinator) Execute(
	ctx context.Context,
	plan ChangePlan,
) (OperationResult, error) {
	if coordinator == nil || ctx == nil || !plan.Valid() {
		return OperationResult{}, ErrInvalidPlan
	}
	operationContext, finish, err := coordinator.beginMutation(ctx)
	if err != nil {
		reason := ReasonCommandIndeterminate
		var failure *OperationError
		if errors.As(err, &failure) {
			reason = failure.Reason()
		}
		return coordinator.result(
			plan,
			statusForReason(reason),
			reason,
			false,
			unknownObservationForPlan(plan),
		), err
	}
	defer finish()

	root, err := coordinator.source.currentPublicRoot(operationContext)
	if err != nil || !planMatchesRoot(plan, root) {
		if reason, cause := cancellationFailure(ctx, operationContext); reason != "" {
			return coordinator.incomplete(
				plan,
				reason,
				unknownObservationForPlan(plan),
				cause,
			)
		}
		return coordinator.result(
			plan,
			ResultStatusFailed,
			ReasonPlanStale,
			false,
			unknownObservationForPlan(plan),
		), operationFailure(ReasonPlanStale, errors.Join(ErrPlanStale, err))
	}
	precondition, err := coordinator.inspect(operationContext, root)
	if err != nil || !precondition.usable() {
		if reason, cause := cancellationFailure(ctx, operationContext); reason != "" {
			return coordinator.incomplete(
				plan,
				reason,
				unknownObservationForPlan(plan),
				cause,
			)
		}
		return coordinator.result(
				plan,
				ResultStatusFailed,
				ReasonObservationUnknown,
				false,
				unknownObservationForPlan(plan),
			), operationFailure(
				ReasonObservationUnknown,
				errors.Join(ErrObservationUnknown, err),
			)
	}
	if precondition != plan.precondition {
		return coordinator.result(
			plan,
			ResultStatusFailed,
			ReasonPlanStale,
			false,
			precondition,
		), operationFailure(ReasonPlanStale, ErrPlanStale)
	}
	root, err = coordinator.source.currentPublicRoot(operationContext)
	if err != nil || !planMatchesRoot(plan, root) {
		if reason, cause := cancellationFailure(ctx, operationContext); reason != "" {
			return coordinator.incomplete(plan, reason, precondition, cause)
		}
		return coordinator.result(
			plan,
			ResultStatusFailed,
			ReasonPlanStale,
			false,
			precondition,
		), operationFailure(ReasonPlanStale, errors.Join(ErrPlanStale, err))
	}
	if len(plan.steps) == 0 {
		return coordinator.result(
			plan,
			ResultStatusApplied,
			ReasonAlreadySatisfied,
			true,
			precondition,
		), nil
	}

	observed := precondition
	for index := 0; index < len(plan.steps); index += 2 {
		step := plan.steps[index]
		if err := ctx.Err(); err != nil {
			return coordinator.incomplete(
				plan,
				ReasonCallerCancelled,
				observed,
				context.Cause(ctx),
			)
		}
		root, err = coordinator.source.currentPublicRoot(operationContext)
		if err != nil || !planMatchesRoot(plan, root) {
			return coordinator.incomplete(
				plan,
				ReasonPlanStale,
				observed,
				errors.Join(ErrPlanStale, err),
			)
		}
		// This synchronous caller check is the command-admission cut. A
		// cancellation observed before it prevents the mutation command; a
		// cancellation after it is reconciled as an in-flight outcome.
		if reason, cause := cancellationFailure(ctx, operationContext); reason != "" {
			return coordinator.incomplete(plan, reason, observed, cause)
		}
		outcome, mutationErr := coordinator.executeMutation(
			operationContext,
			step,
			root,
		)
		reconciled, reconcileErr := coordinator.reconcile(root)
		if reconcileErr != nil || !reconciled.usable() {
			reconciled = unknownObservationForPlan(plan)
		}
		observed = reconciled

		if reason, cause := cancellationFailure(ctx, operationContext); reason != "" {
			return coordinator.incomplete(plan, reason, observed, cause)
		}
		if outcome != CommandOutcomeSucceeded || mutationErr != nil {
			reason := reasonForCommandOutcome(outcome)
			return coordinator.incomplete(plan, reason, observed, mutationErr)
		}
		if reconcileErr != nil || !observed.usable() {
			return coordinator.incomplete(
				plan,
				ReasonReconciliationUnknown,
				observed,
				reconcileErr,
			)
		}
		currentRoot, currentErr := coordinator.source.currentPublicRoot(
			operationContext,
		)
		if currentErr != nil || !planMatchesRoot(plan, currentRoot) {
			if reason, cause := cancellationFailure(ctx, operationContext); reason != "" {
				return coordinator.incomplete(plan, reason, observed, cause)
			}
			return coordinator.incomplete(
				plan,
				ReasonPlanStale,
				observed,
				errors.Join(ErrPlanStale, currentErr),
			)
		}
		if observationSatisfies(observed, plan.desired) {
			return coordinator.result(
				plan,
				ResultStatusApplied,
				ReasonApplied,
				true,
				observed,
			), nil
		}
		if !canContinue(plan.steps, index, observed) {
			return coordinator.incomplete(
				plan,
				ReasonPostconditionMismatch,
				observed,
				ErrInvalidObservation,
			)
		}
	}
	return coordinator.incomplete(
		plan,
		ReasonPostconditionMismatch,
		observed,
		ErrInvalidObservation,
	)
}

func (coordinator *Coordinator) inspect(
	parent context.Context,
	root publicRoot,
) (Observation, error) {
	ctx, cancel := context.WithTimeoutCause(
		parent,
		coordinator.options.CommandTimeout,
		ErrObservationUnknown,
	)
	defer cancel()
	return coordinator.adapter.inspect(ctx, root)
}

func (coordinator *Coordinator) executeMutation(
	parent context.Context,
	step Step,
	root publicRoot,
) (CommandOutcome, error) {
	ctx, cancel := context.WithTimeoutCause(
		parent,
		coordinator.options.CommandTimeout,
		ErrCommandInvalid,
	)
	defer cancel()
	outcome, err := coordinator.adapter.mutate(ctx, step, root)
	if errors.Is(context.Cause(ctx), ErrCommandInvalid) {
		return CommandOutcomeTimedOut, context.Cause(ctx)
	}
	return outcome, err
}

func (coordinator *Coordinator) reconcile(
	root publicRoot,
) (Observation, error) {
	ctx, cancel := context.WithTimeoutCause(
		context.Background(),
		coordinator.options.ReconciliationTimeout,
		ErrObservationUnknown,
	)
	defer cancel()
	return coordinator.adapter.inspect(ctx, root)
}

func cancellationFailure(
	caller context.Context,
	operation context.Context,
) (Reason, error) {
	if caller.Err() != nil {
		return ReasonCallerCancelled, context.Cause(caller)
	}
	if operation.Err() != nil {
		return ReasonShuttingDown, context.Cause(operation)
	}
	return "", nil
}

func (coordinator *Coordinator) incomplete(
	plan ChangePlan,
	reason Reason,
	observation Observation,
	cause error,
) (OperationResult, error) {
	return coordinator.result(
		plan,
		statusForReason(reason),
		reason,
		false,
		observation,
	), operationFailure(reason, cause)
}

func (coordinator *Coordinator) result(
	plan ChangePlan,
	status ResultStatus,
	reason Reason,
	completed bool,
	observation Observation,
) OperationResult {
	return OperationResult{
		operation:    plan.operation,
		status:       status,
		reason:       reason,
		completed:    completed,
		rootRevision: plan.rootRevision,
		rootDigest:   plan.rootDigest,
		observation:  observation,
	}
}

func reasonForCommandOutcome(outcome CommandOutcome) Reason {
	switch outcome {
	case CommandOutcomeUserCancelled:
		return ReasonUserCancelled
	case CommandOutcomePermissionDenied:
		return ReasonPermissionDenied
	case CommandOutcomeTimedOut:
		return ReasonCommandTimeout
	case CommandOutcomeFailed:
		return ReasonCommandFailed
	default:
		return ReasonCommandIndeterminate
	}
}

func observationSatisfies(current, desired Observation) bool {
	return current.Valid() && desired.Valid() && current == desired
}

func canContinue(steps []Step, index int, observed Observation) bool {
	if index+2 >= len(steps) || steps[index] != StepRemoveExactAdminTrustSettings ||
		steps[index+1] != StepInspectExactRoot ||
		steps[index+2] != StepDeleteExactCertificate {
		return false
	}
	return observed.presence == ExactPresencePresent &&
		observed.decision == TrustDecisionUntrusted
}

func unknownObservationForPlan(plan ChangePlan) Observation {
	return Observation{
		rootRevision: plan.rootRevision,
		rootDigest:   plan.rootDigest,
		target:       plan.target,
		presence:     ExactPresenceUnknown,
		decision:     TrustDecisionUnknown,
		evidence:     plan.precondition.evidence,
	}
}

func linkedContext(
	owner context.Context,
	caller context.Context,
) (context.Context, context.CancelCauseFunc, func() bool) {
	// Deriving from the owner makes shutdown cancellation synchronous. The
	// command-admission cut also checks the original caller synchronously, while
	// this callback cancels an in-flight command after that cut.
	ctx, cancel := context.WithCancelCause(owner)
	stop := context.AfterFunc(caller, func() {
		cancel(context.Cause(caller))
	})
	return ctx, cancel, stop
}

func (coordinator *Coordinator) beginPlan() (func(), error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed || coordinator.ownerContext.Err() != nil {
		coordinator.closed = true
		return nil, operationFailure(ReasonShuttingDown, ErrCoordinatorStopping)
	}
	coordinator.activePlans++
	coordinator.notifyLocked()
	return func() {
		coordinator.mu.Lock()
		coordinator.activePlans--
		coordinator.notifyLocked()
		coordinator.mu.Unlock()
	}, nil
}

func (coordinator *Coordinator) beginMutation(
	caller context.Context,
) (context.Context, func(), error) {
	if err := caller.Err(); err != nil {
		return nil, nil, operationFailure(
			ReasonCallerCancelled,
			context.Cause(caller),
		)
	}
	coordinator.mu.Lock()
	if coordinator.closed || coordinator.ownerContext.Err() != nil {
		coordinator.closed = true
		coordinator.mu.Unlock()
		return nil, nil, operationFailure(
			ReasonShuttingDown,
			ErrCoordinatorStopping,
		)
	}
	if coordinator.activeMutation {
		coordinator.mu.Unlock()
		return nil, nil, operationFailure(
			ReasonOperationInProgress,
			ErrOperationInProgress,
		)
	}
	operationContext, cancel, stopCaller := linkedContext(
		coordinator.ownerContext,
		caller,
	)
	coordinator.activeMutation = true
	coordinator.activeCancel = cancel
	coordinator.notifyLocked()
	coordinator.mu.Unlock()
	return operationContext, func() {
		stopCaller()
		cancel(nil)
		coordinator.mu.Lock()
		coordinator.activeMutation = false
		coordinator.activeCancel = nil
		coordinator.notifyLocked()
		coordinator.mu.Unlock()
	}, nil
}

func (coordinator *Coordinator) closeAdmission(cause error) {
	if coordinator == nil {
		return
	}
	coordinator.mu.Lock()
	if !coordinator.closed {
		coordinator.closed = true
		coordinator.cancelOwner(cause)
	}
	if coordinator.activeCancel != nil {
		coordinator.activeCancel(cause)
	}
	coordinator.notifyLocked()
	coordinator.mu.Unlock()
}

func (coordinator *Coordinator) notifyLocked() {
	close(coordinator.changed)
	coordinator.changed = make(chan struct{})
}

// Shutdown closes admission, cancels an active command, and waits through its
// bounded reconciliation. A timed-out call can be retried.
func (coordinator *Coordinator) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("system trust shutdown context is nil")
	}
	if coordinator == nil {
		return nil
	}
	coordinator.closeAdmission(ErrCoordinatorStopping)
	if coordinator.stopOwner != nil {
		coordinator.stopOwner()
	}
	for {
		coordinator.mu.Lock()
		if coordinator.drained {
			coordinator.mu.Unlock()
			return nil
		}
		if coordinator.activePlans == 0 && !coordinator.activeMutation {
			coordinator.drained = true
			coordinator.notifyLocked()
			coordinator.mu.Unlock()
			return nil
		}
		changed := coordinator.changed
		coordinator.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}
