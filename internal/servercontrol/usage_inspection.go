package servercontrol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

func usageSnapshotChanged(err error) bool {
	return errors.Is(err, runtimeusage.ErrSnapshotChanged) || errors.Is(err, recording.ErrInspectionChanged)
}

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
		if usageSnapshotChanged(err) {
			writeProblem(w, http.StatusConflict, "usage_snapshot_changed")
		} else {
			writeProblem(w, http.StatusServiceUnavailable, "runtime_usage_unavailable")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(data, '\n'))
}
