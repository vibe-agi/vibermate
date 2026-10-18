package runtimepersistence

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDaybreakBlueMigrationDefaultsExistingAccountsOff(t *testing.T) {
	fixture := populatedV1MigrationFixture(t)
	result, err := UpgradeRevision1(context.Background(), fixture.input(t))
	if err != nil || !result.Committed {
		t.Fatalf("upgrade: %+v %v", result, err)
	}
	fixture.assertLegacyPreserved(t)
	store := openTestStore(t, filepath.Join(fixture.source, "runtime.db"))
	defer shutdownTestStore(t, store)
	account, exists, err := store.ProviderAccountRepository().Load(context.Background(), "native-legacy")
	if err != nil || !exists || account.DaybreakBlueEnabled || account.SettingsRevision != 1 || account.Revision != 1 {
		t.Fatalf("upgrade changed existing consent or revision: %+v %v", account, err)
	}
	var enabled int
	if err := store.database.QueryRow(`SELECT daybreak_blue_enabled FROM provider_accounts WHERE account_id='native-legacy'`).Scan(&enabled); err != nil || enabled != 0 {
		t.Fatalf("legacy consent default: %d %v", enabled, err)
	}
	if _, err := store.database.Exec(`UPDATE provider_accounts SET daybreak_blue_enabled=1`); err != nil {
		t.Fatal(err)
	}
	if err := migrationAdditionsEmpty(context.Background(), store.database); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("migration certification accepted invented opt-in: %v", err)
	}
}

func TestEarlierPrivateV2WithoutDaybreakBlueIsRejectedUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	database := openSQLiteForTest(t, path)
	previous := strings.Replace(schemaV2SQL, "  daybreak_blue_enabled INTEGER NOT NULL DEFAULT 0 CHECK(daybreak_blue_enabled IN (0,1)),\n", "", 1)
	nativeExec(t, database, previous)
	nativeExec(t, database, `INSERT INTO runtime_metadata VALUES(1,'vibermate-runtime',2,'2026-10-03T00:00:00Z')`)
	nativeExec(t, database, `INSERT INTO provider_accounts(account_id,display_name,credential_origin,endpoint_associations,association_revision,settings_revision,egress_profile_json,automatic_refresh,realm_id,driver_ref,secret_reference,state,revision,created_at_unix_ms,updated_at_unix_ms) VALUES('preserved','Preserved','https://chatgpt.com','[]',1,1,'{}',0,'openai.chatgpt','static_header','secret://provider-account/preserved','active',1,1,1)`)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), Options{DatabasePath: path, BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if store != nil {
		shutdownTestStore(t, store)
		t.Fatal("earlier private-v2 layout was accepted")
	}
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("private-v2 refusal: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("refused private-v2 database was changed: %v", err)
	}
}
