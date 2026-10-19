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
	State      string     `json:"state"`
	Digest     string     `json:"digest,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	ObservedAt *time.Time `json:"observedAt"`
}
type NativeOperationDTO struct {
	InspectionVersion string                 `json:"inspectionVersion"`
	OperationID       string                 `json:"operationId"`
	CaptureKey        string                 `json:"captureKey"`
	Environment       NativeEnvironmentDTO   `json:"environment"`
	Revision          uint64                 `json:"revision"`
	Kind              nativetool.RequestKind `json:"kind"`
	ToolName          string                 `json:"toolName"`
	State             string                 `json:"state"`
	Reason            string                 `json:"reason"`
	DecisionSource    string                 `json:"decisionSource"`
	TargetOrigin      string                 `json:"targetOrigin"`
	CredentialOwner   string                 `json:"credentialOwner"`
	CreatedAt         time.Time              `json:"createdAt"`
	CompletedAt       *time.Time             `json:"completedAt"`
	BytesIn           *int64                 `json:"bytesIn"`
	BytesOut          *int64                 `json:"bytesOut"`
	Content           NativeOperationContent `json:"content"`
}
type NativeOperationContent struct {
	Mode      environment.ContentRecordingMode `json:"mode"`
	Arguments NativePartDescriptor             `json:"arguments"`
	Result    NativePartDescriptor             `json:"result"`
}
type NativeOperationPageDTO struct {
	Items             []NativeOperationDTO `json:"items"`
	NextCursor        string               `json:"nextCursor"`
	InspectionVersion string               `json:"inspectionVersion"`
}

func (h *Handler) nativePart(value nativetool.InspectionOperation, part string) NativePartDescriptor {
	op := value.Operation
	switch op.Recording.Mode {
	case environment.ContentRecordingOff:
		return NativePartDescriptor{State: "off"}
	case environment.ContentRecordingMetadataOnly:
		return NativePartDescriptor{State: "metadata_only"}
	}
	descriptor := value.Arguments
	if part == "tool_result" {
		descriptor = value.Result
	}
	if descriptor == nil {
		return NativePartDescriptor{State: "unavailable"}
	}
	state := "available"
	if !descriptor.ExpiresAt.After(h.clock.Now().UTC()) {
		state = "expired"
	}
	expires := descriptor.ExpiresAt
	return NativePartDescriptor{State: state, Digest: descriptor.Digest.String(), ExpiresAt: &expires, ObservedAt: descriptor.ObservedAt}
}
func (h *Handler) nativeOperationDTO(value nativetool.InspectionOperation) NativeOperationDTO {
	op := value.Operation
	return NativeOperationDTO{InspectionVersion: nativetool.InspectionVersion(value.Token), OperationID: op.ID.String(), CaptureKey: op.Capture.Key(), Environment: NativeEnvironmentDTO{op.EnvironmentID.String(), uint64(op.EnvironmentRevision), op.EnvironmentDigest.String()}, Revision: op.Revision, Kind: op.Kind, ToolName: op.ToolName, State: op.State, Reason: op.Reason, DecisionSource: op.DecisionSource, TargetOrigin: op.TargetOrigin, CredentialOwner: op.CredentialOwner, CreatedAt: op.CreatedAt, CompletedAt: op.CompletedAt, BytesIn: op.BytesIn, BytesOut: op.BytesOut, Content: NativeOperationContent{Mode: op.Recording.Mode, Arguments: h.nativePart(value, "tool_arguments"), Result: h.nativePart(value, "tool_result")}}
}
func nativePageQuery(raw string) (string, int, error) {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return "", 0, nativetool.ErrInvalid
	}
	for key, values := range query {
		if len(values) != 1 || key != "cursor" && key != "limit" {
			return "", 0, nativetool.ErrInvalid
		}
	}
	cursor, limit := "", 50
	if values, ok := query["cursor"]; ok {
		cursor = values[0]
		if len(cursor) == 0 || len(cursor) > 512 {
			return "", 0, nativetool.ErrInvalid
		}
	}
	if values, ok := query["limit"]; ok {
		limit, err = strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > 199 {
			return "", 0, nativetool.ErrInvalid
		}
	}
	return cursor, limit, nil
}

func (h *Handler) nativeOperation(ctx context.Context, capture captureidentity.Reference, raw string) (nativetool.InspectionOperation, error) {
	id, err := nativetool.NewID(raw)
	if err != nil {
		return nativetool.InspectionOperation{}, nativetool.ErrInvalid
	}
	if h.nativeInspection == nil {
		return nativetool.InspectionOperation{}, nativetool.ErrUnavailable
	}
	op, err := h.nativeInspection.Get(ctx, capture, id)
	if err == nil && (op.Operation.ID != id || op.Operation.Capture != capture) {
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
	if h.nativeInspection == nil {
		nativeProblem(w, nativetool.ErrUnavailable)
		return
	}
	page, err := h.nativeInspection.Page(r.Context(), nativetool.InspectionPageRequest{Capture: capture, Cursor: after, Limit: limit})
	if err != nil {
		nativeProblem(w, err)
		return
	}
	result := NativeOperationPageDTO{Items: []NativeOperationDTO{}, NextCursor: page.NextCursor, InspectionVersion: nativetool.InspectionVersion(page.Token)}
	for _, op := range page.Items {
		if op.Operation.Capture != capture || op.Token != page.Token {
			nativeProblem(w, nativetool.ErrNotFound)
			return
		}
		result.Items = append(result.Items, h.nativeOperationDTO(op))
	}
	if err = h.nativeInspection.Check(r.Context(), page.Token); err != nil {
		nativeProblem(w, err)
		return
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
	result := h.nativeOperationDTO(op)
	if err = h.nativeInspection.Check(r.Context(), op.Token); err != nil {
		nativeProblem(w, err)
		return
	}
	writeJSON(w, 200, result)
}
