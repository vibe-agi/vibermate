package systemtrust

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCoordinatorExecutesTypedStepsAndReinspectsEachMutation(t *testing.T) {
	root := testPublicRoot(t)
	tests := []struct {
		name          string
		operation     Operation
		initial       machineState
		wantMutations []CommandKind
		wantFinal     machineState
		already       bool
	}{
		{
			name:      "install",
			operation: OperationInstall,
			initial: machineState{
				presence: ExactPresenceAbsent,
				decision: TrustDecisionUntrusted,
			},
			wantMutations: []CommandKind{CommandEnsureExactTrust},
			wantFinal: machineState{
				presence: ExactPresencePresent,
				decision: TrustDecisionTrusted,
			},
		},
		{
			name:      "install already satisfied",
			operation: OperationInstall,
			initial: machineState{
				presence: ExactPresencePresent,
				decision: TrustDecisionTrusted,
			},
			wantFinal: machineState{
				presence: ExactPresencePresent,
				decision: TrustDecisionTrusted,
			},
			already: true,
		},
		{
			name:      "remove trusted and present",
			operation: OperationRemove,
			initial: machineState{
				presence: ExactPresencePresent,
				decision: TrustDecisionTrusted,
			},
			wantMutations: []CommandKind{
				CommandRemoveExactTrust,
				CommandDeleteExactObject,
			},
			wantFinal: machineState{
				presence: ExactPresenceAbsent,
				decision: TrustDecisionUntrusted,
			},
		},
		{
			name:      "remove residual object",
			operation: OperationRemove,
			initial: machineState{
				presence: ExactPresencePresent,
				decision: TrustDecisionUntrusted,
			},
			wantMutations: []CommandKind{CommandDeleteExactObject},
			wantFinal: machineState{
				presence: ExactPresenceAbsent,
				decision: TrustDecisionUntrusted,
			},
		},
		{
			name:      "remove already satisfied",
			operation: OperationRemove,
			initial: machineState{
				presence: ExactPresenceAbsent,
				decision: TrustDecisionUntrusted,
			},
			wantFinal: machineState{
				presence: ExactPresenceAbsent,
				decision: TrustDecisionUntrusted,
			},
			already: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := newMachineExecutor(root, test.initial)
			coordinator, _ := newTestCoordinator(t, root, executor)
			plan, err := coordinator.Plan(context.Background(), test.operation)
			if err != nil {
				t.Fatal(err)
			}
			result, err := coordinator.Execute(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			wantReason := ReasonApplied
			if test.already {
				wantReason = ReasonAlreadySatisfied
			}
			if !result.Completed() || result.Status() != ResultStatusApplied ||
				result.Reason() != wantReason ||
				result.Observation().Presence() != test.wantFinal.presence ||
				result.Observation().TrustDecision() != test.wantFinal.decision {
				t.Fatalf("unexpected result: %+v", result)
			}
			calls := executor.callKinds()
			var mutations []CommandKind
			for index, kind := range calls {
				if kind != CommandEnsureExactTrust &&
					kind != CommandRemoveExactTrust &&
					kind != CommandDeleteExactObject {
					continue
				}
				mutations = append(mutations, kind)
				if index+2 >= len(calls) ||
					calls[index+1] != CommandInspectExactPresence ||
					calls[index+2] != CommandInspectAdminTrust {
					t.Fatalf("mutation %s was not immediately reinspected: %v", kind, calls)
				}
			}
			if !slices.Equal(mutations, test.wantMutations) {
				t.Fatalf("unexpected mutations: %v", mutations)
			}
			executor.mu.Lock()
			artifactChecked := executor.artifactChecked
			artifactErr := executor.artifactError
			executor.mu.Unlock()
			if artifactErr != nil {
				t.Fatal(artifactErr)
			}
			if slices.Contains(test.wantMutations, CommandEnsureExactTrust) ||
				slices.Contains(test.wantMutations, CommandRemoveExactTrust) {
				if !artifactChecked {
					t.Fatal("adapter did not materialize an exact private public certificate")
				}
			}
		})
	}
}

