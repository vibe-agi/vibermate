// Package captureassignment owns the server-side Environment assignment for
// every managed CaptureRun and ManualCapture.
package captureassignment

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
)

const (
	MaxRevision     = Revision(1<<63 - 1)
	MaxConnectionID = 128
	MaxListLimit    = 2048
)

var (
	ErrInvalidAssignment     = errors.New("Capture Environment assignment is invalid")
	ErrAssignmentNotFound    = errors.New("Capture Environment assignment is not configured")
	ErrAssignmentConflict    = errors.New("Capture Environment assignment revision conflict")
	ErrAssignmentUnavailable = errors.New("Capture Environment assignment is unavailable")
	ErrRuntimeStopping       = errors.New("Capture Environment assignment runtime is stopping")
	ErrOperationInProgress   = errors.New("Capture Environment assignment operation is in progress")
	ErrConnectionNotFound    = errors.New("Capture connection is not registered")
	ErrReconnectUnavailable  = errors.New("Capture connection cannot be closed safely")
	ErrWriteNotCommitted     = errors.New("Capture Environment assignment write was not committed")
	ErrCommitOutcomeUnknown  = errors.New("Capture Environment assignment commit outcome is unknown")
)

type Revision uint64

type Source string

const (
	SourceLaunch            Source = "launch"
	SourceManualCreate      Source = "manual_create"
	SourceSystemTransparent Source = "system_transparent"
)

func (source Source) valid() bool {
	switch source {
	case SourceLaunch, SourceManualCreate, SourceSystemTransparent:
		return true
	default:
		return false
	}
}

type Assignment struct {
	Capture         captureidentity.Reference
	EnvironmentID   environment.EnvironmentID
	Revision        Revision
	Source          Source
	LaunchAuthority environment.LaunchAuthorityBoundary
	UpdatedAt       time.Time
}

func (assignment Assignment) Validate() error {
	if assignment.Capture.Validate() != nil || assignment.EnvironmentID == "" ||
		assignment.Revision != 1 ||
		!assignment.Source.valid() || assignment.LaunchAuthority.Validate() != nil ||
		assignment.UpdatedAt.IsZero() ||
		!assignment.UpdatedAt.Equal(assignment.UpdatedAt.UTC().Truncate(time.Millisecond)) {
		return ErrInvalidAssignment
	}
	if _, err := environment.NewEnvironmentID(assignment.EnvironmentID.String()); err != nil {
		return ErrInvalidAssignment
	}
	if assignment.EnvironmentID != assignment.LaunchAuthority.InitialEnvironmentID() {
		return ErrInvalidAssignment
	}
	return nil
}

type CommitOutcome string

const (
	CommitOutcomeCommitted     CommitOutcome = "committed"
	CommitOutcomeNotCommitted  CommitOutcome = "not_committed"
	CommitOutcomeConflict      CommitOutcome = "conflict"
	CommitOutcomeIndeterminate CommitOutcome = "indeterminate"
)

type CommitResult struct {
	Outcome    CommitOutcome
	Assignment Assignment
	Actual     Revision
}

type Repository interface {
	Load(context.Context, captureidentity.Reference) (Assignment, bool, error)
	ListByEnvironment(context.Context, environment.EnvironmentID, int) ([]Assignment, error)
	Write(context.Context, Revision, Assignment) (CommitResult, error)
}

// CaptureActivity is the lifecycle authority used only to decide whether a
// persisted assignment still represents a live Capture. Assignments are
// retained as immutable historical evidence after a Capture finishes, so
// their mere presence must never make an Environment transition affect a
// process that no longer exists.
type CaptureActivity interface {
	Active(context.Context, captureidentity.Reference) (bool, error)
}

type CreateCommand struct {
	Capture       captureidentity.Reference
	EnvironmentID environment.EnvironmentID
	Source        Source
}

// Controller is the control-plane authority for one Capture's current
// Environment assignment. Connection and request leases stay on the narrower
// data-plane interfaces consumed by the proxy.
type Controller interface {
	Create(context.Context, CreateCommand) (Assignment, error)
	CreateForLaunch(
		context.Context,
		CreateCommand,
	) (Assignment, environment.LaunchEnvironmentPolicy, error)
	Resolve(context.Context, captureidentity.Reference) (Assignment, error)
}

type Clock interface{ Now() time.Time }

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

func canonicalTime(value time.Time) time.Time { return value.UTC().Truncate(time.Millisecond) }

func validConnectionID(value string) bool {
	if value == "" || len(value) > MaxConnectionID || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || character > unicode.MaxASCII ||
			!(character >= 'a' && character <= 'z') &&
				!(character >= 'A' && character <= 'Z') &&
				!(character >= '0' && character <= '9') &&
				character != '-' && character != '_' && character != '.' && character != ':' {
			return false
		}
	}
	return true
}
