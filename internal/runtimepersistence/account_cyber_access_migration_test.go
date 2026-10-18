package runtimepersistence

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/cyberaccess"
)

func TestCyberAccessMigrationDefaultsExistingAccountsToStandard(t *testing.T) {
	fixture := populatedV2MigrationFixture(t, false)
	verified, err := VerifyRevision2MigrationDatabase(context.Background(), filepath.Join(fixture.source, "runtime.db"), filepath.Join(fixture.snapshot, "runtime.db"), fixture.owner, fixture.snapshotOwner, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := UpgradeRevision2(context.Background(), Revision2MigrationInput{Database: verified})
	if err != nil || !result.Committed {
		t.Fatalf("upgrade: %+v %v", result, err)
	}
	fixture.assertLegacyPreserved(t)
	db := sql.OpenDB(newSQLiteReadConnector(filepath.Join(fixture.source, "runtime.db"), time.Second))
	defer db.Close()
	store := &Store{database: db, providerAccounts: newProviderAccountRepository(db, newOperationGate(), time.Second, sqlTransactionCommitter{})}
	account, exists, err := store.ProviderAccountRepository().Load(context.Background(), "native-legacy")
	if err != nil || !exists || account.CyberAccessProgram != cyberaccess.Standard || account.SettingsRevision != 8 || account.Revision != 1 {
		t.Fatalf("upgrade changed existing consent or revision: %+v %v", account, err)
	}
	var preference string
	if err := store.database.QueryRow(`SELECT cyber_access_program FROM provider_accounts WHERE account_id='native-legacy'`).Scan(&preference); err != nil || preference != "standard" {
		t.Fatalf("legacy preference default: %q %v", preference, err)
	}
	writer := openSQLiteForTest(t, filepath.Join(fixture.source, "runtime.db"))
	defer writer.Close()
	if _, err := writer.Exec(`UPDATE provider_accounts SET cyber_access_program='daybreak_red'`); err != nil {
		t.Fatal(err)
	}
	if err := revision2AdditionsStandard(context.Background(), store.database); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("migration certification accepted invented opt-in: %v", err)
	}
}

func TestUnsupportedPrivateV2WithUnpublishedColumnIsRejectedUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	database := openSQLiteForTest(t, path)
	previous := schemaSQL
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
