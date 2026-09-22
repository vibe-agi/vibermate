package desktopcontrol

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"strconv"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
)

func (handler *Handler) refreshProviderAccountCredential(w http.ResponseWriter, r *http.Request) {
	expected, key, headerErr := mutationHeaders(r)
	id, idErr := provideraccount.NewID(r.PathValue("accountId"))
	if headerErr != nil || expected == 0 || idErr != nil || r.URL.RawQuery != "" || !emptyBody(r.Body) {
		writeProblem(w, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256([]byte(r.Method + "\x00" + r.URL.Path + "\x00" + strconv.FormatUint(expected, 10)))
	response, err := handler.idempotent.execute(r.Context(), key, fingerprint, func() cachedResponse {
		view, err := handler.accounts.RefreshCredential(r.Context(), id, expected)
		if errors.Is(err, codexoauth.ErrReconnectRequired) {
			return problemResponse(problemSpec{status: http.StatusConflict, reason: ReasonCredentialReconnectRequired})
		}
		if errors.Is(err, codexoauth.ErrRefreshUnavailable) {
			return problemResponse(problemSpec{status: http.StatusBadGateway, reason: ReasonCredentialRefreshFailed})
		}
		if err != nil {
			return problemResponse(classifyProviderAccountError(err))
		}
		result, err := handler.providerAccountResponse(r.Context(), view)
		if err != nil {
			return problemResponse(problemSpec{status: http.StatusServiceUnavailable, reason: ReasonProviderAccountUnavailable})
		}
		return jsonResponse(http.StatusOK, result)
	})
	if err != nil {
		writeProblem(w, http.StatusConflict, ReasonProviderAccountConflict)
		return
	}
	writeCached(w, response)
}
