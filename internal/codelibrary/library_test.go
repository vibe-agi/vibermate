package codelibrary

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/accountselector"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type memoryRepository struct {
	mu                sync.Mutex
	collections       map[CollectionID]Collection
	revisions         map[TransformID]map[Revision]TransformRevision
	current           map[TransformID]Revision
	selectorRevisions map[AccountSelectorID]map[Revision]AccountSelectorRevision
	selectorCurrent   map[AccountSelectorID]Revision
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		collections:       map[CollectionID]Collection{},
		revisions:         map[TransformID]map[Revision]TransformRevision{},
		current:           map[TransformID]Revision{},
		selectorRevisions: map[AccountSelectorID]map[Revision]AccountSelectorRevision{},
		selectorCurrent:   map[AccountSelectorID]Revision{},
	}
}

func (repository *memoryRepository) WriteAccountSelector(
	_ context.Context,
	expected Revision,
	candidate AccountSelectorRevision,
) (AccountSelectorCommitResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.collections[candidate.CollectionID]; !exists {
		return AccountSelectorCommitResult{}, ErrCollectionNotFound
	}
	actual := repository.selectorCurrent[candidate.ID]
	if actual != expected {
		return AccountSelectorCommitResult{Outcome: CommitConflict, ActualRevision: actual}, nil
	}
	if repository.selectorRevisions[candidate.ID] == nil {
		repository.selectorRevisions[candidate.ID] = map[Revision]AccountSelectorRevision{}
	}
	repository.selectorRevisions[candidate.ID][candidate.Revision] = candidate
	repository.selectorCurrent[candidate.ID] = candidate.Revision
	return AccountSelectorCommitResult{Outcome: CommitCommitted, Revision: candidate}, nil
}

func (repository *memoryRepository) LoadAccountSelectorRevision(
	_ context.Context,
	id AccountSelectorID,
	revision Revision,
) (AccountSelectorRevision, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	value, exists := repository.selectorRevisions[id][revision]
	return value, exists, nil
}

func (repository *memoryRepository) CreateCollection(
	_ context.Context,
	collection Collection,
) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.collections[collection.ID]; exists {
		return ErrRevisionConflict
	}
	repository.collections[collection.ID] = collection
	return nil
}

func (repository *memoryRepository) WriteTransform(
	_ context.Context,
	expected Revision,
	candidate TransformRevision,
) (CommitResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.collections[candidate.CollectionID]; !exists {
		return CommitResult{}, ErrCollectionNotFound
	}
	actual := repository.current[candidate.ID]
	if actual != expected {
		return CommitResult{Outcome: CommitConflict, ActualRevision: actual}, nil
	}
	if repository.revisions[candidate.ID] == nil {
		repository.revisions[candidate.ID] = map[Revision]TransformRevision{}
	}
	repository.revisions[candidate.ID][candidate.Revision] = candidate
	repository.current[candidate.ID] = candidate.Revision
	return CommitResult{Outcome: CommitCommitted, Revision: candidate}, nil
}

func (repository *memoryRepository) LoadTransformRevision(
	_ context.Context,
	id TransformID,
	revision Revision,
) (TransformRevision, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	value, exists := repository.revisions[id][revision]
	return value, exists, nil
}

func (repository *memoryRepository) LoadCurrent(context.Context) (Catalog, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	catalog := Catalog{Collections: make([]Collection, 0, len(repository.collections))}
	for _, collection := range repository.collections {
		catalog.Collections = append(catalog.Collections, collection)
	}
	for id, revision := range repository.current {
		catalog.Transforms = append(catalog.Transforms, repository.revisions[id][revision])
	}
	for id, revision := range repository.selectorCurrent {
		catalog.AccountSelectors = append(
			catalog.AccountSelectors,
			repository.selectorRevisions[id][revision],
		)
	}
	return catalog, nil
}

