package runtimepersistence

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func TestProviderAccountRepositoryCASAndReopenWithoutSecretBytes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, path)
	reference, err := secretstore.ParseReference("secret://provider-account/anthropic-work")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_786_200_000, 0).UTC()
	account := provideraccount.Account{
		ID: "anthropic-work", DisplayName: "Anthropic Work", RealmID: "anthropic.official",
		Driver: providerauth.AnthropicAPIKeyDriverRef(), SecretRef: reference,
		State: provideraccount.StateActive, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	result, err := store.ProviderAccountRepository().Write(context.Background(), 0, account)
	if err != nil || result.Outcome != provideraccount.CommitCommitted || result.Account != account {
		t.Fatalf("create ProviderAccount = %+v err=%v", result, err)
	}
	conflict, err := store.ProviderAccountRepository().Write(context.Background(), 0, account)
	if err != nil || conflict.Outcome != provideraccount.CommitConflict || conflict.Actual != 1 {
		t.Fatalf("stale ProviderAccount create = %+v err=%v", conflict, err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	defer func() { _ = reopened.Shutdown(context.Background()) }()
	loaded, exists, err := reopened.ProviderAccountRepository().Load(context.Background(), account.ID)
	if err != nil || !exists || loaded != account {
		t.Fatalf("reopened ProviderAccount = %+v exists=%t err=%v", loaded, exists, err)
	}
	items, err := reopened.ProviderAccountRepository().LoadAll(context.Background())
	if err != nil || len(items) != 1 || items[0] != account {
		t.Fatalf("reopened ProviderAccounts = %+v err=%v", items, err)
	}
	if _, _, err := reopened.ProviderAccountRepository().Load(context.Background(), "bad id"); !errors.Is(err, provideraccount.ErrInvalidAccount) {
		t.Fatalf("invalid ProviderAccount ID error = %v", err)
	}
}

func TestProviderAccountRepositoryDeleteCASPersistsAcrossReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, path)
	reference, err := secretstore.ParseReference("secret://provider-account/unused")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_786_200_000, 0).UTC()
	account := provideraccount.Account{
		ID: "unused", DisplayName: "Unused", RealmID: "anthropic.official",
		Driver: providerauth.AnthropicAPIKeyDriverRef(), SecretRef: reference,
		State: provideraccount.StateActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if result, writeErr := store.ProviderAccountRepository().Write(context.Background(), 0, account); writeErr != nil || result.Outcome != provideraccount.CommitCommitted {
		t.Fatalf("create ProviderAccount = %+v err=%v", result, writeErr)
	}
	if result, deleteErr := store.ProviderAccountRepository().Delete(context.Background(), account.ID, 2); deleteErr != nil || result.Outcome != provideraccount.CommitConflict || result.Actual != 1 {
		t.Fatalf("stale delete = %+v err=%v", result, deleteErr)
	}
	if result, deleteErr := store.ProviderAccountRepository().Delete(context.Background(), account.ID, 1); deleteErr != nil || result.Outcome != provideraccount.CommitCommitted {
		t.Fatalf("delete ProviderAccount = %+v err=%v", result, deleteErr)
	}
	if _, exists, loadErr := store.ProviderAccountRepository().Load(context.Background(), account.ID); loadErr != nil || exists {
		t.Fatalf("deleted ProviderAccount exists=%t err=%v", exists, loadErr)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	defer func() { _ = reopened.Shutdown(context.Background()) }()
	if _, exists, loadErr := reopened.ProviderAccountRepository().Load(context.Background(), account.ID); loadErr != nil || exists {
		t.Fatalf("reopened deleted ProviderAccount exists=%t err=%v", exists, loadErr)
	}
}
