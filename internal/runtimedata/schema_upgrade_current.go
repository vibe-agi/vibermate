package runtimedata

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"os"
	"path/filepath"
	"time"
)

// UpgradeSchemaToCurrentOwned retains both directory owners and one work budget
// across the two concrete published transitions. No Store is admitted between them.
func UpgradeSchemaToCurrentOwned(ctx context.Context, source, target string, owner, host *instanceguard.Guard, now time.Time, timeout time.Duration, progress func(SchemaUpgradePhase) error) (runtimepersistence.MigrationResult, error) {
	var result runtimepersistence.MigrationResult
	if err := snapshotDirectory(ctx, source); err != nil {
		return result, err
	}
	if !snapshotOwns(owner, source, lockName) || now.IsZero() || now.Location() != time.UTC || timeout < 0 {
		return result, ErrTarget
	}
	if host == nil {
		var err error
		host, err = instanceguard.Acquire(filepath.Join(source, "server.lock"))
		if err != nil {
			return result, errors.Join(ErrBusy, err)
		}
		defer host.Release()
	}
	if !snapshotOwns(host, source, "server.lock") {
		return result, ErrTarget
	}
	if timeout == 0 {
		timeout = 15 * time.Minute
	}
	work, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Desktop bootstrap grants a single extended readiness budget. Both concrete
	// transitions share that one notification as well as this outer deadline.
	notifyProgress := progress
	notified := false
	progress = func(phase SchemaUpgradePhase) error {
		if err := work.Err(); err != nil {
			return err
		}
		if notified {
			return nil
		}
		notified = true
		if notifyProgress != nil {
			return notifyProgress(phase)
		}
		return work.Err()
	}

	receipt, exists, err := readSchemaUpgradeReceipt(source)
	if err != nil {
		return result, errors.Join(ErrSchemaUpgradeIncomplete, err)
	}
	if !exists {
		preparing, err := schemaUpgradeNeedsPreparation(source)
		if err != nil {
			return result, err
		}
		if preparing {
			if progress != nil {
				if err := progress(SchemaUpgradePreparing); err != nil {
					return result, errors.Join(ErrSchemaUpgradeIncomplete, err)
				}
			}
			if err := work.Err(); err != nil {
				return result, errors.Join(ErrSchemaUpgradeIncomplete, err)
			}
			if !snapshotOwns(owner, source, lockName) || !snapshotOwns(host, source, "server.lock") {
				return result, errors.Join(ErrSchemaUpgradeIncomplete, ErrTarget)
			}

		}
	}
	if exists && receipt.Schema == "runtime-schema-upgrade-receipt/v2" {
		return upgradeRevision2Owned(work, source, target, owner, host, now, timeout, progress)
	}
	if exists {
		result, err = UpgradeSchemaOwned(work, source, target, owner, host, now, timeout, progress)
		if err != nil {
			return result, err
		}
		target = ""
	} else {
		state, err := runtimepersistence.InspectSchema(work, filepath.Join(source, "runtime.db"), runtimepersistence.DefaultBusyTimeout)
		if err != nil {
			return result, err
		}
		if state.Revision == 0 || state.Revision == 3 {
			if !snapshotOwns(owner, source, lockName) || !snapshotOwns(host, source, "server.lock") {
				return result, ErrTarget
			}
			return result, work.Err()
		}
		if state.Revision == 1 {
			result, err = UpgradeSchemaOwned(work, source, target, owner, host, now, timeout, progress)
			if err != nil {
				return result, err
			}
			target = ""
		}
	}
	if err := work.Err(); err != nil {
		return result, errors.Join(ErrSchemaUpgradeIncomplete, err)
	}
	if !snapshotOwns(owner, source, lockName) || !snapshotOwns(host, source, "server.lock") {
		return result, errors.Join(ErrSchemaUpgradeIncomplete, ErrTarget)
	}
	return upgradeRevision2Owned(work, source, target, owner, host, now, timeout, progress)
}

type revision2UpgradeRunner struct {
	inspect         func(context.Context, string, time.Duration) (runtimepersistence.SchemaState, error)
	create          func(context.Context, string, string, *instanceguard.Guard, *instanceguard.Guard, time.Time) error
	snapshot        schemaSnapshotRunner
	upgradeDatabase func(context.Context, runtimepersistence.Revision2MigrationInput) (runtimepersistence.MigrationResult, error)
	writeReceipt    func(context.Context, string, schemaUpgradeReceipt) error
	removeReceipt   func(context.Context, string) error
}

