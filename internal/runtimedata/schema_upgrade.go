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
	LegacyDigest  string `json:"legacyDigest,omitempty"`
	SourceDigest  string `json:"sourceDigest,omitempty"`
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
		if receipt.Schema != "runtime-schema-upgrade-receipt/v1" || !validSchemaUpgradeReceipt(receipt, source) {
			return fail(ErrBackupInvalid)
		}
		snapshot, err := lockCertifiedSchemaSnapshot(work.ctx, receipt.Snapshot, runner.snapshot)
		if err != nil {
			return fail(err)
		}
		defer snapshot.close()
		if snapshot.manifest.Database.Revision != 1 || snapshot.manifest.Database.InitializedAt != receipt.InitializedAt {
			return fail(ErrBackupInvalid)
		}
		var digest [32]byte
		decoded, _ := hex.DecodeString(receipt.LegacyDigest)
		copy(digest[:], decoded)
		result, err = runtimepersistence.ReconcileMigrationDatabase(work.ctx, filepath.Join(source, "runtime.db"), filepath.Join(receipt.Snapshot, "runtime.db"), owner, snapshot.owner, digest, timeout)
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
	if state.Revision != 1 {
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
	verified, err := runtimepersistence.VerifyMigrationDatabase(work.ctx, filepath.Join(source, "runtime.db"), filepath.Join(target, "runtime.db"), owner, snapshot.owner, runtimepersistence.DefaultBusyTimeout)
	if err != nil {
		return fail(err)
	}
	digest := verified.LegacyDigest()
	receipt = schemaUpgradeReceipt{Schema: "runtime-schema-upgrade-receipt/v1", Source: source, InitializedAt: state.InitializedAt, FromRevision: 1, ToRevision: 2, Snapshot: target, LegacyDigest: hex.EncodeToString(digest[:]), Status: "pending"}
	if err := runner.writeReceipt(work.ctx, source, receipt); err != nil {
		return fail(err)
	}
	result, err = runner.upgradeDatabase(work.ctx, runtimepersistence.MigrationInput{Database: verified, ReconcileTimeout: 30 * time.Second})
	if err != nil {
		return fail(removeNotCommittedReceipt(work.ctx, source, err, runner.removeReceipt))
	}
	return runner.consumeCommitted(work.ctx, source, owner, hostOwner, receipt, result)
}

func (runner schemaUpgradeRunner) consumeCommitted(ctx context.Context, source string, owner, host *instanceguard.Guard, receipt schemaUpgradeReceipt, result runtimepersistence.MigrationResult) (runtimepersistence.MigrationResult, error) {
	return consumeCommittedSchemaReceipt(ctx, source, owner, host, receipt, result, 2, runner.writeReceipt, runner.removeReceipt)
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
	digestText := r.LegacyDigest
	switch r.Schema {
	case "runtime-schema-upgrade-receipt/v1":
		if r.FromRevision != 1 || r.ToRevision != 2 || r.SourceDigest != "" {
			return false
		}
	case "runtime-schema-upgrade-receipt/v2":
		if r.FromRevision != 2 || r.ToRevision != 3 || r.LegacyDigest != "" {
			return false
		}
		digestText = r.SourceDigest
	default:
		return false
	}
	if r.Source != source || (r.Status != "pending" && r.Status != "committed") || len(digestText) != 64 {
		return false
	}
	digest, err := hex.DecodeString(digestText)
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
	encoded, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(encoded) > 64*1024 {
		return receipt, true, ErrBackupInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return receipt, true, ErrBackupInvalid
	}
	fields := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return receipt, true, ErrBackupInvalid
		}
		key, ok := token.(string)
		if !ok || fields[key] {
			return receipt, true, ErrBackupInvalid
		}
		fields[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return receipt, true, ErrBackupInvalid
		}
	}
	if _, err := decoder.Token(); err != nil {
		return receipt, true, ErrBackupInvalid
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return receipt, true, ErrBackupInvalid
	}
	decoder = json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return receipt, true, ErrBackupInvalid
	}
	digestKey := "legacyDigest"
	if receipt.Schema == "runtime-schema-upgrade-receipt/v2" {
		digestKey = "sourceDigest"
	}
	if len(fields) != 8 {
		return receipt, true, ErrBackupInvalid
	}
	for _, key := range []string{"schema", "source", "initializedAt", "fromRevision", "toRevision", "snapshot", digestKey, "status"} {
		if !fields[key] {
			return receipt, true, ErrBackupInvalid
		}
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
