// Package access owns the durable Access aggregate and its process-local
// immutable snapshot projection.
package access

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxAccessIDBytes             = 128
	MaxAccessNameBytes           = 256
	MaxDescriptionBytes          = 4096
	MaxRevision         Revision = 1<<63 - 1
)

var (
	ErrInvalidAccess          = errors.New("invalid Access aggregate")
	ErrAccessNotConfigured    = errors.New("Access is not configured")
	ErrRevisionConflict       = errors.New("Access revision conflict")
	ErrWriteNotCommitted      = errors.New("Access write was not committed")
	ErrCommitOutcomeUnknown   = errors.New("Access commit outcome is unknown")
	ErrProjectionUnavailable  = errors.New("Access snapshot projection is unavailable")
	ErrAccessRuntimeStopping  = errors.New("Access runtime is stopping")
	ErrInvalidRepositoryState = errors.New("invalid Access repository state")
)

// ReasonCode is a language-independent failure classification.
type ReasonCode string

const (
	ReasonInvalidAccess         ReasonCode = "invalid_access"
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

// AccessID is the stable typed identity of one Access aggregate.
type AccessID struct {
	value string
}

func NewAccessID(value string) (AccessID, error) {
	if err := validateAccessID(value); err != nil {
		return AccessID{}, err
	}
	return AccessID{value: value}, nil
}

func (id AccessID) String() string {
	return id.value
}

func (id AccessID) validate() error {
	return validateAccessID(id.value)
}

func validateAccessID(value string) error {
	if value == "" {
		return fmt.Errorf("%w: Access ID is empty", ErrInvalidAccess)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: Access ID is not valid UTF-8", ErrInvalidAccess)
	}
	if len(value) > MaxAccessIDBytes {
		return fmt.Errorf("%w: Access ID exceeds the byte limit", ErrInvalidAccess)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: Access ID has surrounding whitespace", ErrInvalidAccess)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: Access ID contains a control character", ErrInvalidAccess)
		}
	}
	return nil
}

// Revision is a monotonic revision owned by one Access aggregate. It is
// intentionally independent from the goose schema revision.
type Revision uint64

// Binding contains the stable root fields implemented by this slice. It is a
// value-only projection; future aggregate collections must preserve the same
// defensive-copy contract.
type Binding struct {
	Name        string
	Description string
}

func (b Binding) Validate() error {
	if b.Name == "" {
		return fmt.Errorf("%w: Access name is empty", ErrInvalidAccess)
	}
	if !utf8.ValidString(b.Name) || !utf8.ValidString(b.Description) {
		return fmt.Errorf("%w: Access text is not valid UTF-8", ErrInvalidAccess)
	}
	if strings.TrimSpace(b.Name) != b.Name {
		return fmt.Errorf("%w: Access name has surrounding whitespace", ErrInvalidAccess)
	}
	if len(b.Name) > MaxAccessNameBytes {
		return fmt.Errorf("%w: Access name exceeds the byte limit", ErrInvalidAccess)
	}
	if len(b.Description) > MaxDescriptionBytes {
		return fmt.Errorf("%w: Access description exceeds the byte limit", ErrInvalidAccess)
	}
	return nil
}

// Snapshot is a deeply immutable process-local projection of a committed
// Access aggregate. SQLite remains the only durable authority.
type Snapshot struct {
	accessID AccessID
	revision Revision
	binding  Binding
}

func (s Snapshot) AccessID() AccessID {
	return s.accessID
}

func (s Snapshot) Revision() Revision {
	return s.revision
}

// Binding returns a value copy. Mutating the returned value cannot change the
// published snapshot.
func (s Snapshot) Binding() Binding {
	return s.binding
}

func (s Snapshot) validate() error {
	if err := s.accessID.validate(); err != nil {
		return err
	}
	if s.revision == 0 || s.revision > MaxRevision {
		return fmt.Errorf("%w: snapshot revision is invalid", ErrInvalidAccess)
	}
	return s.binding.Validate()
}

// WriteCommand performs one aggregate-local compare-and-swap.
type WriteCommand struct {
	AccessID         AccessID
	ExpectedRevision Revision
	Binding          Binding
}

func (c WriteCommand) validate() error {
	if err := c.AccessID.validate(); err != nil {
		return err
	}
	if c.ExpectedRevision >= MaxRevision {
		return fmt.Errorf("%w: expected revision cannot advance", ErrInvalidAccess)
	}
	return c.Binding.Validate()
}

// WriteOutcome states whether a caller may safely retry a write.
type WriteOutcome string

const (
	WriteOutcomeCommitted     WriteOutcome = "committed"
	WriteOutcomeNotCommitted  WriteOutcome = "not_committed"
	WriteOutcomeIndeterminate WriteOutcome = "indeterminate"
)

// WriteResult is returned for every mutation path, including typed failures.
type WriteResult struct {
	Outcome  WriteOutcome
	Snapshot Snapshot
}
