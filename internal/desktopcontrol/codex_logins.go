package desktopcontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

const CodexLoginPath = "/api/v1/codex-oauth/logins"

type oauthSessionKey struct{}

// WithOAuthSession is called by a Host only after authorizing a write session.
// A non-reversible session binding reaches the login module, never the token.
func WithOAuthSession(ctx context.Context, credential string) context.Context {
	if credential == "" {
		return ctx
	}
	digest := sha256.Sum256([]byte("vibermate-oauth-session\x00" + credential))
	return context.WithValue(ctx, oauthSessionKey{}, hex.EncodeToString(digest[:]))
}

func (handler *Handler) oauthOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	owner, ok := r.Context().Value(oauthSessionKey{}).(string)
	if !ok || owner == "" {
		writeProblem(w, http.StatusUnauthorized, ReasonUnauthorized)
		return "", false
	}
	if handler.codexLogins == nil {
		writeProblem(w, http.StatusServiceUnavailable, ReasonProviderAccountUnavailable)
		return "", false
	}
	return owner, true
}

func (handler *Handler) startCodexLogin(w http.ResponseWriter, r *http.Request) {
	owner, ok := handler.oauthOwner(w, r)
	if !ok {
		return
	}
	expected, key, headerErr := mutationHeaders(r)
	body, bodyErr := readJSONBody(r)
	defer clear(body)
	var input struct {
		ID           string `json:"accountId"`
		EndpointID   string `json:"upstreamEndpointId"`
		DisplayName  string `json:"displayName"`
		CallbackMode string `json:"callbackMode"`
	}
	if headerErr != nil || expected != 0 || bodyErr != nil || decodeStrictJSON(body, &input) != nil ||
		len(input.DisplayName) > 256 || !utf8.ValidString(input.DisplayName) || strings.ContainsAny(input.DisplayName, "\x00\r\n") ||
		(input.CallbackMode != "loopback" && input.CallbackMode != "manual") {
		writeProblem(w, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	accountID, idErr := provideraccount.NewID(input.ID)
	endpointID, endpointErr := upstreamendpoint.NewID(input.EndpointID)
	if idErr != nil || endpointErr != nil {
		writeProblem(w, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256(append([]byte(owner+"\x00"+r.URL.Path+"\x00"), body...))
	response, err := handler.idempotent.execute(r.Context(), key, fingerprint, func() cachedResponse {
		endpoint, err := handler.endpoints.Get(r.Context(), endpointID)
		if err != nil {
			return problemResponse(classifyUpstreamEndpointError(err))
		}
		if endpoint.State != upstreamendpoint.StateActive || !upstreamendpoint.IsChatGPTCodexOrigin(endpoint.Origin) ||
			!slices.Contains(endpoint.Drivers, providerauth.CodexOAuthDriverRef()) {
			return problemResponse(problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonInvalidRequest})
		}
		if _, err := handler.accounts.Get(r.Context(), accountID); err == nil {
			return problemResponse(problemSpec{status: http.StatusConflict, reason: ReasonProviderAccountConflict})
		} else if !errors.Is(err, provideraccount.ErrAccountNotFound) {
			return problemResponse(classifyProviderAccountError(err))
		}
		view, err := handler.codexLogins.Start(r.Context(), owner, codexoauth.LoginAccount{
			ID: input.ID, EndpointID: input.EndpointID, DisplayName: strings.TrimSpace(input.DisplayName),
		}, input.CallbackMode)
		if err != nil {
			return problemResponse(classifyCodexLoginError(err))
		}
		return jsonResponse(http.StatusCreated, view)
	})
	if err != nil {
		writeProblem(w, http.StatusConflict, ReasonProviderAccountConflict)
		return
	}
	writeCached(w, response)
}

func (handler *Handler) getCodexLogin(w http.ResponseWriter, r *http.Request) {
	owner, ok := handler.oauthOwner(w, r)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeProblem(w, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	view, err := handler.codexLogins.Status(r.Context(), owner, r.PathValue("loginId"))
	if err != nil {
		spec := classifyCodexLoginError(err)
		writeProblem(w, spec.status, spec.reason)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (handler *Handler) completeCodexLogin(w http.ResponseWriter, r *http.Request) {
	owner, ok := handler.oauthOwner(w, r)
	if !ok {
		return
	}
	expected, _, headerErr := mutationHeaders(r)
	body, bodyErr := readJSONBody(r)
	defer clear(body)
	var input struct {
		CallbackURL string `json:"callbackUrl"`
	}
	if headerErr != nil || expected != 0 || bodyErr != nil || decodeStrictJSON(body, &input) != nil {
		writeProblem(w, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	view, err := handler.codexLogins.Complete(r.Context(), owner, r.PathValue("loginId"), input.CallbackURL)
	input.CallbackURL = ""
	if err != nil {
		spec := classifyCodexLoginError(err)
		writeProblem(w, spec.status, spec.reason)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (handler *Handler) cancelCodexLogin(w http.ResponseWriter, r *http.Request) {
	owner, ok := handler.oauthOwner(w, r)
	if !ok {
		return
	}
	expected, _, err := mutationHeaders(r)
	if err != nil || expected != 0 || !emptyBody(r.Body) {
		writeProblem(w, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	if err := handler.codexLogins.Cancel(r.Context(), owner, r.PathValue("loginId")); err != nil {
		spec := classifyCodexLoginError(err)
		writeProblem(w, spec.status, spec.reason)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func classifyCodexLoginError(err error) problemSpec {
	switch {
	case errors.Is(err, codexoauth.ErrLoginNotFound):
		return problemSpec{status: http.StatusNotFound, reason: ReasonCodexLoginNotFound}
	case errors.Is(err, codexoauth.ErrLoginBusy):
		return problemSpec{status: http.StatusConflict, reason: ReasonCodexLoginBusy}
	case errors.Is(err, codexoauth.ErrLoginCapacity):
		return problemSpec{status: http.StatusTooManyRequests, reason: ReasonCodexLoginCapacity}
	default:
		return problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonInvalidRequest}
	}
}