func newRevision2UpgradeRunner() revision2UpgradeRunner {
	return revision2UpgradeRunner{inspect: runtimepersistence.InspectSchema, create: CreateSchemaUpgradeSnapshot, snapshot: newSchemaSnapshotRunner(), upgradeDatabase: runtimepersistence.UpgradeRevision2, writeReceipt: writeSchemaUpgradeReceipt, removeReceipt: removeSchemaUpgradeReceipt}
}

func upgradeRevision2Owned(ctx context.Context, source, target string, owner, hostOwner *instanceguard.Guard, now time.Time, timeout time.Duration, progress func(SchemaUpgradePhase) error) (runtimepersistence.MigrationResult, error) {
	return newRevision2UpgradeRunner().upgrade(ctx, source, target, owner, hostOwner, now, timeout, progress)
}
func (runner revision2UpgradeRunner) upgrade(ctx context.Context, source, target string, owner, hostOwner *instanceguard.Guard, now time.Time, timeout time.Duration, progress func(SchemaUpgradePhase) error) (runtimepersistence.MigrationResult, error) {
	var result runtimepersistence.MigrationResult
	if err := snapshotDirectory(ctx, source); err != nil {
		return result, err
	}
	if !snapshotOwns(owner, source, lockName) || now.IsZero() || now.Location() != time.UTC || timeout < 0 {
		return result, ErrTarget
	}
	if hostOwner == nil {
		var err error
		hostOwner, err = instanceguard.Acquire(filepath.Join(source, "server.lock"))
		if err != nil {
			return result, errors.Join(ErrBusy, err)
		}
		defer hostOwner.Release()
	}
	if !snapshotOwns(hostOwner, source, "server.lock") {
		return result, ErrTarget
	}
	if timeout == 0 {
		timeout = 15 * time.Minute
	}
	work := ctx
	cancel := func() {}
	defer func() { cancel() }()
	emitted := false
	emit := func(phase SchemaUpgradePhase) error {
		if emitted {
			return work.Err()
		}
		emitted = true
		work, cancel = context.WithTimeout(ctx, timeout)
		if progress != nil {
			if err := progress(phase); err != nil {
				return err
			}
		}
		if !snapshotOwns(owner, source, lockName) || !snapshotOwns(hostOwner, source, "server.lock") {
			return ErrTarget
		}
		return work.Err()
	}
	fail := func(err error) (runtimepersistence.MigrationResult, error) {
		return result, errors.Join(ErrSchemaUpgradeIncomplete, err)
	}
	receipt, exists, err := readSchemaUpgradeReceipt(source)
	if err != nil {
		return fail(err)
	}
	if exists {
		if err := emit(SchemaUpgradeRunning); err != nil {
			return fail(err)
		}
		if receipt.Schema != "runtime-schema-upgrade-receipt/v2" || !validSchemaUpgradeReceipt(receipt, source) {
			return fail(ErrBackupInvalid)
		}
		snapshotOwner, snapshotHost, err := lockSchemaSnapshot(receipt.Snapshot)
		if err != nil {
			return fail(err)
		}
		defer snapshotOwner.Release()
		defer snapshotHost.Release()
		manifest, err := runner.snapshot.certifySnapshotLocked(work, receipt.Snapshot)
		if err != nil || manifest.Database.Revision != 2 || manifest.Database.InitializedAt != receipt.InitializedAt {
			return fail(ErrBackupInvalid)
		}
		var digest [32]byte
		decoded, _ := hex.DecodeString(receipt.SourceDigest)
		copy(digest[:], decoded)
		result, err = runtimepersistence.ReconcileRevision2MigrationDatabase(work, filepath.Join(source, "runtime.db"), filepath.Join(receipt.Snapshot, "runtime.db"), owner, snapshotOwner, digest, timeout)
		if errors.Is(err, runtimepersistence.ErrMigrationNotCommitted) {
			if err := runner.removeReceipt(work, source); err != nil {
				return fail(err)
			}
			// This startup remains failed. The next fresh startup may create a new snapshot.
			return fail(runtimepersistence.ErrMigrationNotCommitted)
		}
		if err != nil {
			return fail(err)
		}
		return runner.consumeCommitted(work, source, owner, hostOwner, receipt, result)
	}
	preparing, err := schemaUpgradeNeedsPreparation(source)
	if err != nil {
		return result, err
	}
	if preparing {
		if err := emit(SchemaUpgradePreparing); err != nil {
			return fail(err)
		}
	}
	state, err := runner.inspect(work, filepath.Join(source, "runtime.db"), runtimepersistence.DefaultBusyTimeout)
	if err != nil {
		if work.Err() != nil || emitted && !errors.Is(err, runtimepersistence.ErrUnsupportedSchema) {
			return fail(err)
		}
		return result, err
	}
	if !snapshotOwns(owner, source, lockName) || !snapshotOwns(hostOwner, source, "server.lock") {
		return fail(ErrTarget)
	}
	if state.Revision != 2 {
		if err := work.Err(); err != nil {
			return fail(err)
		}
		return result, nil
	}
	if err := emit(SchemaUpgradeRunning); err != nil {
		return fail(err)
	}
	if target == "" {
		container, err := os.MkdirTemp(filepath.Dir(source), ".vibermate-schema-upgrade-")
		if err != nil {
			return fail(ErrCopy)
		}
		defer os.Remove(container) // Remove only the exact unused empty container.
		target = filepath.Join(container, "snapshot")
	}
	if err := snapshotRoots(work, source, target); err != nil {
		return fail(err)
	}
	if err := runner.create(work, source, target, owner, hostOwner, now); err != nil {
		return fail(err)
	}
	snapshotOwner, snapshotHost, err := lockSchemaSnapshot(target)
	if err != nil {
		return fail(err)
	}
	defer snapshotOwner.Release()
	defer snapshotHost.Release()
	manifest, err := runner.snapshot.certifySnapshotLocked(work, target)
	if err != nil || manifest.Database != state {
		return fail(ErrBackupInvalid)
	}
	verified, err := runtimepersistence.VerifyRevision2MigrationDatabase(work, filepath.Join(source, "runtime.db"), filepath.Join(target, "runtime.db"), owner, snapshotOwner, runtimepersistence.DefaultBusyTimeout)
	if err != nil {
		return fail(err)
	}
	digest := verified.SourceDigest()
	receipt = schemaUpgradeReceipt{Schema: "runtime-schema-upgrade-receipt/v2", Source: source, InitializedAt: state.InitializedAt, FromRevision: 2, ToRevision: 3, Snapshot: target, SourceDigest: hex.EncodeToString(digest[:]), Status: "pending"}
	if err := runner.writeReceipt(work, source, receipt); err != nil {
		return fail(err)
	}
	result, err = runner.upgradeDatabase(work, runtimepersistence.Revision2MigrationInput{Database: verified, ReconcileTimeout: 30 * time.Second})
	if err != nil {
		if errors.Is(err, runtimepersistence.ErrMigrationNotCommitted) && !errors.Is(err, runtimepersistence.ErrMigrationOutcomeUnknown) {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(work), 30*time.Second)
			defer cancel()
			if removeErr := runner.removeReceipt(cleanup, source); removeErr != nil {
				return fail(removeErr)
			}
		}
		return fail(err)
	}
	return runner.consumeCommitted(work, source, owner, hostOwner, receipt, result)
}

func (runner revision2UpgradeRunner) consumeCommitted(ctx context.Context, source string, owner, host *instanceguard.Guard, receipt schemaUpgradeReceipt, result runtimepersistence.MigrationResult) (runtimepersistence.MigrationResult, error) {
	fail := func(err error) (runtimepersistence.MigrationResult, error) {
		return result, errors.Join(ErrSchemaUpgradeIncomplete, err)
	}
	if !result.Committed || result.ToRevision != 3 || !snapshotOwns(owner, source, lockName) || !snapshotOwns(host, source, "server.lock") {
		return fail(runtimepersistence.ErrMigrationOutcomeUnknown)
	}
	// Caller cancellation never starts normal writes, even after known commit.
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	receipt.Status = "committed"
	if err := runner.writeReceipt(ctx, source, receipt); err != nil {
		return fail(err)
	}
	if err := runner.removeReceipt(ctx, source); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return result, nil
}
