package access_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
)

func TestAggregateCatalogReadsEveryDurableAccessWithDefensiveCopies(
	t *testing.T,
) {
	t.Parallel()
	store := openStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownStore(t, store)
	manager := newManager(t, store, newProjection(t))
	defer shutdownManager(t, manager)

	empty, err := manager.ListAccesses(context.Background())
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty durable Access catalog=%+v err=%v", empty, err)
	}

	firstID := newAccessID(t, "catalog-first")
	first := testAggregate(t, firstID, 1, "First")
	if _, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 0,
		Aggregate:        first,
	}); err != nil {
		t.Fatal(err)
	}
	secondID := newAccessID(t, "catalog-second")
	second := testAggregate(t, secondID, 1, "Second")
	second.Binding.Status = access.AccessStatusDisabled
	if _, err := manager.WriteAccess(context.Background(), access.WriteCommand{
		ExpectedRevision: 0,
		Aggregate:        second,
	}); err != nil {
		t.Fatal(err)
	}

	aggregates, err := manager.ListAccesses(context.Background())
	if err != nil || len(aggregates) != 2 {
		t.Fatalf("durable Access catalog=%+v err=%v", aggregates, err)
	}
	byID := make(map[string]access.Aggregate, len(aggregates))
	for _, aggregate := range aggregates {
		byID[aggregate.Binding.ID.String()] = aggregate
	}
	if byID["catalog-first"].Binding.Name != "First" ||
		byID["catalog-second"].Binding.Status != access.AccessStatusDisabled {
		t.Fatalf("durable Access catalog=%+v", aggregates)
	}

	aggregates[0].Binding.Name = "mutated caller copy"
	aggregates[0].Binding.ProfileIDs[0] = aggregates[1].Binding.ProfileIDs[0]
	read, exists, err := manager.ReadAccess(context.Background(), firstID)
	if err != nil || !exists || read.Binding.Name != "First" ||
		read.Binding.ProfileIDs[0] != first.Binding.ProfileIDs[0] {
		t.Fatalf("durable Access read=%+v exists=%t err=%v", read, exists, err)
	}
	missingID := newAccessID(t, "catalog-missing")
	_, exists, err = manager.ReadAccess(context.Background(), missingID)
	if err != nil || exists {
		t.Fatalf("missing durable Access exists=%t err=%v", exists, err)
	}
}
