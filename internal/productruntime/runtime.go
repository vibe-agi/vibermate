// Package productruntime owns the single production composition root.
package productruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

var ErrInvalidBuildResult = errors.New("invalid runtime build result")

// Runtime owns every successfully constructed M0 component.
type Runtime struct {
	status       *statusTracker
	schemaReader runtimepersistence.SchemaStateReader
	accesses     accessRuntime
	monitor      ownedComponent
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
	if builders.storage == nil || builders.access == nil || builders.monitor == nil {
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
	fail := func(stage string, root error) (*Runtime, error) {
		startupErr := fmt.Errorf("start ProductRuntime stage %q: %w", stage, root)
		rollbackContext, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			options.Lifecycle.RollbackTimeout,
		)
		defer cancel()
		rollbackErr := cleanups.shutdown(rollbackContext)
		if rollbackErr != nil {
			return nil, errors.Join(
				startupErr,
				fmt.Errorf("startup rollback: %w", rollbackErr),
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

	if err := options.OfflineHold.Start(ctx, offlinehold.RuntimeBinding{
		InstanceID: instanceID,
	}); err != nil {
		return fail("offline-hold binding", err)
	}
	cleanups.register("offline-hold coordinator", func(ctx context.Context) error {
		options.OfflineHold.BeginShutdown()
		return options.OfflineHold.Drain(ctx)
	})

	finalState, err := storageResult.store.SchemaStateReader().ReadSchemaState(ctx)
	if err != nil {
		return fail("foundation state verification", err)
	}
	tracker.commitInitialized(finalState.Revision)
	return &Runtime{
		status:       tracker,
		schemaReader: storageResult.store.SchemaStateReader(),
		accesses:     accesses,
		monitor:      monitor,
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
