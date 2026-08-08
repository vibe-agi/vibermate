package desktopcontrol

import (
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

// CaptureRunAuditView is the Desktop-only read projection. It extends the
// launcher view with lifecycle and diagnostic evidence; none of those fields
// become launch authority when read back.
type CaptureRunAuditView struct {
	capturecontrol.CaptureRunView
	CanonicalExecutablePath string                    `json:"canonicalExecutablePath"`
	State                   capturerun.State          `json:"state"`
	Observation             capturerun.Observation    `json:"observation"`
	Recognition             clientadapter.Recognition `json:"recognition"`
	LocalUserLabel          string                    `json:"localUserLabel,omitempty"`
	MachineID               string                    `json:"machineId,omitempty"`
	WorkspaceID             string                    `json:"workspaceId,omitempty"`
	WorkspaceLabel          string                    `json:"workspaceLabel,omitempty"`
	WorkspaceEvidence       string                    `json:"workspaceEvidence,omitempty"`
	FirstObservedAt         *time.Time                `json:"firstObservedAt,omitempty"`
	UpdatedAt               time.Time                 `json:"updatedAt"`
}

type CaptureRunAuditPage struct {
	Items []CaptureRunAuditView `json:"items"`
}

// CaptureRunAuditPageOf is also used by the checked-in sample generator so
// preview data cannot drift from the production Desktop projection.
func CaptureRunAuditPageOf(page capturerun.Page) CaptureRunAuditPage {
	items := make([]CaptureRunAuditView, len(page.Items))
	for index, view := range page.Items {
		items[index] = CaptureRunAuditViewOf(view)
	}
	return CaptureRunAuditPage{Items: items}
}

func CaptureRunAuditViewOf(view capturerun.View) CaptureRunAuditView {
	var firstObservedAt *time.Time
	if !view.FirstObservedAt.IsZero() {
		observedAt := view.FirstObservedAt
		firstObservedAt = &observedAt
	}
	return CaptureRunAuditView{
		CaptureRunView:          capturecontrol.CaptureRunViewOf(view),
		CanonicalExecutablePath: view.CanonicalExecutablePath,
		State:                   view.State,
		Observation:             view.Observation,
		Recognition:             capturerun.NormalizedRecognition(view.Recognition),
		LocalUserLabel:          view.LocalUserLabel,
		MachineID:               view.MachineID,
		WorkspaceID:             view.WorkspaceID,
		WorkspaceLabel:          view.WorkspaceLabel,
		WorkspaceEvidence:       string(view.WorkspaceEvidence),
		FirstObservedAt:         firstObservedAt,
		UpdatedAt:               view.UpdatedAt,
	}
}
