package desktopcontrol

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/workspaceroute"
)

type WorkspaceRouteBindingPage struct {
	Items []WorkspaceRouteBindingResponse `json:"items"`
}

type WorkspaceRouteBindingResponse struct {
	ID                          string                                `json:"id"`
	AccessID                    string                                `json:"accessId"`
	MachineID                   string                                `json:"machineId"`
	MachineShortID              string                                `json:"machineShortId"`
	MachineDisplayName          string                                `json:"machineDisplayName"`
	MachineRegistrationRevision uint64                                `json:"machineRegistrationRevision"`
	WorkspaceID                 string                                `json:"workspaceId"`
	WorkspaceLabel              string                                `json:"workspaceLabel"`
	WorkspaceEvidence           string                                `json:"workspaceEvidence"`
	ProfileID                   string                                `json:"profileId"`
	Revision                    workspaceroute.Revision               `json:"revision"`
	State                       workspaceroute.State                  `json:"state"`
	ActiveRunCount              int                                   `json:"activeRunCount"`
	ActiveRuns                  []WorkspaceRouteRunSummary            `json:"activeRuns"`
	PinnedRequestCount          int                                   `json:"pinnedRequestCount"`
	ApprovedProfiles            []WorkspaceRouteProfileOptionResponse `json:"approvedProfiles"`
	UpdatedAt                   time.Time                             `json:"updatedAt"`
}

type WorkspaceRouteRunSummary struct {
	RunID          string    `json:"runId"`
	ClientLabel    string    `json:"clientLabel"`
	LocalUserLabel string    `json:"localUserLabel,omitempty"`
	State          string    `json:"state"`
	StartedAt      time.Time `json:"startedAt"`
	LastActivityAt time.Time `json:"lastActivityAt"`
}

type WorkspaceRouteProfileOptionResponse struct {
	ProfileID         string                          `json:"profileId"`
	Label             string                          `json:"label"`
	ModelPresentation string                          `json:"modelPresentation"`
	AuthPresentation  workspaceroute.AuthPresentation `json:"authPresentation"`
	AuthLabel         string                          `json:"authLabel"`
	Available         bool                            `json:"available"`
}

type WorkspaceRouteBindingUpdate struct {
	ProfileID string `json:"profileId"`
}

func (handler *Handler) listWorkspaceRouteBindings(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if handler.workspaceRoutes == nil || handler.captureRuns == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonWorkspaceRouteUnavailable)
		return
	}
	limit, err := queryLimit(request, workspaceroute.DefaultPageLimit)
	if err != nil || request.URL.Query().Get("cursor") != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	var accessFilter access.AccessID
	if raw := request.URL.Query().Get("accessId"); raw != "" {
		accessFilter, err = access.NewAccessID(raw)
		if err != nil {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
	}
	views, err := handler.workspaceRoutes.ListBindings(
		request.Context(),
		workspaceroute.PageRequest{Limit: limit},
	)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonWorkspaceRouteUnavailable)
		return
	}
	runs, err := handler.captureRuns.ListRuns(
		request.Context(),
		capturerun.PageRequest{Limit: capturerun.MaxPageLimit},
	)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	items := make([]WorkspaceRouteBindingResponse, 0, len(views))
	for _, view := range views {
		if accessFilter.String() != "" && view.Record.AccessID != accessFilter {
			continue
		}
		items = append(items, workspaceRouteBindingResponseOf(view, runs.Items))
	}
	writeJSON(writer, http.StatusOK, WorkspaceRouteBindingPage{Items: items})
}