func TestCoordinatorRejectsStaleRevisionDigestAndObservation(t *testing.T) {
	root := testPublicRoot(t)
	tests := []struct {
		name   string
		mutate func(*ChangePlan, *mutableRootSource, *machineExecutor)
	}{
		{
			name: "Root revision",
			mutate: func(
				plan *ChangePlan,
				_ *mutableRootSource,
				_ *machineExecutor,
			) {
				plan.rootRevision++
				plan.desired.rootRevision++
				plan.precondition.rootRevision++
			},
		},
		{
			name: "Root digest",
			mutate: func(
				_ *ChangePlan,
				source *mutableRootSource,
				_ *machineExecutor,
			) {
				source.replace(testPublicRoot(t))
			},
		},
		{
			name: "observation precondition",
			mutate: func(
				_ *ChangePlan,
				_ *mutableRootSource,
				executor *machineExecutor,
			) {
				executor.setState(machineState{
					presence: ExactPresencePresent,
					decision: TrustDecisionUntrusted,
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := newMachineExecutor(root, machineState{
				presence: ExactPresenceAbsent,
				decision: TrustDecisionUntrusted,
			})
			coordinator, source := newTestCoordinator(t, root, executor)
			plan, err := coordinator.Plan(context.Background(), OperationInstall)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&plan, source, executor)
			result, err := coordinator.Execute(context.Background(), plan)
			if !errors.Is(err, ErrPlanStale) || result.Completed() ||
				result.Reason() != ReasonPlanStale {
				t.Fatalf("stale plan was not rejected: result=%+v error=%v", result, err)
			}
			for _, kind := range executor.callKinds() {
				if kind == CommandEnsureExactTrust || kind == CommandRemoveExactTrust ||
					kind == CommandDeleteExactObject {
					t.Fatalf("stale plan invoked mutation %s", kind)
				}
			}
		})
	}
}

func TestCoordinatorPreservesFailedOutcomeAfterReconciliation(t *testing.T) {
	root := testPublicRoot(t)
	tests := []struct {
		name    string
		outcome CommandOutcome
		reason  Reason
		status  ResultStatus
	}{
		{
			name:    "user cancelled",
			outcome: CommandOutcomeUserCancelled,
			reason:  ReasonUserCancelled,
			status:  ResultStatusUserCancelled,
		},
		{
			name:    "permission denied",
			outcome: CommandOutcomePermissionDenied,
			reason:  ReasonPermissionDenied,
			status:  ResultStatusNeedsManual,
		},
		{
			name:    "timeout",
			outcome: CommandOutcomeTimedOut,
			reason:  ReasonCommandTimeout,
			status:  ResultStatusFailed,
		},
		{
			name:    "failed",
			outcome: CommandOutcomeFailed,
			reason:  ReasonCommandFailed,
			status:  ResultStatusFailed,
		},
		{
			name:    "indeterminate",
			outcome: CommandOutcomeIndeterminate,
			reason:  ReasonCommandIndeterminate,
			status:  ResultStatusFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := newMachineExecutor(root, machineState{
				presence: ExactPresenceAbsent,
				decision: TrustDecisionUntrusted,
			})
			executor.mutationResults[CommandEnsureExactTrust] = []CommandOutcome{test.outcome}
			executor.effectOnFailure[CommandEnsureExactTrust] = true
			coordinator, _ := newTestCoordinator(t, root, executor)
			plan, err := coordinator.Plan(context.Background(), OperationInstall)
			if err != nil {
				t.Fatal(err)
			}
			result, err := coordinator.Execute(context.Background(), plan)
			if err == nil || result.Completed() || result.Reason() != test.reason ||
				result.Status() != test.status ||
				result.Observation().Presence() != ExactPresencePresent ||
				result.Observation().TrustDecision() != TrustDecisionTrusted {
				t.Fatalf("outcome was not preserved: result=%+v error=%v", result, err)
			}
			retry, err := coordinator.Plan(context.Background(), OperationInstall)
			if err != nil {
				t.Fatal(err)
			}
			retryResult, err := coordinator.Execute(context.Background(), retry)
			if err != nil || !retryResult.Completed() ||
				retryResult.Reason() != ReasonAlreadySatisfied {
				t.Fatalf("idempotent retry failed: result=%+v error=%v", retryResult, err)
			}
		})
	}
}

