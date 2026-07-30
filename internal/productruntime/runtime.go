// Package productruntime owns the single production composition root.
package productruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

var ErrInvalidBuildResult = errors.New("invalid runtime build result")

// Runtime owns every successfully constructed M0 component.
type Runtime struct {
	status       *statusTracker
	schemaReader runtimepersistence.SchemaStateReader
	accesses     accessRuntime
	probeCatalog access.ProviderProbeCatalog
	monitor      ownedComponent
	provider     providerRuntime
	original     originalRuntime
	offlineHold  offlinehold.RuntimeCoordinator
	resumeProber offlinehold.Prober
	cleanups     cleanupStack
	clock        Clock
	timeout      LifecycleOptions

	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
}

// Start validates all dependencies and executes the typed production builder.
func Start(ctx context.Context, options Options) (*Runtime, error) {
	return startWithBuilders(ctx, options, productionBuilders())
}

func startWithBuilders(
	ctx context.Context,
	options Options,
	builders runtimeBuilders,
) (*Runtime, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: startup context is nil", ErrInvalidOptions)
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start ProductRuntime: %w", err)
	}
	if builders.storage == nil ||
		builders.access == nil ||
		builders.monitor == nil ||
		builders.provider == nil ||
		builders.original == nil {
		return nil, fmt.Errorf("%w: component builder is missing", ErrInvalidBuildResult)
	}

	instanceID, err := options.InstanceIDs.NewInstanceID(ctx)
	if err != nil {
		return nil, fmt.Errorf("create runtime incarnation: %w", err)
	}
	if instanceID == "" {
		return nil, fmt.Errorf("%w: instance ID is empty", ErrInvalidBuildResult)
	}

	tracker := newStatusTracker(instanceID, options.Host.Kind(), options.Clock.Now())
	var cleanups cleanupStack
	var pending cleanupStack
	fail := func(stage string, root error) (*Runtime, error) {
		startupErr := fmt.Errorf("start ProductRuntime stage %q: %w", stage, root)
		rollbackContext, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			options.Lifecycle.RollbackTimeout,
		)
		defer cancel()
		pendingErr := pending.shutdown(rollbackContext)
		rollbackErr := cleanups.shutdown(rollbackContext)
		if pendingErr != nil || rollbackErr != nil {
			return nil, errors.Join(
				startupErr,
				wrapOptionalError("pending startup rollback", pendingErr),
				wrapOptionalError("startup rollback", rollbackErr),
			)
		}
		return nil, startupErr
	}

	storageResult, err := builders.storage.Build(ctx, storageBuildRequest{
		databasePath: options.Paths.DatabasePath(),
	})
	if err != nil {
		return fail("sqlite", err)
	}
	if storageResult.store == nil || storageResult.state.Revision <= 0 {
		incompleteErr := fmt.Errorf("%w: storage component is incomplete", ErrInvalidBuildResult)
		if storageResult.store != nil {
			closeContext, cancel := context.WithTimeout(
				context.WithoutCancel(ctx),
				options.Lifecycle.RollbackTimeout,
			)
			closeErr := storageResult.store.Shutdown(closeContext)
			cancel()
			incompleteErr = errors.Join(
				incompleteErr,
				wrapOptionalError("close incomplete storage component", closeErr),
			)
		}
		return fail("sqlite", incompleteErr)
	}
	cleanups.register("sqlite", storageResult.store.Shutdown)

	accesses, err := builders.access.Build(ctx, accessBuildRequest{
		repository: storageResult.store.AccessRepository(),
	})
	if err != nil {
		return fail("Access recovery", err)
	}
	if accesses == nil {
		return fail(
			"Access recovery",
			fmt.Errorf("%w: Access component is nil", ErrInvalidBuildResult),
		)
	}
	cleanups.register("Access runtime", accesses.Shutdown)

	ownerContext, cancelOwner := context.WithCancelCause(context.WithoutCancel(ctx))
	cleanups.register("runtime owner context", func(context.Context) error {
		cancelOwner(errors.New("runtime owner context stopped"))
		return nil
	})

	monitor, err := builders.monitor.Build(monitorBuildRequest{
		ownerContext: ownerContext,
		reader:       storageResult.store.SchemaStateReader(),
		interval:     options.Lifecycle.HealthPollInterval,
		observe: func(state runtimepersistence.SchemaState, observationErr error) {
			tracker.observeStorage(state.Revision, observationErr)
		},
	})
	if err != nil {
		return fail("storage health monitor", err)
	}
	if monitor == nil {
		return fail(
			"storage health monitor",
			fmt.Errorf("%w: monitor component is nil", ErrInvalidBuildResult),
		)
	}
	cleanups.register("storage health monitor", monitor.Shutdown)

	providerResumeProber, err := providertransport.NewTLSProber()
	if err != nil {
		return fail("offline-hold provider probe", err)
	}
	originalResumeProber, err := originaltransport.NewTLSProber()
	if err != nil {
		return fail("offline-hold original-origin probe", err)
	}
	resumeProber, err := newRuntimeResumeProber(
		providerResumeProber,
		originalResumeProber,
	)
	if err != nil {
		return fail("offline-hold resume probe", err)
	}

	provider, err := builders.provider.Build(providerBuildRequest{
		coordinator: options.OfflineHold,
		secrets:     options.Secrets,
	})
	if err != nil {
		return fail("provider transport", err)
	}
	if provider == nil {
		return fail(
			"provider transport",
			fmt.Errorf("%w: provider transport is nil", ErrInvalidBuildResult),
		)
	}
	pending.register("provider transport", provider.Shutdown)

	original, err := builders.original.Build(originalBuildRequest{
		coordinator: options.OfflineHold,
	})
	if err != nil {
		return fail("original-origin transport", err)
	}
	if original == nil {
		return fail(
			"original-origin transport",
			fmt.Errorf(
				"%w: original-origin transport is nil",
				ErrInvalidBuildResult,
			),
		)
	}
	pending.register("original-origin transport", original.Shutdown)

	if err := options.OfflineHold.Start(ctx, offlinehold.RuntimeBinding{
		InstanceID: instanceID,
	}); err != nil {
		return fail("offline-hold binding", err)
	}
	pending.register("offline-hold binding", func(ctx context.Context) error {
		options.OfflineHold.BeginShutdown()
		return options.OfflineHold.Drain(ctx)
	})

	cleanups.register("offline-hold drain", options.OfflineHold.Drain)
	cleanups.register("provider transport", provider.Shutdown)
	cleanups.register("original-origin transport", original.Shutdown)
	cleanups.register("offline-hold admission", func(context.Context) error {
		options.OfflineHold.BeginShutdown()
		return nil
	})
	pending = cleanupStack{}

	finalState, err := storageResult.store.SchemaStateReader().ReadSchemaState(ctx)
	if err != nil {
		return fail("foundation state verification", err)
	}
	tracker.commitInitialized(finalState.Revision)
	return &Runtime{
		status:       tracker,
		schemaReader: storageResult.store.SchemaStateReader(),
		accesses:     accesses,
		probeCatalog: accesses,
		monitor:      monitor,
		provider:     provider,
		original:     original,
		offlineHold:  options.OfflineHold,
		resumeProber: resumeProber,
		cleanups:     cleanups,
		clock:        options.Clock,
		timeout:      options.Lifecycle,
		shutdownDone: make(chan struct{}),
	}, nil
}

