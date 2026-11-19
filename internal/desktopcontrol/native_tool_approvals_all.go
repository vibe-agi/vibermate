package desktopcontrol

import (
	"errors"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// NativeAllApprovalsPath lists the tool questions waiting in every session
// the caller manages. Each is answered through its own session's decide path.
const NativeAllApprovalsPath = "/api/v1/native-tools/approvals"

// NativeSessionApprovalDTO is one waiting question and the name its session
// shows under in the session list. Approval is exactly what the session's own
// list returns for it.
type NativeSessionApprovalDTO struct {
	CaptureDisplayName string            `json:"captureDisplayName"`
	Approval           NativeApprovalDTO `json:"approval"`
}
type NativeSessionApprovalPageDTO struct {
	Items      []NativeSessionApprovalDTO `json:"items"`
	NextCursor string                     `json:"nextCursor"`
}

// listAllNativeApprovals pages waiting questions oldest first, so the agent
// blocked longest, and closest to its deadline, is the first one shown. A
// question is listed only when its session's own list would show it to this
// caller; a Capture of another owner is skipped, not named.
func (h *Handler) listAllNativeApprovals(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	authority, err := nativeManagement(r.Context(), false)
	if err != nil {
		writeProblem(w, http.StatusUnauthorized, ReasonUnauthorized)
		return
	}
	if h.nativeAuthority == nil {
		nativeProblem(w, nativetool.ErrUnavailable)
		return
	}
	now := h.clock.Now().UTC()
	query, err := parseAllNativeApprovalQuery(r.URL.RawQuery, now)
	if err != nil {
		nativeProblem(w, err)
		return
	}
	page, err := h.nativeAuthority.ListAllPendingApprovals(r.Context(), query)
	if err != nil {
		nativeProblem(w, err)
		return
	}
	result := NativeSessionApprovalPageDTO{Items: []NativeSessionApprovalDTO{}}
	if page.NextID != "" {
		result.NextCursor, err = encodeNativeApprovalCursor(nativeAllCapturesCursorDomain, page.NextCreatedAt, page.NextID)
		if err != nil {
			nativeProblem(w, err)
			return
		}
	}
	// One visibility check per Capture on the page, not per question.
	names := map[captureidentity.Reference]string{}
	hidden := map[captureidentity.Reference]bool{}
	for _, item := range page.Items {
		a, op := item.Approval, item.Operation
		if op.Capture.Validate() != nil || op.ID != a.OperationID || op.ApprovalID == nil || *op.ApprovalID != a.ID || op.BindingDigest == nil || *op.BindingDigest != a.BindingDigest {
			nativeProblem(w, nativetool.ErrInvalid)
			return
		}
		if a.State != "pending" || !a.ExpiresAt.After(now) || hidden[op.Capture] {
			continue
		}
		name, known := names[op.Capture]
		if !known {
			name, err = h.nativeCaptureName(r.Context(), authority, op.Capture)
			if errors.Is(err, errNativeCaptureHidden) {
				hidden[op.Capture] = true
				continue
			}
			// A failed lookup must not quietly drop a blocked agent's question.
			if errors.Is(err, errNativeCaptureLookup) {
				err = nativetool.ErrUnavailable
			}
			if err != nil {
				nativeProblem(w, err)
				return
			}
			names[op.Capture] = name
		}
		result.Items = append(result.Items, NativeSessionApprovalDTO{CaptureDisplayName: name, Approval: h.nativeApprovalDTO(r.Context(), a, op)})
	}
	writeJSON(w, http.StatusOK, result)
}