func TestCoordinatorDoesNotContinueAfterMutationFailure(t *testing.T) {
	root := testPublicRoot(t)
	executor := newMachineExecutor(root, machineState{
		presence: ExactPresencePresent,
		decision: TrustDecisionTrusted,
	})
	executor.mutationResults[CommandRemoveExactTrust] = []CommandOutcome{
		CommandOutcomeFailed,
	}
	coordinator, _ := newTestCoordinator(t, root, executor)
	plan, err := coordinator.Plan(context.Background(), OperationRemove)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Execute(context.Background(), plan)
	if err == nil || result.Completed() || result.Reason() != ReasonCommandFailed {
		t.Fatalf("unexpected failed result: result=%+v error=%v", result, err)
	}
	if slices.Contains(executor.callKinds(), CommandDeleteExactObject) {
		t.Fatal("later destructive step ran after trust-removal failure")
	}
}

func TestCoordinatorRequiresPostconditionAfterSuccessfulCommand(t *testing.T) {
	root := testPublicRoot(t)
	executor := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	executor.skipEffects[CommandEnsureExactTrust] = true
	coordinator, _ := newTestCoordinator(t, root, executor)
	plan, err := coordinator.Plan(context.Background(), OperationInstall)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Execute(context.Background(), plan)
	if err == nil || result.Completed() ||
		result.Reason() != ReasonPostconditionMismatch {
		t.Fatalf("command success fabricated completion: result=%+v error=%v", result, err)
	}
}

func TestCoordinatorFailsClosedWhenReconciliationIsUnknown(t *testing.T) {
	root := testPublicRoot(t)
	executor := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	executor.invalidAfterMutation = true
	coordinator, _ := newTestCoordinator(t, root, executor)
	plan, err := coordinator.Plan(context.Background(), OperationInstall)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Execute(context.Background(), plan)
	if err == nil || result.Completed() ||
		result.Reason() != ReasonReconciliationUnknown ||
		result.Observation().Presence() != ExactPresenceUnknown ||
		result.Observation().TrustDecision() != TrustDecisionUnknown {
		t.Fatalf("unknown reconciliation was not fail-closed: result=%+v error=%v", result, err)
	}
}

func TestCoordinatorFailFastAdmission(t *testing.T) {
	root := testPublicRoot(t)
	executor := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	executor.blockKind = CommandEnsureExactTrust
	executor.blockEntered = make(chan struct{})
	executor.blockRelease = make(chan struct{})
	coordinator, _ := newTestCoordinator(t, root, executor)
	plan, err := coordinator.Plan(context.Background(), OperationInstall)
	if err != nil {
		t.Fatal(err)
	}
	type executionResult struct {
		result OperationResult
		err    error
	}
	firstDone := make(chan executionResult, 1)
	go func() {
		result, executeErr := coordinator.Execute(context.Background(), plan)
		firstDone <- executionResult{result: result, err: executeErr}
	}()
	<-executor.blockEntered
	second, err := coordinator.Execute(context.Background(), plan)
	if !errors.Is(err, ErrOperationInProgress) || second.Completed() {
		t.Fatalf("concurrent operation did not fail fast: result=%+v error=%v", second, err)
	}
	var failure *OperationError
	if !errors.As(err, &failure) || failure.Reason() != ReasonOperationInProgress {
		t.Fatalf("unexpected fail-fast reason: %v", err)
	}
	close(executor.blockRelease)
	first := <-firstDone
	if first.err != nil || !first.result.Completed() {
		t.Fatalf("owned operation failed: result=%+v error=%v", first.result, first.err)
	}
}

func TestCallerCancellationBeforeAdmissionRunsNoCommand(t *testing.T) {
	root := testPublicRoot(t)
	executor := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	coordinator, _ := newTestCoordinator(t, root, executor)
	plan, err := coordinator.Plan(context.Background(), OperationInstall)
	if err != nil {
		t.Fatal(err)
	}
	before := len(executor.callKinds())
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := coordinator.Execute(cancelled, plan)
	if err == nil || result.Completed() ||
		result.Reason() != ReasonCallerCancelled ||
		len(executor.callKinds()) != before {
		t.Fatalf("pre-admission cancellation was not fail-closed: result=%+v error=%v", result, err)
	}
}

