package runtimepersistence

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
)

func ruleStoreAt(t *testing.T, path string) *Store {
	t.Helper()
	return openTestStore(t, path)
}

func seedTime() time.Time {
	return time.Unix(1_780_000_000, 0).UTC()
}

// An unseeded store is not a permissive one. A runtime that read "no rules" as
// "no restrictions" would make the outbound firewall the one control that
// never fires.
func TestAnUnseededStoreReportsItself(t *testing.T) {
	t.Parallel()

	store := ruleStoreAt(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)

	_, err := store.ConnectionRuleRepository().Load(context.Background())
	if !errors.Is(err, connectionpolicy.ErrNoRuleSet) {
		t.Fatalf("unseeded load error = %v", err)
	}
}

// Seeding is a first-start act. A person's edits must not be replaced by the
// shipped placeholder on the next launch.
func TestSeedingHappensOnceAndSurvivesRestart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := ruleStoreAt(t, path)
	rules := store.ConnectionRuleRepository()
	shipped := connectionpolicy.ShippedSnapshot(1)
	if _, err := rules.Seed(context.Background(), shipped, seedTime()); err != nil {
		t.Fatal(err)
	}
	edited := connectionpolicy.Snapshot{
		Revision: 2,
		Mode:     connectionpolicy.ModeDenyUnknown,
		Rules: []connectionpolicy.Rule{{
			ID:       "allow.one-host",
			Priority: 100,
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchExactHost("api.example.com"),
		}},
	}
	if _, err := rules.Replace(
		context.Background(),
		1,
		edited,
		seedTime(),
	); err != nil {
		t.Fatal(err)
	}
	// A second seed on an already-seeded store changes nothing.
	if _, err := rules.Seed(
		context.Background(),
		connectionpolicy.ShippedSnapshot(9),
		seedTime(),
	); err != nil {
		t.Fatal(err)
	}
	shutdownTestStore(t, store)

	reopened := ruleStoreAt(t, path)
	defer shutdownTestStore(t, reopened)
	loaded, err := reopened.ConnectionRuleRepository().Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 2 || len(loaded.Rules) != 1 {
		t.Fatalf("loaded set = %+v", loaded)
	}
	if loaded.Rules[0].ID != "allow.one-host" ||
		loaded.Rules[0].Priority != 100 ||
		loaded.Rules[0].Match != connectionpolicy.MatchExactHost("api.example.com") {
		t.Fatalf("loaded rule = %+v", loaded.Rules[0])
	}
	if loaded.Mode != connectionpolicy.ModeDenyUnknown {
		t.Fatalf("loaded mode = %q", loaded.Mode)
	}
}

// A change is prepared against a revision. If the rules moved underneath, the
// change is refused rather than applied to a set its author never saw.
func TestAStaleChangeIsRefused(t *testing.T) {
	t.Parallel()

	store := ruleStoreAt(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)
	rules := store.ConnectionRuleRepository()
	if _, err := rules.Seed(
		context.Background(),
		connectionpolicy.ShippedSnapshot(1),
		seedTime(),
	); err != nil {
		t.Fatal(err)
	}
	next := connectionpolicy.ShippedSnapshot(2)
	next.Rules = []connectionpolicy.Rule{{
		ID:       "allow.decided-host",
		Priority: 100,
		Decision: connectionpolicy.DecisionAllow,
		Match:    connectionpolicy.MatchExactHost("decided.example.com"),
	}}
	if _, err := rules.Replace(
		context.Background(),
		1,
		next,
		seedTime(),
	); err != nil {
		t.Fatal(err)
	}
	stale := connectionpolicy.ShippedSnapshot(3)
	stale.Rules = nil
	if _, err := rules.Replace(
		context.Background(),
		1,
		stale,
		seedTime(),
	); !errors.Is(err, connectionpolicy.ErrRevisionConflict) {
		t.Fatalf("stale replace error = %v", err)
	}
	loaded, err := rules.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Rules) != 1 ||
		loaded.Rules[0].ID != "allow.decided-host" ||
		loaded.Revision != 2 {
		t.Fatalf("a refused change altered the set: %+v", loaded)
	}
}

// A set that would not construct never reaches storage, so the runtime cannot
// be left holding rules it would have rejected.
func TestARuleSetThatWouldNotConstructIsNeverStored(t *testing.T) {
	t.Parallel()

	store := ruleStoreAt(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer shutdownTestStore(t, store)
	rules := store.ConnectionRuleRepository()
	if _, err := rules.Seed(
		context.Background(),
		connectionpolicy.ShippedSnapshot(1),
		seedTime(),
	); err != nil {
		t.Fatal(err)
	}
	invalid := connectionpolicy.Snapshot{
		Revision: 2,
		Mode:     connectionpolicy.Mode("allow_everything"),
	}
	if _, err := rules.Replace(
		context.Background(),
		1,
		invalid,
		seedTime(),
	); !errors.Is(err, connectionpolicy.ErrInvalidRuleSet) {
		t.Fatalf("invalid mode error = %v", err)
	}
	loaded, err := rules.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Mode != connectionpolicy.ModeMonitor {
		t.Fatalf("the stored mode changed: %q", loaded.Mode)
	}
}
