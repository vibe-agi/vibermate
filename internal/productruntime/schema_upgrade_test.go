package productruntime

import (
	"context"
	"database/sql"
	"errors"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSchemaSwitchStartupUsesHeldGuards(t *testing.T) {
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	guard, err := instanceguard.Acquire(filepath.Join(options.Paths.DataDirectory(), "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	_, err = Start(context.Background(), options)
	if !errors.Is(err, instanceguard.ErrAlreadyOwned) {
		t.Fatalf("existing host admitted: %v", err)
	}
	options.SchemaUpgradeHostGuard = guard
	runtime, err := Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Shutdown(context.Background())
	if !guard.OwnsDirectory(options.Paths.DataDirectory()) {
		t.Fatal("caller host ownership released")
	}
}

func schemaStartupV1(t *testing.T) Options {
	t.Helper()
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	frozen, err := os.ReadFile("../runtimepersistence/schema/v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", options.Paths.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(frozen) + `;INSERT INTO runtime_metadata VALUES(1,'vibermate-runtime',1,'2026-10-03T00:00:00Z');UPDATE runtime_usage_policy SET enabled=0,retention_days=90,collecting_since_unix_ms=NULL`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return options
}
func TestSchemaSwitchSecondHostCannotEnter(t *testing.T) {
	options := schemaStartupV1(t)
	var phases int
	options.SchemaUpgradeProgress = func(phase runtimedata.SchemaUpgradePhase) error {
		phases++
		for _, name := range []string{"server.lock", "runtime-data.lock"} {
			guard, err := instanceguard.Acquire(filepath.Join(options.Paths.DataDirectory(), name))
			if guard != nil {
				guard.Release()
				t.Fatal("second host entered")
			}
			if !errors.Is(err, instanceguard.ErrAlreadyOwned) {
				t.Fatal(err)
			}
		}
		return nil
	}
	runtime, err := Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Shutdown(context.Background())
	if phases < 1 || runtime.Status().SchemaRevision != 3 {
		t.Fatal("startup did not perform one verified upgrade")
	}
	policy, err := runtime.storage.UsagePolicyRepository().UsagePolicy(context.Background())
	if err != nil || policy.Enabled || policy.RetentionDays != 90 {
		t.Fatal("startup reseeded opt-out")
	}
	if _, err := os.Lstat(filepath.Join(options.Paths.DataDirectory(), runtimepersistence.SchemaUpgradeReceiptName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("normal writers began with upgrade receipt")
	}
}
func TestSchemaSwitchDeadlineCannotActivateBackground(t *testing.T) {
	options := schemaStartupV1(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	options.SchemaUpgradeProgress = func(runtimedata.SchemaUpgradePhase) error { cancel(); return nil }
	runtime, err := Start(ctx, options)
	if runtime != nil || !errors.Is(err, runtimedata.ErrSchemaUpgradeIncomplete) {
		t.Fatalf("canceled startup=%v %v", runtime, err)
	}
	state, err := runtimepersistence.InspectSchema(context.Background(), options.Paths.DatabasePath(), time.Second)
	if err != nil || state.Revision != 1 {
		t.Fatal("cancel changed old source")
	}
	for _, name := range []string{"runtime-data.lock", "server.lock"} {
		guard, err := instanceguard.Acquire(filepath.Join(options.Paths.DataDirectory(), name))
		if err != nil {
			t.Fatal("failed startup left an activating owner")
		}
		guard.Release()
	}
}

func TestSchemaSwitchCurrent2NeedsNoWritableParent(t *testing.T) {
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	runtime, err := Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(options.Paths.DataDirectory())
	if err := os.Chmod(parent, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0700)
	options.OfflineHold = &coordinatorDouble{}
	runtime, err = Start(context.Background(), options)
	if err != nil {
		t.Fatalf("healthy current2 required sibling writes: %v", err)
	}
	runtime.Shutdown(context.Background())
}