type planCancellationExecutor struct {
	delegate CommandExecutor
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (executor *planCancellationExecutor) Execute(
	ctx context.Context,
	spec CommandSpec,
) (CommandResult, error) {
	if spec.Kind() != CommandInspectAdminTrust {
		return executor.delegate.Execute(ctx, spec)
	}
	executor.once.Do(func() { close(executor.entered) })
	<-executor.release
	return executor.delegate.Execute(context.Background(), spec)
}

func TestPlanDoesNotReturnSuccessAfterCallerCancellation(t *testing.T) {
	root := testPublicRoot(t)
	machine := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	executor := &planCancellationExecutor{
		delegate: machine,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	adapter, err := NewMacOSAdapter(executor)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(
		&mutableRootSource{root: root.clone()},
		adapter,
		DefaultCoordinatorOptions(context.Background()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := coordinator.Shutdown(ctx); err != nil {
			t.Errorf("shutdown coordinator: %v", err)
		}
	})
	caller, cancelCaller := context.WithCancel(context.Background())
	done := make(chan struct {
		plan ChangePlan
		err  error
	}, 1)
	go func() {
		plan, planErr := coordinator.Plan(caller, OperationInstall)
		done <- struct {
			plan ChangePlan
			err  error
		}{plan: plan, err: planErr}
	}()
	<-executor.entered
	cancelCaller()
	close(executor.release)
	finished := <-done
	if !errors.Is(finished.err, context.Canceled) || finished.plan.Valid() {
		t.Fatalf("cancelled planning returned success: plan=%+v error=%v", finished.plan, finished.err)
	}
}

type barrierRootSource struct {
	root    publicRoot
	blockAt int
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	reads   int
}

func (source *barrierRootSource) currentPublicRoot(
	ctx context.Context,
) (publicRoot, error) {
	if ctx == nil {
		return publicRoot{}, ErrCurrentRootInvalid
	}
	if err := ctx.Err(); err != nil {
		return publicRoot{}, context.Cause(ctx)
	}
	source.mu.Lock()
	source.reads++
	read := source.reads
	source.mu.Unlock()
	if read == source.blockAt {
		source.once.Do(func() { close(source.entered) })
		<-source.release
	}
	return source.root.clone(), nil
}

func TestCallerCancellationBetweenPreinspectionAndMutationRunsNoCommand(
	t *testing.T,
) {
	root := testPublicRoot(t)
	executor := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	source := &barrierRootSource{
		root:    root.clone(),
		blockAt: 4,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	adapter, err := NewMacOSAdapter(executor)
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultCoordinatorOptions(context.Background())
	options.CommandTimeout = time.Second
	options.ReconciliationTimeout = time.Second
	coordinator, err := NewCoordinator(source, adapter, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := coordinator.Shutdown(ctx); err != nil {
			t.Errorf("shutdown coordinator: %v", err)
		}
	})
	plan, err := coordinator.Plan(context.Background(), OperationInstall)
	if err != nil {
		t.Fatal(err)
	}
	caller, cancelCaller := context.WithCancel(context.Background())
	done := make(chan struct {
		result OperationResult
		err    error
	}, 1)
	go func() {
		result, executeErr := coordinator.Execute(caller, plan)
		done <- struct {
			result OperationResult
			err    error
		}{result: result, err: executeErr}
	}()
	<-source.entered
	cancelCaller()
	close(source.release)
	finished := <-done
	if finished.err == nil || finished.result.Completed() ||
		finished.result.Status() != ResultStatusFailed ||
		finished.result.Reason() != ReasonCallerCancelled {
		t.Fatalf("pre-mutation cancellation was not preserved: result=%+v error=%v", finished.result, finished.err)
	}
	if calls := executor.callKinds(); !slices.Equal(calls, []CommandKind{
		CommandInspectExactPresence,
		CommandInspectAdminTrust,
		CommandInspectExactPresence,
		CommandInspectAdminTrust,
	}) {
		t.Fatalf("pre-mutation cancellation ran a command or reconciliation: %v", calls)
	}
}

func TestOwnerCancellationBetweenPreinspectionAndMutationRunsNoCommand(
	t *testing.T,
) {
	root := testPublicRoot(t)
	executor := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	source := &barrierRootSource{
		root:    root.clone(),
		blockAt: 4,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	adapter, err := NewMacOSAdapter(executor)
	if err != nil {
		t.Fatal(err)
	}
	owner, cancelOwner := context.WithCancel(context.Background())
	options := DefaultCoordinatorOptions(owner)
	options.CommandTimeout = time.Second
	options.ReconciliationTimeout = time.Second
	coordinator, err := NewCoordinator(source, adapter, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := coordinator.Shutdown(ctx); err != nil {
			t.Errorf("shutdown coordinator: %v", err)
		}
	})
	plan, err := coordinator.Plan(context.Background(), OperationInstall)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct {
		result OperationResult
		err    error
	}, 1)
	go func() {
		result, executeErr := coordinator.Execute(context.Background(), plan)
		done <- struct {
			result OperationResult
			err    error
		}{result: result, err: executeErr}
	}()
	<-source.entered
	cancelOwner()
	close(source.release)
	finished := <-done
	if finished.err == nil || finished.result.Completed() ||
		finished.result.Status() != ResultStatusFailed ||
		finished.result.Reason() != ReasonShuttingDown {
		t.Fatalf("pre-mutation owner cancellation was not preserved: result=%+v error=%v", finished.result, finished.err)
	}
	if calls := executor.callKinds(); !slices.Equal(calls, []CommandKind{
		CommandInspectExactPresence,
		CommandInspectAdminTrust,
		CommandInspectExactPresence,
		CommandInspectAdminTrust,
	}) {
		t.Fatalf("pre-mutation owner cancellation ran a command or reconciliation: %v", calls)
	}
}

func TestCallerCancellationTriggersIndependentReconciliation(t *testing.T) {
	root := testPublicRoot(t)
	executor := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	executor.blockKind = CommandEnsureExactTrust
	executor.blockEntered = make(chan struct{})
	executor.blockUntilCancel = true
	coordinator, _ := newTestCoordinator(t, root, executor)
	plan, err := coordinator.Plan(context.Background(), OperationInstall)
	if err != nil {
		t.Fatal(err)
	}
	caller, cancelCaller := context.WithCancel(context.Background())
	done := make(chan struct {
		result OperationResult
		err    error
	}, 1)
	go func() {
		result, executeErr := coordinator.Execute(caller, plan)
		done <- struct {
			result OperationResult
			err    error
		}{result: result, err: executeErr}
	}()
	<-executor.blockEntered
	cancelCaller()
	finished := <-done
	if finished.err == nil || finished.result.Completed() ||
		finished.result.Reason() != ReasonCallerCancelled ||
		finished.result.Observation().Presence() != ExactPresenceAbsent {
		t.Fatalf("cancellation was not reconciled: result=%+v error=%v", finished.result, finished.err)
	}
	calls := executor.callKinds()
	mutationIndex := slices.Index(calls, CommandEnsureExactTrust)
	if mutationIndex < 0 || mutationIndex+2 >= len(calls) ||
		calls[mutationIndex+1] != CommandInspectExactPresence ||
		calls[mutationIndex+2] != CommandInspectAdminTrust {
		t.Fatalf("caller cancellation skipped independent reconciliation: %v", calls)
	}
}

type cancellationAppliedExecutor struct {
	delegate *machineExecutor
	entered  chan struct{}
	once     sync.Once
}

func (executor *cancellationAppliedExecutor) Execute(
	ctx context.Context,
	spec CommandSpec,
) (CommandResult, error) {
	if spec.Kind() != CommandEnsureExactTrust {
		return executor.delegate.Execute(ctx, spec)
	}
	executor.once.Do(func() { close(executor.entered) })
	<-ctx.Done()
	executor.delegate.setState(machineState{
		presence: ExactPresencePresent,
		decision: TrustDecisionTrusted,
	})
	return commandResult(CommandOutcomeIndeterminate, nil), context.Cause(ctx)
}

func TestCallerCancellationNeverConvertsObservedApplicationToSuccess(t *testing.T) {
	root := testPublicRoot(t)
	machine := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	executor := &cancellationAppliedExecutor{
		delegate: machine,
		entered:  make(chan struct{}),
	}
	adapter, err := NewMacOSAdapter(executor)
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultCoordinatorOptions(context.Background())
	options.CommandTimeout = time.Second
	options.ReconciliationTimeout = time.Second
	coordinator, err := NewCoordinator(
		&mutableRootSource{root: root.clone()},
		adapter,
		options,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := coordinator.Shutdown(ctx); err != nil {
			t.Errorf("shutdown coordinator: %v", err)
		}
	})
	plan, err := coordinator.Plan(context.Background(), OperationInstall)
	if err != nil {
		t.Fatal(err)
	}
	caller, cancelCaller := context.WithCancel(context.Background())
	done := make(chan struct {
		result OperationResult
		err    error
	}, 1)
	go func() {
		result, executeErr := coordinator.Execute(caller, plan)
		done <- struct {
			result OperationResult
			err    error
		}{result: result, err: executeErr}
	}()
	<-executor.entered
	cancelCaller()
	finished := <-done
	if finished.err == nil || finished.result.Completed() ||
		finished.result.Reason() != ReasonCallerCancelled ||
		finished.result.Observation().Presence() != ExactPresencePresent ||
		finished.result.Observation().TrustDecision() != TrustDecisionTrusted {
		t.Fatalf("caller cancellation became ambiguous success: result=%+v error=%v", finished.result, finished.err)
	}
}

