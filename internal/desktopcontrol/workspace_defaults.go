package desktopcontrol

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/workspacedefault"
)

type WorkspaceDefaultResponse struct {
	MachineID       string                    `json:"machineId"`
	WorkspaceID     string                    `json:"workspaceId"`
	EnvironmentID   environment.EnvironmentID `json:"environmentId"`
	EnvironmentName string                    `json:"environmentName"`
	Revision        uint64                    `json:"revision"`
	UpdatedAt       time.Time                 `json:"updatedAt"`
}

type WorkspaceDefaultInput struct {
	EnvironmentID environment.EnvironmentID `json:"environmentId"`
}

func (handler *Handler) getWorkspaceDefault(writer http.ResponseWriter, request *http.Request) {
	if handler.workspaceDefaults == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	key, err := workspaceDefaultKey(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonWorkspaceDefaultInvalid)
		return
	}
	record, exists, err := handler.workspaceDefaults.Get(request.Context(), key)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	if !exists {
		writeProblem(writer, http.StatusNotFound, ReasonWorkspaceDefaultNotFound)
		return
	}
	response, err := handler.workspaceDefaultResponse(request, record)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonEnvironmentUnavailable)
		return
	}
	writer.Header().Set("ETag", strconv.Quote("revision-"+strconv.FormatUint(record.Revision, 10)))
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) putWorkspaceDefault(writer http.ResponseWriter, request *http.Request) {
	if handler.workspaceDefaults == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	key, err := workspaceDefaultKey(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonWorkspaceDefaultInvalid)
		return
	}
	expected, idempotencyKey, err := mutationHeaders(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	var input WorkspaceDefaultInput
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxControlBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || !emptyBody(request.Body) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonWorkspaceDefaultInvalid)
		return
	}
	if _, err := environment.NewEnvironmentID(input.EnvironmentID.String()); err != nil ||
		input.EnvironmentID == environment.SystemTransparentID {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonWorkspaceDefaultInvalid)
		return
	}
	fingerprint := sha256.Sum256(bytes.Join([][]byte{
		[]byte(request.Method), []byte(request.URL.Path),
		[]byte(strconv.FormatUint(expected, 10)), []byte(input.EnvironmentID),
	}, []byte{0}))
	response, err := handler.idempotent.execute(
		request.Context(), idempotencyKey, fingerprint,
		func() cachedResponse {
			record, setErr := handler.workspaceDefaults.Set(request.Context(), workspacedefault.SetCommand{
				Key: key, ExpectedRevision: expected, EnvironmentID: input.EnvironmentID,
			})
			if setErr != nil {
				return problemResponse(classifyWorkspaceDefaultError(setErr))
			}
			view, viewErr := handler.workspaceDefaultResponse(request, record)
			if viewErr != nil {
				return problemResponse(problemSpec{status: http.StatusServiceUnavailable, reason: ReasonEnvironmentUnavailable})
			}
			return jsonResponse(http.StatusOK, view)
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonRevisionConflict)
		return
	}
	writeCached(writer, response)
}

func (handler *Handler) deleteWorkspaceDefault(writer http.ResponseWriter, request *http.Request) {
	if handler.workspaceDefaults == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	key, err := workspaceDefaultKey(request)
	if err != nil || !emptyBody(request.Body) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonWorkspaceDefaultInvalid)
		return
	}
	expected, idempotencyKey, err := mutationHeaders(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256(bytes.Join([][]byte{
		[]byte(request.Method), []byte(request.URL.Path), []byte(strconv.FormatUint(expected, 10)),
	}, []byte{0}))
	response, err := handler.idempotent.execute(
		request.Context(), idempotencyKey, fingerprint,
		func() cachedResponse {
			clearErr := handler.workspaceDefaults.Clear(request.Context(), workspacedefault.ClearCommand{
				Key: key, ExpectedRevision: expected,
			})
			if clearErr != nil {
				return problemResponse(classifyWorkspaceDefaultError(clearErr))
			}
			return cachedResponse{status: http.StatusNoContent, body: nil}
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonRevisionConflict)
		return
	}
	writeCached(writer, response)
}

func workspaceDefaultKey(request *http.Request) (workspacedefault.Key, error) {
	return workspacedefault.NewKey(request.PathValue("machineId"), request.PathValue("workspaceId"))
}

func (handler *Handler) workspaceDefaultResponse(
	request *http.Request,
	record workspacedefault.Record,
) (WorkspaceDefaultResponse, error) {
	snapshot, err := handler.environments.Get(request.Context(), record.EnvironmentID)
	if err != nil {
		return WorkspaceDefaultResponse{}, err
	}
	return WorkspaceDefaultResponse{
		MachineID: record.Key.MachineID.String(), WorkspaceID: record.Key.WorkspaceID.String(),
		EnvironmentID: record.EnvironmentID, EnvironmentName: snapshot.Aggregate().Name,
		Revision: record.Revision, UpdatedAt: record.UpdatedAt,
	}, nil
}

func classifyWorkspaceDefaultError(err error) problemSpec {
	switch {
	case errors.Is(err, workspacedefault.ErrDefaultNotFound):
		return problemSpec{status: http.StatusNotFound, reason: ReasonWorkspaceDefaultNotFound}
	case errors.Is(err, workspacedefault.ErrRevisionConflict):
		return problemSpec{status: http.StatusConflict, reason: ReasonRevisionConflict}
	case errors.Is(err, workspacedefault.ErrInvalidDefault),
		errors.Is(err, workspacedefault.ErrEnvironmentNotActive):
		return problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonWorkspaceDefaultInvalid}
	default:
		return problemSpec{status: http.StatusServiceUnavailable, reason: ReasonRuntimeUnavailable}
	}
}
