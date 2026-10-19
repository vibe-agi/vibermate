package desktopcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// Capture every actual independent reader's boundary before constituent reads.
// Legacy-only injected ports retain their existing contract until composition.
func inspectionResponseBoundary(ctx context.Context, readers ...any) (func() error, error) {
	type check struct {
		reader recording.InspectionBoundary
		token  recording.InspectionToken
	}
	checks := make([]check, 0, len(readers))
	for _, value := range readers {
		if reader, ok := value.(recording.InspectionBoundary); ok {
			token, err := reader.Begin(ctx)
			if err != nil {
				return nil, err
			}
			if len(checks) > 0 && checks[0].token != token {
				return nil, recording.ErrInspectionChanged
			}
			checks = append(checks, check{reader, token})
		}
	}
	return func() error {
		for _, check := range checks {
			if err := check.reader.Check(ctx, check.token); err != nil {
				return err
			}
		}
		return nil
	}, nil
}
func writeInspectionProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, recording.ErrInspectionChanged):
		writeProblem(w, http.StatusConflict, ReasonInspectionChanged)
	case errors.Is(err, recording.ErrInspectionCursor), errors.Is(err, connectionevent.ErrInvalidEvent), errors.Is(err, connectionevent.ErrInvalidCursor), errors.Is(err, egressaudit.ErrInvalidCursor):
		writeProblem(w, http.StatusUnprocessableEntity, ReasonInvalidRequest)
	default:
		writeProblem(w, http.StatusServiceUnavailable, ReasonInspectionUnavailable)
	}
}
func writeInspectionJSON(w http.ResponseWriter, value any, check func() error) {
	data, err := json.Marshal(value)
	if err != nil {
		writeInspectionProblem(w, err)
		return
	}
	defer clear(data)
	if err = check(); err != nil {
		writeInspectionProblem(w, err)
		return
	}
	writeCached(w, cachedResponse{status: http.StatusOK, contentType: "application/json", body: data})
}
