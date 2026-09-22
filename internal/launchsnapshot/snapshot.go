// Package launchsnapshot holds bounded, name-only launcher observations for
// the environment-policy editor. Values never cross this boundary. Snapshots
// are advisory, not policy authority, and disappear when the runtime stops.
package launchsnapshot

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
)

const (
	MaxNames      = 256
	MaxNameBytes  = 128
	MaxTotalBytes = 8192
	MaxSources    = 16
)

type Inventory struct {
	Names     []string `json:"names"`
	Truncated bool     `json:"truncated"`
}

func validName(name string) bool {
	if len(name) == 0 || len(name) > MaxNameBytes {
		return false
	}
	for i, c := range name {
		if c != '_' && !(c >= 'A' && c <= 'Z') && !(c >= 'a' && c <= 'z') && !(i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// Collect takes the exact launcher input, not the server's os.Environ().
func Collect(environ []string) Inventory {
	result := Inventory{Names: []string{}}
	unique := make(map[string]bool)
	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || !validName(name) {
			result.Truncated = true
			continue
		}
		unique[name] = true
	}
	all := make([]string, 0, len(unique))
	for name := range unique {
		all = append(all, name)
	}
	sort.Strings(all)
	bytes := 0
	for _, name := range all {
		if len(result.Names) == MaxNames || bytes+len(name) > MaxTotalBytes {
			result.Truncated = true
			break
		}
		result.Names = append(result.Names, name)
		bytes += len(name)
	}
	return result
}

func (inventory Inventory) Validate() error {
	if len(inventory.Names) > MaxNames {
		return errors.New("too many environment names")
	}
	bytes, previous := 0, ""
	for _, name := range inventory.Names {
		if !validName(name) || name <= previous {
			return errors.New("invalid environment names")
		}
		bytes += len(name)
		previous = name
	}
	if bytes > MaxTotalBytes {
		return errors.New("environment names exceed limit")
	}
	return nil
}

type Snapshot struct {
	ID          string    `json:"id"`
	DeviceName  string    `json:"deviceName"`
	UserLabel   string    `json:"userLabel"`
	Executable  string    `json:"executable"`
	Remote      bool      `json:"remote"`
	CollectedAt time.Time `json:"collectedAt"`
	Inventory   Inventory `json:"inventory"`
	source      string
}

// Store's zero value is ready to use. Only successful, authenticated launches
// enter this store; management authentication protects reads at the host.
type Store struct {
	mu    sync.Mutex
	items []Snapshot
}

func (store *Store) Record(run capturerun.View, inventory Inventory) {
	if inventory.Validate() != nil {
		return
	}
	user := run.RuntimeUsername
	if user == "" {
		user = run.LocalUserLabel
	}
	source := string(run.RuntimeUserID) + "\x00" + run.MachineID + "\x00" + user
	item := Snapshot{ID: run.ID, DeviceName: run.DeviceName, UserLabel: user,
		Executable: run.ExecutableLabel, Remote: run.RuntimeUserID != "",
		CollectedAt: run.CreatedAt, Inventory: clone(inventory), source: source}
	store.mu.Lock()
	defer store.mu.Unlock()
	retained := []Snapshot{item}
	for _, old := range store.items {
		if old.source != source && len(retained) < MaxSources {
			retained = append(retained, old)
		}
	}
	store.items = retained
}

func (store *Store) List() []Snapshot {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]Snapshot, len(store.items))
	for i, item := range store.items {
		item.Inventory = clone(item.Inventory)
		result[i] = item
	}
	return result
}

func clone(inventory Inventory) Inventory {
	inventory.Names = append([]string{}, inventory.Names...)
	return inventory
}
