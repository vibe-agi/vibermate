package desktopcontrol

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type NativePartDescriptor struct {
	State     string     `json:"state"`
	Digest    string     `json:"digest,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}
type NativeOperationDTO struct {
	OperationID     string                 `json:"operationId"`
	CaptureKey      string                 `json:"captureKey"`
	Environment     NativeEnvironmentDTO   `json:"environment"`
	Revision        uint64                 `json:"revision"`
	Kind            nativetool.RequestKind `json:"kind"`
	ToolName        string                 `json:"toolName"`
	State           string                 `json:"state"`
	Reason          string                 `json:"reason"`
	DecisionSource  string                 `json:"decisionSource"`
	TargetOrigin    string                 `json:"targetOrigin"`
	CredentialOwner string                 `json:"credentialOwner"`
	CreatedAt       time.Time              `json:"createdAt"`
	CompletedAt     *time.Time             `json:"completedAt"`
	BytesIn         *int64                 `json:"bytesIn"`
	BytesOut        *int64                 `json:"bytesOut"`
	Content         NativeOperationContent `json:"content"`
}
type NativeOperationContent struct {
	Mode      environment.ContentRecordingMode `json:"mode"`
	Arguments NativePartDescriptor             `json:"arguments"`
	Result    NativePartDescriptor             `json:"result"`
}
type NativeOperationPageDTO struct {
	Items        []NativeOperationDTO `json:"items"`
	NextSequence int64                `json:"nextSequence"`
}

func (h *Handler) nativePart(op nativetool.Operation, part string) NativePartDescriptor {
	switch op.Recording.Mode {
	case environment.ContentRecordingOff:
		return NativePartDescriptor{State: "off"}
	case environment.ContentRecordingMetadataOnly:
		return NativePartDescriptor{State: "metadata_only"}
	}
	descriptor := op.Arguments
	if part == "tool_result" {
		descriptor = op.Result
	}
	if descriptor == nil {
		return NativePartDescriptor{State: "unavailable"}
	}
	state := "available"
	if !descriptor.ExpiresAt.After(h.clock.Now().UTC()) {
		state = "expired"
	}
	expires := descriptor.ExpiresAt
	return NativePartDescriptor{State: state, Digest: descriptor.Digest.String(), ExpiresAt: &expires}
}
func (h *Handler) nativeOperationDTO(op nativetool.Operation) NativeOperationDTO {
	return NativeOperationDTO{OperationID: op.ID.String(), CaptureKey: op.Capture.Key(), Environment: NativeEnvironmentDTO{op.EnvironmentID.String(), uint64(op.EnvironmentRevision), op.EnvironmentDigest.String()}, Revision: op.Revision, Kind: op.Kind, ToolName: op.ToolName, State: op.State, Reason: op.Reason, DecisionSource: op.DecisionSource, TargetOrigin: op.TargetOrigin, CredentialOwner: op.CredentialOwner, CreatedAt: op.CreatedAt, CompletedAt: op.CompletedAt, BytesIn: op.BytesIn, BytesOut: op.BytesOut, Content: NativeOperationContent{Mode: op.Recording.Mode, Arguments: h.nativePart(op, "tool_arguments"), Result: h.nativePart(op, "tool_result")}}
}
func nativePageQuery(raw string) (int64, int, error) {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return 0, 0, nativetool.ErrInvalid
	}
	for key, values := range query {
		if len(values) != 1 || key != "afterSequence" && key != "limit" {
			return 0, 0, nativetool.ErrInvalid
		}
	}
	after, limit := int64(0), 50
	if raw, ok := query["afterSequence"]; ok {
		after, err = strconv.ParseInt(raw[0], 10, 64)
		if err != nil || after < 0 {
			return 0, 0, nativetool.ErrInvalid
		}
	}
	if raw, ok := query["limit"]; ok {
		limit, err = strconv.Atoi(raw[0])
		if err != nil || limit < 1 || limit > 199 {
			return 0, 0, nativetool.ErrInvalid
		}
	}
	return after, limit, nil
}
func (h *Handler) nativeOperation(ctx context.Context, capture captureidentity.Reference, raw string) (nativetool.Operation, error) {
	id, err := nativetool.NewID(raw)
	if err != nil {
		return nativetool.Operation{}, nativetool.ErrInvalid
	}
	op, err := h.nativeTools.Get(ctx, id)
	if err == nil && (op.ID != id || op.Capture != capture) {
		err = nativetool.ErrNotFound
	}
	return op, err
}
func (h *Handler) listNativeOperations(w http.ResponseWriter, r *http.Request) {
	capture, ok := h.nativeCapture(w, r, false)
	if !ok {
		return
	}
	after, limit, err := nativePageQuery(r.URL.RawQuery)
	if err != nil {
		nativeProblem(w, err)
		return
	}
	page, err := h.nativeTools.Page(r.Context(), nativetool.PageRequest{Capture: capture, AfterSequence: after, Limit: limit})
	if err != nil {
		nativeProblem(w, err)
		return
	}
	result := NativeOperationPageDTO{Items: []NativeOperationDTO{}, NextSequence: page.NextSequence}
	if len(page.Items) == 0 && page.NextSequence == 0 {
		result.NextSequence = after
	}
	seen := map[nativetool.ID]bool{}
	for _, op := range page.Items {
		if op.Capture != capture {
			nativeProblem(w, nativetool.ErrNotFound)
			return
		}
		if !seen[op.ID] {
			seen[op.ID] = true
			result.Items = append(result.Items, h.nativeOperationDTO(op))
		}
	}
	writeJSON(w, 200, result)
}
func (h *Handler) getNativeOperation(w http.ResponseWriter, r *http.Request) {
	capture, ok := h.nativeCapture(w, r, false)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		nativeProblem(w, nativetool.ErrInvalid)
		return
	}
	op, err := h.nativeOperation(r.Context(), capture, r.PathValue("operationId"))
	if err != nil {
		nativeProblem(w, err)
		return
	}
	writeJSON(w, 200, h.nativeOperationDTO(op))
}
