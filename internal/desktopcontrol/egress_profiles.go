package desktopcontrol

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
)

type EgressProfileResponse struct {
	ID          string               `json:"id"`
	Revision    uint64               `json:"revision"`
	DisplayName string               `json:"displayName"`
	Policy      egressnetwork.Policy `json:"policy"`
	PublishedAt string               `json:"publishedAt"`
}

type EgressProfileListResponse struct {
	Items []EgressProfileResponse `json:"items"`
}

type egressProfileInput struct {
	DisplayName string               `json:"displayName"`
	Policy      egressnetwork.Policy `json:"policy"`
}

func (handler *Handler) listEgressProfiles(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	profiles, err := handler.egressProfiles.List(request.Context())
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonEgressProfileUnavailable)
		return
	}
	response := EgressProfileListResponse{Items: make([]EgressProfileResponse, len(profiles))}
	for index, profile := range profiles {
		response.Items[index] = egressProfileResponseOf(profile)
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) publishEgressProfile(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	var input egressProfileInput
	decodeErr := bodyErr
	if decodeErr == nil {
		decodeErr = decodeStrictJSON(body, &input)
	}
	id, idErr := egressprofile.NewID(request.PathValue("egressId"))
	if headerErr != nil || decodeErr != nil || idErr != nil || request.URL.RawQuery != "" ||
		expected > uint64(egressprofile.MaxRevision) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := codeLibraryFingerprint(request, expected, body)
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		profile, publishErr := handler.egressProfiles.Publish(
			request.Context(),
			egressprofile.PublishCommand{
				ID: id, ExpectedRevision: egressprofile.Revision(expected),
				DisplayName: input.DisplayName, Policy: input.Policy,
			},
		)
		if publishErr != nil {
			return problemResponse(classifyEgressProfileError(publishErr))
		}
		return jsonResponse(http.StatusOK, egressProfileResponseOf(profile))
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonEgressProfileConflict)
		return
	}
	writeCached(writer, response)
}

func (handler *Handler) getEgressProfileRevision(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, idErr := egressprofile.NewID(request.PathValue("egressId"))
	revision, revisionErr := strconv.ParseUint(request.PathValue("egressRevision"), 10, 64)
	if idErr != nil || revisionErr != nil || revision == 0 ||
		revision > uint64(egressprofile.MaxRevision) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	profile, err := handler.egressProfiles.GetRevision(
		request.Context(), id, egressprofile.Revision(revision),
	)
	if err != nil {
		spec := classifyEgressProfileError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	writeJSON(writer, http.StatusOK, egressProfileResponseOf(profile))
}

func egressProfileResponseOf(profile egressprofile.ProfileRevision) EgressProfileResponse {
	return EgressProfileResponse{
		ID: profile.ID.String(), Revision: uint64(profile.Revision),
		DisplayName: profile.DisplayName, Policy: profile.Policy,
		PublishedAt: profile.PublishedAt.Format(time.RFC3339Nano),
	}
}

func classifyEgressProfileError(err error) problemSpec {
	switch {
	case errors.Is(err, egressprofile.ErrInvalidProfile):
		return problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonInvalidRequest}
	case errors.Is(err, egressprofile.ErrProfileNotFound):
		return problemSpec{status: http.StatusNotFound, reason: ReasonEgressProfileNotFound}
	case errors.Is(err, egressprofile.ErrRevisionConflict):
		return problemSpec{status: http.StatusConflict, reason: ReasonEgressProfileConflict}
	default:
		return problemSpec{status: http.StatusServiceUnavailable, reason: ReasonEgressProfileUnavailable}
	}
}
