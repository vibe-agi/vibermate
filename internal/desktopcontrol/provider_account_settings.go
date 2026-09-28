package desktopcontrol

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
)

func (handler *Handler) setProviderAccountSettings(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	defer clear(body)
	var input struct {
		AutomaticRefresh *bool           `json:"automaticRefresh"`
		EgressProfile    json.RawMessage `json:"egressProfile"`
	}
	id, idErr := provideraccount.NewID(request.PathValue("accountId"))
	if headerErr != nil || bodyErr != nil || idErr != nil || expected >= provideraccount.MaxRevision ||
		decodeStrictJSON(body, &input) != nil || input.AutomaticRefresh == nil || len(input.EgressProfile) == 0 || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256([]byte(request.Method + "\x00" + request.URL.Path + "\x00" + strconv.FormatUint(expected, 10) + "\x00" + string(body)))
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		var profile egressprofile.ProfileRevision
		if string(input.EgressProfile) != "null" {
			var ref struct {
				ID       string `json:"id"`
				Revision uint64 `json:"revision"`
			}
			if decodeStrictJSON(input.EgressProfile, &ref) != nil || ref.Revision == 0 || ref.Revision > uint64(egressprofile.MaxRevision) {
				return problemResponse(problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonInvalidRequest})
			}
			profileID, err := egressprofile.NewID(ref.ID)
			if err != nil {
				return problemResponse(problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonInvalidRequest})
			}
			if handler.egressProfiles == nil {
				return problemResponse(problemSpec{status: http.StatusServiceUnavailable, reason: ReasonEgressProfileUnavailable})
			}
			profile, err = handler.egressProfiles.GetRevision(request.Context(), profileID, egressprofile.Revision(ref.Revision))
			if err != nil {
				return problemResponse(classifyEgressProfileError(err))
			}
		}
		view, err := handler.accounts.SetSettings(request.Context(), provideraccount.SettingsCommand{ID: id, ExpectedRevision: expected, EgressProfile: profile, AutomaticRefresh: *input.AutomaticRefresh})
		if err != nil {
			return problemResponse(classifyProviderAccountError(err))
		}
		result, err := handler.providerAccountResponse(request.Context(), view)
		if err != nil {
			return problemResponse(problemSpec{status: http.StatusServiceUnavailable, reason: ReasonProviderAccountUnavailable})
		}
		return jsonResponse(http.StatusOK, result)
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonProviderAccountConflict)
		return
	}
	writeCached(writer, response)
}
