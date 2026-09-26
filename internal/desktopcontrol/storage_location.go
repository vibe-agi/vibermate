package desktopcontrol

import (
	"net/http"

	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

func (handler *Handler) getStorageLocation(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	if handler.storage == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	location, err := handler.storage.StorageLocation(request.Context())
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	writeJSON(writer, http.StatusOK, location)
}

func (handler *Handler) cleanupExpiredStorage(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if handler.storage == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	expected, key, headerErr := mutationHeaders(request)
	if headerErr != nil || !emptyBody(request.Body) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	handler.respondToDeletion(writer, request, key, expected, func() cachedResponse {
		released, err := handler.storage.CleanupExpiredStorage(request.Context())
		if err != nil {
			return problemResponse(problemSpec{
				status: http.StatusServiceUnavailable, reason: ReasonRuntimeUnavailable,
			})
		}
		return jsonResponse(http.StatusOK, deletionResponseOf(
			resourcedeletion.Completed(), releasedResponseOf(released),
		))
	})
}

func (handler *Handler) previewArchiveClear(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	if handler.storage == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	released, err := handler.storage.EvidenceArchivePreview(request.Context())
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	writeJSON(writer, http.StatusOK, releasedResponseOf(released))
}
