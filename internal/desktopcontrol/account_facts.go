package desktopcontrol

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/vibe-agi/vibermate/internal/accountoperation"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

type OwnedAccountReader interface {
	ReadOwned(context.Context, provideraccount.ID, string) (accountoperation.Result, error)
}

func (handler *Handler) getAccountFacts(writer http.ResponseWriter, request *http.Request) {
	query, err := url.ParseQuery(request.URL.RawQuery)
	kind := query.Get("kind")
	if err != nil || len(query) > 1 || len(query["kind"]) > 1 ||
		len(query) == 1 && len(query["kind"]) == 0 || kind != "" && kind != "quota" && kind != "history" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, err := provideraccount.NewID(request.PathValue("accountId"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	if handler.accountReads == nil {
		writeProblem(writer, http.StatusServiceUnavailable, "account_read_unavailable")
		return
	}
	operation := upstreamservice.CodexRateLimits
	if kind == "history" {
		operation = upstreamservice.CodexUsageHistory
	}
	result, err := handler.accountReads.ReadOwned(request.Context(), id, operation)
	if err != nil {
		status, reason := http.StatusBadGateway, ReasonCode("account_read_unavailable")
		if errors.Is(err, provideraccount.ErrAccountNotFound) {
			status, reason = http.StatusNotFound, ReasonProviderAccountNotFound
		}
		if errors.Is(err, upstreamservice.ErrUnsupported) {
			status, reason = http.StatusUnprocessableEntity, "account_read_unsupported"
		}
		writeProblem(writer, status, reason)
		return
	}
	// Never expose the native response body or headers on the management API.
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, result.Facts)
}
