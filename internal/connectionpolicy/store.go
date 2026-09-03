package connectionpolicy

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrRevisionConflict reports that the stored rules moved while a change was
// being prepared. The change is refused rather than applied to a set the
// author never saw.
var ErrRevisionConflict = errors.New("connection rule set revision conflict")

// Snapshot is a whole rule set as it is stored and handed around. Rules are
// never changed one at a time: a set that would not construct must be refused
// before anything is written, and a partial write would leave the runtime
// holding rules it would not have accepted.
type Snapshot struct {
	Revision uint64
	Rules    []Rule
	Mode     Mode
}

// Compile turns stored rules into the frozen set the proxy evaluates. Every
// refusal the constructor makes applies to stored rules too.
func (snapshot Snapshot) Compile() (RuleSet, error) {
	return NewRuleSet(RuleSetOptions{
		Revision: snapshot.Revision,
		Rules:    snapshot.Rules,
		Mode:     snapshot.Mode,
	})
}

// Repository is where rules live between runs.
type Repository interface {
	// Load returns the stored set. A store that has never been seeded reports
	// ErrNoRuleSet rather than inventing a permissive one.
	Load(context.Context) (Snapshot, error)
	// Seed writes the shipped set if and only if nothing is stored yet.
	Seed(context.Context, Snapshot, time.Time) (Snapshot, error)
	// Replace swaps the whole set under the revision the author read. It
	// returns ErrRevisionConflict if that revision is no longer current.
	Replace(context.Context, uint64, Snapshot, time.Time) (Snapshot, error)
}

// ErrNoRuleSet reports an unseeded store. It is not a permissive state: a
// runtime with no rules must seed before it proxies anything.
var ErrNoRuleSet = errors.New("no connection rule set is stored")

// ShippedSnapshot is what a fresh runtime starts with.
//
// A fresh local installation starts in Monitor mode. Network policy
// is an optional governance layer; it must not make an otherwise unmodified
// captured program hang on every previously unseen host. The mode is explicit
// stored state rather than a wildcard rule or an implicit missing default.
//
// This does not widen semantic inspection. Environment-owned exact endpoints,
// Root admission, account selection, plugins, and tool decisions retain their
// independent authorities.
func ShippedSnapshot(revision uint64) Snapshot {
	return Snapshot{
		Revision: revision,
		Mode:     ModeMonitor,
	}
}

func (snapshot Snapshot) validate() error {
	if snapshot.Revision == 0 {
		return fmt.Errorf("%w: revision is required", ErrInvalidRuleSet)
	}
	_, err := snapshot.Compile()
	return err
}
