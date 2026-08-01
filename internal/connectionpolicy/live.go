package connectionpolicy

import "sync"

// Source hands the proxy the set in force. The proxy reads it once per
// connection, so a connection is decided by one revision whole rather than by
// half of one and half of another.
type Source interface {
	Current() RuleSet
}

// Live is a rule set that can be replaced while the runtime runs. Replacing it
// does not revisit a connection that was already decided.
type Live struct {
	mu  sync.RWMutex
	set RuleSet
}

func NewLive(set RuleSet) *Live {
	return &Live{set: set}
}

func (live *Live) Current() RuleSet {
	live.mu.RLock()
	defer live.mu.RUnlock()
	return live.set
}

// Adopt takes a set that has already been constructed, so nothing that failed
// to construct can become the set in force.
func (live *Live) Adopt(set RuleSet) {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.set = set
}

// Fixed is a set that never changes. It exists so a caller that has no store
// still names what it is doing.
type Fixed struct {
	set RuleSet
}

func NewFixed(set RuleSet) Fixed {
	return Fixed{set: set}
}

func (fixed Fixed) Current() RuleSet { return fixed.set }
