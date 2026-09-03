package servercontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

const (
	RuntimeUserSessionPath          = "/api/v1/server/login-sessions"
	RuntimeUserCurrentSessionPath   = "/api/v1/server/login-sessions/current"
	RuntimeUserLoginSchema          = "vibermate-runtime-user-login-v1"
	RuntimeUserSessionSchema        = "vibermate-runtime-user-session-v1"
	RuntimeUserCurrentSessionSchema = "vibermate-runtime-user-current-session-v1"
	maxRuntimeUserLoginBytes        = 16 << 10
)

type RuntimeUserSessionsOptions struct {
	InstanceID string
	Users      RuntimeUserSessionAuthority
}

type RuntimeUserSessionsHandler struct {
	instanceID string
	users      RuntimeUserSessionAuthority
	logins     *runtimeUserLoginAdmission
}

// RuntimeUserSessionAuthority is the narrow password/session boundary needed
// by the unauthenticated Server login route. Keeping the HTTP admission policy
// outside runtimeuser.Manager lets the network edge reject abusive work before
// any Argon2 allocation begins.
type RuntimeUserSessionAuthority interface {
	Login(context.Context, runtimeuser.LoginCommand) (runtimeuser.LoginSession, error)
	Authenticate(context.Context, string) (runtimeuser.Identity, error)
	Logout(context.Context, string) error
}

type RuntimeUserLogin struct {
	Schema     string `json:"schema"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	MachineID  string `json:"machineId"`
	DeviceName string `json:"deviceName"`
}

type RuntimeUserView struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type RuntimeUserSession struct {
	Schema       string          `json:"schema"`
	InstanceID   string          `json:"instanceId"`
	APIVersion   string          `json:"apiVersion"`
	User         RuntimeUserView `json:"user"`
	SessionID    string          `json:"sessionId"`
	SessionToken string          `json:"sessionToken"`
	ExpiresAt    time.Time       `json:"expiresAt"`
}

type RuntimeUserCurrentSession struct {
	Schema     string          `json:"schema"`
	InstanceID string          `json:"instanceId"`
	APIVersion string          `json:"apiVersion"`
	User       RuntimeUserView `json:"user"`
	SessionID  string          `json:"sessionId"`
	MachineID  string          `json:"machineId"`
	DeviceName string          `json:"deviceName"`
}

func NewRuntimeUserSessions(
	options RuntimeUserSessionsOptions,
) (*RuntimeUserSessionsHandler, error) {
	if options.InstanceID == "" || options.Users == nil {
		return nil, errors.New("Runtime User Login Session dependencies are incomplete")
	}
	return &RuntimeUserSessionsHandler{
		instanceID: options.InstanceID,
		users:      options.Users,
		logins:     newRuntimeUserLoginAdmission(),
	}, nil
}

func (handler *RuntimeUserSessionsHandler) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if handler == nil || request == nil || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
		return
	}
	switch {
	case request.URL.Path == RuntimeUserSessionPath && request.Method == http.MethodPost:
		handler.login(writer, request)
	case request.URL.Path == RuntimeUserCurrentSessionPath && request.Method == http.MethodGet:
		handler.current(writer, request)
	case request.URL.Path == RuntimeUserCurrentSessionPath && request.Method == http.MethodDelete:
		handler.logout(writer, request)
	default:
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
	}
}

func (handler *RuntimeUserSessionsHandler) current(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.Header.Get("Proxy-Authorization") != "" {
		writeProblem(writer, http.StatusForbidden, "runtime_user_session_transport_rejected")
		return
	}
	token, valid := takeRuntimeUserBearer(request)
	if !valid {
		writeProblem(writer, http.StatusUnauthorized, "runtime_user_session_invalid")
		return
	}
	if request.Body != nil {
		body, err := io.ReadAll(io.LimitReader(request.Body, 1))
		if err != nil || len(body) != 0 {
			writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_session")
			return
		}
	}
	identity, err := handler.users.Authenticate(request.Context(), token)
	if err != nil {
		writeProblem(writer, http.StatusUnauthorized, "runtime_user_session_invalid")
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(RuntimeUserCurrentSession{
		Schema: RuntimeUserCurrentSessionSchema, InstanceID: handler.instanceID,
		APIVersion: "v1",
		User: RuntimeUserView{
			ID: string(identity.User.ID), Username: identity.User.Username,
		},
		SessionID: string(identity.SessionID), MachineID: identity.MachineID.String(),
		DeviceName: identity.DeviceName,
	})
}

func (handler *RuntimeUserSessionsHandler) login(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.Header.Get("Authorization") != "" ||
		request.Header.Get("Proxy-Authorization") != "" {
		writeProblem(writer, http.StatusForbidden, "runtime_user_login_transport_rejected")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_login")
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxRuntimeUserLoginBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxRuntimeUserLoginBytes {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_login")
		return
	}
	var input RuntimeUserLogin
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Schema != RuntimeUserLoginSchema {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_login")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_login")
		return
	}
	machineID, err := workspaceidentity.ParseMachineID(input.MachineID)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_login")
		return
	}
	password := []byte(input.Password)
	input.Password = ""
	release, retryAfter, admitted := handler.logins.acquire(request.RemoteAddr)
	if !admitted {
		clear(password)
		writer.Header().Set("Retry-After", retryAfterHeader(retryAfter))
		writeProblem(writer, http.StatusTooManyRequests, "runtime_user_login_rate_limited")
		return
	}
	defer release()
	session, err := handler.users.Login(request.Context(), runtimeuser.LoginCommand{
		Username: input.Username, Password: password,
		MachineID: machineID, DeviceName: input.DeviceName,
	})
	clear(password)
	if err != nil {
		if errors.Is(err, runtimeuser.ErrInvalidCredentials) {
			writeProblem(writer, http.StatusUnauthorized, "runtime_user_access_denied")
			return
		}
		writeProblem(writer, http.StatusServiceUnavailable, "runtime_user_login_unavailable")
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(writer).Encode(RuntimeUserSession{
		Schema: RuntimeUserSessionSchema, InstanceID: handler.instanceID,
		APIVersion: "v1",
		User: RuntimeUserView{
			ID: string(session.User.ID), Username: session.User.Username,
		},
		SessionID:    string(session.ID),
		SessionToken: session.Token.Value(), ExpiresAt: session.ExpiresAt,
	})
}

func (handler *RuntimeUserSessionsHandler) logout(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.Header.Get("Proxy-Authorization") != "" {
		writeProblem(writer, http.StatusForbidden, "runtime_user_logout_transport_rejected")
		return
	}
	token, valid := takeRuntimeUserBearer(request)
	if !valid {
		writeProblem(writer, http.StatusUnauthorized, "runtime_user_session_invalid")
		return
	}
	if request.Body != nil {
		body, err := io.ReadAll(io.LimitReader(request.Body, 1))
		if err != nil || len(body) != 0 {
			writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_logout")
			return
		}
	}
	if err := handler.users.Logout(request.Context(), token); err != nil {
		writeProblem(writer, http.StatusUnauthorized, "runtime_user_session_invalid")
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func takeRuntimeUserBearer(request *http.Request) (string, bool) {
	values := request.Header.Values("Authorization")
	request.Header.Del("Authorization")
	if len(values) != 1 {
		return "", false
	}
	token, found := strings.CutPrefix(values[0], "Bearer ")
	return token, found && token != "" && strings.TrimSpace(token) == token
}
