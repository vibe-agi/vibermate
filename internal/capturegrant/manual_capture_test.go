package capturegrant

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecredential"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

type recordingManualCaptures struct {
	command manualcapture.CreateCommand
	grant   manualcapture.Grant
	err     error
	calls   int
}

func (controller *recordingManualCaptures) Create(
	_ context.Context,
	command manualcapture.CreateCommand,
) (manualcapture.Grant, error) {
	controller.calls++
	controller.command = command
	return controller.grant, controller.err
}

func (*recordingManualCaptures) Rotate(
	context.Context,
	manualcapture.RotateCommand,
) (manualcapture.Grant, error) {
	return manualcapture.Grant{}, errors.New("unexpected rotate")
}

func (*recordingManualCaptures) Revoke(
	context.Context,
	manualcapture.RevokeCommand,
) (manualcapture.View, error) {
	return manualcapture.View{}, errors.New("unexpected revoke")
}

func (*recordingManualCaptures) Get(
	context.Context,
	manualcapture.OwnerScope,
	manualcapture.ID,
) (manualcapture.View, error) {
	return manualcapture.View{}, errors.New("unexpected get")
}

func (*recordingManualCaptures) List(
	context.Context,
	manualcapture.PageRequest,
) (manualcapture.Page, error) {
	return manualcapture.Page{}, errors.New("unexpected list")
}

func TestIssueManualCaptureDerivesOwnerFromPrincipal(t *testing.T) {
	t.Parallel()
	credential, err := capturecredential.New(
		capturecredential.KindManualCapture,
		make([]byte, capturecredential.EntropyBytes),
	)
	if err != nil {
		t.Fatal(err)
	}
	proxyCredential, err := manualcapture.NewProxyCredential(credential.Value())
	if err != nil {
		t.Fatal(err)
	}
	manuals := &recordingManualCaptures{grant: manualcapture.Grant{
		Capture:    manualcapture.View{ID: "manual-one"},
		Credential: proxyCredential,
	}}
	authority, err := localca.Open(
		context.Background(),
		localca.DefaultOptions(
			filepath.Join(t.TempDir(), "ca"),
			context.Background(),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := authority.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown local CA: %v", err)
		}
	})
	generation := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	issuer := &Issuer{
		manuals:     manuals,
		proxyOrigin: "http://127.0.0.1:41080",
		generation:  generation,
		rootID:      authority.Identity(),
		root:        authority.Certificate(),
	}
	principal, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                    "enrolled:one",
		Kind:                  controlprincipal.KindEnrolledClient,
		ProxyClientBindingID:  "binding-one",
		MachineRegistrationID: "machine-one",
		CredentialRevision:    1,
		AllowedGrantKinds: []controlprincipal.GrantKind{
			controlprincipal.GrantManualCapture,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	captureContext, err := issuer.GetManualCaptureContext(
		context.Background(),
		principal,
	)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := issuer.IssueManualCapture(
		context.Background(),
		principal,
		ManualCaptureRequest{
			DisplayName:       "Remote desktop",
			ClientClass:       manualcapture.ClientDesktopApp,
			Lifetime:          manualcapture.LifetimeUntilRevoked,
			ConfirmationToken: captureContext.ConfirmationToken,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	bindingID, ok := manuals.command.Owner.ProxyClientBindingID()
	if manuals.calls != 1 || !ok || bindingID != "binding-one" ||
		grant.Context.ProxyAddress != issuer.proxyOrigin ||
		grant.Context.ConfirmationToken != captureContext.ConfirmationToken ||
		grant.Context.RootIdentity != authority.Identity() ||
		grant.Context.RootCertificate.Path() != authority.Certificate().Path() {
		t.Fatalf(
			"command=%+v grant=%+v controller calls=%d",
			manuals.command,
			grant,
			manuals.calls,
		)
	}
}

func TestIssueManualCaptureRejectsUnauthorizedPrincipalBeforeDependencies(
	t *testing.T,
) {
	t.Parallel()
	principal, err := controlprincipal.New(controlprincipal.Attributes{
		ID:                 "local-cli:run-only",
		Kind:               controlprincipal.KindLocalCLI,
		CredentialRevision: 1,
		AllowedGrantKinds: []controlprincipal.GrantKind{
			controlprincipal.GrantCaptureRun,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Issuer{}).IssueManualCapture(
		context.Background(),
		principal,
		ManualCaptureRequest{},
	); !errors.Is(err, ErrPrincipalUnauthorized) {
		t.Fatalf("IssueManualCapture() error = %v", err)
	}
}
