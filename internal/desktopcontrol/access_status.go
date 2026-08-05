package desktopcontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strconv"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/activity"
)

type AccessStatusUpdate struct {
	Status access.AccessStatus `json:"status"`
}

func (handler *Handler) updateAccess(
	writer http.ResponseWriter,
	request *http.Request,
) {
	expectedValue, key, err := mutationHeaders(request)
	if err != nil || expectedValue == 0 {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	body, err := readJSONBody(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	var input AccessStatusUpdate
	if decodeStrictJSON(body, &input) != nil ||
		(input.Status != access.AccessStatusEnabled &&
			input.Status != access.AccessStatusDisabled) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	accessID, err := access.NewAccessID(request.PathValue("accessId"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	expected := access.Revision(expectedValue)
	fingerprint := sha256.Sum256(bytes.Join([][]byte{
		[]byte(request.Method),
		[]byte(request.URL.Path),
		[]byte(strconv.FormatUint(expectedValue, 10)),
		body,
	}, []byte{0}))
	response, err := handler.idempotent.execute(
		request.Context(),
		key,
		fingerprint,
		func() cachedResponse {
			aggregate, spec := handler.readAggregateForMutation(
				request.Context(),
				accessID,
				expected,
			)
			if spec != nil {
				return problemResponse(*spec)
			}
			if !validAccessStatusTransition(aggregate.Binding.Status, input.Status) {
				return problemResponse(problemSpec{
					status: http.StatusUnprocessableEntity,
					reason: ReasonInvalidRequest,
				})
			}
			candidate := aggregate.Clone()
			candidate.Binding.Revision = expected + 1
			candidate.Binding.Status = input.Status
			return handler.writeAccessStatusMutation(
				request.Context(),
				access.WriteCommand{
					ExpectedRevision: expected,
					Aggregate:        candidate,
				},
				input.Status,
			)
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonRevisionConflict)
		return
	}
	writeCached(writer, response)
}

func validAccessStatusTransition(current, target access.AccessStatus) bool {
	return (current == access.AccessStatusEnabled &&
		target == access.AccessStatusDisabled) ||
		(current == access.AccessStatusDisabled &&
			target == access.AccessStatusEnabled)
}

func (handler *Handler) writeAccessStatusMutation(
	ctx context.Context,
	command access.WriteCommand,
	status access.AccessStatus,
) cachedResponse {
	result, err := handler.accesses.WriteAccess(ctx, command)
	kind := activity.KindAccessEnabled
	applicationState := AccessApplicationStateActive
	if status == access.AccessStatusDisabled {
		kind = activity.KindAccessDisabled
		applicationState = AccessApplicationStateInactive
	}
	if err != nil {
		if result.Outcome == access.WriteOutcomeCommitted &&
			errors.Is(err, access.ErrProjectionUnavailable) {
			handler.recordActivity(ctx, activity.Event{
				Kind:       kind,
				AccessID:   command.Aggregate.Binding.ID,
				SubjectID:  strconv.FormatUint(uint64(result.Revision), 10),
				Status:     activity.StatusFailed,
				ReasonCode: string(access.ReasonProjectionUnavailable),
			})
			return jsonResponse(http.StatusOK, AccessApplyResponse{
				Outcome:          result.Outcome,
				Revision:         result.Revision,
				ApplicationState: AccessApplicationStateUnavailable,
			})
		}
		return problemResponse(classifyAccessError(err))
	}
	handler.recordActivity(ctx, activity.Event{
		Kind:      kind,
		AccessID:  command.Aggregate.Binding.ID,
		SubjectID: strconv.FormatUint(uint64(result.Revision), 10),
		Status:    activity.StatusSucceeded,
	})
	response := AccessApplyResponse{
		Outcome:          result.Outcome,
		Revision:         result.Revision,
		ApplicationState: applicationState,
	}
	if applicationState == AccessApplicationStateActive {
		response.PlanHash = result.PlanHash.String()
	}
	return jsonResponse(http.StatusOK, response)
}
