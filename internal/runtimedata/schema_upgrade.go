package runtimedata

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

type SchemaUpgradePhase string

const (
	SchemaUpgradePreparing   SchemaUpgradePhase = "storage_preparing"
	SchemaUpgradeRunning     SchemaUpgradePhase = "storage_upgrade"
	schemaUpgradeReceiptName                    = runtimepersistence.SchemaUpgradeReceiptName
)

var ErrSchemaUpgradeIncomplete = errors.New("data upgrade incomplete; original data retained")

type schemaUpgradeReceipt struct {
	Schema        string `json:"schema"`
	Source        string `json:"source"`
	InitializedAt string `json:"initializedAt"`
	FromRevision  int64  `json:"fromRevision"`
	ToRevision    int64  `json:"toRevision"`
	Snapshot      string `json:"snapshot"`
	LegacyDigest  string `json:"legacyDigest"`
	Status        string `json:"status"`
}
type schemaUpgradeRunner struct {
	inspect         func(context.Context, string, time.Duration) (runtimepersistence.SchemaState, error)
	create          func(context.Context, string, string, *instanceguard.Guard, *instanceguard.Guard, time.Time) error
	snapshot        schemaSnapshotRunner
	upgradeDatabase func(context.Context, runtimepersistence.MigrationInput) (runtimepersistence.MigrationResult, error)
	writeReceipt    func(context.Context, string, schemaUpgradeReceipt) error
	removeReceipt   func(context.Context, string) error
}

func newSchemaUpgradeRunner() schemaUpgradeRunner {
	return schemaUpgradeRunner{inspect: runtimepersistence.InspectSchema, create: CreateSchemaUpgradeSnapshot, snapshot: newSchemaSnapshotRunner(), upgradeDatabase: runtimepersistence.UpgradeRevision1, writeReceipt: writeSchemaUpgradeReceipt, removeReceipt: removeSchemaUpgradeReceipt}
}

