package desktopcontrol

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"strconv"

	"github.com/vibe-agi/vibermate/internal/accountoperation"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

// A banked reset is an owner action. It is not a captured Agent request, a
// payment, or the local UI's refresh of previously observed quota facts.
func (handler *Handler) redeemResetCredit(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	var input struct {
		CreditID string `json:"creditId"`
	}
	id, idErr := provideraccount.NewID(request.PathValue("accountId"))
	if headerErr != nil || expected == 0 || bodyErr != nil || idErr != nil ||
		request.URL.RawQuery != "" || decodeStrictJSON(body, &input) != nil ||
		!providertransport.ValidResetCreditID(input.CreditID) || handler.accountReads == nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256([]byte(request.Method + "\x00" + request.URL.Path + "\x00" +
		strconv.FormatUint(expected, 10) + "\x00" + input.CreditID))
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		result, err := handler.accountReads.RedeemOwned(request.Context(), id, expected, input.CreditID)
		switch {
		case errors.Is(err, provideraccount.ErrAccountNotFound):
			return problemResponse(problemSpec{status: http.StatusNotFound, reason: ReasonProviderAccountNotFound})
		case errors.Is(err, provideraccount.ErrRevisionConflict):
			return problemResponse(problemSpec{status: http.StatusConflict, reason: ReasonProviderAccountConflict})
		case errors.Is(err, upstreamservice.ErrUnsupported):
			return problemResponse(problemSpec{status: http.StatusUnprocessableEntity, reason: "reset_credit_unavailable"})
		case errors.Is(err, accountoperation.ErrResetUnconfirmed):
			return problemResponse(problemSpec{status: http.StatusBadGateway, reason: "reset_result_unconfirmed"})
		case err != nil:
			return problemResponse(problemSpec{status: http.StatusServiceUnavailable, reason: "reset_credit_lookup_failed"})
		default:
			return jsonResponse(http.StatusOK, result)
		}
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonProviderAccountConflict)
		return
	}
	writeCached(writer, response)
}
