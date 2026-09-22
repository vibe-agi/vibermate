package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func TestNotesPreserveAccountsAcrossDevelopmentUpgradeAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, path)
	ref, _ := secretstore.ParseReference("secret://provider-account/noted")
	now := time.Unix(1786200000, 0).UTC()
	account := provideraccount.Account{ID: "noted", DisplayName: "Noted", Origin: providerTestOrigin(t), RealmID: "anthropic.official", Driver: providerauth.AnthropicAPIKeyDriverRef(), SecretRef: ref, State: provideraccount.StateActive, Revision: 1, AssociationRevision: 1, CreatedAt: now, UpdatedAt: now}
	if result, err := store.ProviderAccountRepository().Write(ctx, 0, account); err != nil || result.Outcome != provideraccount.CommitCommitted {
		t.Fatalf("create: %+v %v", result, err)
	}
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	prior := strings.Replace(schemaSQL, accountNoteColumnsSQL, "", 1)
	if fmt.Sprintf("%x", sha256.Sum256([]byte(prior))) != accountNotesDevelopmentDigest {
		t.Fatal("previous baseline fixture drifted")
	}
	db := sql.OpenDB(newSQLiteConnector(path, DefaultBusyTimeout))
	if _, err := db.Exec(`ALTER TABLE provider_accounts DROP COLUMN note; ALTER TABLE provider_accounts DROP COLUMN note_revision; UPDATE runtime_metadata SET schema_source_sha256 = ?`, accountNotesDevelopmentDigest); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store = openTestStore(t, path)
	loaded, exists, err := store.ProviderAccountRepository().Load(ctx, account.ID)
	if err != nil || !exists || loaded != account {
		t.Fatalf("upgrade lost account: %+v %v", loaded, err)
	}
	account.Note = "\u7814\u53d1\u81ea\u7528"
	account.NoteRevision = 1
	account.UpdatedAt = now.Add(time.Second)
	if result, err := store.ProviderAccountRepository().WriteNote(ctx, 0, account); err != nil || result.Outcome != provideraccount.CommitCommitted {
		t.Fatalf("note: %+v %v", result, err)
	}
	if result, err := store.ProviderAccountRepository().WriteNote(ctx, 0, account); err != nil || result.Outcome != provideraccount.CommitConflict {
		t.Fatalf("CAS: %+v %v", result, err)
	}
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	store = openTestStore(t, path)
	defer store.Shutdown(ctx)
	loaded, exists, err = store.ProviderAccountRepository().Load(ctx, account.ID)
	if err != nil || !exists || loaded != account {
		t.Fatalf("restart lost note: %+v %v", loaded, err)
	}
	account.Note = ""
	account.NoteRevision = 2
	if result, err := store.ProviderAccountRepository().WriteNote(ctx, 1, account); err != nil || result.Outcome != provideraccount.CommitCommitted {
		t.Fatalf("clear: %+v %v", result, err)
	}
}
