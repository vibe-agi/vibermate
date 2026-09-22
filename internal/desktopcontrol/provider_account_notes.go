package desktopcontrol

import (
	"crypto/sha256"
	"net/http"
	"strconv"

	"github.com/vibe-agi/vibermate/internal/provideraccount"
)

func (handler *Handler) setProviderAccountNote(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	defer clear(body)
	var input struct {
		Note *string `json:"note"`
	}
	id, idErr := provideraccount.NewID(request.PathValue("accountId"))
	if headerErr != nil || bodyErr != nil || idErr != nil || expected >= provideraccount.MaxRevision ||
		decodeStrictJSON(body, &input) != nil || input.Note == nil || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256([]byte(request.Method + "\x00" + request.URL.Path + "\x00" + strconv.FormatUint(expected, 10) + "\x00" + string(body)))
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		view, err := handler.accounts.SetNote(request.Context(), provideraccount.NoteCommand{ID: id, ExpectedRevision: expected, Note: *input.Note})
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
