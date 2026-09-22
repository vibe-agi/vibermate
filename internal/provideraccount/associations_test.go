package provideraccount

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestIndependentAccountLinksReuseOneCredentialAndUnlinkWithoutDeletion(t *testing.T) {
	ctx := context.Background()
	endpoints := testEndpoints(t)
	first := endpoints[upstreamendpoint.AnthropicOfficialID]
	second := first.Clone()
	second.ID = "profile.second"
	second.RealmID = second.ID.String()
	endpoints[second.ID] = second
	repository := &memoryRepository{accounts: map[ID]Account{}}
	secrets := newMemorySecrets()
	manager, err := NewManager(ctx, repository, secrets, endpoints, BuiltInRealms(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(ctx) })
	guard := &deletionGuard{}
	if err := manager.BindDeletionGuard(guard); err != nil {
		t.Fatal(err)
	}
	secret, err := newTestCredentialValue(t, "test-independent-credential")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	view, err := manager.Create(ctx, CreateCommand{ID: "independent", DisplayName: "Independent", UpstreamEndpointID: first.ID, Unlinked: true, Driver: providerauth.AnthropicAPIKeyDriverRef(), Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Account.Associations.IDs()) != 0 {
		t.Fatal("creating an account authorized a profile")
	}
	if _, found := manager.LookupAccount("independent", first.ID.String()); found {
		t.Fatal("unlinked account entered route catalog")
	}
	if _, err := manager.Create(ctx, CreateCommand{ID: "wrong.scope", DisplayName: "Wrong scope", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Unlinked: true, Driver: providerauth.AnthropicAPIKeyDriverRef(), Secret: secret}); !errors.Is(err, ErrEndpointMismatch) {
		t.Fatalf("unlinked creation bypassed credential scope: %v", err)
	}
	for _, endpoint := range []upstreamendpoint.Endpoint{first, second} {
		view, err = manager.SetAssociation(ctx, AssociationCommand{ID: view.Account.ID, EndpointID: endpoint.ID, ExpectedRevision: view.Account.AssociationRevision, Linked: true})
		if err != nil {
			t.Fatal(err)
		}
		if view.Account.Revision != 1 || view.Health.CredentialEpoch != 1 {
			t.Fatal("linking rotated account identity or credentials")
		}
		if descriptor, found := manager.LookupAccount("independent", endpoint.ID.String()); !found || descriptor.UpstreamEndpointID != endpoint.ID.String() || descriptor.RealmID != endpoint.RealmID {
			t.Fatalf("linked descriptor=%+v found=%t", descriptor, found)
		}
		lease, err := manager.AcquireEndpointCredential(ctx, view.Account.ID, endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.SetAssociation(ctx, AssociationCommand{ID: view.Account.ID, EndpointID: endpoint.ID, ExpectedRevision: view.Account.AssociationRevision}); !errors.Is(err, ErrAccountInUse) {
			t.Fatalf("unlinked an in-flight account: %v", err)
		}
		lease.Release()
	}
	if len(secrets.values) != 1 {
		t.Fatal("linking copied the credential")
	}
	guard.references = []environment.AccountReference{{EnvironmentID: "active.policy"}}
	if _, err := manager.SetAssociation(ctx, AssociationCommand{ID: view.Account.ID, EndpointID: first.ID, ExpectedRevision: view.Account.AssociationRevision}); !errors.Is(err, ErrAccountInUse) {
		t.Fatalf("unlinked a published account reference: %v", err)
	}
	guard.references = nil
	view, err = manager.SetAssociation(ctx, AssociationCommand{ID: view.Account.ID, EndpointID: first.ID, ExpectedRevision: view.Account.AssociationRevision, Linked: false})
	if err != nil {
		t.Fatal(err)
	}
	if view.Account.Associations.Contains(first.ID) || !view.Account.Associations.Contains(second.ID) || view.Health.CredentialEpoch != 1 {
		t.Fatalf("unlink altered another association or credential: %+v", view)
	}
	if _, found := manager.LookupAccount("independent", first.ID.String()); found {
		t.Fatal("unlinked profile still authorized")
	}
	if _, err := manager.Get(ctx, "independent"); err != nil {
		t.Fatal("unlink deleted the account")
	}
	if _, err := manager.SetAssociation(ctx, AssociationCommand{ID: view.Account.ID, EndpointID: first.ID, ExpectedRevision: 1, Linked: true}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale link err=%v", err)
	}
	foreign := first.Clone()
	foreign.ID = "profile.foreign"
	foreign.RealmID = foreign.ID.String()
	foreign.Origin, _ = originidentity.ParseProviderOrigin("https://other.example")
	endpoints[foreign.ID] = foreign
	if _, err := manager.SetAssociation(ctx, AssociationCommand{ID: view.Account.ID, EndpointID: foreign.ID, ExpectedRevision: view.Account.AssociationRevision, Linked: true}); !errors.Is(err, ErrEndpointMismatch) {
		t.Fatalf("foreign origin link err=%v", err)
	}
}

func TestEndpointAssociationsCanonicalAndBounded(t *testing.T) {
	for _, invalid := range []string{`null`, `["b","a"]`, `["a","a"]`, `["bad id"]`, `{}`} {
		if _, err := ParseEndpointAssociations(invalid); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
	set, err := NewEndpointAssociations([]upstreamendpoint.ID{"b", "a"})
	if err != nil || set.String() != `["a","b"]` {
		t.Fatalf("set=%v err=%v", set, err)
	}
	ids := set.IDs()
	ids[0] = "changed"
	if set.Contains("changed") {
		t.Fatal("caller mutated account associations")
	}
}
