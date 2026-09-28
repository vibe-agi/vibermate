package runtimepersistence

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func TestAccountSettingsPersistExplicitConsentInOneRecord(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, path)
	ref, _ := secretstore.ParseReference("secret://provider-account/settings")
	origin, _ := originidentity.ParseProviderOrigin("https://chatgpt.com")
	now := time.Unix(1786200000, 0).UTC()
	account := provideraccount.Account{ID: "settings", DisplayName: "Settings", Origin: origin, RealmID: "openai.chatgpt", Driver: providerauth.CodexOAuthDriverRef(), SecretRef: ref, State: provideraccount.StateActive, Revision: 1, AssociationRevision: 1, SettingsRevision: 1, CreatedAt: now, UpdatedAt: now}
	if result, err := store.ProviderAccountRepository().Write(ctx, 0, account); err != nil || result.Outcome != provideraccount.CommitCommitted {
		t.Fatalf("create: %+v %v", result, err)
	}
	loaded, exists, err := store.ProviderAccountRepository().Load(ctx, account.ID)
	if err != nil || !exists || loaded.AutomaticRefresh || loaded.SettingsRevision != 1 {
		t.Fatalf("explicit consent changed: %+v %v", loaded, err)
	}
	account.AutomaticRefresh = true
	account.SettingsRevision = 2
	account.EgressProfile = egressprofile.Direct()
	account.EgressProfile.ID = "profile.us"
	account.EgressProfile.DisplayName = "US"
	account.EgressProfile.Policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:1080"}
	if result, err := store.ProviderAccountRepository().WriteSettings(ctx, 1, account); err != nil || result.Outcome != provideraccount.CommitCommitted {
		t.Fatalf("settings: %+v %v", result, err)
	}
	if result, err := store.ProviderAccountRepository().WriteSettings(ctx, 1, account); err != nil || result.Outcome != provideraccount.CommitConflict {
		t.Fatalf("stale CAS: %+v %v", result, err)
	}
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	store = openTestStore(t, path)
	defer store.Shutdown(ctx)
	loaded, exists, err = store.ProviderAccountRepository().Load(ctx, account.ID)
	if err != nil || !exists || loaded != account {
		t.Fatalf("restart changed settings: %+v %v", loaded, err)
	}
	if result, err := store.ProviderAccountRepository().Delete(ctx, account.ID, 1); err != nil || result.Outcome != provideraccount.CommitCommitted {
		t.Fatalf("delete: %+v %v", result, err)
	}
	var rows int
	if err := store.database.QueryRowContext(ctx, `SELECT count(*) FROM provider_accounts`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("deleted account remained", rows, err)
	}
	if err := store.database.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name IN ('provider_account_settings','provider_account_settings_schema')`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("runtime created a second account settings schema", rows, err)
	}
}
