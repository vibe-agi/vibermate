package access

import "context"

// PlanCompiler is the pure compilation boundary consumed by Manager.
type PlanCompiler interface {
	Compile(Aggregate) (AccessPlanSnapshot, error)
}

// Mutation contains a precompiled candidate and its compare-and-swap base.
type Mutation struct {
	ExpectedRevision Revision
	Candidate        Aggregate
}

func (mutation Mutation) Validate() error {
	if err := mutation.Candidate.Validate(); err != nil {
		return err
	}
	if mutation.ExpectedRevision >= MaxRevision {
		return ErrInvalidAccess
	}
	if mutation.Candidate.Binding.Revision != mutation.ExpectedRevision+1 {
		return ErrInvalidAccess
	}
	return nil
}

// CommitOutcome reports the durable outcome observed by a Repository.
type CommitOutcome string

const (
	CommitOutcomeCommitted     CommitOutcome = "committed"
	CommitOutcomeNotCommitted  CommitOutcome = "not_committed"
	CommitOutcomeIndeterminate CommitOutcome = "indeterminate"
	CommitOutcomeConflict      CommitOutcome = "revision_conflict"
	CommitOutcomeNotConfigured CommitOutcome = "access_not_configured"
)

// CommitResult contains authoritative state when the outcome makes it known.
type CommitResult struct {
	Outcome        CommitOutcome
	Aggregate      Aggregate
	ActualRevision Revision
}

// AggregateReader reads the durable Access configuration rather than the
// process-local active-plan projection. A missing Access is not a repository
// failure: Load returns a zero Aggregate, false, and a nil error.
type AggregateReader interface {
	Load(context.Context, AccessID) (Aggregate, bool, error)
}

// Repository is the SQLite persistence port consumed by Manager.
type Repository interface {
	AggregateReader
	LoadAll(context.Context) ([]Aggregate, error)
	CompareAndSwap(context.Context, Mutation) (CommitResult, error)
}
