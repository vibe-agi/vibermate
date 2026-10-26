package processlineage

import (
	"errors"
	"strings"
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
	// markers maps an exact environment entry to the run that set it in its
	// Agent's environment. It finds a descendant whose parents exited before
	// its first connection, which a parent walk cannot.
	markers    map[string]string
	runMarkers map[string]string
}

func NewRegistry(reader Reader) *Registry {
	return &Registry{
		reader:  reader,
		roots:   map[Identity]string{},
		runs:    map[string]Identity{},
		learned: map[Identity]string{},

		markers:    map[string]string{},
		runMarkers: map[string]string{},
	}
}

// Register makes root's descendants owned by run. The caller must have
// established root from an unforgeable source such as socket peer
// credentials, never from a request field.
func (registry *Registry) Register(root Identity, run string) error {
	return registry.RegisterWithMarker(root, run, "")
}

// RegisterWithMarker also attributes to run any process whose environment
// contains marker, an exact KEY=value entry the run placed in its Agent's
// environment and that descendants inherit. An empty marker registers lineage
// only.
func (registry *Registry) RegisterWithMarker(root Identity, run, marker string) error {
	if root.PID <= 1 || run == "" || (marker != "" && !strings.Contains(marker, "=")) {
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
	if owner, taken := registry.markers[marker]; marker != "" && taken && owner != run {
		return errInvalidRegistration
	}
	if previous, exists := registry.runs[run]; exists {
		delete(registry.roots, previous)
	}
	if previous, exists := registry.runMarkers[run]; exists {
		delete(registry.markers, previous)
		delete(registry.runMarkers, run)
	}
	registry.roots[root] = run
	registry.runs[run] = root
	if marker != "" {
		registry.markers[marker] = run
		registry.runMarkers[run] = marker
	}
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
	if marker, exists := registry.runMarkers[run]; exists {
		delete(registry.markers, marker)
		delete(registry.runMarkers, run)
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
// keeps its run. When the walk finds no owner (the process was orphaned
// before its first connection), a run's marker in the process's environment
// attributes it instead.
func (registry *Registry) Owner(pid int) (string, bool) {
	var path []Identity
	current := pid
	for depth := 0; depth < maxDepth && current > 1; depth++ {
		process, err := registry.reader.Read(current)
		if err != nil {
			if depth == 0 {
				return "", false
			}
			break // A parent exited mid-walk; the marker may still apply.
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
	if len(path) == 0 {
		return "", false
	}
	if run, found := registry.markedRun(pid); found {
		registry.remember(path[:1], run)
		return run, true
	}
	return "", false
}

// markedRun looks for a registered run's marker in pid's environment. It
// reads nothing while no run has registered a marker.
func (registry *Registry) markedRun(pid int) (string, bool) {
	environment, ok := registry.reader.(EnvironmentReader)
	if !ok {
		return "", false
	}
	registry.mu.Lock()
	marked := len(registry.markers) != 0
	registry.mu.Unlock()
	if !marked {
		return "", false
	}
	entries, err := environment.Environment(pid)
	if err != nil {
		return "", false
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for _, entry := range entries {
		if run, exists := registry.markers[entry]; exists {
			return run, true
		}
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
