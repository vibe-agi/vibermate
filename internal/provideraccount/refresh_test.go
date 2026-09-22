package provideraccount

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func (preparer *rotatingCredentialPreparer) Refresh(ctx context.Context, driver providerauth.DriverRef, ref secretstore.Reference, revision secretstore.Revision) (secretstore.Revision, error) {
	return preparer.Prepare(ctx, driver, ref, revision)
}

func TestExplicitRefreshChecksEpochAndDoesNotRetargetIdentity(t *testing.T) {
	ctx := context.Background()
	secrets := newMemorySecrets()
	manager, err := NewManager(ctx, &memoryRepository{accounts: make(map[ID]Account)}, secrets, testEndpoints(t), BuiltInRealms(), nil)
	if err != nil {
		t.Fatal(err)
	}
	material, err := providerauth.NewMaterial("synthetic-oauth", map[string]string{"User-Agent": "synthetic-agent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	encoded, err := material.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	value, err := secretstore.NewValue(encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	before, err := manager.Create(ctx, CreateCommand{ID: "oauth", DisplayName: "OAuth", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.CodexOAuthDriverRef(), Secret: value})
	if err != nil {
		t.Fatal(err)
	}
	preparer := &rotatingCredentialPreparer{secrets: secrets, replacement: value}
	if err := manager.BindCredentialPreparer(preparer); err != nil {
		t.Fatal(err)
	}
	after, err := manager.RefreshCredential(ctx, before.Account.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if after.Account != before.Account || after.Health.CredentialEpoch != 2 || len(after.SetHeaderNames) != 1 {
		t.Fatal("refresh changed account identity or lost header policy")
	}
	if _, err := manager.RefreshCredential(ctx, before.Account.ID, 1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale epoch error=%v", err)
	}
	if _, err := manager.RefreshCredential(ctx, before.Account.ID, 0); !errors.Is(err, ErrInvalidAccount) {
		t.Fatalf("zero epoch error=%v", err)
	}
	if preparer.calls != 1 {
		t.Fatal("stale click started a second exchange")
	}
}
