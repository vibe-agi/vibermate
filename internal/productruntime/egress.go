package productruntime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

// runtimeEgressRepository turns an otherwise easy-to-ignore terminal-write
// error into a generation-level durability failure. Some terminal callbacks
// run after the outbound result can no longer be changed, so their callers
// intentionally cannot return the audit error to the client. The production
// composition therefore latches storage unhealthy and cancels the owner
// context instead of allowing the generation to keep emitting unverifiable
// egress.
type runtimeEgressRepository struct {
	delegate          egressaudit.Repository
	status            *statusTracker
	owner             context.Context
	completionTimeout time.Duration
	stop              context.CancelCauseFunc

	mu              sync.Mutex
	failureErr      error
	shutdownContext context.Context
	nextCompletion  uint64
	completions     map[uint64]context.CancelCauseFunc
}

var _ egressaudit.Repository = (*runtimeEgressRepository)(nil)

func newRuntimeEgressRepository(
	delegate egressaudit.Repository,
	status *statusTracker,
	owner context.Context,
	lifecycle LifecycleOptions,
	stop context.CancelCauseFunc,
) *runtimeEgressRepository {
	completionTimeout := lifecycle.RollbackTimeout
	if lifecycle.ShutdownTimeout < completionTimeout {
		completionTimeout = lifecycle.ShutdownTimeout
	}
	return &runtimeEgressRepository{
		delegate:          delegate,
		status:            status,
		owner:             owner,
		completionTimeout: completionTimeout,
		stop:              stop,
		completions:       make(map[uint64]context.CancelCauseFunc),
	}
}

func (repository *runtimeEgressRepository) Append(
	ctx context.Context,
	attempt egressaudit.Attempt,
) (egressaudit.Record, error) {
	// Append precedes any outbound byte, so the caller can propagate a failure
	// normally and no missing terminal evidence exists yet.
	return repository.delegate.Append(ctx, attempt)
}

func (repository *runtimeEgressRepository) Complete(
	_ context.Context,
	attempt egressaudit.Attempt,
) (egressaudit.Record, error) {
	// Once outbound work has happened, request cancellation must not cancel its
	// audit terminal too. The runtime owner and a production lifecycle bound own
	// this final write; shutdown drains the data plane before canceling owner.
	completionContext, cancel := repository.completionContext()
	defer cancel()
	record, err := repository.delegate.Complete(completionContext, attempt)
	if err != nil {
		repository.fail("complete EgressAttempt", err)
	}
	return record, err
}

// ReportTerminalFailure is the production-owned failure boundary implemented
// for transport packages. It covers failures that happen before Complete can
// receive a valid terminal Attempt.
func (repository *runtimeEgressRepository) ReportTerminalFailure(err error) {
	if err != nil {
		repository.fail("construct EgressAttempt terminal", err)
	}
}

func (repository *runtimeEgressRepository) List(
	ctx context.Context,
	request egressaudit.PageRequest,
) (egressaudit.Page, error) {
	return repository.delegate.List(ctx, request)
}

func (repository *runtimeEgressRepository) Recover(
	ctx context.Context,
	completedAt time.Time,
) (int, error) {
	return repository.delegate.Recover(ctx, completedAt)
}

func (repository *runtimeEgressRepository) fail(operation string, root error) {
	failure := fmt.Errorf("%s durability failure: %w", operation, root)
	repository.mu.Lock()
	if repository.failureErr == nil {
		repository.failureErr = failure
	}
	repository.mu.Unlock()
	repository.status.failStorage()
	repository.stop(failure)
}

func (repository *runtimeEgressRepository) failure() error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.failureErr
}

// beginShutdown makes every current and future completion a child of the one
// ProductRuntime shutdown budget. The normal completion timeout is also
// capped at ShutdownTimeout, so work that started just before shutdown cannot
// outlive the later shutdown deadline.
func (repository *runtimeEgressRepository) beginShutdown(ctx context.Context) {
	repository.mu.Lock()
	if repository.shutdownContext != nil {
		repository.mu.Unlock()
		return
	}
	repository.shutdownContext = ctx
	repository.mu.Unlock()
	context.AfterFunc(ctx, func() {
		repository.mu.Lock()
		cancellations := make(
			[]context.CancelCauseFunc,
			0,
			len(repository.completions),
		)
		for _, cancel := range repository.completions {
			cancellations = append(cancellations, cancel)
		}
		repository.mu.Unlock()
		for _, cancel := range cancellations {
			cancel(context.Cause(ctx))
		}
	})
}

// finishShutdown closes the last race at the cleanup boundary. Component
// drains normally make the set empty; if a completion is nevertheless still
// live after cleanup, SQLite is no longer allowed to present shutdown as
// durable success.
func (repository *runtimeEgressRepository) finishShutdown() {
	repository.mu.Lock()
	outstanding := len(repository.completions)
	repository.mu.Unlock()
	if outstanding != 0 {
		repository.fail(
			"drain EgressAttempt terminal writes",
			fmt.Errorf("%d terminal writes remain", outstanding),
		)
	}
}

func (repository *runtimeEgressRepository) completionContext() (
	context.Context,
	context.CancelFunc,
) {
	ownedContext, cancelOwned := context.WithCancelCause(repository.owner)

	repository.mu.Lock()
	repository.nextCompletion++
	completionID := repository.nextCompletion
	repository.completions[completionID] = cancelOwned
	shutdownContext := repository.shutdownContext
	repository.mu.Unlock()

	deadline := time.Now().Add(repository.completionTimeout)
	if shutdownContext != nil {
		if shutdownDeadline, ok := shutdownContext.Deadline(); ok &&
			shutdownDeadline.Before(deadline) {
			deadline = shutdownDeadline
		}
		if err := shutdownContext.Err(); err != nil {
			cancelOwned(context.Cause(shutdownContext))
		}
	}
	completionContext, cancelDeadline := context.WithDeadline(
		ownedContext,
		deadline,
	)
	return completionContext, func() {
		cancelDeadline()
		cancelOwned(context.Canceled)
		repository.mu.Lock()
		delete(repository.completions, completionID)
		repository.mu.Unlock()
	}
}
