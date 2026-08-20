package environment

import "context"

type Draft struct {
	EnvironmentID   EnvironmentID
	BaseRevision    Revision
	Revision        Revision
	Candidate       Environment
	CandidateDigest CandidateDigest
}

func (draft Draft) Clone() Draft {
	draft.Candidate = draft.Candidate.Clone()
	return draft
}

type DraftMutation struct {
	EnvironmentID        EnvironmentID
	ExpectedBaseRevision Revision
	// ExpectedDraftRevision compares against the current draft row. Zero means
	// absent; the repository still allocates monotonically increasing private
	// draft revisions across publish cycles.
	ExpectedDraftRevision Revision
	Candidate             Environment
	CandidateDigest       CandidateDigest
}

type PublishMutation struct {
	EnvironmentID        EnvironmentID
	ExpectedBaseRevision Revision
	DraftRevision        Revision
	CandidateDigest      CandidateDigest
	Candidate            Environment
}

type CommitOutcome string

const (
	CommitOutcomeCommitted     CommitOutcome = "committed"
	CommitOutcomeNotCommitted  CommitOutcome = "not_committed"
	CommitOutcomeConflict      CommitOutcome = "revision_conflict"
	CommitOutcomeIndeterminate CommitOutcome = "indeterminate"
)

type CommitResult struct {
	Outcome        CommitOutcome
	Aggregate      Environment
	ActualRevision Revision
}

type Repository interface {
	LoadAllActive(context.Context) ([]Environment, error)
	LoadActive(context.Context, EnvironmentID) (Environment, bool, error)
	LoadRevision(context.Context, EnvironmentID, Revision) (Environment, bool, error)
	LoadDraft(context.Context, EnvironmentID) (Draft, bool, error)
	SaveDraft(context.Context, DraftMutation) (Draft, error)
	PublishDraft(context.Context, PublishMutation) (CommitResult, error)
	// Retire makes an Environment stop being active without touching the
	// revisions frozen evidence points at. Deleting those would leave every
	// historical Turn unable to open the exact authority it ran under, which
	// design 08 section 4 forbids drifting; an Environment the user deleted is
	// gone from the product, not from the record.
	Retire(context.Context, EnvironmentID) (bool, error)
}
