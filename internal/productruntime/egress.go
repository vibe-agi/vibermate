package productruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

// ErrCoreAuditRecovering refuses a new EgressAttempt while an earlier
// terminal is not yet durable.
var ErrCoreAuditRecovering = errors.New("egress audit is writing a pending terminal; new egress is refused")

// runtimeEgressRepository keeps "no audit, no egress" without turning one slow
// write into an outage. Every attempt is appended before its first outbound
// byte. When a terminal write fails, the terminal is kept and retried, and no
// new attempt may start until it is durable; the Runtime then recovers on its
// own. Terminal callbacks run after the outbound result is fixed, so their
// callers never see these errors. A terminal still unwritten at shutdown, or a
// terminal that could not even be constructed, fails the generation.
type runtimeEgressRepository struct {
	delegate          egressaudit.Repository
	status            *statusTracker
	owner             context.Context
	completionTimeout time.Duration
	retryInterval     time.Duration
	stop              context.CancelCauseFunc

	mu                      sync.Mutex
	pending                 []egressaudit.Attempt
	retrying                bool
	failureErr              error
	rawEvidenceFailureErr   error
	rawEvidenceFailureCount uint64
	shutdownContext         context.Context
	nextCompletion          uint64
	completions             map[uint64]context.CancelCauseFunc
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
		retryInterval:     time.Second,
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
	repository.mu.Lock()
	recovering := len(repository.pending) != 0
	repository.mu.Unlock()
	if recovering {
		return egressaudit.Record{}, ErrCoreAuditRecovering
	}
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
		repository.hold(attempt, err)
	}
	return record, err
}

// hold keeps an unwritten terminal and starts the single retry loop.
func (repository *runtimeEgressRepository) hold(attempt egressaudit.Attempt, cause error) {
	repository.mu.Lock()
	repository.pending = append(repository.pending, attempt)
	start := !repository.retrying
	repository.retrying = true
	repository.mu.Unlock()
	if repository.status != nil {
		repository.status.holdCoreAudit("egress_complete", cause)
	}
	if start {
		go repository.retryPending()
	}
}

// retryPending writes held terminals in order until none remain or the
// Runtime stops. Shutdown reports whatever is still pending.
func (repository *runtimeEgressRepository) retryPending() {
	timer := time.NewTimer(repository.retryInterval)
	defer timer.Stop()
	for {
		select {
		case <-repository.owner.Done():
			repository.mu.Lock()
			repository.retrying = false
			repository.mu.Unlock()
			return
		case <-timer.C:
		}
		repository.mu.Lock()
		held := append([]egressaudit.Attempt(nil), repository.pending...)
		repository.mu.Unlock()
		written := 0
		for _, attempt := range held {
			ctx, cancel := repository.completionContext()
			_, err := repository.delegate.Complete(ctx, attempt)
			cancel()
			if err != nil {
				break
			}
			written++
		}
		repository.mu.Lock()
		repository.pending = repository.pending[written:]
		done := len(repository.pending) == 0
		if done {
			repository.retrying = false
		}
		repository.mu.Unlock()
		if done {
			if repository.status != nil {
				repository.status.releaseCoreAudit()
			}
			return
		}
		timer.Reset(repository.retryInterval)
	}
}

// ReportTerminalFailure is the production-owned failure boundary implemented
// for transport packages. It covers failures that happen before Complete can
// receive a valid terminal Attempt.
func (repository *runtimeEgressRepository) ReportTerminalFailure(err error) {
	if err != nil {
		repository.fail("egress_terminal", err)
	}
}

// ReportRawEvidenceFailure records an optional Raw HTTP retention failure
// without weakening or stopping the core Activity/Egress audit. ViberMate is a
// transparent runtime by default: losing a secondary encrypted packet copy is
// observable degradation, never authority to interrupt Agent traffic.
func (repository *runtimeEgressRepository) ReportRawEvidenceFailure(err error) {
	if err == nil {
		return
	}
	repository.mu.Lock()
	repository.rawEvidenceFailureErr = fmt.Errorf(
		"record Raw evidence: %w", err,
	)
	repository.rawEvidenceFailureCount++
	repository.mu.Unlock()
	if repository.status != nil {
		repository.status.failRecording("raw_evidence", err)
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
	if repository.failureErr != nil {
		repository.mu.Unlock()
		return
	}
	repository.failureErr = failure
	repository.status.failStorage(operation, root)
	repository.mu.Unlock()
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
	outstanding := len(repository.completions) + len(repository.pending)
	repository.mu.Unlock()
	if outstanding != 0 {
		repository.fail(
			"egress_drain",
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
