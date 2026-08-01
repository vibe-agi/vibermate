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
	// Default answers a connection no rule matched. It is stored like any
	// other rule so that reading the set back shows every answer it can give.
	Default Rule
}

// Compile turns stored rules into the frozen set the proxy evaluates. Every
// refusal the constructor makes applies to stored rules too.
func (snapshot Snapshot) Compile() (RuleSet, error) {
	return NewRuleSet(RuleSetOptions{
		Revision: snapshot.Revision,
		Rules:    snapshot.Rules,
		Default:  snapshot.Default,
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
// Design 06 §4.1 makes the released answer for an unknown host `ask`, and
// INV-FIREWALL-NO-WILDCARD forbids a wildcard allow in the shipped
// configuration. Allowing everything is still possible; it is a rule a person
// writes on purpose and can see in the list.
//
// Nothing is allowed here in advance, not even a well-known model host: what
// an agent is allowed to reach is a decision about that installation, and a
// shipped allow list would be this product deciding it for everyone.
func ShippedSnapshot(revision uint64) Snapshot {
	return Snapshot{
		Revision: revision,
		Default: Rule{
			ID:       DefaultAskRuleID,
			Decision: DecisionAsk,
			Match:    MatchAny(),
		},
	}
}

// DefaultAskRuleID names the answer an undecided connection gets.
const DefaultAskRuleID = "default.ask"

// DefaultDenyRuleID names the answer a connection gets when no rule matched.
const DefaultDenyRuleID = "default.deny"

func (snapshot Snapshot) validate() error {
	if snapshot.Revision == 0 {
		return fmt.Errorf("%w: revision is required", ErrInvalidRuleSet)
	}
	_, err := snapshot.Compile()
	return err
}
