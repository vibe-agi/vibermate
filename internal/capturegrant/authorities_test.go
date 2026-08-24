package capturegrant

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

func TestCaptureAuthoritySetKeepsPersistedLaunchBoundaryImmutable(t *testing.T) {
	t.Parallel()

	protected := []string{"api.openai.com:443", "api.anthropic.com:443"}
	managed := []string{"api.openai.com:443"}
	assignment := captureAuthorityAssignment(t, protected, managed)
	set, err := NewCaptureAuthoritySet(assignment)
	if err != nil {
		t.Fatal(err)
	}
	protected[0] = "mutated.example:443"
	managed[0] = "mutated.example:443"
	gotProtected := set.ProtectedAuthorities()
	gotManaged := set.ManagedCredentialAuthorities()
	if len(gotProtected) != 2 ||
		gotProtected[0] != "api.anthropic.com:443" ||
		gotProtected[1] != "api.openai.com:443" ||
		len(gotManaged) != 1 ||
		gotManaged[0] != "api.openai.com:443" {
		t.Fatalf("CaptureAuthoritySet protected=%v managed=%v", gotProtected, gotManaged)
	}
	gotProtected[0] = "changed.example:443"
	if set.ProtectedAuthorities()[0] != "api.anthropic.com:443" {
		t.Fatal("CaptureAuthoritySet getter returned mutable authority storage")
	}
	if set.Capture() != assignment.Capture ||
		set.AssignmentRevision() != assignment.Revision ||
		set.EnvironmentID() != assignment.EnvironmentID ||
		set.InitialEnvironmentID() != assignment.EnvironmentID ||
		set.AuthorityDigest() != assignment.LaunchAuthority.Digest() {
		t.Fatalf("CaptureAuthoritySet lost assignment provenance: %+v", set)
	}
}

func TestCaptureAuthoritySetRejectsInvalidPersistedBoundary(t *testing.T) {
	t.Parallel()

	assignment := captureAuthorityAssignment(
		t,
		[]string{"api.anthropic.com:443"},
		nil,
	)
	initialDigest := assignment.LaunchAuthority.InitialEnvironmentDigest()
	assignment.LaunchAuthority = environment.LaunchAuthorityBoundary{}
	if _, err := NewCaptureAuthoritySet(assignment); err == nil {
		t.Fatal("NewCaptureAuthoritySet() accepted an invalid launch boundary")
	}
	if _, err := environment.NewLaunchAuthorityBoundaryFromScopes(
		assignment.EnvironmentID,
		1,
		initialDigest,
		[]string{"api.anthropic.com:443"},
		[]string{"api.openai.com:443"},
	); err == nil {
		t.Fatal("launch boundary accepted a managed authority outside protection")
	}
}

func TestEnvironmentAuthorityResolverCreatesTypedCaptureAssignment(t *testing.T) {
	t.Parallel()

	capture, err := captureidentity.New(captureidentity.KindManualCapture, "manual-one")
	if err != nil {
		t.Fatal(err)
	}
	assignment := captureAuthorityAssignment(t, nil, nil)
	assignment.Capture = capture
	assignment.Source = captureassignment.SourceManualCreate
	assignments := &recordingCaptureAssignmentAuthority{assignment: assignment}
	resolver, err := NewEnvironmentAuthorityResolver(assignments, fixedSnapshotResolver{})
	if err != nil {
		t.Fatal(err)
	}
	set, err := resolver.AssignAndResolve(
		context.Background(), capture, "work", captureassignment.SourceManualCreate,
	)
	if err != nil {
		t.Fatal(err)
	}
	if assignments.create.Capture != capture ||
		assignments.create.EnvironmentID != "work" ||
		assignments.create.Source != captureassignment.SourceManualCreate ||
		set.AuthorityDigest() != assignment.LaunchAuthority.Digest() {
		t.Fatalf("create=%+v set=%+v", assignments.create, set)
	}
}

type recordingCaptureAssignmentAuthority struct {
	create     captureassignment.CreateCommand
	assignment captureassignment.Assignment
	err        error
}

func (authority *recordingCaptureAssignmentAuthority) Create(
	_ context.Context,
	command captureassignment.CreateCommand,
) (captureassignment.Assignment, error) {
	authority.create = command
	return authority.assignment, authority.err
}

func (authority *recordingCaptureAssignmentAuthority) Resolve(
	context.Context,
	captureidentity.Reference,
) (captureassignment.Assignment, error) {
	return authority.assignment, authority.err
}

type fixedSnapshotResolver struct{}

func (fixedSnapshotResolver) Resolve(environment.EnvironmentID) (environment.EnvironmentSnapshot, error) {
	return environment.EnvironmentSnapshot{}, errors.New("unexpected Environment resolve")
}

func (fixedSnapshotResolver) ResolveRevision(
	context.Context,
	environment.EnvironmentID,
	environment.Revision,
) (environment.EnvironmentSnapshot, error) {
	return environment.EnvironmentSnapshot{}, errors.New("unexpected Environment revision resolve")
}

func (fixedSnapshotResolver) ResolveClientOrigin(
	environment.EnvironmentID,
	originidentity.ClientOrigin,
) (environment.ClientEndpointSnapshot, error) {
	return environment.ClientEndpointSnapshot{}, errors.New("unexpected ClientOrigin resolve")
}

func captureAuthorityAssignment(
	t *testing.T,
	protected []string,
	managed []string,
) captureassignment.Assignment {
	t.Helper()
	capture, err := captureidentity.New(captureidentity.KindManagedRun, "run-one")
	if err != nil {
		t.Fatal(err)
	}
	var environmentDigest environment.CandidateDigest
	environmentDigest[0] = 1
	boundary, err := environment.NewLaunchAuthorityBoundaryFromScopes(
		"work", 1, environmentDigest, protected, managed,
	)
	if err != nil {
		t.Fatal(err)
	}
	return captureassignment.Assignment{
		Capture: capture, EnvironmentID: "work", Revision: 1,
		Source: captureassignment.SourceLaunch, LaunchAuthority: boundary,
		UpdatedAt: time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC),
	}
}
