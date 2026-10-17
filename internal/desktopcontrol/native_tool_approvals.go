package desktopcontrol

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type PendingPreviewReader interface {
	PendingPreview(context.Context, captureidentity.Reference, nativetool.ID, nativetool.Digest) (nativemcp.PendingPreview, error)
}
type NativeEnvironmentDTO struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
	Digest   string `json:"digest"`
}
type PendingPreviewDTO struct {
	Intents   []nativemcp.NativeIntentPreview `json:"intents"`
	Truncated bool                            `json:"truncated"`
}
type NativeApprovalDTO struct {
	ID              string               `json:"id"`
	OperationID     string               `json:"operationId"`
	CaptureKey      string               `json:"captureKey"`
	Environment     NativeEnvironmentDTO `json:"environment"`
	Revision        uint64               `json:"revision"`
	State           string               `json:"state"`
	ExpiresAt       time.Time            `json:"expiresAt"`
	BindingDigest   string               `json:"bindingDigest"`
	Preview         *PendingPreviewDTO   `json:"preview,omitempty"`
	PreviewState    string               `json:"previewState"`
	CredentialOwner string               `json:"credentialOwner"`
}
type NativeApprovalPageDTO struct {
	Items      []NativeApprovalDTO `json:"items"`
	NextCursor string              `json:"nextCursor"`
}
type NativeDecisionDTO struct {
	Decision      nativetool.Decision `json:"decision"`
	BindingDigest string              `json:"bindingDigest"`
}

func nativeProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, nativetool.ErrDenied):
		writeProblem(w, 409, ReasonRevisionConflict)
	case errors.Is(err, nativetool.ErrNotFound):
		writeProblem(w, 404, ReasonApprovalNotFound)
	case errors.Is(err, nativetool.ErrInvalid), errors.Is(err, recording.ErrInspectionCursor):
		writeProblem(w, 422, ReasonInvalidRequest)
	case errors.Is(err, nativetool.ErrConflict), errors.Is(err, nativetool.ErrReplay), errors.Is(err, recording.ErrInspectionChanged):
		writeProblem(w, 409, ReasonRevisionConflict)
	default:
		writeProblem(w, 503, ReasonRuntimeUnavailable)
	}
}
func (h *Handler) nativeCapture(w http.ResponseWriter, r *http.Request, write bool) (captureidentity.Reference, bool) {
	w.Header().Set("Cache-Control", "no-store")
	ref, err := h.authorizeNativeCapture(r.Context(), r.PathValue("captureKey"), write)
	if errors.Is(err, nativetool.ErrDenied) {
		writeProblem(w, 401, ReasonUnauthorized)
		return ref, false
	}
	if err == nil && h.nativeTools == nil && h.nativeAuthority == nil && h.nativeInspection == nil {
		err = nativetool.ErrUnavailable
	}
	if err != nil {
		nativeProblem(w, err)
		return ref, false
	}
	return ref, true
}
func (h *Handler) nativeApproval(ctx context.Context, ref captureidentity.Reference, id nativetool.ID) (nativetool.Approval, nativetool.Operation, error) {
	if h.nativeAuthority == nil {
		return nativetool.Approval{}, nativetool.Operation{}, nativetool.ErrUnavailable
	}
	a, err := h.nativeAuthority.GetApproval(ctx, id)
	if err != nil {
		return a, nativetool.Operation{}, err
	}
	op, err := h.nativeAuthority.Get(ctx, a.OperationID)
	if err == nil && (op.ID != a.OperationID || op.Capture != ref || op.ApprovalID == nil || *op.ApprovalID != id || op.BindingDigest == nil || *op.BindingDigest != a.BindingDigest) {
		err = nativetool.ErrNotFound
	}
	return a, op, err
}
func (h *Handler) nativeApprovalDTO(ctx context.Context, a nativetool.Approval, op nativetool.Operation) NativeApprovalDTO {
	d := NativeApprovalDTO{ID: a.ID.String(), OperationID: op.ID.String(), CaptureKey: op.Capture.Key(), Environment: NativeEnvironmentDTO{op.EnvironmentID.String(), uint64(op.EnvironmentRevision), op.EnvironmentDigest.String()}, Revision: a.Revision, State: a.State, ExpiresAt: a.ExpiresAt, BindingDigest: a.BindingDigest.String(), PreviewState: "unavailable", CredentialOwner: op.CredentialOwner}
	if !a.ExpiresAt.After(h.clock.Now().UTC()) {
		d.PreviewState = "expired"
		if d.State == "pending" {
			d.State = "expired"
		}
	}
	if h.nativePreviews != nil {
		p, err := h.nativePreviews.PendingPreview(ctx, op.Capture, op.ID, a.BindingDigest)
		if err == nil && a.State == "pending" && d.PreviewState != "expired" && p.OperationID == op.ID && p.BindingDigest == a.BindingDigest && p.Revision == a.Revision && p.ExpiresAt.Equal(a.ExpiresAt) && len(p.Intents) > 0 {
			d.Preview = &PendingPreviewDTO{Intents: p.Intents}
			for _, i := range p.Intents {
				d.Preview.Truncated = d.Preview.Truncated || i.Truncated
			}
			d.PreviewState = "available"
		}
	}
	return d
}
func (h *Handler) listNativeApprovals(w http.ResponseWriter, r *http.Request) {
	ref, ok := h.nativeCapture(w, r, false)
	if !ok {
		return
	}
	if h.nativeAuthority == nil {
		nativeProblem(w, nativetool.ErrUnavailable)
		return
	}
	query, err := parseNativeApprovalQuery(r.URL.RawQuery, ref)
	if err != nil {
		nativeProblem(w, err)
		return
	}
	page, err := h.nativeAuthority.ListPendingApprovals(r.Context(), query)
	if err != nil {
		nativeProblem(w, err)
		return
	}
	result := NativeApprovalPageDTO{Items: []NativeApprovalDTO{}}
	if page.NextID != "" {
		result.NextCursor, err = nativeApprovalCursor(ref, page.NextCreatedAt, page.NextID)
		if err != nil {
			nativeProblem(w, err)
			return
		}
	}
	for _, item := range page.Items {
		a, op := item.Approval, item.Operation
		if op.Capture != ref || op.ID != a.OperationID || op.ApprovalID == nil || *op.ApprovalID != a.ID || op.BindingDigest == nil || *op.BindingDigest != a.BindingDigest {
			nativeProblem(w, nativetool.ErrInvalid)
			return
		}
		if a.State == "pending" && a.ExpiresAt.After(h.clock.Now().UTC()) {
			result.Items = append(result.Items, h.nativeApprovalDTO(r.Context(), a, op))
		}
	}
	writeJSON(w, 200, result)
}
func (h *Handler) getNativeApproval(w http.ResponseWriter, r *http.Request) {
	ref, ok := h.nativeCapture(w, r, false)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		nativeProblem(w, nativetool.ErrInvalid)
		return
	}
	id, err := nativetool.NewID(r.PathValue("approvalId"))
	if err != nil {
		nativeProblem(w, nativetool.ErrInvalid)
		return
	}
	a, op, err := h.nativeApproval(r.Context(), ref, id)
	if err != nil {
		nativeProblem(w, err)
		return
	}
	writeJSON(w, 200, h.nativeApprovalDTO(r.Context(), a, op))
}
func (h *Handler) decideNativeApproval(w http.ResponseWriter, r *http.Request) {
	ref, ok := h.nativeCapture(w, r, true)
	if !ok {
		return
	}
	id, err := nativetool.NewID(r.PathValue("approvalId"))
	if err != nil {
		nativeProblem(w, nativetool.ErrInvalid)
		return
	}
	revision, key, err := mutationHeaders(r)
	if err != nil || revision == 0 || len(r.Header.Values("If-Match")) != 1 || len(r.Header.Values("Idempotency-Key")) != 1 || r.URL.RawQuery != "" {
		nativeProblem(w, nativetool.ErrInvalid)
		return
	}
	body, err := readJSONBody(r)
	if err != nil {
		nativeProblem(w, nativetool.ErrInvalid)
		return
	}
	var input NativeDecisionDTO
	// Exact field spelling and uniqueness: encoding/json alone folds case and
	// accepts duplicate keys, which must not create alternate decision shapes.
	dec := json.NewDecoder(bytes.NewReader(body))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		nativeProblem(w, nativetool.ErrInvalid)
		return
	}
	seen := map[string]bool{}
	for dec.More() {
		token, err = dec.Token()
		name, valid := token.(string)
		if err != nil || !valid || seen[name] || name != "decision" && name != "bindingDigest" {
			nativeProblem(w, nativetool.ErrInvalid)
			return
		}
		seen[name] = true
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			nativeProblem(w, nativetool.ErrInvalid)
			return
		}
	}
	if len(seen) != 2 || decodeStrictJSON(body, &input) != nil || input.Decision != nativetool.DecisionAllowOnce && input.Decision != nativetool.DecisionDeny {
		nativeProblem(w, nativetool.ErrInvalid)
		return
	}
	digest, err := hex.DecodeString(input.BindingDigest)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != input.BindingDigest {
		nativeProblem(w, nativetool.ErrInvalid)
		return
	}
	a, op, err := h.nativeApproval(r.Context(), ref, id)
	if err != nil {
		nativeProblem(w, err)
		return
	}
	if input.BindingDigest != a.BindingDigest.String() {
		nativeProblem(w, nativetool.ErrConflict)
		return
	}
	if input.Decision == nativetool.DecisionAllowOnce && a.State == "pending" && h.nativeApprovalDTO(r.Context(), a, op).Preview == nil {
		nativeProblem(w, nativetool.ErrConflict)
		return
	}
	a, err = h.nativeAuthority.DecideApproval(r.Context(), nativetool.DecisionInput{ApprovalID: id, ExpectedRevision: revision, Decision: input.Decision, IdempotencyKey: key, ObservedAt: h.clock.Now().UTC()})
	if err != nil {
		nativeProblem(w, err)
		return
	}
	writeJSON(w, 200, h.nativeApprovalDTO(r.Context(), a, op))
}
