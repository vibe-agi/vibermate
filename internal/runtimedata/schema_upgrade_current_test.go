package runtimedata

import (
	"context"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCurrentUpgradeCompletesBothStagesBeforeAdmission(t *testing.T) {
	source := frozenUpgradeFixture(t)
	owner, host := upgradeGuards(t, source)
	_, err := UpgradeSchemaToCurrentOwned(context.Background(), source, "", owner, host, time.Now().UTC(), time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, err := runtimepersistence.InspectSchema(context.Background(), filepath.Join(source, "runtime.db"), time.Second)
	if err != nil || state.Revision != 3 {
		t.Fatalf("current startup stopped at %+v: %v", state, err)
	}
}

func TestCurrentReceiptRejectsMixedAndDuplicateFields(t *testing.T) {
	source := frozenRevision2UpgradeFixture(t)
	owner, host := upgradeGuards(t, source)
	runner := newRevision2UpgradeRunner()
	runner.upgradeDatabase = func(context.Context, runtimepersistence.Revision2MigrationInput) (runtimepersistence.MigrationResult, error) {
		return runtimepersistence.MigrationResult{}, runtimepersistence.ErrMigrationOutcomeUnknown
	}
	_, err := runner.upgrade(context.Background(), source, "", owner, host, time.Now().UTC(), time.Minute, nil)
	if !errors.Is(err, ErrSchemaUpgradeIncomplete) {
		t.Fatal(err)
	}
	path := filepath.Join(source, schemaUpgradeReceiptName)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`,"legacyDigest":""`, `,"sourceDigest":""`, `,"status":"pending"`, `,"unexpected":false`} {
		mutated := append(append([]byte(nil), original[:len(original)-1]...), []byte(field+"}")...)
		if err := os.WriteFile(path, mutated, 0600); err != nil {
			t.Fatal(err)
		}
		receipt, exists, err := readSchemaUpgradeReceipt(source)
		if err == nil && exists && validSchemaUpgradeReceipt(receipt, source) {
			t.Fatalf("accepted mixed/duplicate protocol fields: %s", field)
		}
	}
}

func TestCurrentUpgradeCancellationAfterLegacyCommitRetainsV2AndRetries(t *testing.T) {
	source := frozenUpgradeFixture(t)
	owner, host := upgradeGuards(t, source)
	if _, err := UpgradeSchemaOwned(context.Background(), source, "", owner, host, time.Now().UTC(), time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := UpgradeSchemaToCurrentOwned(ctx, source, "", owner, host, time.Now().UTC(), time.Minute, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	state, err := runtimepersistence.InspectSchema(context.Background(), filepath.Join(source, "runtime.db"), time.Second)
	if err != nil || state.Revision != 2 {
		t.Fatalf("partial transition %+v %v", state, err)
	}
	if _, err = UpgradeSchemaToCurrentOwned(context.Background(), source, "", owner, host, time.Now().UTC(), time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	state, err = runtimepersistence.InspectSchema(context.Background(), filepath.Join(source, "runtime.db"), time.Second)
	if err != nil || state.Revision != 3 {
		t.Fatalf("retry %+v %v", state, err)
	}
}

func TestCurrentUpgradeDispatchesBothReceiptVersions(t *testing.T) {
	for _, from := range []int{1, 2} {
		t.Run(fmt.Sprint(from), func(t *testing.T) {
			source := frozenUpgradeFixture(t)
			if from == 2 {
				source = frozenRevision2UpgradeFixture(t)
			}
			owner, host := upgradeGuards(t, source)
			target := filepath.Join(filepath.Dir(source), "pending-snapshot")
			if from == 1 {
				runner := newSchemaUpgradeRunner()
				runner.upgradeDatabase = func(ctx context.Context, in runtimepersistence.MigrationInput) (runtimepersistence.MigrationResult, error) {
					result, err := runtimepersistence.UpgradeRevision1(ctx, in)
					if err != nil {
						return result, err
					}
					return result, runtimepersistence.ErrMigrationOutcomeUnknown
				}
				if _, err := runner.upgrade(context.Background(), source, target, owner, host, time.Now().UTC(), time.Minute, nil); !errors.Is(err, ErrSchemaUpgradeIncomplete) {
					t.Fatal(err)
				}
			} else {
				runner := newRevision2UpgradeRunner()
				runner.upgradeDatabase = func(ctx context.Context, in runtimepersistence.Revision2MigrationInput) (runtimepersistence.MigrationResult, error) {
					result, err := runtimepersistence.UpgradeRevision2(ctx, in)
					if err != nil {
						return result, err
					}
					return result, runtimepersistence.ErrMigrationOutcomeUnknown
				}
				if _, err := runner.upgrade(context.Background(), source, target, owner, host, time.Now().UTC(), time.Minute, nil); !errors.Is(err, ErrSchemaUpgradeIncomplete) {
					t.Fatal(err)
				}
			}
			receipt, exists, err := readSchemaUpgradeReceipt(source)
			if err != nil || !exists || receipt.FromRevision != int64(from) {
				t.Fatalf("pending receipt %+v %v", receipt, err)
			}
			before := snapshotTestFiles(t, target)
			if _, err := UpgradeSchemaToCurrentOwned(context.Background(), source, "", owner, host, time.Now().UTC(), time.Minute, nil); err != nil {
				t.Fatal(err)
			}
			state, err := runtimepersistence.InspectSchema(context.Background(), filepath.Join(source, "runtime.db"), time.Second)
			if err != nil || state.Revision != 3 {
				t.Fatalf("resume %+v %v", state, err)
			}
			if _, exists, err := readSchemaUpgradeReceipt(source); err != nil || exists {
				t.Fatal("current admitted pending receipt")
			}
			if fmt.Sprint(before) != fmt.Sprint(snapshotTestFiles(t, target)) {
				t.Fatal("resume mutated rollback source")
			}
		})
	}
}

func TestCurrentUpgradeReportsPreparingBeforeLargeWALInspection(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	db, err := os.Create(filepath.Join(source, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Truncate(128 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.WriteFile(filepath.Join(source, "runtime.db-wal"), make([]byte, 33), 0600); err != nil {
		t.Fatal(err)
	}
	owner, host := upgradeGuards(t, source)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := false
	_, err = UpgradeSchemaToCurrentOwned(ctx, source, "", owner, host, time.Now().UTC(), time.Second, func(phase SchemaUpgradePhase) error {
		called = true
		if phase != SchemaUpgradePreparing {
			t.Fatal(phase)
		}
		cancel()
		return nil
	})
	if !called || !errors.Is(err, ErrSchemaUpgradeIncomplete) || !errors.Is(err, context.Canceled) {
		t.Fatalf("large inspection before progress/cancel: called=%v error=%v", called, err)
	}
}
