package captureadmission

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/vibe-agi/vibermate/internal/captureauthority"
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
	admission, err := NewManagedRun(ManagedRunEvidence{
		CaptureRunID:    evidence.RunID,
		SourceLabel:     evidence.ExecutableLabel,
		WorkspaceRoot:   evidence.CWD,
		Workspace:       evidence.Workspace,
		Adapter:         evidence.Adapter,
		Runtime:         evidence.Runtime,
		RuntimeUsername: evidence.RuntimeUsername,
		RuntimeUserID:   evidence.RuntimeUserID,
	})
	if err != nil {
		return Admission{}, err
	}
	if reader, ok := authorizer.runs.(interface {
		ProxyValidity(context.Context, capturerun.ProxyCapability) (captureauthority.Validity, error)
	}); ok {
		admission.receipt = &retainedReceipt{issuer: authorizer, read: func(ctx context.Context) (captureauthority.Validity, error) {
			return reader.ProxyValidity(ctx, capability)
		}}
	}
	return admission, nil
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
	admission, err := NewManual(
		evidence.ManualCaptureID.String(),
		uint64(evidence.CredentialRevision),
		evidence.DisplayName,
	)
	if err != nil {
		return Admission{}, err
	}
	if reader, ok := authorizer.manuals.(interface {
		ProxyValidity(context.Context, manualcapture.ProxyCredential) (captureauthority.Validity, error)
	}); ok {
		admission.receipt = &retainedReceipt{issuer: authorizer, read: func(ctx context.Context) (captureauthority.Validity, error) {
			return reader.ProxyValidity(ctx, credential)
		}}
	}
	return admission, nil
}

// retainedReceipt is sealed by successful authentication. Public attribution
// constructors cannot turn a Capture ID into retained authority.
type retainedReceipt struct {
	withdrawn atomic.Bool
	issuer    *compositeAuthorizer
	read      func(context.Context) (captureauthority.Validity, error)
}

func (authorizer *compositeAuthorizer) Revalidate(ctx context.Context, admission Admission) (captureauthority.Validity, error) {
	if admission.receipt == nil || admission.receipt.issuer != authorizer || admission.receipt.withdrawn.Load() {
		return captureauthority.Validity{}, ErrCredentialRejected
	}
	validity, err := admission.receipt.read(ctx)
	capture, captureErr := admission.CaptureReference()
	if err != nil || captureErr != nil || validity.Capture != capture || validity.CredentialRevision != admission.credentialRevision {
		return captureauthority.Validity{}, errors.Join(ErrCredentialRejected, err, captureErr)
	}
	return validity, nil
}
