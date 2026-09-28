package desktopcontrol

import (
	"net/http"
	"net/url"

	"github.com/vibe-agi/vibermate/internal/activity"
)

func (handler *Handler) summarizeActivities(writer http.ResponseWriter, request *http.Request) {
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	for key, entries := range values {
		if (key != "captureRunId" && key != "manualCaptureId" && key != "client" && key != "sessionId") || len(entries) != 1 || entries[0] == "" {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
	}
	scope := activity.SummaryScope{
		CaptureRunID: values.Get("captureRunId"), ManualCaptureID: values.Get("manualCaptureId"),
		Client: values.Get("client"), SessionID: values.Get("sessionId"),
	}
	if scope.Validate() != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	result, err := handler.activities.SummarizeExchanges(request.Context(), scope)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}
