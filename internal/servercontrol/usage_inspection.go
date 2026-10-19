package servercontrol

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

// The separate-role Projector carries its original read token through cache and
// pricing. Encoding finishes before the final visibility/source recheck.
func writeUsageReport(w http.ResponseWriter, request *http.Request, report runtimeusage.Report, reader any) {
	data, err := json.Marshal(report)
	if err == nil {
		if boundary, ok := reader.(interface {
			CheckReport(context.Context, runtimeusage.Report) error
		}); ok {
			err = boundary.CheckReport(request.Context(), report)
		}
	}
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "runtime_usage_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(data, '\n'))
}
