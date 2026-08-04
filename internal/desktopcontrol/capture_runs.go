package desktopcontrol

import (
	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

// CaptureRunAuditView is the Desktop-only, uncontracted read projection. It
// deliberately extends the contracted launcher view with lifecycle and real
// traffic-observation facts needed by the local dashboard. Recognition is
// retained as a compatibility alias for existing Desktop clients.
type CaptureRunAuditView struct {
	capturecontrol.CaptureRunView
	State             capturerun.State          `json:"state"`
	Observation       capturerun.Observation    `json:"observation"`
	Recognition       clientadapter.Recognition `json:"recognition"`
	LocalUserLabel    string                    `json:"localUserLabel,omitempty"`
	MachineID         string                    `json:"machineId,omitempty"`
	WorkspaceID       string                    `json:"workspaceId,omitempty"`
	WorkspaceLabel    string                    `json:"workspaceLabel,omitempty"`
	WorkspaceEvidence string                    `json:"workspaceEvidence,omitempty"`
}

type CaptureRunAuditPage struct {
	Items []CaptureRunAuditView `json:"items"`
}

// CaptureRunAuditPageOf is also used by the checked-in sample generator so
// preview data cannot drift from the production Desktop projection.
func CaptureRunAuditPageOf(page capturerun.Page) CaptureRunAuditPage {
	items := make([]CaptureRunAuditView, len(page.Items))
	for index, view := range page.Items {
		items[index] = CaptureRunAuditView{
			CaptureRunView:    capturecontrol.CaptureRunViewOf(view),
			State:             view.State,
			Observation:       view.Observation,
			Recognition:       capturerun.NormalizedRecognition(view.Recognition),
			LocalUserLabel:    view.LocalUserLabel,
			MachineID:         view.MachineID,
			WorkspaceID:       view.WorkspaceID,
			WorkspaceLabel:    view.WorkspaceLabel,
			WorkspaceEvidence: string(view.WorkspaceEvidence),
		}
	}
	return CaptureRunAuditPage{Items: items}
}