type panicOnceExecutor struct {
	delegate *machineExecutor
	mu       sync.Mutex
	panicked bool
}

func (executor *panicOnceExecutor) Execute(
	ctx context.Context,
	spec CommandSpec,
) (CommandResult, error) {
	executor.mu.Lock()
	shouldPanic := spec.Kind() == CommandEnsureExactTrust && !executor.panicked
	if shouldPanic {
		executor.panicked = true
	}
	executor.mu.Unlock()
	if shouldPanic {
		panic("injected executor panic")
	}
	return executor.delegate.Execute(ctx, spec)
}

func TestExecutorPanicReleasesOwnershipAndIsRetryable(t *testing.T) {
	root := testPublicRoot(t)
	machine := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	executor := &panicOnceExecutor{delegate: machine}
	adapter, err := NewMacOSAdapter(executor)
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultCoordinatorOptions(context.Background())
	options.CommandTimeout = time.Second
	options.ReconciliationTimeout = time.Second
	coordinator, err := NewCoordinator(
		&mutableRootSource{root: root.clone()},
		adapter,
		options,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := coordinator.Shutdown(ctx); err != nil {
			t.Errorf("shutdown coordinator: %v", err)
		}
	})
	plan, err := coordinator.Plan(context.Background(), OperationInstall)
	if err != nil {
		t.Fatal(err)
	}
	first, err := coordinator.Execute(context.Background(), plan)
	if err == nil || first.Completed() ||
		first.Reason() != ReasonCommandIndeterminate {
		t.Fatalf("panic was not contained: result=%+v error=%v", first, err)
	}
	retry, err := coordinator.Execute(context.Background(), plan)
	if err != nil || !retry.Completed() {
		t.Fatalf("panic stranded ownership: result=%+v error=%v", retry, err)
	}
}

