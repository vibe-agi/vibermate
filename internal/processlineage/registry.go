package processlineage

import (
	"errors"
	"sync"
)

const (
	// maxDepth bounds a parent walk; real process trees are far shallower.
	maxDepth = 64
	// maxRemembered bounds remembered descendants across all runs.
	maxRemembered = 1 << 16
)

var (
	errInvalidRegistration = errors.New("lineage registration is invalid")
	errRootTaken           = errors.New("process is already a registered root")
)

// Registry maps live processes to the run whose root they descend from. A
// root's own pid is never owned: the launcher's connections stay its own,
// only its descendants are captured.
type Registry struct {
	reader Reader

	mu      sync.Mutex
	roots   map[Identity]string
	runs    map[string]Identity
	learned map[Identity]string
}

func NewRegistry(reader Reader) *Registry {
	return &Registry{
		reader:  reader,
		roots:   map[Identity]string{},
		runs:    map[string]Identity{},
		learned: map[Identity]string{},
	}
}

// Register makes root's descendants owned by run. The caller must have
// established root from an unforgeable source such as socket peer
// credentials, never from a request field.
func (registry *Registry) Register(root Identity, run string) error {
	if root.PID <= 1 || run == "" {
		return errInvalidRegistration
	}
	live, err := registry.reader.Read(root.PID)
	if err != nil || live.Identity != root {
		return errInvalidRegistration
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, taken := registry.roots[root]; taken {
		return errRootTaken
	}
	if previous, exists := registry.runs[run]; exists {
		delete(registry.roots, previous)
	}
	registry.roots[root] = run
	registry.runs[run] = root
	return nil
}

// RootRun returns the run registered for root, if any.
func (registry *Registry) RootRun(root Identity) (string, bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	run, exists := registry.roots[root]
	return run, exists
}

// Deregister forgets run and every descendant learned for it.
func (registry *Registry) Deregister(run string) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if root, exists := registry.runs[run]; exists {
		delete(registry.roots, root)
		delete(registry.runs, run)
	}
	for identity, owner := range registry.learned {
		if owner == run {
			delete(registry.learned, identity)
		}
	}
}

// Owner returns the run that owns pid. It walks parents until it meets a
// registered root or a process already attributed, then remembers every
// process on the path, so a descendant later reparented away from the root
// keeps its run.
func (registry *Registry) Owner(pid int) (string, bool) {
	var path []Identity
	current := pid
	for depth := 0; depth < maxDepth && current > 1; depth++ {
		process, err := registry.reader.Read(current)
		if err != nil {
			return "", false
		}
		registry.mu.Lock()
		run, isRoot := registry.roots[process.Identity]
		if !isRoot {
			run = registry.learned[process.Identity]
		}
		registry.mu.Unlock()
		if isRoot && depth == 0 {
			return "", false
		}
		if run != "" {
			registry.remember(path, run)
			return run, true
		}
		path = append(path, process.Identity)
		current = process.Parent
	}
	return "", false
}

func (registry *Registry) remember(path []Identity, run string) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, live := registry.runs[run]; !live {
		return
	}
	for _, identity := range path {
		if len(registry.learned) >= maxRemembered {
			registry.evictDead()
			if len(registry.learned) >= maxRemembered {
				return
			}
		}
		registry.learned[identity] = run
	}
}

// evictDead drops remembered processes that no longer exist. It runs only at
// the size bound, with the lock held.
func (registry *Registry) evictDead() {
	for identity := range registry.learned {
		if process, err := registry.reader.Read(identity.PID); err != nil || process.Identity != identity {
			delete(registry.learned, identity)
		}
	}
}
