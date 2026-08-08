package productruntime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func TestProductRuntimeRecoversInterruptedEgressAtStartup(t *testing.T) {
	t.Parallel()

	paths, err := NewRuntimePaths(filepath.Join(t.TempDir(), "runtime-data"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := runtimepersistence.Open(
		context.Background(),
		runtimepersistence.Options{
			DatabasePath:           paths.DatabasePath(),
			BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
			CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	attempt := runtimeProviderAttempt(t, "egress-before-restart")
	record, err := store.EgressAttemptRepository().Append(
		context.Background(),
		attempt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	runtime := startTestRuntime(t, testOptionsWithPaths(
		t,
		paths,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	))
	defer shutdownRuntime(t, runtime)
	page, err := runtime.EgressAttempts().List(
		context.Background(),
		egressaudit.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("recovered attempt count = %d, want 1", len(page.Items))
	}
	recovered := page.Items[0]
	if recovered.Sequence != record.Sequence ||
		recovered.Attempt.ID() != attempt.ID() ||
		!recovered.Attempt.Terminal() ||
		recovered.Attempt.Outcome() != egressaudit.OutcomeFailed ||
		recovered.Attempt.ErrorClass() != egressaudit.RecoveryErrorClass ||
		recovered.Attempt.CompletedAt().Before(attempt.StartedAt()) ||
		recovered.Attempt.Parent() != attempt.Parent() ||
		recovered.Attempt.Decision() != attempt.Decision() {
		t.Fatalf("startup recovery changed or omitted evidence: %+v", recovered)
	}
}

func TestProductRuntimeRefusesToStartWhenEgressRecoveryFails(t *testing.T) {
	t.Parallel()

	recoveryErr := errors.New("injected EgressAttempt recovery failure")
	closed := make(chan struct{}, 1)
	builders := productionBuilders()
	builders.storage = failingEgressRecoveryStorageBuilder{
		delegate:    builders.storage,
		recoveryErr: recoveryErr,
		closed:      closed,
	}
	_, err := startWithBuilders(
		context.Background(),
		testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}),
		builders,
	)
	if !errors.Is(err, recoveryErr) ||
		!strings.Contains(err.Error(), `stage "EgressAttempt recovery"`) {
		t.Fatalf("startup recovery error = %v", err)
	}
	select {
	case <-closed:
	default:
		t.Fatal("SQLite was not closed after EgressAttempt recovery failed")
	}
}

func TestRuntimeEgressCompletionUsesOwnerContextAndLatchesFailure(t *testing.T) {
	t.Parallel()

	completeErr := errors.New("injected completion failure")
	delegate := &completionRepositoryDouble{completeErr: completeErr}
	tracker := newStatusTracker(
		"instance-egress-failure",
		hostcontract.KindDesktop,
		time.Now().UTC(),
	)
	tracker.commitInitialized(25)
	owner, stop := context.WithCancelCause(context.Background())
	repository := newRuntimeEgressRepository(
		delegate,
		tracker,
		owner,
		LifecycleOptions{
			RollbackTimeout: time.Second,
			ShutdownTimeout: time.Second,
		},
		stop,
	)
	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()

	if _, err := repository.Complete(
		requestContext,
		egressaudit.Attempt{},
	); !errors.Is(err, completeErr) {
		t.Fatalf("completion error = %v", err)
	}
	if delegate.completeContextErr != nil {
		t.Fatalf(
			"terminal write inherited request cancellation: %v",
			delegate.completeContextErr,
		)
	}
	if !errors.Is(context.Cause(owner), completeErr) {
		t.Fatalf("runtime owner cause = %v", context.Cause(owner))
	}
	status := tracker.snapshot()
	if status.State != RuntimeStateDegraded ||
		status.Storage != StorageStateUnavailable {
		t.Fatalf("completion failure remained silent: %+v", status)
	}
	tracker.observeStorage(25, nil)
	status = tracker.snapshot()
	if status.State != RuntimeStateDegraded ||
		status.Storage != StorageStateUnavailable {
		t.Fatalf("read-only health poll cleared durability failure: %+v", status)
	}
}

func TestRuntimeEgressTerminalConstructionFailureCrossesDurabilityBoundary(
	t *testing.T,
) {
	t.Parallel()

	terminalErr := errors.New("injected terminal construction failure")
	tracker := newStatusTracker(
		"instance-terminal-construction-failure",
		hostcontract.KindDesktop,
		time.Now().UTC(),
	)
	tracker.commitInitialized(25)
	owner, stop := context.WithCancelCause(context.Background())
	repository := newRuntimeEgressRepository(
		&completionRepositoryDouble{},
		tracker,
		owner,
		LifecycleOptions{
			RollbackTimeout: time.Second,
			ShutdownTimeout: time.Second,
		},
		stop,
	)

	repository.ReportTerminalFailure(terminalErr)

	if !errors.Is(repository.failure(), terminalErr) {
		t.Fatalf("durability failure = %v", repository.failure())
	}
	if !errors.Is(context.Cause(owner), terminalErr) {
		t.Fatalf("runtime owner cause = %v", context.Cause(owner))
	}
	status := tracker.snapshot()
	if status.State != RuntimeStateDegraded ||
		status.Storage != StorageStateUnavailable {
		t.Fatalf("terminal construction failure remained silent: %+v", status)
	}
}

func TestRuntimeEgressCompletionCannotOutliveShutdownWhenRollbackIsLonger(
	t *testing.T,
) {
	t.Parallel()

	delegate := &deadlineCompletionRepositoryDouble{}
	tracker := newStatusTracker(
		"instance-egress-deadline",
		hostcontract.KindDesktop,
		time.Now().UTC(),
	)
	owner, stop := context.WithCancelCause(context.Background())
	repository := newRuntimeEgressRepository(
		delegate,
		tracker,
		owner,
		LifecycleOptions{
			RollbackTimeout: 2 * time.Second,
			ShutdownTimeout: 80 * time.Millisecond,
		},
		stop,
	)
	shutdownContext, cancelShutdown := context.WithTimeout(
		context.Background(),
		40*time.Millisecond,
	)
	defer cancelShutdown()
	shutdownDeadline, _ := shutdownContext.Deadline()
	repository.beginShutdown(shutdownContext)

	started := time.Now()
	_, err := repository.Complete(
		context.Background(),
		egressaudit.Attempt{},
	)
	if !errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, context.Canceled) {
		t.Fatalf("completion error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("completion outlived shutdown budget: %s", elapsed)
	}
	if delegate.deadline.IsZero() || delegate.deadline.After(shutdownDeadline) {
		t.Fatalf(
			"completion deadline %s exceeds shutdown deadline %s",
			delegate.deadline,
			shutdownDeadline,
		)
	}
}

func TestProductRuntimeShutdownReturnsTerminalDurabilityFailureDuringStopping(
	t *testing.T,
) {
	t.Parallel()

	terminalErr := errors.New("injected terminal durability failure during stop")
	tracker := newStatusTracker(
		"instance-egress-stop-failure",
		hostcontract.KindDesktop,
		time.Now().UTC(),
	)
	tracker.commitInitialized(25)
	owner, stop := context.WithCancelCause(context.Background())
	repository := newRuntimeEgressRepository(
		&completionRepositoryDouble{},
		tracker,
		owner,
		LifecycleOptions{
			RollbackTimeout: time.Second,
			ShutdownTimeout: time.Second,
		},
		stop,
	)
	cleanups := cleanupStack{}
	cleanups.register("terminal construction fixture", func(context.Context) error {
		repository.ReportTerminalFailure(terminalErr)
		return nil
	})
	runtime := &Runtime{
		status:           tracker,
		egressCompletion: repository,
		cleanups:         cleanups,
		clock:            SystemClock{},
		timeout: LifecycleOptions{
			RollbackTimeout: time.Second,
			ShutdownTimeout: time.Second,
		},
		shutdownDone: make(chan struct{}),
	}

	shutdownErr := runtime.Shutdown(context.Background())
	if !errors.Is(shutdownErr, terminalErr) {
		t.Fatalf("Shutdown() error = %v", shutdownErr)
	}
	status := tracker.snapshot()
	if status.State != RuntimeStateStopFailed ||
		status.Storage != StorageStateUnavailable ||
		status.StopReasonCode != StopReasonShutdownFailed ||
		status.StoppedAt != nil {
		t.Fatalf("terminal durability failure status = %+v", status)
	}
}

func runtimeProviderAttempt(t *testing.T, id string) egressaudit.Attempt {
	t.Helper()
	attempt, err := egressaudit.New(egressaudit.NewInput{
		ID:           id,
		ConnectionID: "connection-before-restart",
		Purpose:      egressaudit.PurposeProviderAttempt,
		PayloadClass: egressaudit.PayloadClientSemantic,
		Parent: egressaudit.ParentRef{
			Kind:       egressaudit.ParentUpstreamAttempt,
			ID:         "upstream-before-restart",
			ExchangeID: "exchange-before-restart",
		},
		Caller:       egressaudit.CallerCore,
		TargetOrigin: "https://provider.example:443",
		Decision: egressaudit.DecisionRef{
			PolicyID:       "policy-before-restart",
			PolicyRevision: 7,
			Authority:      egressaudit.AuthorityEnvironment,
			RuleID:         "rule-before-restart",
			ProxyID:        "direct",
		},
		StartedAt: time.Now().UTC().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

type failingEgressRecoveryStorageBuilder struct {
	delegate    storageBuilder
	recoveryErr error
	closed      chan<- struct{}
}

func (builder failingEgressRecoveryStorageBuilder) Build(
	ctx context.Context,
	request storageBuildRequest,
) (storageBuildResult, error) {
	result, err := builder.delegate.Build(ctx, request)
	if err != nil {
		return storageBuildResult{}, err
	}
	delegate := result.store.EgressAttemptRepository()
	result.store = &failingEgressRecoveryStore{
		RuntimeStore: result.store,
		repository: &failingEgressRecoveryRepository{
			Repository: delegate,
			err:        builder.recoveryErr,
		},
		closed: builder.closed,
	}
	return result, nil
}

type failingEgressRecoveryStore struct {
	runtimepersistence.RuntimeStore
	repository egressaudit.Repository
	closed     chan<- struct{}
}

func (store *failingEgressRecoveryStore) EgressAttemptRepository() egressaudit.Repository {
	return store.repository
}

func (store *failingEgressRecoveryStore) Shutdown(ctx context.Context) error {
	err := store.RuntimeStore.Shutdown(ctx)
	select {
	case store.closed <- struct{}{}:
	default:
	}
	return err
}

type failingEgressRecoveryRepository struct {
	egressaudit.Repository
	err error
}

func (repository *failingEgressRecoveryRepository) Recover(
	context.Context,
	time.Time,
) (int, error) {
	return 0, repository.err
}

type completionRepositoryDouble struct {
	completeErr        error
	completeContextErr error
}

type deadlineCompletionRepositoryDouble struct {
	deadline time.Time
}

func (*deadlineCompletionRepositoryDouble) Append(
	context.Context,
	egressaudit.Attempt,
) (egressaudit.Record, error) {
	return egressaudit.Record{}, nil
}

func (repository *deadlineCompletionRepositoryDouble) Complete(
	ctx context.Context,
	_ egressaudit.Attempt,
) (egressaudit.Record, error) {
	repository.deadline, _ = ctx.Deadline()
	<-ctx.Done()
	return egressaudit.Record{}, ctx.Err()
}

func (*deadlineCompletionRepositoryDouble) List(
	context.Context,
	egressaudit.PageRequest,
) (egressaudit.Page, error) {
	return egressaudit.Page{}, nil
}

func (*deadlineCompletionRepositoryDouble) Recover(
	context.Context,
	time.Time,
) (int, error) {
	return 0, nil
}

func (*completionRepositoryDouble) Append(
	context.Context,
	egressaudit.Attempt,
) (egressaudit.Record, error) {
	return egressaudit.Record{}, nil
}

func (repository *completionRepositoryDouble) Complete(
	ctx context.Context,
	_ egressaudit.Attempt,
) (egressaudit.Record, error) {
	repository.completeContextErr = ctx.Err()
	return egressaudit.Record{}, repository.completeErr
}

func (*completionRepositoryDouble) List(
	context.Context,
	egressaudit.PageRequest,
) (egressaudit.Page, error) {
	return egressaudit.Page{}, nil
}

func (*completionRepositoryDouble) Recover(
	context.Context,
	time.Time,
) (int, error) {
	return 0, nil
}
