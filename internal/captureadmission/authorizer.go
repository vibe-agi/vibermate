package captureadmission

import (
	"context"
	"errors"

	"github.com/vibe-agi/vibermate/internal/capturecredential"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

type Authorizer interface {
	Authorize(context.Context, ProxyCredential) (Admission, error)
}

// compositeAuthorizer is the sole proxy admission dispatcher. The closed
// credential type tag selects exactly one authority; rejection never falls
// through to a second credential store.
type compositeAuthorizer struct {
	runs    capturerun.ProxyAuthorizer
	manuals manualcapture.ProxyAuthorizer
}

func NewAuthorizer(
	runs capturerun.ProxyAuthorizer,
	manuals manualcapture.ProxyAuthorizer,
) (Authorizer, error) {
	if runs == nil || manuals == nil {
		return nil, errors.New("capture admission authorities are incomplete")
	}
	return &compositeAuthorizer{runs: runs, manuals: manuals}, nil
}

func (authorizer *compositeAuthorizer) Authorize(
	ctx context.Context,
	credential ProxyCredential,
) (Admission, error) {
	if authorizer == nil || !credential.value.Valid() {
		return Admission{}, ErrCredentialRejected
	}
	switch credential.value.Kind() {
	case capturecredential.KindManagedRun:
		return authorizer.authorizeManaged(ctx, credential.value.Value())
	case capturecredential.KindManualCapture:
		return authorizer.authorizeManual(ctx, credential.value.Value())
	default:
		return Admission{}, ErrCredentialRejected
	}
}

func (authorizer *compositeAuthorizer) authorizeManaged(
	ctx context.Context,
	value string,
) (Admission, error) {
	capability, err := capturerun.NewProxyCapability(value)
	if err != nil {
		return Admission{}, errors.Join(ErrCredentialRejected, err)
	}
	evidence, err := authorizer.runs.AuthorizeProxy(ctx, capability)
	if err != nil {
		return Admission{}, errors.Join(ErrCredentialRejected, err)
	}
	return NewManagedRun(ManagedRunEvidence{
		CaptureRunID:    evidence.RunID,
		SourceLabel:     evidence.ExecutableLabel,
		WorkspaceRoot:   evidence.CWD,
		Workspace:       evidence.Workspace,
		Adapter:         evidence.Adapter,
		Runtime:         evidence.Runtime,
		RuntimeUsername: evidence.RuntimeUsername,
	})
}

func (authorizer *compositeAuthorizer) authorizeManual(
	ctx context.Context,
	value string,
) (Admission, error) {
	credential, err := manualcapture.NewProxyCredential(value)
	if err != nil {
		return Admission{}, errors.Join(ErrCredentialRejected, err)
	}
	evidence, err := authorizer.manuals.AuthorizeProxy(ctx, credential)
	if err != nil {
		return Admission{}, errors.Join(ErrCredentialRejected, err)
	}
	return NewManual(
		evidence.ManualCaptureID.String(),
		uint64(evidence.CredentialRevision),
		evidence.DisplayName,
	)
}
