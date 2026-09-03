package egressprofile

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
)

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type memoryRepository struct {
	mu        sync.Mutex
	revisions map[ID]map[Revision]ProfileRevision
	current   map[ID]Revision
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		revisions: map[ID]map[Revision]ProfileRevision{},
		current:   map[ID]Revision{},
	}
}

func (repository *memoryRepository) Write(
	_ context.Context,
	expected Revision,
	candidate ProfileRevision,
) (CommitResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	actual := repository.current[candidate.ID]
	if actual != expected {
		return CommitResult{Outcome: CommitConflict, ActualRevision: actual}, nil
	}
	if repository.revisions[candidate.ID] == nil {
		repository.revisions[candidate.ID] = map[Revision]ProfileRevision{}
	}
	repository.revisions[candidate.ID][candidate.Revision] = candidate
	repository.current[candidate.ID] = candidate.Revision
	return CommitResult{Outcome: CommitCommitted, Revision: candidate}, nil
}

func (repository *memoryRepository) LoadRevision(
	_ context.Context,
	id ID,
	revision Revision,
) (ProfileRevision, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	value, exists := repository.revisions[id][revision]
	return value, exists, nil
}

func (repository *memoryRepository) LoadCurrent(context.Context) ([]ProfileRevision, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	profiles := make([]ProfileRevision, 0, len(repository.current))
	for id, revision := range repository.current {
		profiles = append(profiles, repository.revisions[id][revision])
	}
	return profiles, nil
}

func TestProfilesPublishImmutableRevisionsBesideBuiltInDirect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	publishedAt := time.Date(2026, time.August, 27, 13, 14, 15, 987654321, time.FixedZone("SGT", 8*60*60))
	manager, err := NewManager(newMemoryRepository(), fixedClock{now: publishedAt})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Publish(ctx, PublishCommand{
		ID: "profile.office", DisplayName: "Office proxy",
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
	second, err := manager.Publish(ctx, PublishCommand{
		ID: "profile.office", ExpectedRevision: first.Revision,
		DisplayName: "Office proxy", Policy: egressnetwork.Policy{
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
	old, err := manager.GetRevision(ctx, first.ID, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || second.Revision != 2 ||
		old.Policy.Proxy.Endpoint != "127.0.0.1:7890" ||
		second.Policy.Proxy.Endpoint != "127.0.0.1:7891" {
		t.Fatalf("revisions changed: first=%+v old=%+v second=%+v", first, old, second)
	}
	if want := publishedAt.UTC().Truncate(time.Millisecond); !first.PublishedAt.Equal(want) {
		t.Fatalf("PublishedAt = %s, want %s", first.PublishedAt, want)
	}
	profiles, err := manager.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || !profiles[0].Equal(Direct()) || !profiles[1].Equal(second) {
		t.Fatalf("List() = %+v, want built-in direct and current custom profile", profiles)
	}
}
