package desktopcontrol

import (
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
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
	check, err := inspectionResponseBoundary(request.Context(), handler.activities)
	if err != nil {
		writeInspectionProblem(writer, err)
		return
	}
	result, err := handler.activities.SummarizeExchanges(request.Context(), scope)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	result.CaptureObservation = nil
	var observation *recording.CaptureObservationView
	if handler.captureObservations != nil && (scope.CaptureRunID != "" || scope.ManualCaptureID != "") {
		ref := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: scope.CaptureRunID}
		if scope.ManualCaptureID != "" {
			ref = captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: scope.ManualCaptureID}
		}
		value, err := handler.captureObservations.ReadCaptureObservation(request.Context(), ref)
		if err != nil {
			writeInspectionProblem(writer, err)
			return
		}
		result.CaptureObservation, err = captureSummaryObservation(ref, value)
		if err != nil {
			writeInspectionProblem(writer, err)
			return
		}
		observation = &value
	}
	writeInspectionJSON(writer, result, handler.activityExpiryCheck(func() error {
		if err := check(); err != nil {
			return err
		}
		if observation != nil {
			return handler.captureObservations.CheckCaptureObservation(request.Context(), *observation)
		}
		return nil
	}, result.InspectionExpiresAt))
}

func captureSummaryObservation(ref captureidentity.Reference, value recording.CaptureObservationView) (*activity.CaptureObservationSummary, error) {
	selection := value.Selection
	if selection.Scope.Capture != ref || !selection.Stamp.Valid() {
		return nil, recording.ErrInspectionUnavailable
	}
	if !selection.Visible {
		if value.FirstObservedAt != nil || value.LastObservedAt != nil {
			return nil, recording.ErrInspectionUnavailable
		}
		return nil, nil
	}
	if selection.Scope.Validate() != nil || selection.Scope.Fence.ArchiveEpoch != selection.Stamp.ArchiveEpoch {
		return nil, recording.ErrInspectionUnavailable
	}
	if ref.Kind == captureidentity.KindManagedRun && selection.CredentialRevision != 0 || ref.Kind == captureidentity.KindManualCapture && selection.CredentialRevision == 0 {
		return nil, recording.ErrInspectionUnavailable
	}
	if value.FirstObservedAt != nil && value.LastObservedAt != nil && value.FirstObservedAt.After(*value.LastObservedAt) {
		return nil, recording.ErrInspectionUnavailable
	}
	return &activity.CaptureObservationSummary{
		CaptureKind: ref.Kind, CaptureID: ref.ID,
		ArchiveEpoch: selection.Scope.Fence.ArchiveEpoch, CaptureGeneration: selection.Scope.Fence.CaptureGeneration,
		CredentialRevision: selection.CredentialRevision, FirstObservedAt: value.FirstObservedAt, LastObservedAt: value.LastObservedAt,
	}, nil
}
