package desktopcontrol

import "net/http"

func (handler *Handler) getStorageLocation(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	if handler.storage == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	writeJSON(writer, http.StatusOK, handler.storage.StorageLocation())
}
