package provideraccount

import (
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestOverwriteReadbackIsEpochBoundAndCannotReadPrimaryCredential(t *testing.T) {
	ctx := context.Background()
	manager, err := NewManager(ctx, &memoryRepository{accounts: make(map[ID]Account)}, newMemorySecrets(), testEndpoints(t), BuiltInRealms(), fixedClock{now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	material, err := providerauth.NewMaterial("primary-secret", map[string]string{"User-Agent": "account-agent", "X-Custom-Secret": "custom-secret"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	encoded, err := material.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	secret, err := secretstore.NewValue(encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	view, err := manager.Create(ctx, CreateCommand{ID: "account-header", DisplayName: "Header account", UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID, Driver: providerauth.AnthropicAPIKeyDriverRef(), Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	lookup := providerauth.HeaderLookup{AccountID: view.Account.ID.String(), AccountRevision: 1, CredentialEpoch: 1, UpstreamEndpointID: view.Account.UpstreamEndpointID.String(), UpstreamEndpointRevision: 1, Name: "user-agent"}
	if value, err := manager.ReadOverwriteHeader(ctx, lookup); err != nil || value != "account-agent" {
		t.Fatalf("overwrite=%q err=%v", value, err)
	}
	for _, name := range []string{"Authorization", "X-Api-Key", "Cookie", "Missing", "", "User-Agent\r\n"} {
		invalid := lookup
		invalid.Name = name
		if value, err := manager.ReadOverwriteHeader(ctx, invalid); err == nil || value != "" {
			t.Fatalf("read non-overwrite %q", name)
		}
	}
	for _, mutate := range []func(*providerauth.HeaderLookup){
		func(v *providerauth.HeaderLookup) { v.AccountRevision++ },
		func(v *providerauth.HeaderLookup) { v.CredentialEpoch++ },
		func(v *providerauth.HeaderLookup) { v.UpstreamEndpointID = "other" },
		func(v *providerauth.HeaderLookup) { v.UpstreamEndpointRevision++ },
	} {
		invalid := lookup
		mutate(&invalid)
		if value, err := manager.ReadOverwriteHeader(ctx, invalid); err == nil || value != "" {
			t.Fatal("mismatched credential ownership returned a value")
		}
	}
	if _, err := manager.ReplaceSecret(ctx, ReplaceSecretCommand{ID: view.Account.ID, ExpectedCredentialEpoch: 1, Secret: secret}); err != nil {
		t.Fatal(err)
	}
	if value, err := manager.ReadOverwriteHeader(ctx, lookup); err == nil || value != "" {
		t.Fatal("rotated epoch fell back to current value even though value is identical")
	}
}