func (handler *Handler) getWorkspaceRouteBinding(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if handler.workspaceRoutes == nil || handler.captureRuns == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonWorkspaceRouteUnavailable)
		return
	}
	id, err := workspaceroute.ParseBindingID(request.PathValue("bindingId"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	view, err := handler.workspaceRoutes.GetBinding(request.Context(), id)
	if err != nil {
		spec := classifyWorkspaceRouteError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	runs, err := handler.captureRuns.ListRuns(
		request.Context(),
		capturerun.PageRequest{Limit: capturerun.MaxPageLimit},
	)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	writer.Header().Set("ETag", strconv.FormatUint(uint64(view.Record.Revision), 10))
	writeJSON(writer, http.StatusOK, workspaceRouteBindingResponseOf(view, runs.Items))
}

func (handler *Handler) updateWorkspaceRouteBinding(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if handler.workspaceRoutes == nil || handler.captureRuns == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonWorkspaceRouteUnavailable)
		return
	}
	expected, key, err := mutationHeaders(request)
	if err != nil || expected == 0 {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	body, err := readJSONBody(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, err := workspaceroute.ParseBindingID(request.PathValue("bindingId"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	var input WorkspaceRouteBindingUpdate
	if decodeStrictJSON(body, &input) != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	profileID, err := access.NewEndpointProfileID(input.ProfileID)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256(bytes.Join([][]byte{
		[]byte(request.Method),
		[]byte(request.URL.Path),
		[]byte(strconv.FormatUint(expected, 10)),
		body,
	}, []byte{0}))
	response, err := handler.idempotent.execute(
		request.Context(),
		key,
		fingerprint,
		func() cachedResponse {
			view, updateErr := handler.workspaceRoutes.UpdateBinding(
				request.Context(),
				id,
				workspaceroute.Revision(expected),
				profileID,
			)
			if updateErr != nil {
				return problemResponse(classifyWorkspaceRouteError(updateErr))
			}
			runs, listErr := handler.captureRuns.ListRuns(
				request.Context(),
				capturerun.PageRequest{Limit: capturerun.MaxPageLimit},
			)
			if listErr != nil {
				return problemResponse(problemSpec{
					status: http.StatusServiceUnavailable,
					reason: ReasonRuntimeUnavailable,
				})
			}
			return jsonResponse(
				http.StatusOK,
				workspaceRouteBindingResponseOf(view, runs.Items),
			)
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonRevisionConflict)
		return
	}
	writeCached(writer, response)
}

func workspaceRouteBindingResponseOf(
	view workspaceroute.View,
	runs []capturerun.View,
) WorkspaceRouteBindingResponse {
	activeRuns := make([]WorkspaceRouteRunSummary, 0)
	for _, run := range runs {
		if run.MachineID != view.Record.MachineID.String() ||
			run.WorkspaceID != view.Record.WorkspaceID.String() ||
			(run.State != capturerun.StateCreated && run.State != capturerun.StateAttached) {
			continue
		}
		state := "idle"
		if run.State == capturerun.StateAttached {
			state = "active"
		}
		lastActivity := run.UpdatedAt
		if lastActivity.IsZero() {
			lastActivity = run.CreatedAt
		}
		activeRuns = append(activeRuns, WorkspaceRouteRunSummary{
			RunID:          run.ID,
			ClientLabel:    run.ExecutableLabel,
			LocalUserLabel: run.LocalUserLabel,
			State:          state,
			StartedAt:      run.CreatedAt,
			LastActivityAt: lastActivity,
		})
	}
	profiles := make([]WorkspaceRouteProfileOptionResponse, len(view.Profiles))
	for index, profile := range view.Profiles {
		profiles[index] = WorkspaceRouteProfileOptionResponse{
			ProfileID:         profile.ProfileID.String(),
			Label:             profile.Label,
			ModelPresentation: profile.ModelPresentation,
			AuthPresentation:  profile.AuthPresentation,
			AuthLabel:         profile.AuthLabel,
			Available:         profile.Available,
		}
	}
	shortID := view.Record.MachineID.Short()
	return WorkspaceRouteBindingResponse{
		ID:                          view.Record.ID.String(),
		AccessID:                    view.Record.AccessID.String(),
		MachineID:                   view.Record.MachineID.String(),
		MachineShortID:              shortID,
		MachineDisplayName:          "Local machine " + shortID,
		MachineRegistrationRevision: view.Record.MachineRegistrationRevision,
		WorkspaceID:                 view.Record.WorkspaceID.String(),
		WorkspaceLabel:              view.Record.WorkspaceLabel,
		WorkspaceEvidence:           string(view.Record.WorkspaceEvidence),
		ProfileID:                   view.Record.ProfileID.String(),
		Revision:                    view.Record.Revision,
		State:                       view.State,
		ActiveRunCount:              len(activeRuns),
		ActiveRuns:                  activeRuns,
		PinnedRequestCount:          view.PinnedRequestCount,
		ApprovedProfiles:            profiles,
		UpdatedAt:                   view.Record.UpdatedAt,
	}
}

func classifyWorkspaceRouteError(err error) problemSpec {
	switch {
	case errors.Is(err, workspaceroute.ErrBindingNotFound):
		return problemSpec{status: http.StatusNotFound, reason: ReasonWorkspaceRouteNotFound}
	case errors.Is(err, workspaceroute.ErrRevisionConflict):
		return problemSpec{status: http.StatusConflict, reason: ReasonRevisionConflict}
	case errors.Is(err, workspaceroute.ErrRouteUnavailable):
		return problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonWorkspaceRouteUnavailable}
	default:
		return problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonInvalidRequest}
	}
}
