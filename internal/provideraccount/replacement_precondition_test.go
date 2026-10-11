package provideraccount

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestReplacementPreconditionGuardsIncarnationUnderAuthorityLock(t *testing.T) {
	for _, scenario := range []string{"recreated", "wrong reference", "missing reference", "missing incarnation", "matching"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			secrets := newMemorySecrets()
			manager, err := NewManager(ctx, &memoryRepository{accounts: map[ID]Account{}}, secrets, testEndpoints(t), BuiltInRealms(), nil)
			if err != nil {
				t.Fatal(err)
			}
			value, err := newTestCredentialValue(t, "original-synthetic")
			if err != nil {
				t.Fatal(err)
			}
			defer value.Destroy()
			original, err := manager.Create(ctx, CreateCommand{ID: "account.incarnation", DisplayName: "Original", UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID,
				Driver: providerauth.AnthropicAPIKeyDriverRef(), Secret: value})
			if err != nil {
				t.Fatal(err)
			}
			condition := &ReplacementPrecondition{CreatedAt: original.Account.CreatedAt, SecretRef: original.Account.SecretRef}
			switch scenario {
			case "recreated":
				// Model a completed deletion/recreation with the same deterministic reference and epoch.
				manager.mu.Lock()
				candidate := manager.accounts[original.Account.ID]
				candidate.CreatedAt = candidate.CreatedAt.Add(time.Second)
				candidate.UpdatedAt = candidate.CreatedAt
				manager.accounts[original.Account.ID] = candidate
				manager.mu.Unlock()
			case "wrong reference":
				condition.SecretRef, _ = secretstore.ParseReference("secret://provider-account/other")
			case "missing reference":
				condition.SecretRef = secretstore.Reference{}
			case "missing incarnation":
				condition.CreatedAt = time.Time{}
			}
			fresh, err := newTestCredentialValue(t, "fresh-synthetic")
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Destroy()
			_, err = manager.ReplaceSecret(ctx, ReplaceSecretCommand{ID: original.Account.ID, ExpectedCredentialEpoch: 1, Secret: fresh, Precondition: condition})
			metadata, _ := secrets.Inspect(ctx, original.Account.SecretRef)
			if scenario == "matching" {
				if err != nil || metadata.Revision != 2 {
					t.Fatalf("matching replacement failed: %v epoch=%d", err, metadata.Revision)
				}
			} else if err == nil || metadata.Revision != 1 {
				t.Fatalf("invalid incarnation admitted: err=%v epoch=%d", err, metadata.Revision)
			} else if scenario == "recreated" || scenario == "wrong reference" {
				if !errors.Is(err, ErrRevisionConflict) {
					t.Fatalf("wrong conflict class: %v", err)
				}
			} else if !errors.Is(err, ErrInvalidAccount) {
				t.Fatalf("incomplete condition not invalid: %v", err)
			}
		})
	}
}
