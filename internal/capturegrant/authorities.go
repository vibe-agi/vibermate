package capturegrant

import (
	"context"
	"errors"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/clienttarget"
	"github.com/vibe-agi/vibermate/internal/environment"
)

// CaptureAuthoritySet freezes the exact non-secret launch boundary attached
// to a durable Capture assignment. Protected authorities may receive the
// local Root and be removed from NO_PROXY. Only the managed subset may have
// client login inputs replaced with a local placeholder.
type CaptureAuthoritySet struct {
	capture            captureidentity.Reference
	assignmentRevision captureassignment.Revision
	environmentID      environment.EnvironmentID
	launchAuthority    environment.LaunchAuthorityBoundary
	launchEnvironment  environment.LaunchEnvironmentPolicy
}

func NewCaptureAuthoritySet(
	assignment captureassignment.Assignment,
) (CaptureAuthoritySet, error) {
	if assignment.Validate() != nil || assignment.LaunchAuthority.Validate() != nil {
		return CaptureAuthoritySet{}, errors.New("CaptureRun launch authority is invalid")
	}
	return CaptureAuthoritySet{
		capture: assignment.Capture, assignmentRevision: assignment.Revision,
		environmentID:   assignment.EnvironmentID,
		launchAuthority: assignment.LaunchAuthority,
	}, nil
}

func (set CaptureAuthoritySet) Capture() captureidentity.Reference { return set.capture }

func (set CaptureAuthoritySet) AssignmentRevision() captureassignment.Revision {
	return set.assignmentRevision
}

func (set CaptureAuthoritySet) EnvironmentID() environment.EnvironmentID {
	return set.environmentID
}

func (set CaptureAuthoritySet) InitialEnvironmentID() environment.EnvironmentID {
	return set.launchAuthority.InitialEnvironmentID()
}

func (set CaptureAuthoritySet) InitialEnvironmentRevision() environment.Revision {
	return set.launchAuthority.InitialEnvironmentRevision()
}

func (set CaptureAuthoritySet) InitialEnvironmentDigest() environment.CandidateDigest {
	return set.launchAuthority.InitialEnvironmentDigest()
}

func (set CaptureAuthoritySet) AuthorityDigest() environment.LaunchAuthorityDigest {
	return set.launchAuthority.Digest()
}

func (set CaptureAuthoritySet) ProtectedAuthorities() []string {
	return set.launchAuthority.ProtectedAuthorities()
}

func (set CaptureAuthoritySet) ManagedCredentialAuthorities() []string {
	return set.launchAuthority.ManagedCredentialAuthorities()
}

func (set CaptureAuthoritySet) LaunchEnvironment() environment.LaunchEnvironmentPolicy {
	return set.launchEnvironment.Clone()
}

func (set CaptureAuthoritySet) Review() CaptureAuthorityReview {
	return CaptureAuthorityReview{
		environmentID:   set.environmentID,
		launchAuthority: set.launchAuthority,
	}
}

type CaptureAuthorityResolver interface {
	Review(
		context.Context,
		environment.EnvironmentID,
	) (CaptureAuthorityReview, error)
	AssignAndResolve(
		context.Context,
		captureidentity.Reference,
		environment.EnvironmentID,
		captureassignment.Source,
		clienttarget.Profile,
	) (CaptureAuthoritySet, error)
	Resolve(
		context.Context,
		captureidentity.Reference,
	) (CaptureAuthoritySet, error)
}

type captureAssignmentAuthority interface {
	CreateForLaunch(
		context.Context,
		captureassignment.CreateCommand,
	) (
		captureassignment.Assignment,
		environment.LaunchEnvironmentPolicy,
		error,
	)
	Resolve(context.Context, captureidentity.Reference) (captureassignment.Assignment, error)
}

type environmentAuthorityResolver struct {
	assignments  captureAssignmentAuthority
	environments environment.SnapshotResolver
}

func NewEnvironmentAuthorityResolver(
	assignments captureAssignmentAuthority,
	environments environment.SnapshotResolver,
) (CaptureAuthorityResolver, error) {
	if assignments == nil || environments == nil {
		return nil, errors.New("CaptureRun Environment authority is unavailable")
	}
	return &environmentAuthorityResolver{
		assignments: assignments, environments: environments,
	}, nil
}

// CaptureAuthorityReview is a non-secret, immutable pre-create fact used to
// bind ManualCapture confirmation to the exact Environment authority a person
// reviewed.
type CaptureAuthorityReview struct {
	environmentID   environment.EnvironmentID
	launchAuthority environment.LaunchAuthorityBoundary
}

func (review CaptureAuthorityReview) EnvironmentID() environment.EnvironmentID {
	return review.environmentID
}
func (review CaptureAuthorityReview) EnvironmentRevision() environment.Revision {
	return review.launchAuthority.InitialEnvironmentRevision()
}
func (review CaptureAuthorityReview) EnvironmentDigest() environment.CandidateDigest {
	return review.launchAuthority.InitialEnvironmentDigest()
}
func (review CaptureAuthorityReview) AuthorityDigest() environment.LaunchAuthorityDigest {
	return review.launchAuthority.Digest()
}
func (review CaptureAuthorityReview) ProtectedAuthorities() []string {
	return review.launchAuthority.ProtectedAuthorities()
}
func (review CaptureAuthorityReview) ManagedCredentialAuthorities() []string {
	return review.launchAuthority.ManagedCredentialAuthorities()
}

