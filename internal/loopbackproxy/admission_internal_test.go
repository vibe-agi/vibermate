package loopbackproxy

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/capturecredential"
)

type fixedCaptureAuthorizer struct {
	admission captureadmission.Admission
	err       error
	calls     int
}

func (authorizer *fixedCaptureAuthorizer) Authorize(
	context.Context,
	captureadmission.ProxyCredential,
) (captureadmission.Admission, error) {
	authorizer.calls++
	return authorizer.admission, authorizer.err
}

func validCaptureCredential(t *testing.T) string {
	t.Helper()
	credential, err := capturecredential.New(
		capturecredential.KindManualCapture,
		make([]byte, capturecredential.EntropyBytes),
	)
	if err != nil {
		t.Fatal(err)
	}
	return credential.Value()
}

func TestCaptureAdmissionBoundaryRevalidatesAuthorizerOutput(t *testing.T) {
	t.Parallel()

	authorizer := &fixedCaptureAuthorizer{}
	if _, err := authorizeCaptureAdmission(
		context.Background(),
		authorizer,
		validCaptureCredential(t),
	); !errors.Is(err, captureadmission.ErrCredentialRejected) ||
		!errors.Is(err, captureadmission.ErrInvalidAdmission) {
		t.Fatalf("invalid authorizer output error = %v", err)
	}
	if authorizer.calls != 1 {
		t.Fatalf("authorizer calls = %d", authorizer.calls)
	}
}

func TestMalformedCaptureCredentialNeverReachesAuthorizer(t *testing.T) {
	t.Parallel()

	authorizer := &fixedCaptureAuthorizer{}
	if _, err := authorizeCaptureAdmission(
		context.Background(),
		authorizer,
		"not-a-capability",
	); !errors.Is(err, captureadmission.ErrCredentialRejected) {
		t.Fatalf("malformed credential error = %v", err)
	}
	if authorizer.calls != 0 {
		t.Fatalf("authorizer calls = %d", authorizer.calls)
	}
}

func TestValidCaptureAdmissionCrossesBoundaryUnchanged(t *testing.T) {
	t.Parallel()

	want, err := captureadmission.NewManual("manual-one", 2, "Desktop app")
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &fixedCaptureAuthorizer{admission: want}
	got, err := authorizeCaptureAdmission(
		context.Background(),
		authorizer,
		validCaptureCredential(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.IngressProfileID() != want.IngressProfileID() ||
		got.Kind() != want.Kind() ||
		got.CredentialRevision() != want.CredentialRevision() {
		t.Fatalf("admission = %#v, want %#v", got, want)
	}
}
