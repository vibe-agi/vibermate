package capturecontrol

import (
	"errors"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/capturerun"
)

type StartACPRequest struct {
	RecordContent bool `json:"recordContent"`
}

func (handler *Handler) startACP(writer http.ResponseWriter, request *http.Request) {
	capability, ok := consumeRunCapability(request)
	if !ok {
		writeProblem(writer, http.StatusForbidden, ReasonRunCapabilityRejected)
		return
	}
	var input StartACPRequest
	if decodeJSON(request, &input, 4096) != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_acp_observation")
		return
	}
	record, err := handler.acp.Start(request.Context(), request.PathValue("runId"), capability, input.RecordContent)
	if err != nil {
		writeACPFailure(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, record)
}

func (handler *Handler) observeACP(writer http.ResponseWriter, request *http.Request) {
	capability, ok := consumeRunCapability(request)
	if !ok {
		writeProblem(writer, http.StatusForbidden, ReasonRunCapabilityRejected)
		return
	}
	var input acpobservation.Snapshot
	if decodeJSON(request, &input, acpobservation.MaxSnapshotBytes) != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_acp_observation")
		return
	}
	if err := handler.acp.Publish(request.Context(), request.PathValue("runId"), capability, input); err != nil {
		writeACPFailure(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func writeACPFailure(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, capturerun.ErrCapabilityRejected):
		writeProblem(writer, http.StatusForbidden, ReasonRunCapabilityRejected)
	case errors.Is(err, acpobservation.ErrConflict):
		writeProblem(writer, http.StatusConflict, "acp_observation_conflict")
	case errors.Is(err, acpobservation.ErrInvalid):
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_acp_observation")
	case errors.Is(err, acpobservation.ErrNotFound):
		writeProblem(writer, http.StatusNotFound, "acp_observation_not_found")
	default:
		writeProblem(writer, http.StatusServiceUnavailable, "acp_observation_unavailable")
	}
}
