package captureadmission

import (
	"errors"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

// ManagedRunEvidence is the frozen attribution produced after a CaptureRun
// capability is authorized. Constructing the value does not authenticate a
// request; only an Authorizer may place an Admission on a proxy connection.
type ManagedRunEvidence struct {
	CaptureRunID  string
	SourceLabel   string
	WorkspaceRoot string
	Workspace     workspaceidentity.Scope
	Adapter       *clientadapter.Evidence
	Runtime       capturerun.RuntimeMetadata
}

type ManagedRuntimeMetadata = capturerun.RuntimeMetadata

func NewManagedRun(evidence ManagedRunEvidence) (Admission, error) {
	admissionRef, err := capturerun.AdmissionRef(evidence.CaptureRunID)
	if err != nil {
		return Admission{}, errors.Join(ErrInvalidAdmission, err)
	}
	confidence := AttributionConfigured
	if evidence.Adapter != nil {
		confidence = AttributionVerified
	}
	admission := Admission{
		kind:               KindManagedRun,
		admissionRef:       admissionRef,
		captureRunID:       evidence.CaptureRunID,
		credentialRevision: 1,
		confidence:         confidence,
		sourceLabel:        evidence.SourceLabel,
		workspaceRoot:      evidence.WorkspaceRoot,
		adapter:            cloneAdapter(evidence.Adapter),
		runtime:            evidence.Runtime,
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
