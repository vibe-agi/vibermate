package desktopcontrol

import (
	"crypto/sha256"
	"net/http"
	"strconv"

	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func (handler *Handler) setProviderAccountAssociation(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	defer clear(body)
	var input struct {
		Linked *bool `json:"linked"`
	}
	id, idErr := provideraccount.NewID(request.PathValue("accountId"))
	endpointID, endpointErr := upstreamendpoint.NewID(request.PathValue("endpointId"))
	if headerErr != nil || bodyErr != nil || idErr != nil || endpointErr != nil || expected == 0 ||
		decodeStrictJSON(body, &input) != nil || input.Linked == nil || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256([]byte(request.Method + "\x00" + request.URL.Path + "\x00" + strconv.FormatUint(expected, 10) + "\x00" + string(body)))
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		view, err := handler.accounts.SetAssociation(request.Context(), provideraccount.AssociationCommand{
			ID: id, EndpointID: endpointID, ExpectedRevision: expected, Linked: *input.Linked,
		})
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
