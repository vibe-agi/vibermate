package servercontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/serveradmin"
)

const (
	WebAuthPath           = "/api/v1/server/web-auth"
	WebSetupPath          = "/api/v1/server/web-setup"
	WebSessionPath        = "/api/v1/server/web-sessions"
	WebCurrentSessionPath = WebSessionPath + "/current"
	WebPasswordPath       = "/api/v1/server/web-account/password"
	WebRecoveryPath       = "/api/v1/server/web-recovery"

	WebAuthSchema       = "vibermate-web-auth-v1"
	WebSetupSchema      = "vibermate-web-setup-v1"
	WebLoginSchema      = "vibermate-web-login-v1"
	WebSessionSchema    = "vibermate-web-session-v1"
	WebPasswordSchema   = "vibermate-web-password-v1"
	WebRecoverySchema   = "vibermate-web-recovery-v1"
	maxWebLoginBodySize = 16 << 10
)

type WebUserAuthority interface {
	Create(context.Context, runtimeuser.CreateCommand) (runtimeuser.User, error)
	VerifyCredentials(context.Context, string, []byte) (runtimeuser.User, error)
	User(context.Context, runtimeuser.UserID) (runtimeuser.User, error)
	ReplacePassword(context.Context, runtimeuser.UserID, []byte) (runtimeuser.User, error)
	ChangePassword(context.Context, runtimeuser.User, []byte) (runtimeuser.User, error)
}

type WebSessionsOptions struct {
	InstanceID string
	Users      WebUserAuthority
	Sessions   *serveradmin.Authority
}

type WebSessionsHandler struct {
	instanceID string
	users      WebUserAuthority
	sessions   *serveradmin.Authority
	logins     *runtimeUserLoginAdmission
	setupMu    sync.Mutex
}

type WebAuthState struct {
	Schema        string `json:"schema"`
	SetupRequired bool   `json:"setupRequired"`
}

type WebSetup struct {
	Schema      string `json:"schema"`
	RecoveryKey string `json:"recoveryKey"`
	Username    string `json:"username"`
	Password    string `json:"password"`
}

