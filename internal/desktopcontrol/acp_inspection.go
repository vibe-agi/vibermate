package desktopcontrol

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
)

func (h *Handler) readACPInspection(w http.ResponseWriter, request *http.Request, reader acpobservation.InspectionReader) {
	ref, err := captureidentity.ParseKey(request.PathValue("captureKey"))
	if err != nil || ref.Kind != captureidentity.KindManagedRun {
		writeProblem(w, http.StatusNotFound, ReasonCaptureNotFound)
		return
	}
	token, err := reader.Begin(request.Context())
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "acp_observation_unavailable")
		return
	}
	record, err := reader.Read(request.Context(), ref.ID, h.clock.Now().UnixMilli())
	if errors.Is(err, acpobservation.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, "acp_observation_not_found")
		return
	}
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "acp_observation_unavailable")
		return
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "acp_observation_unavailable")
		return
	}
	defer clear(encoded)
	if err = reader.Check(request.Context(), token); err != nil || (!record.Expired && h.clock.Now().UnixMilli() >= record.ExpiresAtMillis) {
		writeProblem(w, http.StatusServiceUnavailable, "acp_observation_unavailable")
		return
	}
	writeCached(w, cachedResponse{status: http.StatusOK, contentType: "application/json", body: encoded})
}
