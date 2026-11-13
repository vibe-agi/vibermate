package desktopcontrol

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
)

// scriptActivityReader is implemented by the inspection reader. A Runtime
// whose reader cannot answer reports the counts as unavailable; the screen
// treats them as optional.
type scriptActivityReader interface {
	SummarizeScripts(context.Context, activity.ScriptActivityQuery) (activity.ScriptActivity, error)
}

// summarizeScriptActivity answers GET /api/v1/scripts/activity?days=N with
// what each message script did to recent requests. Counts only, never
// message content.
func (handler *Handler) summarizeScriptActivity(writer http.ResponseWriter, request *http.Request) {
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	days := 7
	for key, entries := range values {
		if key != "days" || len(entries) != 1 {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
		days, err = strconv.Atoi(entries[0])
		if err != nil || days < 1 || days > activity.MaximumScriptActivityDays {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
	}
	reader, ok := handler.activities.(scriptActivityReader)
	if !ok {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonInspectionUnavailable)
		return
	}
	check, err := inspectionResponseBoundary(request.Context(), handler.activities)
	if err != nil {
		writeInspectionProblem(writer, inspectionPhase("begin", err))
		return
	}
	result, err := reader.SummarizeScripts(request.Context(), activity.ScriptActivityQuery{
		Since: time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour),
	})
	if err != nil {
		writeInspectionProblem(writer, inspectionPhase("aggregate", err))
		return
	}
	writeInspectionJSON(writer, result, func() error { return inspectionPhase("final_visibility", check()) })
}
