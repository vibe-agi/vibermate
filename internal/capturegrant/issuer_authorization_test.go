package capturegrant

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

func TestIssueCaptureRunRejectsGrantBeforeReadingDependencies(t *testing.T) {
	t.Parallel()

	principal, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                 "local-cli:manual-only",
		Kind:               controlprincipal.KindLocalCLI,
		CredentialRevision: 1,
		AllowedGrantKinds: []controlprincipal.GrantKind{
			controlprincipal.GrantManualCapture,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&Issuer{}).IssueCaptureRun(
		context.Background(),
		principal,
		CaptureRunRequest{},
	)
	if !errors.Is(err, ErrPrincipalUnauthorized) {
		t.Fatalf("IssueCaptureRun() error = %v", err)
	}
}

func TestLocalWorkspaceResolverRejectsEnrolledPrincipalBeforeLocalLookup(
	t *testing.T,
) {
	t.Parallel()

	local := &recordingLocalResolver{}
	resolver, err := NewLocalWorkspaceResolver(local)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                    "enrolled-client:test",
		Kind:                  controlprincipal.KindEnrolledClient,
		ProxyClientBindingID:  "binding:test",
		MachineRegistrationID: "machine:test",
		MachineID:             "machine-source:test",
		CredentialRevision:    1,
		AllowedGrantKinds: []controlprincipal.GrantKind{
			controlprincipal.GrantCaptureRun,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.ResolveCaptureRun(
		context.Background(),
		principal,
		"/server-must-not-resolve-this-path",
		workspaceidentity.Scope{},
	)
	if !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("ResolveCaptureRun() error = %v", err)
	}
	if local.calls != 0 {
		t.Fatalf("local resolver calls = %d", local.calls)
	}
}

func TestCompanionWorkspaceResolverRequiresTheAuthenticatedMachine(t *testing.T) {
	t.Parallel()

	machineID, _ := workspaceidentity.ParseMachineID(
		"uRmbW_GvQ7LZ9poYHh0aC8W3vQoJ0lZB7iK2s6xQfEk",
	)
	workspaceID, _ := workspaceidentity.ParseWorkspaceID(
		"sVf8kYgRUlTYtRgxgYf5I7O0gX4C2iJ1R7zE9pQmKAs",
	)
	scope, err := workspaceidentity.NewScope(
		machineID,
		workspaceID,
		"vibermate",
		workspaceidentity.EvidenceRegisteredCompanion,
		1,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                    "remote:client",
		Kind:                  controlprincipal.KindEnrolledClient,
		ProxyClientBindingID:  "network.no-review",
		MachineRegistrationID: "machine." + machineID.String(),
		MachineID:             machineID.String(),
		CredentialRevision:    1,
		AllowedGrantKinds: []controlprincipal.GrantKind{
			controlprincipal.GrantCaptureRun,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewCompanionWorkspaceResolver()
	got, err := resolver.ResolveCaptureRun(
		context.Background(), principal, "/client/path", scope,
	)
	if err != nil || got != scope {
		t.Fatalf("ResolveCaptureRun() = %+v, %v", got, err)
	}

	otherMachine, _ := workspaceidentity.ParseMachineID(
		"wRmbW_GvQ7LZ9poYHh0aC8W3vQoJ0lZB7iK2s6xQfEk",
	)
	other, _ := workspaceidentity.NewScope(
		otherMachine,
		workspaceID,
		"vibermate",
		workspaceidentity.EvidenceRegisteredCompanion,
		1,
		1,
	)
	if _, err := resolver.ResolveCaptureRun(
		context.Background(), principal, "/client/path", other,
	); !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("mismatched machine error = %v", err)
	}
}

type recordingLocalResolver struct {
	calls int
}

func (resolver *recordingLocalResolver) ResolveLocal(
	context.Context,
	string,
) (workspaceidentity.Scope, error) {
	resolver.calls++
	return workspaceidentity.Scope{}, nil
}

func (*recordingLocalResolver) MachineID() workspaceidentity.MachineID {
	return ""
}
