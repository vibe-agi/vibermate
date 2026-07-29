package access

import "context"

// Record is the typed persistence boundary between Access and SQLite.
type Record struct {
	AccessID AccessID
	Revision Revision
	Binding  Binding
}

func (r Record) Validate() error {
	return Snapshot{
		accessID: r.AccessID,
		revision: r.Revision,
		binding:  r.Binding,
	}.validate()
}

func (r Record) snapshot() (Snapshot, error) {
	if err := r.Validate(); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		accessID: r.AccessID,
		revision: r.Revision,
		binding:  r.Binding,
	}, nil
}

// Mutation contains a prevalidated candidate and its compare-and-swap base.
type Mutation struct {
	ExpectedRevision Revision
	Candidate        Record
}

func (m Mutation) Validate() error {
	if err := m.Candidate.Validate(); err != nil {
		return err
	}
	if m.ExpectedRevision >= MaxRevision {
		return ErrInvalidAccess
	}
	if m.Candidate.Revision != m.ExpectedRevision+1 {
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
	Record         Record
	ActualRevision Revision
}

// Repository is the SQLite persistence port consumed by Manager.
type Repository interface {
	LoadAll(context.Context) ([]Record, error)
	CompareAndSwap(context.Context, Mutation) (CommitResult, error)
}
