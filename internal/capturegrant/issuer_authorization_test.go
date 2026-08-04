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
	)
	if !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("ResolveCaptureRun() error = %v", err)
	}
	if local.calls != 0 {
		t.Fatalf("local resolver calls = %d", local.calls)
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
