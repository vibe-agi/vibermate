// Package access owns the durable Access aggregate and its single process-local
// executable plan projection.
package access

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidAccess          = errors.New("invalid Access aggregate")
	ErrInvalidAccessPlan      = errors.New("invalid Access plan")
	ErrAccessNotConfigured    = errors.New("Access is not configured")
	ErrRevisionConflict       = errors.New("Access revision conflict")
	ErrWriteNotCommitted      = errors.New("Access write was not committed")
	ErrCommitOutcomeUnknown   = errors.New("Access commit outcome is unknown")
	ErrProjectionUnavailable  = errors.New("Access plan projection is unavailable")
	ErrAccessRuntimeStopping  = errors.New("Access runtime is stopping")
	ErrInvalidRepositoryState = errors.New("invalid Access repository state")
)

// ReasonCode is a language-independent failure classification.
type ReasonCode string

const (
	ReasonInvalidAccess         ReasonCode = "invalid_access"
	ReasonInvalidAccessPlan     ReasonCode = "invalid_access_plan"
	ReasonAccessNotConfigured   ReasonCode = "access_not_configured"
	ReasonRevisionConflict      ReasonCode = "revision_conflict"
	ReasonWriteNotCommitted     ReasonCode = "access_write_not_committed"
	ReasonCommitOutcomeUnknown  ReasonCode = "access_commit_outcome_unknown"
	ReasonProjectionUnavailable ReasonCode = "access_projection_unavailable"
	ReasonAccessRuntimeStopping ReasonCode = "access_runtime_stopping"
)

// Failure carries a stable reason code without embedding localized copy.
type Failure struct {
	Code             ReasonCode
	AccessID         AccessID
	ExpectedRevision Revision
	ActualRevision   Revision
	cause            error
}

func (f *Failure) Error() string {
	if f == nil {
		return "Access failure"
	}
	message := fmt.Sprintf(
		"Access operation failed: code=%s accessId=%q expectedRevision=%d actualRevision=%d",
		f.Code,
		f.AccessID.String(),
		f.ExpectedRevision,
		f.ActualRevision,
	)
	if f.cause != nil {
		return message + ": " + f.cause.Error()
	}
	return message
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.cause
}

func newFailure(
	code ReasonCode,
	sentinel error,
	accessID AccessID,
	expected Revision,
	actual Revision,
	cause error,
) *Failure {
	if cause == nil {
		cause = sentinel
	} else {
		cause = errors.Join(sentinel, cause)
	}
	return &Failure{
		Code:             code,
		AccessID:         accessID,
		ExpectedRevision: expected,
		ActualRevision:   actual,
		cause:            cause,
	}
}

// WriteCommand applies one complete Access aggregate using aggregate-local CAS.
type WriteCommand struct {
	ExpectedRevision Revision
	Aggregate        Aggregate
}

func (c WriteCommand) validate() error {
	if err := c.Aggregate.Validate(); err != nil {
		return err
	}
	if c.ExpectedRevision >= MaxRevision {
		return fmt.Errorf("%w: expected revision cannot advance", ErrInvalidAccess)
	}
	if c.Aggregate.Binding.Revision != c.ExpectedRevision+1 {
		return fmt.Errorf(
			"%w: candidate revision=%d expectedRevision=%d",
			ErrInvalidAccess,
			c.Aggregate.Binding.Revision,
			c.ExpectedRevision,
		)
	}
	return nil
}

func (c WriteCommand) accessID() AccessID {
	return c.Aggregate.Binding.ID
}

// WriteOutcome states whether a caller may safely retry a write.
type WriteOutcome string

const (
	WriteOutcomeCommitted     WriteOutcome = "committed"
	WriteOutcomeNotCommitted  WriteOutcome = "not_committed"
	WriteOutcomeIndeterminate WriteOutcome = "indeterminate"
)

// WriteResult is the receipt from the serialized commit-to-publication
// boundary. PlanHash is non-zero only when this write successfully published
// the exact active candidate; callers must not resolve the mutable current
// projection to reconstruct this receipt.
type WriteResult struct {
	Outcome  WriteOutcome
	Revision Revision
	PlanHash PlanHash
}
