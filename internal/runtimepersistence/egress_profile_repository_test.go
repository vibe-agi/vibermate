package runtimepersistence

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
)

func TestEgressProfilesReopenEveryPublishedRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, databasePath)
	manager, err := egressprofile.NewManager(store.EgressProfileRepository(), nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Publish(ctx, egressprofile.PublishCommand{
		ID: "profile.office", DisplayName: "Office",
		Policy: egressnetwork.Policy{
			Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:7890"},
			Resolver: egressnetwork.ResolverPolicy{
				Kind: egressnetwork.ResolverDoH, DoHURL: "https://8.8.8.8/dns-query",
				Transport: egressnetwork.ResolverTransportProxy,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Publish(ctx, egressprofile.PublishCommand{
		ID: "profile.office", ExpectedRevision: first.Revision, DisplayName: "Office",
		Policy: egressnetwork.Policy{
			Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:7891"},
			Resolver: egressnetwork.ResolverPolicy{
				Kind:      egressnetwork.ResolverSystem,
				Transport: egressnetwork.ResolverTransportDirect,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, databasePath)
	defer shutdownTestStore(t, reopened)
	recovered, err := egressprofile.NewManager(reopened.EgressProfileRepository(), nil)
	if err != nil {
		t.Fatal(err)
	}
	old, err := recovered.GetRevision(ctx, first.ID, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := recovered.GetRevision(ctx, second.ID, second.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if old.Policy.Proxy.Endpoint != "127.0.0.1:7890" ||
		latest.Policy.Proxy.Endpoint != "127.0.0.1:7891" ||
		!old.PublishedAt.Equal(first.PublishedAt) ||
		!latest.PublishedAt.Equal(second.PublishedAt) {
		t.Fatalf("reopened profiles changed: old=%+v latest=%+v", old, latest)
	}
	profiles, err := recovered.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || !profiles[0].Equal(egressprofile.Direct()) ||
		!profiles[1].Equal(latest) {
		t.Fatalf("List() = %+v, want direct and current custom profile", profiles)
	}
}