func UpgradeSchemaOwned(ctx context.Context, source, target string, owner, hostOwner *instanceguard.Guard, now time.Time, timeout time.Duration, progress func(SchemaUpgradePhase) error) (runtimepersistence.MigrationResult, error) {
	return newSchemaUpgradeRunner().upgrade(ctx, source, target, owner, hostOwner, now, timeout, progress)
}
func (runner schemaUpgradeRunner) upgrade(ctx context.Context, source, target string, owner, hostOwner *instanceguard.Guard, now time.Time, timeout time.Duration, progress func(SchemaUpgradePhase) error) (runtimepersistence.MigrationResult, error) {
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
		if !validSchemaUpgradeReceipt(receipt, source) {
			return fail(ErrBackupInvalid)
		}
		snapshotOwner, snapshotHost, err := lockSchemaSnapshot(receipt.Snapshot)
		if err != nil {
			return fail(err)
		}
		defer snapshotOwner.Release()
		defer snapshotHost.Release()
		manifest, err := runner.snapshot.certifySnapshotLocked(work, receipt.Snapshot)
		if err != nil || manifest.Database.Revision != 1 || manifest.Database.InitializedAt != receipt.InitializedAt {
			return fail(ErrBackupInvalid)
		}
		var digest [32]byte
		decoded, _ := hex.DecodeString(receipt.LegacyDigest)
		copy(digest[:], decoded)
		result, err = runtimepersistence.ReconcileMigrationDatabase(work, filepath.Join(source, "runtime.db"), filepath.Join(receipt.Snapshot, "runtime.db"), owner, snapshotOwner, digest, timeout)
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
	if state.Revision != 1 {
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
	verified, err := runtimepersistence.VerifyMigrationDatabase(work, filepath.Join(source, "runtime.db"), filepath.Join(target, "runtime.db"), owner, snapshotOwner, runtimepersistence.DefaultBusyTimeout)
	if err != nil {
		return fail(err)
	}
	digest := verified.LegacyDigest()
	receipt = schemaUpgradeReceipt{Schema: "runtime-schema-upgrade-receipt/v1", Source: source, InitializedAt: state.InitializedAt, FromRevision: 1, ToRevision: 2, Snapshot: target, LegacyDigest: hex.EncodeToString(digest[:]), Status: "pending"}
	if err := runner.writeReceipt(work, source, receipt); err != nil {
		return fail(err)
	}
	result, err = runner.upgradeDatabase(work, runtimepersistence.MigrationInput{Database: verified, ReconcileTimeout: 30 * time.Second})
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

func (runner schemaUpgradeRunner) consumeCommitted(ctx context.Context, source string, owner, host *instanceguard.Guard, receipt schemaUpgradeReceipt, result runtimepersistence.MigrationResult) (runtimepersistence.MigrationResult, error) {
	fail := func(err error) (runtimepersistence.MigrationResult, error) {
		return result, errors.Join(ErrSchemaUpgradeIncomplete, err)
	}
	if !result.Committed || result.ToRevision != 2 || !snapshotOwns(owner, source, lockName) || !snapshotOwns(host, source, "server.lock") {
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

func schemaUpgradeNeedsPreparation(source string) (bool, error) {
	db, err := os.Lstat(filepath.Join(source, "runtime.db"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !db.Mode().IsRegular() {
		return false, runtimepersistence.ErrInvalidDatabasePath
	}
	wal, err := os.Lstat(filepath.Join(source, "runtime.db-wal"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !wal.Mode().IsRegular() {
		return false, runtimepersistence.ErrInvalidDatabasePath
	}
	return db.Size() >= 128*1024*1024 && wal.Size() > 32, nil
}
func validSchemaUpgradeReceipt(r schemaUpgradeReceipt, source string) bool {
	if r.Schema != "runtime-schema-upgrade-receipt/v1" || r.Source != source || r.FromRevision != 1 || r.ToRevision != 2 || (r.Status != "pending" && r.Status != "committed") || len(r.LegacyDigest) != 64 {
		return false
	}
	digest, err := hex.DecodeString(r.LegacyDigest)
	if err != nil || bytes.Equal(digest, make([]byte, 32)) {
		return false
	}
	now, err := time.Parse(time.RFC3339Nano, r.InitializedAt)
	if err != nil || now.IsZero() || now.Location() != time.UTC {
		return false
	}
	if !filepath.IsAbs(r.Snapshot) || filepath.Clean(r.Snapshot) != r.Snapshot || within(source, r.Snapshot) || within(r.Snapshot, source) || r.Snapshot == string(filepath.Separator) {
		return false
	}
	// Production uses a private sibling container; direct callers may use a
	// sibling snapshot. Never allow a receipt to redirect to an unrelated root.
	parent := filepath.Dir(source)
	if filepath.Dir(r.Snapshot) != parent && filepath.Dir(filepath.Dir(r.Snapshot)) != parent {
		return false
	}
	physical, err := filepath.EvalSymlinks(r.Snapshot)
	return err == nil && physical == r.Snapshot
}
func readSchemaUpgradeReceipt(source string) (schemaUpgradeReceipt, bool, error) {
	var receipt schemaUpgradeReceipt
	path := filepath.Join(source, schemaUpgradeReceiptName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return receipt, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 || info.Mode().Perm() != 0600 {
		return receipt, true, ErrBackupInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return receipt, true, ErrBackupInvalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return receipt, true, ErrBackupInvalid
	}
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return receipt, true, ErrBackupInvalid
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return receipt, true, ErrBackupInvalid
	}
	return receipt, true, nil
}
func writeSchemaUpgradeReceipt(ctx context.Context, source string, receipt schemaUpgradeReceipt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return ErrCopy
	}
	file, err := os.CreateTemp(source, ".schema-upgrade-receipt-")
	if err != nil {
		return ErrCopy
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(encoded); err != nil {
		file.Close()
		return ErrCopy
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return ErrCopy
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(source, schemaUpgradeReceiptName)); err != nil {
		return ErrCopy
	}
	if err := newSchemaSnapshotRunner().syncPath(source); err != nil {
		return ErrCopy
	}
	return ctx.Err()
}
func removeSchemaUpgradeReceipt(ctx context.Context, source string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(source, schemaUpgradeReceiptName)); err != nil {
		return ErrCopy
	}
	if err := newSchemaSnapshotRunner().syncPath(source); err != nil {
		return ErrCopy
	}
	return ctx.Err()
}
