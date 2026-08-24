package servercontrol

import (
	"bytes"
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
	RuntimeUserSessionPath        = "/api/v1/server/login-sessions"
	RuntimeUserCurrentSessionPath = "/api/v1/server/login-sessions/current"
	RuntimeUserLoginSchema        = "vibermate-runtime-user-login-v1"
	RuntimeUserSessionSchema      = "vibermate-runtime-user-session-v1"
	maxRuntimeUserLoginBytes      = 16 << 10
)

type RuntimeUserSessionsOptions struct {
	InstanceID string
	Users      *runtimeuser.Manager
}

type RuntimeUserSessionsHandler struct {
	instanceID string
	users      *runtimeuser.Manager
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

func NewRuntimeUserSessions(
	options RuntimeUserSessionsOptions,
) (*RuntimeUserSessionsHandler, error) {
	if options.InstanceID == "" || options.Users == nil {
		return nil, errors.New("Runtime User Login Session dependencies are incomplete")
	}
	return &RuntimeUserSessionsHandler{
		instanceID: options.InstanceID,
		users:      options.Users,
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
	case request.URL.Path == RuntimeUserCurrentSessionPath && request.Method == http.MethodDelete:
		handler.logout(writer, request)
	default:
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
	}
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
	values := request.Header.Values("Authorization")
	request.Header.Del("Authorization")
	if len(values) != 1 {
		writeProblem(writer, http.StatusUnauthorized, "runtime_user_session_invalid")
		return
	}
	token, found := strings.CutPrefix(values[0], "Bearer ")
	if !found || token == "" || strings.TrimSpace(token) != token {
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
