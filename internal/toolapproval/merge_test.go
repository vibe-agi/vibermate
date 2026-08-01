package toolapproval

import "testing"

// A burst of the same question must be one entry with counts, not one prompt
// per event. The registry is what makes that true at runtime; the record only
// carries the result.
func TestIdenticalPendingQuestionsShareOneEntry(t *testing.T) {
	t.Parallel()

	registry := newWaiterRegistry()
	first, joinedFirst := registry.join("aggregate-1", "approval-1")
	if joinedFirst {
		t.Fatal("the first waiter joined a nonexistent entry")
	}
	second, joinedSecond := registry.join("aggregate-1", "approval-2")
	if !joinedSecond {
		t.Fatal("an identical question created a second entry")
	}
	if got := registry.recordFor("aggregate-1"); got != "approval-1" {
		t.Fatalf("aggregate entry = %q", got)
	}
	if got := registry.waiterCount("approval-1"); got != 2 {
		t.Fatalf("waiter count = %d", got)
	}

	// One decision releases every waiter on that entry.
	registry.resolve("approval-1", Record{State: StateAllowed})
	for index, pending := range []*waiter{first, second} {
		select {
		case resolved := <-pending.result:
			if resolved.State != StateAllowed {
				t.Fatalf("waiter %d state = %q", index, resolved.State)
			}
		default:
			t.Fatalf("waiter %d was not released", index)
		}
	}
	if registry.recordFor("aggregate-1") != "" {
		t.Fatal("a resolved entry stayed in the aggregate index")
	}
}

// A different question is a different entry.
func TestDifferentQuestionsDoNotMerge(t *testing.T) {
	t.Parallel()

	registry := newWaiterRegistry()
	registry.join("aggregate-1", "approval-1")
	if _, joined := registry.join("aggregate-2", "approval-2"); joined {
		t.Fatal("a different question merged into an existing entry")
	}
	if registry.recordFor("aggregate-2") != "approval-2" {
		t.Fatal("the second question got no entry of its own")
	}
}

// Removing one waiter leaves the others waiting; removing the last one frees
// the entry so a later identical question starts fresh.
func TestRemovingWaitersFreesTheEntryOnlyWhenEmpty(t *testing.T) {
	t.Parallel()

	registry := newWaiterRegistry()
	first, _ := registry.join("aggregate-1", "approval-1")
	second, _ := registry.join("aggregate-1", "approval-2")

	registry.remove("approval-1", first)
	if registry.recordFor("aggregate-1") != "approval-1" {
		t.Fatal("the entry was freed while a waiter remained")
	}
	registry.remove("approval-1", second)
	if registry.recordFor("aggregate-1") != "" {
		t.Fatal("the entry survived its last waiter")
	}
}
