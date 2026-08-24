package servercontrol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/vibe-agi/vibermate/internal/serveradmin"
)

const (
	AdminSessionPath       = "/api/v1/server/admin-sessions"
	AdminLoginSchema       = "vibermate-server-admin-login-v1"
	AdminSessionSchema     = "vibermate-server-admin-session-v1"
	maxAdminLoginBodyBytes = 8 << 10
)

type AdminSessionsOptions struct {
	InstanceID string
	Authority  *serveradmin.Authority
}

type AdminSessionsHandler struct {
	instanceID string
	authority  *serveradmin.Authority
}

type AdminLogin struct {
	Schema    string `json:"schema"`
	AccessKey string `json:"accessKey"`
}

type AdminSession struct {
	Schema     string    `json:"schema"`
	InstanceID string    `json:"instanceId"`
	APIVersion string    `json:"apiVersion"`
	ReadToken  string    `json:"readToken"`
	WriteToken string    `json:"writeToken"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func NewAdminSessions(options AdminSessionsOptions) (*AdminSessionsHandler, error) {
	if options.InstanceID == "" || options.Authority == nil {
		return nil, errors.New("Runtime Server admin session dependencies are incomplete")
	}
	return &AdminSessionsHandler{instanceID: options.InstanceID, authority: options.Authority}, nil
}

func (handler *AdminSessionsHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler == nil || request == nil || request.Method != http.MethodPost ||
		request.URL.Path != AdminSessionPath || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
		return
	}
	if request.Header.Get("Authorization") != "" {
		writeProblem(writer, http.StatusForbidden, "admin_session_transport_rejected")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_admin_login")
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxAdminLoginBodyBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxAdminLoginBodyBytes {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_admin_login")
		return
	}
	var input AdminLogin
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Schema != AdminLoginSchema {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_admin_login")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_admin_login")
		return
	}
	session, err := handler.authority.Login(input.AccessKey)
	if err != nil {
		switch {
		case errors.Is(err, serveradmin.ErrUnauthorized):
			writeProblem(writer, http.StatusUnauthorized, "admin_access_denied")
		case errors.Is(err, serveradmin.ErrSessionCapacity):
			writeProblem(writer, http.StatusServiceUnavailable, "admin_session_capacity")
		default:
			writeProblem(writer, http.StatusServiceUnavailable, "admin_session_unavailable")
		}
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(writer).Encode(AdminSession{
		Schema: AdminSessionSchema, InstanceID: handler.instanceID, APIVersion: "v1",
		ReadToken: session.ReadToken.Value(), WriteToken: session.WriteToken.Value(),
		ExpiresAt: session.ExpiresAt,
	})
}
