package capturecontrol

import (
	"github.com/vibe-agi/vibermate/internal/capturegrant"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

// CompanionAttestationInput is produced on the machine that will execute the
// child. Authentication remains a separate TLS control-session concern. The
// Server revalidates the exact built-in adapter evidence and constructs the
// registered-companion workspace evidence itself.
type CompanionAttestationInput struct {
	Detection clientadapter.Detection `json:"detection"`
	Workspace CompanionWorkspaceInput `json:"workspace"`
}

type CompanionWorkspaceInput struct {
	MachineID            string `json:"machineId"`
	WorkspaceID          string `json:"workspaceId"`
	WorkspaceLabel       string `json:"workspaceLabel"`
	RegistrationRevision uint64 `json:"registrationRevision"`
	DerivationRevision   uint64 `json:"derivationRevision"`
}

func (input *CompanionAttestationInput) domain() (*capturegrant.CompanionAttestation, error) {
	if input == nil {
		return nil, nil
	}
	machineID, err := workspaceidentity.ParseMachineID(input.Workspace.MachineID)
	if err != nil {
		return nil, err
	}
	workspaceID, err := workspaceidentity.ParseWorkspaceID(input.Workspace.WorkspaceID)
	if err != nil {
		return nil, err
	}
	scope, err := workspaceidentity.NewScope(
		machineID,
		workspaceID,
		input.Workspace.WorkspaceLabel,
		workspaceidentity.EvidenceRegisteredCompanion,
		input.Workspace.RegistrationRevision,
		input.Workspace.DerivationRevision,
	)
	if err != nil {
		return nil, err
	}
	return &capturegrant.CompanionAttestation{
		Detection: input.Detection,
		Workspace: scope,
	}, nil
}