func (resolver *environmentAuthorityResolver) Review(
	ctx context.Context,
	environmentID environment.EnvironmentID,
) (CaptureAuthorityReview, error) {
	if resolver == nil || resolver.environments == nil || ctx == nil {
		return CaptureAuthorityReview{}, errors.New("Capture Environment review is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return CaptureAuthorityReview{}, err
	}
	snapshot, err := resolver.environments.Resolve(environmentID)
	if err != nil {
		return CaptureAuthorityReview{}, err
	}
	boundary, err := environment.NewLaunchAuthorityBoundary(snapshot)
	if err != nil {
		return CaptureAuthorityReview{}, err
	}
	return CaptureAuthorityReview{
		environmentID: environmentID, launchAuthority: boundary,
	}, nil
}

// AssignAndResolve creates revision one as the linearization point. The
// assignment manager derives and persists LaunchAuthorityBoundary while it
// holds the selected Environment's publish gate, so this package never
// rebuilds authority from a second mutable read.
func (resolver *environmentAuthorityResolver) AssignAndResolve(
	ctx context.Context,
	capture captureidentity.Reference,
	environmentID environment.EnvironmentID,
	source captureassignment.Source,
	profile clienttarget.Profile,
) (CaptureAuthoritySet, error) {
	if resolver == nil || resolver.assignments == nil || ctx == nil ||
		capture.Validate() != nil {
		return CaptureAuthoritySet{}, errors.New("CaptureRun Environment authority is unavailable")
	}
	if _, err := environment.NewEnvironmentID(environmentID.String()); err != nil {
		return CaptureAuthoritySet{}, errors.New("CaptureRun Environment is invalid")
	}
	if err := ctx.Err(); err != nil {
		return CaptureAuthoritySet{}, err
	}
	if (capture.Kind == captureidentity.KindManualCapture && source != captureassignment.SourceManualCreate) ||
		(capture.Kind == captureidentity.KindManagedRun && source != captureassignment.SourceLaunch &&
			source != captureassignment.SourceSystemTransparent) {
		return CaptureAuthoritySet{}, errors.New("CaptureRun Environment assignment source is invalid")
	}
	assignment, launchEnvironment, err := resolver.assignments.CreateForLaunch(
		ctx,
		captureassignment.CreateCommand{
			Capture: capture, EnvironmentID: environmentID, Source: source,
			ClientProfile: profile,
		},
	)
	if err != nil {
		return CaptureAuthoritySet{}, err
	}
	if assignment.Capture != capture || assignment.EnvironmentID != environmentID ||
		assignment.Revision != 1 || assignment.Source != source ||
		assignment.LaunchAuthority.InitialEnvironmentID() != environmentID {
		return CaptureAuthoritySet{}, errors.New("CaptureRun Environment assignment is inconsistent")
	}
	set, err := NewCaptureAuthoritySet(assignment)
	if err != nil {
		return CaptureAuthoritySet{}, err
	}
	set.launchEnvironment = launchEnvironment.Clone()
	return set, nil
}

func (resolver *environmentAuthorityResolver) Resolve(
	ctx context.Context,
	capture captureidentity.Reference,
) (CaptureAuthoritySet, error) {
	if resolver == nil || resolver.assignments == nil || ctx == nil ||
		capture.Validate() != nil {
		return CaptureAuthoritySet{}, errors.New("Capture Environment authority is unavailable")
	}
	assignment, err := resolver.assignments.Resolve(ctx, capture)
	if err != nil {
		return CaptureAuthoritySet{}, err
	}
	if assignment.Capture != capture {
		return CaptureAuthoritySet{}, errors.New("Capture Environment assignment is inconsistent")
	}
	set, err := NewCaptureAuthoritySet(assignment)
	if err != nil {
		return CaptureAuthoritySet{}, err
	}
	return resolver.attachLaunchEnvironment(ctx, set)
}

func (resolver *environmentAuthorityResolver) attachLaunchEnvironment(
	ctx context.Context,
	set CaptureAuthoritySet,
) (CaptureAuthoritySet, error) {
	snapshot, err := resolver.environments.ResolveRevision(
		ctx,
		set.InitialEnvironmentID(),
		set.InitialEnvironmentRevision(),
	)
	if err != nil || snapshot.ID() != set.InitialEnvironmentID() ||
		snapshot.Revision() != set.InitialEnvironmentRevision() ||
		snapshot.Digest() != set.InitialEnvironmentDigest() ||
		snapshot.State() != environment.StateActive {
		return CaptureAuthoritySet{}, errors.New(
			"CaptureRun launch Environment revision is unavailable",
		)
	}
	set.launchEnvironment = snapshot.LaunchEnvironment()
	return set, nil
}
