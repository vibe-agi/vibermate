package capturecontrol

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

func TestCompanionAttestationBuildsRegisteredWorkspaceEvidence(t *testing.T) {
	t.Parallel()

	input := &CompanionAttestationInput{
		Detection: clientadapter.Detection{
			Status:          clientadapter.StatusGeneric,
			Recognition:     clientadapter.RecognitionUnknown,
			CatalogRevision: 1,
			CanonicalPath:   "/usr/local/bin/agent",
			ExecutableLabel: "agent",
		},
		Workspace: CompanionWorkspaceInput{
			MachineID:            "uRmbW_GvQ7LZ9poYHh0aC8W3vQoJ0lZB7iK2s6xQfEk",
			WorkspaceID:          "QfEkuRmbW_GvQ7LZ9poYHh0aC8W3vQoJ0lZB7iK2s6w",
			WorkspaceLabel:       "project",
			RegistrationRevision: 3,
			DerivationRevision:   1,
		},
	}
	attestation, err := input.domain()
	if err != nil {
		t.Fatal(err)
	}
	if attestation == nil ||
		attestation.Workspace.Evidence() != workspaceidentity.EvidenceRegisteredCompanion ||
		attestation.Workspace.MachineID().String() != input.Workspace.MachineID ||
		attestation.Workspace.WorkspaceID().String() != input.Workspace.WorkspaceID {
		t.Fatalf("attestation = %+v", attestation)
	}
}

func TestCompanionAttestationRejectsInvalidOpaqueIdentity(t *testing.T) {
	t.Parallel()

	input := &CompanionAttestationInput{Workspace: CompanionWorkspaceInput{
		MachineID:            "machine/escaped",
		WorkspaceID:          "QfEkuRmbW_GvQ7LZ9poYHh0aC8W3vQoJ0lZB7iK2s6w",
		WorkspaceLabel:       "project",
		RegistrationRevision: 1,
		DerivationRevision:   1,
	}}
	if _, err := input.domain(); err == nil {
		t.Fatal("invalid companion machine identity was accepted")
	}
}
