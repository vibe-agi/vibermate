package launchsnapshot

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
)

func TestCollectOnlyNamesBoundedAndDeterministic(t *testing.T) {
	inventory := Collect([]string{"PATH=/private/path", "GH_TOKEN=secret-canary", "LANG=en_US", "GH_TOKEN=second-secret", "bad=key=value", "NOT-A-NAME=invalid", "no_equals"})
	if !slices.Equal(inventory.Names, []string{"GH_TOKEN", "LANG", "PATH", "bad"}) || !inventory.Truncated || inventory.Validate() != nil {
		t.Fatalf("invalid inventory: %+v", inventory)
	}
	wire, _ := json.Marshal(inventory)
	for _, forbidden := range []string{"secret-canary", "/private/path", "second-secret", "en_US", "key=value"} {
		if strings.Contains(string(wire), forbidden) {
			t.Fatalf("value leaked: %s", forbidden)
		}
	}
	entries := []string{}
	for i := 0; i < 1000; i++ {
		entries = append(entries, fmt.Sprintf("KEY_%04d=value", i))
	}
	full := Collect(entries)
	if len(full.Names) != MaxNames || !full.Truncated || full.Validate() != nil {
		t.Fatalf("unbounded inventory")
	}
	for _, names := range [][]string{{"GH_TOKEN=secret"}, {"A", "A"}, {"Z", "A"}, {"A\nB"}, {strings.Repeat("A", 129)}} {
		if (Inventory{Names: names}).Validate() == nil {
			t.Fatalf("accepted invalid names: %q", names)
		}
	}
}

func TestStoreReplacesPerSourceAndDoesNotShareMutableSlices(t *testing.T) {
	var store Store
	inventory := Collect([]string{"TOKEN=canary"})
	run := capturerun.View{ID: "run-1", MachineID: "machine-1", LocalUserLabel: "alice", CreatedAt: time.Now()}
	store.Record(run, inventory)
	inventory.Names[0] = "MUTATED"
	first := store.List()
	if len(first) != 1 || first[0].Inventory.Names[0] != "TOKEN" {
		t.Fatal("mutable input retained")
	}
	first[0].Inventory.Names[0] = "MUTATED"
	if store.List()[0].Inventory.Names[0] != "TOKEN" {
		t.Fatal("mutable output retained")
	}
	run.ID = "run-2"
	store.Record(run, Collect([]string{"NEXT=hidden"}))
	if len(store.List()) != 1 || store.List()[0].ID != "run-2" {
		t.Fatal("source did not replace prior snapshot")
	}
	var group sync.WaitGroup
	for i := 0; i < 40; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			store.Record(capturerun.View{ID: fmt.Sprint(i), MachineID: fmt.Sprint(i)}, Collect(nil))
			_ = store.List()
		}(i)
	}
	group.Wait()
	if len(store.List()) != MaxSources {
		t.Fatal("unbounded snapshot retention")
	}
}
