package captureadmission

import (
	"context"
	"errors"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

type Authorizer interface {
	Authorize(context.Context, ProxyCredential) (Admission, error)
}

// ManagedRunAuthorizer is the only adapter allowed to translate CaptureRun
// evidence into the shared proxy admission type.
type ManagedRunAuthorizer struct {
	runs capturerun.ProxyAuthorizer
}

// ManagedRunEvidence is the frozen attribution produced after a CaptureRun
// capability is authorized. Constructing the value does not authenticate a
// request; only an Authorizer may place an Admission on a proxy connection.
type ManagedRunEvidence struct {
	CaptureRunID string
	SourceLabel  string
	Workspace    workspaceidentity.Scope
	Adapter      *clientadapter.Evidence
}

func NewManagedRunAuthorizer(
	runs capturerun.ProxyAuthorizer,
) (*ManagedRunAuthorizer, error) {
	if runs == nil {
		return nil, errors.New("CaptureRun authorizer is required")
	}
	return &ManagedRunAuthorizer{runs: runs}, nil
}

func (authorizer *ManagedRunAuthorizer) Authorize(
	ctx context.Context,
	credential ProxyCredential,
) (Admission, error) {
	if authorizer == nil || authorizer.runs == nil {
		return Admission{}, ErrCredentialRejected
	}
	capability, err := capturerun.NewProxyCapability(credential.value)
	if err != nil {
		return Admission{}, errors.Join(ErrCredentialRejected, err)
	}
	evidence, err := authorizer.runs.AuthorizeProxy(ctx, capability)
	if err != nil {
		return Admission{}, errors.Join(ErrCredentialRejected, err)
	}
	return NewManagedRun(ManagedRunEvidence{
		CaptureRunID: evidence.RunID,
		SourceLabel:  evidence.ExecutableLabel,
		Workspace:    evidence.Workspace,
		Adapter:      evidence.Adapter,
	})
}

func NewManagedRun(evidence ManagedRunEvidence) (Admission, error) {
	confidence := AttributionConfigured
	if evidence.Adapter != nil {
		confidence = AttributionVerified
	}
	admission := Admission{
		kind:               KindManagedRun,
		ingressProfileID:   "capture-run/" + evidence.CaptureRunID,
		captureRunID:       evidence.CaptureRunID,
		credentialRevision: 1,
		confidence:         confidence,
		sourceLabel:        evidence.SourceLabel,
		adapter:            cloneAdapter(evidence.Adapter),
	}
	if evidence.Workspace != (admission.workspace) {
		if err := evidence.Workspace.Validate(); err != nil {
			return Admission{}, errors.Join(ErrInvalidAdmission, err)
		}
		admission.workspace = evidence.Workspace
		admission.machineID = evidence.Workspace.MachineID()
		admission.workspaceID = evidence.Workspace.WorkspaceID()
	}
	if err := admission.Validate(); err != nil {
		return Admission{}, err
	}
	return admission, nil
}

func cloneAdapter(evidence *clientadapter.Evidence) *clientadapter.Evidence {
	if evidence == nil {
		return nil
	}
	cloned := *evidence
	return &cloned
}