// Status returns an immutable copy of the current runtime state.
func (r *Runtime) Status() RuntimeStatus {
	status := r.status.snapshot()
	status.AccessProjection = r.accesses.ProjectionHealth()
	status.OfflineHold = r.offlineHold.Snapshot()
	if status.AccessProjection.State == access.ProjectionStateUnavailable &&
		status.State == RuntimeStateInitialized {
		status.State = RuntimeStateDegraded
	}
	return status
}

// SchemaStateReader returns the SQLite-backed initialization-state reader.
// It is distinct from the Access aggregate SnapshotResolver.
func (r *Runtime) SchemaStateReader() runtimepersistence.SchemaStateReader {
	return r.schemaReader
}

// AccessWriter returns the serialized Access aggregate mutation boundary.
func (r *Runtime) AccessWriter() access.Writer {
	return r.accesses
}

// SnapshotResolver returns the current process-local Access projection.
func (r *Runtime) SnapshotResolver() access.SnapshotResolver {
	return r.accesses
}

// AccessProjectionHealth reports whether process-local Access snapshots can be
// trusted. It is an internal health signal, not product readiness.
func (r *Runtime) AccessProjectionHealth() access.ProjectionHealth {
	return r.accesses.ProjectionHealth()
}

// Shutdown starts one bounded LIFO shutdown and waits for either that result or
// the caller deadline. Repeated calls never run component cleanup twice.
func (r *Runtime) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("ProductRuntime shutdown context is nil")
	}
	r.shutdownOnce.Do(func() {
		r.status.beginStopping()
		go r.executeShutdown()
	})

	select {
	case <-r.shutdownDone:
		return r.shutdownErr
	default:
	}
	select {
	case <-r.shutdownDone:
		return r.shutdownErr
	case <-ctx.Done():
		return fmt.Errorf("wait for ProductRuntime shutdown: %w", ctx.Err())
	}
}

func (r *Runtime) executeShutdown() {
	shutdownContext, cancel := context.WithTimeout(
		context.Background(),
		r.timeout.ShutdownTimeout,
	)
	r.shutdownErr = r.cleanups.shutdown(shutdownContext)
	cancel()
	r.status.finishStopping(r.clock.Now(), r.shutdownErr)
	close(r.shutdownDone)
}
