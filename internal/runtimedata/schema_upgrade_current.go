package runtimedata

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

// UpgradeSchemaToCurrentOwned retains both directory owners and one work budget
// across the two concrete published transitions. No Store is admitted between them.
func UpgradeSchemaToCurrentOwned(ctx context.Context, source, target string, owner, host *instanceguard.Guard, now time.Time, timeout time.Duration, progress func(SchemaUpgradePhase) error) (runtimepersistence.MigrationResult, error) {
	var result runtimepersistence.MigrationResult
	work, err := newSchemaUpgradeWork(ctx, source, owner, host, now, timeout, progress)
	if err != nil {
		return result, err
	}
	defer work.close()
	host, timeout = work.host, work.timeout
	// Current startup owns one deadline and one Desktop readiness notification
	// across both concrete transitions, including their preparatory inspection.
	work.start()
	progress = func(phase SchemaUpgradePhase) error {
		if err := work.ctx.Err(); err != nil {
			return err
		}
		return work.emit(phase)
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
			if err := work.ctx.Err(); err != nil {
				return result, errors.Join(ErrSchemaUpgradeIncomplete, err)
			}
			if !snapshotOwns(owner, source, lockName) || !snapshotOwns(host, source, "server.lock") {
				return result, errors.Join(ErrSchemaUpgradeIncomplete, ErrTarget)
			}

		}
	}
	if exists && receipt.Schema == "runtime-schema-upgrade-receipt/v2" {
		return upgradeRevision2Owned(work.ctx, source, target, owner, host, now, timeout, progress)
	}
	if exists {
		result, err = UpgradeSchemaOwned(work.ctx, source, target, owner, host, now, timeout, progress)
		if err != nil {
			return result, err
		}
		target = ""
	} else {
		state, err := runtimepersistence.InspectSchema(work.ctx, filepath.Join(source, "runtime.db"), runtimepersistence.DefaultBusyTimeout)
		if err != nil {
			return result, err
		}
		if state.Revision == 0 || state.Revision == 3 {
			if !snapshotOwns(owner, source, lockName) || !snapshotOwns(host, source, "server.lock") {
				return result, ErrTarget
			}
			return result, work.ctx.Err()
		}
		if state.Revision == 1 {
			result, err = UpgradeSchemaOwned(work.ctx, source, target, owner, host, now, timeout, progress)
			if err != nil {
				return result, err
			}
			target = ""
		}
	}
	if err := work.ctx.Err(); err != nil {
		return result, errors.Join(ErrSchemaUpgradeIncomplete, err)
	}
	if !snapshotOwns(owner, source, lockName) || !snapshotOwns(host, source, "server.lock") {
		return result, errors.Join(ErrSchemaUpgradeIncomplete, ErrTarget)
	}
	return upgradeRevision2Owned(work.ctx, source, target, owner, host, now, timeout, progress)
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
	work, err := newSchemaUpgradeWork(ctx, source, owner, hostOwner, now, timeout, progress)
	if err != nil {
		return result, err
	}
	defer work.close()
	hostOwner, timeout = work.host, work.timeout
	emit := work.emit
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
		snapshot, err := lockCertifiedSchemaSnapshot(work.ctx, receipt.Snapshot, runner.snapshot)
		if err != nil {
			return fail(err)
		}
		defer snapshot.close()
		if snapshot.manifest.Database.Revision != 2 || snapshot.manifest.Database.InitializedAt != receipt.InitializedAt {
			return fail(ErrBackupInvalid)
		}
		var digest [32]byte
		decoded, _ := hex.DecodeString(receipt.SourceDigest)
		copy(digest[:], decoded)
		result, err = runtimepersistence.ReconcileRevision2MigrationDatabase(work.ctx, filepath.Join(source, "runtime.db"), filepath.Join(receipt.Snapshot, "runtime.db"), owner, snapshot.owner, digest, timeout)
		if errors.Is(err, runtimepersistence.ErrMigrationNotCommitted) {
			if err := runner.removeReceipt(work.ctx, source); err != nil {
				return fail(err)
			}
			// This startup remains failed. The next fresh startup may create a new snapshot.
			return fail(runtimepersistence.ErrMigrationNotCommitted)
		}
		if err != nil {
			return fail(err)
		}
		return runner.consumeCommitted(work.ctx, source, owner, hostOwner, receipt, result)
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
	state, err := runner.inspect(work.ctx, filepath.Join(source, "runtime.db"), runtimepersistence.DefaultBusyTimeout)
	if err != nil {
		if work.ctx.Err() != nil || work.emitted && !errors.Is(err, runtimepersistence.ErrUnsupportedSchema) {
			return fail(err)
		}
		return result, err
	}
	if !snapshotOwns(owner, source, lockName) || !snapshotOwns(hostOwner, source, "server.lock") {
		return fail(ErrTarget)
	}
	if state.Revision != 2 {
		if err := work.ctx.Err(); err != nil {
			return fail(err)
		}
		return result, nil
	}
	if err := emit(SchemaUpgradeRunning); err != nil {
		return fail(err)
	}
	snapshot, err := createOwnedSchemaSnapshot(work.ctx, source, target, owner, hostOwner, now, state, runner.create, runner.snapshot)
	if err != nil {
		return fail(err)
	}
	defer snapshot.close()
	target = snapshot.path
	verified, err := runtimepersistence.VerifyRevision2MigrationDatabase(work.ctx, filepath.Join(source, "runtime.db"), filepath.Join(target, "runtime.db"), owner, snapshot.owner, runtimepersistence.DefaultBusyTimeout)
	if err != nil {
		return fail(err)
	}
	digest := verified.SourceDigest()
	receipt = schemaUpgradeReceipt{Schema: "runtime-schema-upgrade-receipt/v2", Source: source, InitializedAt: state.InitializedAt, FromRevision: 2, ToRevision: 3, Snapshot: target, SourceDigest: hex.EncodeToString(digest[:]), Status: "pending"}
	if err := runner.writeReceipt(work.ctx, source, receipt); err != nil {
		return fail(err)
	}
	result, err = runner.upgradeDatabase(work.ctx, runtimepersistence.Revision2MigrationInput{Database: verified, ReconcileTimeout: 30 * time.Second})
	if err != nil {
		return fail(removeNotCommittedReceipt(work.ctx, source, err, runner.removeReceipt))
	}
	return runner.consumeCommitted(work.ctx, source, owner, hostOwner, receipt, result)
}

func (runner revision2UpgradeRunner) consumeCommitted(ctx context.Context, source string, owner, host *instanceguard.Guard, receipt schemaUpgradeReceipt, result runtimepersistence.MigrationResult) (runtimepersistence.MigrationResult, error) {
	return consumeCommittedSchemaReceipt(ctx, source, owner, host, receipt, result, 3, runner.writeReceipt, runner.removeReceipt)
}