type WebLogin struct {
	Schema   string `json:"schema"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type WebPasswordChange struct {
	Schema          string `json:"schema"`
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type WebRecovery struct {
	Schema      string `json:"schema"`
	RecoveryKey string `json:"recoveryKey"`
	NewPassword string `json:"newPassword"`
}

type WebPrincipalView struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type WebSession struct {
	Schema     string           `json:"schema"`
	InstanceID string           `json:"instanceId"`
	APIVersion string           `json:"apiVersion"`
	Principal  WebPrincipalView `json:"principal"`
	ReadToken  string           `json:"readToken"`
	WriteToken string           `json:"writeToken"`
	ExpiresAt  time.Time        `json:"expiresAt"`
}

func NewWebSessions(options WebSessionsOptions) (*WebSessionsHandler, error) {
	if options.InstanceID == "" || options.Users == nil || options.Sessions == nil {
		return nil, errors.New("Runtime Server Web Session dependencies are incomplete")
	}
	return &WebSessionsHandler{
		instanceID: options.InstanceID,
		users:      options.Users,
		sessions:   options.Sessions,
		logins:     newRuntimeUserLoginAdmission(),
	}, nil
}

func (handler *WebSessionsHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler == nil || request == nil || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
		return
	}
	switch {
	case request.URL.Path == WebAuthPath && request.Method == http.MethodGet:
		_, configured := handler.sessions.Owner()
		writeServerJSON(writer, http.StatusOK, WebAuthState{
			Schema: WebAuthSchema, SetupRequired: !configured,
		})
	case request.URL.Path == WebSetupPath && request.Method == http.MethodPost:
		handler.setup(writer, request)
	case request.URL.Path == WebSessionPath && request.Method == http.MethodPost:
		handler.login(writer, request)
	case request.URL.Path == WebCurrentSessionPath && request.Method == http.MethodGet:
		handler.current(writer, request)
	case request.URL.Path == WebCurrentSessionPath && request.Method == http.MethodDelete:
		handler.logout(writer, request)
	case request.URL.Path == WebPasswordPath && request.Method == http.MethodPatch:
		handler.changePassword(writer, request)
	case request.URL.Path == WebRecoveryPath && request.Method == http.MethodPost:
		handler.recover(writer, request)
	default:
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
	}
}

func (handler *WebSessionsHandler) setup(writer http.ResponseWriter, request *http.Request) {
	var input WebSetup
	if !decodeWebInput(writer, request, WebSetupSchema, &input, func() string { return input.Schema }) {
		return
	}
	password := []byte(input.Password)
	input.Password = ""
	defer clear(password)
	release, retryAfter, admitted := handler.logins.acquire(request.RemoteAddr)
	if !admitted {
		writer.Header().Set("Retry-After", retryAfterHeader(retryAfter))
		writeProblem(writer, http.StatusTooManyRequests, "web_login_rate_limited")
		return
	}
	defer release()
	handler.setupMu.Lock()
	defer handler.setupMu.Unlock()
	if _, configured := handler.sessions.Owner(); configured ||
		!handler.sessions.RecoveryKeyValid(input.RecoveryKey) {
		writeProblem(writer, http.StatusConflict, "web_setup_unavailable")
		return
	}
	user, err := handler.users.Create(request.Context(), runtimeuser.CreateCommand{
		Username: input.Username, Password: password,
	})
	if errors.Is(err, runtimeuser.ErrUsernameConflict) {
		user, err = handler.users.VerifyCredentials(request.Context(), input.Username, password)
	}
	if err != nil {
		if errors.Is(err, runtimeuser.ErrInvalidUser) ||
			errors.Is(err, runtimeuser.ErrInvalidCredentials) ||
			errors.Is(err, runtimeuser.ErrUsernameConflict) {
			writeProblem(writer, http.StatusUnprocessableEntity, "invalid_web_setup")
			return
		}
		writeProblem(writer, http.StatusServiceUnavailable, "web_setup_unavailable")
		return
	}
	if err := handler.sessions.ClaimOwner(input.RecoveryKey, user.ID); err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "web_setup_unavailable")
		return
	}
	// The bootstrap value is intentionally one-use. A fresh value remains on
	// the Server machine for owner recovery, but the copied setup value dies.
	if err := handler.sessions.RotateRecoveryKey(); err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "web_setup_unavailable")
		return
	}
	session, err := handler.sessions.LoginUser(request.Context(), user)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "web_login_unavailable")
		return
	}
	writeWebSession(writer, handler.instanceID, session)
}

func (handler *WebSessionsHandler) login(writer http.ResponseWriter, request *http.Request) {
	var input WebLogin
	if !decodeWebInput(writer, request, WebLoginSchema, &input, func() string { return input.Schema }) {
		return
	}
	if _, configured := handler.sessions.Owner(); !configured {
		writeProblem(writer, http.StatusConflict, "web_setup_required")
		return
	}
	password := []byte(input.Password)
	input.Password = ""
	defer clear(password)
	release, retryAfter, admitted := handler.logins.acquire(request.RemoteAddr)
	if !admitted {
		writer.Header().Set("Retry-After", retryAfterHeader(retryAfter))
		writeProblem(writer, http.StatusTooManyRequests, "web_login_rate_limited")
		return
	}
	defer release()
	user, err := handler.users.VerifyCredentials(request.Context(), input.Username, password)
	if err != nil {
		if errors.Is(err, runtimeuser.ErrInvalidCredentials) {
			writeProblem(writer, http.StatusUnauthorized, "web_access_denied")
			return
		}
		writeProblem(writer, http.StatusServiceUnavailable, "web_login_unavailable")
		return
	}
	session, err := handler.sessions.LoginUser(request.Context(), user)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "web_login_unavailable")
		return
	}
	writeWebSession(writer, handler.instanceID, session)
}

func (handler *WebSessionsHandler) current(writer http.ResponseWriter, request *http.Request) {
	principal, valid := takeWebPrincipal(request, handler.sessions, serveradmin.ScopeRead)
	if !valid || !principal.Valid() {
		writeProblem(writer, http.StatusUnauthorized, "web_session_invalid")
		return
	}
	writeServerJSON(writer, http.StatusOK, WebPrincipalView{
		ID: string(principal.UserID), Username: principal.Username, Role: string(principal.Role),
	})
}

func (handler *WebSessionsHandler) logout(writer http.ResponseWriter, request *http.Request) {
	token, valid := takeRuntimeUserBearer(request)
	if !valid || !handler.sessions.Revoke(token) {
		writeProblem(writer, http.StatusUnauthorized, "web_session_invalid")
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *WebSessionsHandler) changePassword(writer http.ResponseWriter, request *http.Request) {
	principal, valid := takeWebPrincipal(request, handler.sessions, serveradmin.ScopeWrite)
	if !valid || !principal.Valid() {
		writeProblem(writer, http.StatusUnauthorized, "web_session_invalid")
		return
	}
	var input WebPasswordChange
	if !decodeWebInput(writer, request, WebPasswordSchema, &input, func() string { return input.Schema }) {
		return
	}
	current := []byte(input.CurrentPassword)
	next := []byte(input.NewPassword)
	input.CurrentPassword, input.NewPassword = "", ""
	defer clear(current)
	defer clear(next)
	release, retryAfter, admitted := handler.logins.acquire(request.RemoteAddr)
	if !admitted {
		writer.Header().Set("Retry-After", retryAfterHeader(retryAfter))
		writeProblem(writer, http.StatusTooManyRequests, "web_login_rate_limited")
		return
	}
	defer release()
	verified, err := handler.users.VerifyCredentials(request.Context(), principal.Username, current)
	if err != nil || verified.ID != principal.UserID {
		writeProblem(writer, http.StatusUnauthorized, "web_current_password_rejected")
		return
	}
	updated, err := handler.users.ChangePassword(request.Context(), verified, next)
	if err != nil {
		if errors.Is(err, runtimeuser.ErrInvalidCredentials) {
			writeProblem(writer, http.StatusUnauthorized, "web_current_password_rejected")
			return
		}
		if errors.Is(err, runtimeuser.ErrInvalidUser) {
			writeProblem(writer, http.StatusUnprocessableEntity, "invalid_web_password")
			return
		}
		writeProblem(writer, http.StatusServiceUnavailable, "web_password_unavailable")
		return
	}
	handler.sessions.RevokeUserSessions(principal.UserID)
	session, err := handler.sessions.LoginUser(request.Context(), updated)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "web_login_unavailable")
		return
	}
	writeWebSession(writer, handler.instanceID, session)
}

func (handler *WebSessionsHandler) recover(writer http.ResponseWriter, request *http.Request) {
	var input WebRecovery
	if !decodeWebInput(writer, request, WebRecoverySchema, &input, func() string { return input.Schema }) {
		return
	}
	password := []byte(input.NewPassword)
	input.NewPassword = ""
	defer clear(password)
	if !runtimeuser.ValidPassword(password) {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_web_password")
		return
	}
	release, retryAfter, admitted := handler.logins.acquire(request.RemoteAddr)
	if !admitted {
		writer.Header().Set("Retry-After", retryAfterHeader(retryAfter))
		writeProblem(writer, http.StatusTooManyRequests, "web_login_rate_limited")
		return
	}
	defer release()
	handler.setupMu.Lock()
	defer handler.setupMu.Unlock()
	ownerID, configured := handler.sessions.Owner()
	if !configured || !handler.sessions.RecoveryKeyValid(input.RecoveryKey) {
		writeProblem(writer, http.StatusUnauthorized, "web_recovery_rejected")
		return
	}
	if err := handler.sessions.RotateRecoveryKey(); err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "web_recovery_unavailable")
		return
	}
	updated, err := handler.users.ReplacePassword(request.Context(), ownerID, password)
	if err != nil {
		if errors.Is(err, runtimeuser.ErrInvalidUser) {
			writeProblem(writer, http.StatusUnprocessableEntity, "invalid_web_password")
			return
		}
		writeProblem(writer, http.StatusServiceUnavailable, "web_recovery_unavailable")
		return
	}
	handler.sessions.RevokeUserSessions(ownerID)
	session, err := handler.sessions.LoginUser(request.Context(), updated)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "web_login_unavailable")
		return
	}
	writeWebSession(writer, handler.instanceID, session)
}

func decodeWebInput(
	writer http.ResponseWriter,
	request *http.Request,
	expectedSchema string,
	destination any,
	schema func() string,
) bool {
	if (request.Header.Get("Authorization") != "" && request.URL.Path != WebPasswordPath) ||
		request.Header.Get("Proxy-Authorization") != "" {
		writeProblem(writer, http.StatusForbidden, "web_login_transport_rejected")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_web_request")
		return false
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxWebLoginBodySize+1))
	if err != nil || len(payload) == 0 || len(payload) > maxWebLoginBodySize {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_web_request")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || schema() != expectedSchema {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_web_request")
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_web_request")
		return false
	}
	return true
}

func takeWebPrincipal(
	request *http.Request,
	authority *serveradmin.Authority,
	scope serveradmin.Scope,
) (serveradmin.Principal, bool) {
	if request.Header.Get("Proxy-Authorization") != "" {
		return serveradmin.Principal{}, false
	}
	token, valid := takeRuntimeUserBearer(request)
	if !valid {
		return serveradmin.Principal{}, false
	}
	return authority.Authenticate(request.Context(), token, scope)
}

func writeWebSession(writer http.ResponseWriter, instanceID string, session serveradmin.Session) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(writer).Encode(WebSession{
		Schema: WebSessionSchema, InstanceID: instanceID, APIVersion: "v1",
		Principal: WebPrincipalView{
			ID: string(session.Principal.UserID), Username: session.Principal.Username,
			Role: string(session.Principal.Role),
		},
		ReadToken: session.ReadToken.Value(), WriteToken: session.WriteToken.Value(),
		ExpiresAt: session.ExpiresAt,
	})
}