func TestShutdownCancelsCommandButWaitsForReconciliation(t *testing.T) {
	root := testPublicRoot(t)
	executor := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	executor.blockKind = CommandEnsureExactTrust
	executor.blockEntered = make(chan struct{})
	executor.blockUntilCancel = true
	adapter, err := NewMacOSAdapter(executor)
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultCoordinatorOptions(context.Background())
	options.CommandTimeout = time.Second
	options.ReconciliationTimeout = time.Second
	coordinator, err := NewCoordinator(
		&mutableRootSource{root: root.clone()},
		adapter,
		options,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := coordinator.Plan(context.Background(), OperationInstall)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct {
		result OperationResult
		err    error
	}, 1)
	go func() {
		result, executeErr := coordinator.Execute(context.Background(), plan)
		done <- struct {
			result OperationResult
			err    error
		}{result: result, err: executeErr}
	}()
	<-executor.blockEntered
	shutdownContext, cancelShutdown := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer cancelShutdown()
	if err := coordinator.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
	finished := <-done
	if finished.err == nil || finished.result.Completed() ||
		finished.result.Reason() != ReasonShuttingDown ||
		finished.result.Observation().Presence() != ExactPresenceAbsent {
		t.Fatalf("shutdown did not reconcile active command: result=%+v error=%v", finished.result, finished.err)
	}
}

