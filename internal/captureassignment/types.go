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
	ErrLaunchRestartRequired = environment.ErrLaunchAuthorityRestartRequired
)

type Revision uint64

type Source string

const (
	SourceLaunch            Source = "launch"
	SourceManualCreate      Source = "manual_create"
	SourceWorkspaceDefault  Source = "workspace_default"
	SourceOperatorSwitch    Source = "operator_switch"
	SourceSystemTransparent Source = "system_transparent"
)

func (source Source) valid() bool {
	switch source {
	case SourceLaunch, SourceManualCreate, SourceWorkspaceDefault, SourceOperatorSwitch, SourceSystemTransparent:
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
		assignment.Revision == 0 || assignment.Revision > MaxRevision ||
		!assignment.Source.valid() || assignment.LaunchAuthority.Validate() != nil ||
		assignment.UpdatedAt.IsZero() ||
		!assignment.UpdatedAt.Equal(assignment.UpdatedAt.UTC().Truncate(time.Millisecond)) {
		return ErrInvalidAssignment
	}
	if _, err := environment.NewEnvironmentID(assignment.EnvironmentID.String()); err != nil {
		return ErrInvalidAssignment
	}
	if assignment.Revision == 1 &&
		assignment.EnvironmentID != assignment.LaunchAuthority.InitialEnvironmentID() {
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

type Boundary string

const (
	BoundaryNoChange          Boundary = "no_change"
	BoundaryHotSwitch         Boundary = "hot_switch"
	BoundaryReconnectRequired Boundary = "reconnect_required"
	BoundaryRestartRequired   Boundary = "restart_required"
)

type CreateCommand struct {
	Capture       captureidentity.Reference
	EnvironmentID environment.EnvironmentID
	Source        Source
}

type SwitchCommand struct {
	Capture             captureidentity.Reference
	ExpectedRevision    Revision
	TargetEnvironmentID environment.EnvironmentID
	Source              Source
}

type SwitchResult struct {
	Assignment        Assignment
	Boundary          Boundary
	ClosedConnections []string
}

// ConnectionCloseHandle is owned by one registered downstream connection.
// The proxy supplies the handle at registration time so assignment lifecycle
// code never depends on a mutable, process-global proxy back-reference.
type ConnectionCloseHandle interface {
	Close(context.Context) error
}

// LeafCacheInvalidation is emitted only after an Environment revision has
// been durably committed and published. Its fields identify derived leaves
// from the obsolete revision; it carries no certificate or secret material.
type LeafCacheInvalidation struct {
	environmentID       environment.EnvironmentID
	environmentRevision environment.Revision
	endpointID          environment.ClientEndpointID
	endpointRevision    environment.Revision
}

func (invalidation LeafCacheInvalidation) EnvironmentID() environment.EnvironmentID {
	return invalidation.environmentID
}
func (invalidation LeafCacheInvalidation) EnvironmentRevision() environment.Revision {
	return invalidation.environmentRevision
}
func (invalidation LeafCacheInvalidation) ClientEndpointID() environment.ClientEndpointID {
	return invalidation.endpointID
}
func (invalidation LeafCacheInvalidation) ClientEndpointRevision() environment.Revision {
	return invalidation.endpointRevision
}

type LeafCacheInvalidator interface {
	InvalidateLeafCache(LeafCacheInvalidation)
}

// Controller is the control-plane authority for one Capture's current
// Environment assignment. Connection and request leases stay on the narrower
// data-plane interfaces consumed by the proxy.
type Controller interface {
	Create(context.Context, CreateCommand) (Assignment, error)
	Resolve(context.Context, captureidentity.Reference) (Assignment, error)
	Switch(context.Context, SwitchCommand) (SwitchResult, error)
}

func LeafCacheInvalidations(snapshot environment.EnvironmentSnapshot) []LeafCacheInvalidation {
	if snapshot.ID() == "" || snapshot.SystemOwned() {
		return nil
	}
	endpoints := snapshot.ClientEndpoints()
	result := make([]LeafCacheInvalidation, 0, len(endpoints))
	for _, endpoint := range endpoints {
		result = append(result, LeafCacheInvalidation{
			environmentID: snapshot.ID(), environmentRevision: snapshot.Revision(),
			endpointID: endpoint.ID(), endpointRevision: endpoint.Revision(),
		})
	}
	return result
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
