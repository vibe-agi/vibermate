package toolapproval

import "testing"

// A burst of the same question must be one entry with counts, not one prompt
// per event. The registry is what makes that true at runtime; the record only
// carries the result.
func TestIdenticalPendingQuestionsShareOneEntry(t *testing.T) {
	t.Parallel()

	registry := newWaiterRegistry()
	first, created, joinedFirst := registry.join("aggregate-1", "approval-1")
	if joinedFirst {
		t.Fatal("the first waiter joined a nonexistent entry")
	}
	second, joined, joinedSecond := registry.join("aggregate-1", "approval-2")
	if !joinedSecond {
		t.Fatal("an identical question created a second entry")
	}
	if joined != created {
		t.Fatal("an identical question got a different entry")
	}
	if created.recordID != "approval-1" {
		t.Fatalf("aggregate entry = %q", created.recordID)
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
	if registry.waiterCount("approval-1") != 0 {
		t.Fatal("a resolved entry stayed in the aggregate index")
	}
	if _, again, joinedAgain := registry.join(
		"aggregate-1",
		"approval-3",
	); joinedAgain || again.recordID != "approval-3" {
		t.Fatal("a later identical question did not start fresh")
	}
}

// A different question is a different entry.
func TestDifferentQuestionsDoNotMerge(t *testing.T) {
	t.Parallel()

	registry := newWaiterRegistry()
	registry.join("aggregate-1", "approval-1")
	_, second, joined := registry.join("aggregate-2", "approval-2")
	if joined {
		t.Fatal("a different question merged into an existing entry")
	}
	if second.recordID != "approval-2" {
		t.Fatal("the second question got no entry of its own")
	}
}

// Removing one waiter leaves the others waiting; removing the last one frees
// the entry so a later identical question starts fresh.
func TestRemovingWaitersFreesTheEntryOnlyWhenEmpty(t *testing.T) {
	t.Parallel()

	registry := newWaiterRegistry()
	first, _, _ := registry.join("aggregate-1", "approval-1")
	second, _, _ := registry.join("aggregate-1", "approval-2")

	registry.remove("approval-1", first)
	if registry.waiterCount("approval-1") != 1 {
		t.Fatal("the entry was freed while a waiter remained")
	}
	registry.remove("approval-1", second)
	if registry.waiterCount("approval-1") != 0 {
		t.Fatal("the entry survived its last waiter")
	}
}

// A joiner arrives while the durable record is still being written. It must
// not act on the record until that write is known to have happened, and a
// write that failed must not leave it waiting on a question nobody was asked.
func TestAJoinerWaitsForTheQuestionToBecomeDurable(t *testing.T) {
	t.Parallel()

	registry := newWaiterRegistry()
	_, created, _ := registry.join("aggregate-1", "approval-1")
	_, joined, _ := registry.join("aggregate-1", "approval-2")
	select {
	case <-joined.ready:
		t.Fatal("a joiner was released before the record was written")
	default:
	}

	registry.publish(created, true)
	<-joined.ready
	if !registry.durable(joined) {
		t.Fatal("a written record was reported as missing")
	}

	failing := newWaiterRegistry()
	_, failed, _ := failing.join("aggregate-2", "approval-3")
	_, alsoFailed, _ := failing.join("aggregate-2", "approval-4")
	failing.publish(failed, false)
	<-alsoFailed.ready
	if failing.durable(alsoFailed) {
		t.Fatal("a failed write was reported as durable")
	}
}