type stubbornExecutor struct {
	delegate *machineExecutor
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (executor *stubbornExecutor) Execute(
	ctx context.Context,
	spec CommandSpec,
) (CommandResult, error) {
	if spec.Kind() == CommandEnsureExactTrust {
		executor.once.Do(func() { close(executor.entered) })
		<-executor.release
		return commandResult(CommandOutcomeIndeterminate, nil), context.Canceled
	}
	return executor.delegate.Execute(ctx, spec)
}

func TestShutdownClosesAdmissionAndCanBeRetriedAfterDeadline(t *testing.T) {
	root := testPublicRoot(t)
	machine := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	executor := &stubbornExecutor{
		delegate: machine,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	adapter, err := NewMacOSAdapter(executor)
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultCoordinatorOptions(context.Background())
	options.CommandTimeout = time.Second
	options.ReconciliationTimeout = time.Second
	coordinator, err := NewCoordinator(
		&mutableRootSource{root: root.clone()},
		adapter,
		options,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := coordinator.Plan(context.Background(), OperationInstall)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, executeErr := coordinator.Execute(context.Background(), plan)
		done <- executeErr
	}()
	<-executor.entered
	expired, cancelExpired := context.WithCancel(context.Background())
	cancelExpired()
	if err := coordinator.Shutdown(expired); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown did not honor caller deadline: %v", err)
	}
	if _, err := coordinator.Plan(context.Background(), OperationInstall); !errors.Is(
		err,
		ErrCoordinatorStopping,
	) {
		t.Fatalf("shutdown did not close admission: %v", err)
	}
	close(executor.release)
	if err := <-done; err == nil {
		t.Fatal("canceled active operation reported success")
	}
	retryContext, cancelRetry := context.WithTimeout(context.Background(), time.Second)
	defer cancelRetry()
	if err := coordinator.Shutdown(retryContext); err != nil {
		t.Fatalf("retryable shutdown failed: %v", err)
	}
	if err := coordinator.Shutdown(retryContext); err != nil {
		t.Fatalf("idempotent shutdown failed: %v", err)
	}
}

func TestOperationResultAndErrorDoNotExposeCommandEvidence(t *testing.T) {
	root := testPublicRoot(t)
	executor := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	executor.mutationResults[CommandEnsureExactTrust] = []CommandOutcome{
		CommandOutcomePermissionDenied,
	}
	coordinator, _ := newTestCoordinator(t, root, executor)
	plan, err := coordinator.Plan(context.Background(), OperationInstall)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Execute(context.Background(), plan)
	combined := fmt.Sprintf("%+v %v", result, err)
	for _, forbidden := range []string{
		macOSSecurityExecutable,
		macOSSystemKeychain,
		"add-trusted-cert",
		"root.cer",
		string(root.certificateDER),
	} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("operation output exposed private adapter evidence %q", forbidden)
		}
	}
}

type sensitiveErrorExecutor struct {
	delegate *machineExecutor
}

func (executor *sensitiveErrorExecutor) Execute(
	ctx context.Context,
	spec CommandSpec,
) (CommandResult, error) {
	if spec.Kind() == CommandEnsureExactTrust {
		return CommandResult{}, errors.New(
			"injected raw stderr and /private/tmp/sensitive/root.cer",
		)
	}
	return executor.delegate.Execute(ctx, spec)
}

func TestExecutorErrorsAreSanitizedBeforeLeavingAdapter(t *testing.T) {
	root := testPublicRoot(t)
	machine := newMachineExecutor(root, machineState{
		presence: ExactPresenceAbsent,
		decision: TrustDecisionUntrusted,
	})
	adapter, err := NewMacOSAdapter(&sensitiveErrorExecutor{delegate: machine})
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultCoordinatorOptions(context.Background())
	options.CommandTimeout = time.Second
	options.ReconciliationTimeout = time.Second
	coordinator, err := NewCoordinator(
		&mutableRootSource{root: root.clone()},
		adapter,
		options,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := coordinator.Shutdown(ctx); err != nil {
			t.Errorf("shutdown coordinator: %v", err)
		}
	})
	plan, err := coordinator.Plan(context.Background(), OperationInstall)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Execute(context.Background(), plan)
	if err == nil || result.Completed() ||
		result.Reason() != ReasonCommandIndeterminate {
		t.Fatalf("unexpected sanitized failure: result=%+v error=%v", result, err)
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		serialized := current.Error()
		if strings.Contains(serialized, "raw stderr") ||
			strings.Contains(serialized, "/private/tmp") {
			t.Fatalf("executor error escaped adapter: %v", current)
		}
	}
}