func TestPublishingATransformCreatesImmutableRevisions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	publishedAt := time.Date(2026, time.August, 27, 9, 10, 11, 123456789, time.FixedZone("SGT", 8*60*60))
	manager, err := NewManager(newMemoryRepository(), fixedClock{now: publishedAt})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	collection, err := manager.CreateCollection(ctx, CreateCollectionCommand{
		ID: "privacy", DisplayName: "Privacy",
	})
	if err != nil {
		t.Fatalf("CreateCollection() error = %v", err)
	}
	first, err := manager.PublishTransform(ctx, PublishTransformCommand{
		ID: "home-redaction", CollectionID: collection.ID,
		DisplayName: "Home redaction", ExpectedRevision: 0,
		Policy: messagetransform.Policy{
			RequestJavaScript: `request.body = request.body.replaceAll("/Users/jack", "/Users/guest");`,
		},
	})
	if err != nil {
		t.Fatalf("PublishTransform(first) error = %v", err)
	}
	second, err := manager.PublishTransform(ctx, PublishTransformCommand{
		ID: "home-redaction", CollectionID: collection.ID,
		DisplayName: "Home redaction", ExpectedRevision: first.Revision,
		Policy: messagetransform.Policy{
			RequestJavaScript:  `context.home = "/Users/jack"; request.body = request.body.replaceAll(context.home, "/Users/guest");`,
			ResponseJavaScript: `response.body = response.body.replaceAll("/Users/guest", context.home);`,
		},
	})
	if err != nil {
		t.Fatalf("PublishTransform(second) error = %v", err)
	}
	old, err := manager.GetTransformRevision(ctx, first.ID, first.Revision)
	if err != nil {
		t.Fatalf("GetTransformRevision(first) error = %v", err)
	}
	if first.Revision != 1 || second.Revision != 2 {
		t.Fatalf("published revisions = %d, %d; want 1, 2", first.Revision, second.Revision)
	}
	if old.Policy.ResponseJavaScript != "" || old.Policy.RequestJavaScript != first.Policy.RequestJavaScript {
		t.Fatalf("first revision changed after publishing second: %#v", old.Policy)
	}
	wantPublishedAt := publishedAt.UTC().Truncate(time.Millisecond)
	if second.Policy.ResponseJavaScript == "" ||
		!first.PublishedAt.Equal(wantPublishedAt) || !second.PublishedAt.Equal(wantPublishedAt) {
		t.Fatalf("published revisions are incomplete: first=%+v second=%+v", first, second)
	}
	catalog, err := manager.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(catalog.Collections) != 1 || catalog.Collections[0] != collection ||
		len(catalog.Transforms) != 1 || !catalog.Transforms[0].Equal(second) {
		t.Fatalf("List() = %+v, want collection with current revision 2", catalog)
	}
}

func TestPublishingAnAccountSelectorCreatesImmutableRevisions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	publishedAt := time.Date(2026, time.August, 28, 13, 14, 15, 0, time.UTC)
	manager, err := NewManager(newMemoryRepository(), fixedClock{now: publishedAt})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	collection, err := manager.CreateCollection(ctx, CreateCollectionCommand{
		ID: "routing", DisplayName: "Routing",
	})
	if err != nil {
		t.Fatalf("CreateCollection() error = %v", err)
	}
	first, err := manager.PublishAccountSelector(ctx, PublishAccountSelectorCommand{
		ID: "workspace-account", CollectionID: collection.ID,
		DisplayName: "Workspace account", ExpectedRevision: 0,
		Policy: accountselector.Policy{
			JavaScript: `selection.accountId = accounts[0].id;`,
		},
	})
	if err != nil {
		t.Fatalf("PublishAccountSelector(first) error = %v", err)
	}
	second, err := manager.PublishAccountSelector(ctx, PublishAccountSelectorCommand{
		ID: "workspace-account", CollectionID: collection.ID,
		DisplayName: "Workspace account", ExpectedRevision: first.Revision,
		Policy: accountselector.Policy{
			JavaScript: `selection.accountId = accounts.find((account) => account.id.endsWith("premium")).id;`,
		},
	})
	if err != nil {
		t.Fatalf("PublishAccountSelector(second) error = %v", err)
	}
	old, err := manager.GetAccountSelectorRevision(ctx, first.ID, first.Revision)
	if err != nil {
		t.Fatalf("GetAccountSelectorRevision(first) error = %v", err)
	}
	if first.Revision != 1 || second.Revision != 2 || !old.Equal(first) {
		t.Fatalf("selector revisions = first=%+v second=%+v old=%+v", first, second, old)
	}
	catalog, err := manager.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(catalog.AccountSelectors) != 1 || !catalog.AccountSelectors[0].Equal(second) {
		t.Fatalf("List().AccountSelectors = %+v, want current revision 2", catalog.AccountSelectors)
	}
}
