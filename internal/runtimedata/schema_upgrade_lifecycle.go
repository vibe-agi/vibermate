package runtimedata

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

// schemaUpgradeWork owns directory admission and the single progress budget.
// Concrete transition runners still select their own verifier and receipt proof.
type schemaUpgradeWork struct {
	ctx         context.Context
	parent      context.Context
	source      string
	owner, host *instanceguard.Guard
	ownsHost    bool
	timeout     time.Duration
	progress    func(SchemaUpgradePhase) error
	cancel      context.CancelFunc
	emitted     bool
}

func newSchemaUpgradeWork(ctx context.Context, source string, owner, host *instanceguard.Guard, now time.Time, timeout time.Duration, progress func(SchemaUpgradePhase) error) (*schemaUpgradeWork, error) {
	if err := snapshotDirectory(ctx, source); err != nil {
		return nil, err
	}
	if !snapshotOwns(owner, source, lockName) || now.IsZero() || now.Location() != time.UTC || timeout < 0 {
		return nil, ErrTarget
	}
	work := &schemaUpgradeWork{ctx: ctx, parent: ctx, source: source, owner: owner, host: host, timeout: timeout, progress: progress}
	if host == nil {
		var err error
		work.host, err = instanceguard.Acquire(filepath.Join(source, "server.lock"))
		if err != nil {
			return nil, errors.Join(ErrBusy, err)
		}
		work.ownsHost = true
	}
	if !snapshotOwns(work.host, source, "server.lock") {
		work.close()
		return nil, ErrTarget
	}
	if work.timeout == 0 {
		work.timeout = 15 * time.Minute
	}
	return work, nil
}

func (work *schemaUpgradeWork) start() {
	if work.cancel == nil {
		work.ctx, work.cancel = context.WithTimeout(work.parent, work.timeout)
	}
}

func (work *schemaUpgradeWork) emit(phase SchemaUpgradePhase) error {
	if work.emitted {
		return work.ctx.Err()
	}
	work.emitted = true
	work.start()
	if work.progress != nil {
		if err := work.progress(phase); err != nil {
			return err
		}
	}
	if !snapshotOwns(work.owner, work.source, lockName) || !snapshotOwns(work.host, work.source, "server.lock") {
		return ErrTarget
	}
	return work.ctx.Err()
}

func (work *schemaUpgradeWork) close() {
	if work.cancel != nil {
		work.cancel()
	}
	if work.ownsHost {
		work.host.Release()
	}
}

type ownedSchemaSnapshot struct {
	path        string
	owner, host *instanceguard.Guard
	manifest    schemaSnapshotManifest
	container   string
}

func (snapshot *ownedSchemaSnapshot) close() {
	if snapshot.host != nil {
		snapshot.host.Release()
	}
	if snapshot.owner != nil {
		snapshot.owner.Release()
	}
	if snapshot.container != "" {
		_ = os.Remove(snapshot.container) // Only the exact unused empty container.
	}
}

func lockCertifiedSchemaSnapshot(ctx context.Context, target string, runner schemaSnapshotRunner) (*ownedSchemaSnapshot, error) {
	snapshot := &ownedSchemaSnapshot{path: target}
	var err error
	snapshot.owner, snapshot.host, err = lockSchemaSnapshot(target)
	if err != nil {
		return nil, err
	}
	snapshot.manifest, err = runner.certifySnapshotLocked(ctx, target)
	if err != nil {
		snapshot.close()
		return nil, ErrBackupInvalid
	}
	return snapshot, nil
}

func createOwnedSchemaSnapshot(ctx context.Context, source, target string, owner, host *instanceguard.Guard, now time.Time, state runtimepersistence.SchemaState, create func(context.Context, string, string, *instanceguard.Guard, *instanceguard.Guard, time.Time) error, runner schemaSnapshotRunner) (*ownedSchemaSnapshot, error) {
	container := ""
	if target == "" {
		var err error
		container, err = os.MkdirTemp(filepath.Dir(source), ".vibermate-schema-upgrade-")
		if err != nil {
			return nil, ErrCopy
		}
		defer func() {
			if container != "" {
				_ = os.Remove(container)
			}
		}()
		target = filepath.Join(container, "snapshot")
	}
	if err := snapshotRoots(ctx, source, target); err != nil {
		return nil, err
	}
	if err := create(ctx, source, target, owner, host, now); err != nil {
		return nil, err
	}
	snapshot, err := lockCertifiedSchemaSnapshot(ctx, target, runner)
	if err != nil {
		return nil, err
	}
	if snapshot.manifest.Database != state {
		snapshot.close()
		return nil, ErrBackupInvalid
	}
	snapshot.container = container
	container = "" // Ownership transfers to snapshot.close.
	return snapshot, nil
}

func removeNotCommittedReceipt(ctx context.Context, source string, migrationErr error, remove func(context.Context, string) error) error {
	if errors.Is(migrationErr, runtimepersistence.ErrMigrationNotCommitted) && !errors.Is(migrationErr, runtimepersistence.ErrMigrationOutcomeUnknown) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := remove(cleanup, source); err != nil {
			return err
		}
	}
	return migrationErr
}

func consumeCommittedSchemaReceipt(ctx context.Context, source string, owner, host *instanceguard.Guard, receipt schemaUpgradeReceipt, result runtimepersistence.MigrationResult, toRevision int64, write func(context.Context, string, schemaUpgradeReceipt) error, remove func(context.Context, string) error) (runtimepersistence.MigrationResult, error) {
	fail := func(err error) (runtimepersistence.MigrationResult, error) {
		return result, errors.Join(ErrSchemaUpgradeIncomplete, err)
	}
	if !result.Committed || result.ToRevision != toRevision || !snapshotOwns(owner, source, lockName) || !snapshotOwns(host, source, "server.lock") {
		return fail(runtimepersistence.ErrMigrationOutcomeUnknown)
	}
	// Caller cancellation never starts normal writes, even after known commit.
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	receipt.Status = "committed"
	if err := write(ctx, source, receipt); err != nil {
		return fail(err)
	}
	if err := remove(ctx, source); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return result, nil
}
