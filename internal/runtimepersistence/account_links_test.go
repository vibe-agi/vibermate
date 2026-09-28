package runtimepersistence

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestAccountLinksPersistIndependentlyWithCAS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, path)
	ref, err := secretstore.ParseReference("secret://provider-account/independent")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1786200000, 0).UTC()
	account := provideraccount.Account{ID: "independent", DisplayName: "Independent", Origin: providerTestOrigin(t), RealmID: "anthropic.official", Driver: providerauth.AnthropicAPIKeyDriverRef(), SecretRef: ref, State: provideraccount.StateActive, Revision: 1, SettingsRevision: 1, AssociationRevision: 1, CreatedAt: now, UpdatedAt: now}
	if result, err := store.ProviderAccountRepository().Write(ctx, 0, account); err != nil || result.Outcome != provideraccount.CommitCommitted {
		t.Fatalf("create: %+v %v", result, err)
	}
	account.Associations, err = provideraccount.NewEndpointAssociations([]upstreamendpoint.ID{"profile.first", "profile.second"})
	if err != nil {
		t.Fatal(err)
	}
	account.AssociationRevision = 2
	account.UpdatedAt = now.Add(time.Second)
	if result, err := store.ProviderAccountRepository().WriteAssociations(ctx, 1, account); err != nil || result.Outcome != provideraccount.CommitCommitted {
		t.Fatalf("link: %+v %v", result, err)
	}
	if result, err := store.ProviderAccountRepository().WriteAssociations(ctx, 1, account); err != nil || result.Outcome != provideraccount.CommitConflict || result.Actual != 2 {
		t.Fatalf("stale link: %+v %v", result, err)
	}
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	t.Cleanup(func() { _ = reopened.Shutdown(ctx) })
	loaded, exists, err := reopened.ProviderAccountRepository().Load(ctx, account.ID)
	if err != nil || !exists || loaded != account {
		t.Fatalf("reopen: %+v %t %v", loaded, exists, err)
	}
	loaded.Associations = provideraccount.EndpointAssociations{}
	loaded.AssociationRevision++
	if result, err := reopened.ProviderAccountRepository().WriteAssociations(ctx, 2, loaded); err != nil || result.Outcome != provideraccount.CommitCommitted || result.Account.SecretRef != ref || result.Account.Revision != 1 {
		t.Fatalf("unlink: %+v %v", result, err)
	}
}
