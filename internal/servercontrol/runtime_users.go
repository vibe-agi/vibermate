package servercontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/serveradmin"
)

const (
	RuntimeUsersPath          = "/api/v1/server/runtime-users"
	RuntimeUserUsagePath      = RuntimeUsersPath + "/usage"
	RuntimeUserCreateSchema   = "vibermate-runtime-user-create-v1"
	RuntimeUserUpdateSchema   = "vibermate-runtime-user-update-v1"
	RuntimeUserPasswordSchema = "vibermate-runtime-user-password-v1"
	RuntimeUserListSchema     = "vibermate-runtime-user-list-v1"
	maxRuntimeUserBodyBytes   = 16 << 10
)

type RuntimeUsageReader interface {
	Report(context.Context, runtimeusage.Query) (runtimeusage.Report, error)
}

type RuntimeUsersOptions struct {
	Users                   *runtimeuser.Manager
	Usage                   RuntimeUsageReader
	Sessions                RuntimeUserWebSessions
	AllowOwnerPasswordReset bool
}

type RuntimeUserWebSessions interface {
	IsOwner(runtimeuser.UserID) bool
	EnsureOwner(runtimeuser.UserID) (bool, error)
	RevokeUserSessions(runtimeuser.UserID)
}

type RuntimeUsersHandler struct {
	users                   *runtimeuser.Manager
	usage                   RuntimeUsageReader
	sessions                RuntimeUserWebSessions
	allowOwnerPasswordReset bool
}

type RuntimeUserCreate struct {
	Schema   string `json:"schema"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type RuntimeUserUpdate struct {
	Schema string `json:"schema"`
	State  string `json:"state"`
}

type RuntimeUserPassword struct {
	Schema   string `json:"schema"`
	Password string `json:"password"`
}

type RuntimeUserAdminView struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Role      string    `json:"role"`
}

type RuntimeUserList struct {
	Schema string                 `json:"schema"`
	Items  []RuntimeUserAdminView `json:"items"`
}

func NewRuntimeUsers(options RuntimeUsersOptions) (*RuntimeUsersHandler, error) {
	if options.Users == nil || options.Usage == nil || options.Sessions == nil {
		return nil, errors.New("Runtime User management dependencies are incomplete")
	}
	return &RuntimeUsersHandler{
		users: options.Users, usage: options.Usage, sessions: options.Sessions,
		allowOwnerPasswordReset: options.AllowOwnerPasswordReset,
	}, nil
}

func (handler *RuntimeUsersHandler) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if handler == nil || request == nil {
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
		return
	}
	if request.URL.Path == RuntimeUserUsagePath {
		if request.Method != http.MethodGet {
			writeProblem(writer, http.StatusNotFound, "server_route_not_found")
			return
		}
		query, err := runtimeUsageQuery(request.URL.RawQuery)
		if err != nil {
			writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_usage_query")
			return
		}
		report, err := handler.usage.Report(request.Context(), query)
		if err != nil {
			writeProblem(writer, http.StatusServiceUnavailable, "runtime_user_usage_unavailable")
			return
		}
		writeServerJSON(writer, http.StatusOK, report)
		return
	}
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
		return
	}
	if strings.HasPrefix(request.URL.Path, RuntimeUsersPath+"/") {
		if request.Method != http.MethodPatch {
			writeProblem(writer, http.StatusNotFound, "server_route_not_found")
			return
		}
		if strings.HasSuffix(request.URL.Path, "/password") {
			handler.replacePassword(writer, request)
		} else {
			handler.update(writer, request)
		}
		return
	}
	if request.URL.Path != RuntimeUsersPath {
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
		return
	}
	switch request.Method {
	case http.MethodGet:
		handler.list(writer, request)
	case http.MethodPost:
		handler.create(writer, request)
	default:
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
	}
}

func runtimeUsageQuery(rawQuery string) (runtimeusage.Query, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil || len(values) != 3 ||
		len(values["from"]) != 1 || len(values["until"]) != 1 ||
		len(values["timeZone"]) != 1 {
		return runtimeusage.Query{}, runtimeusage.ErrInvalidQuery
	}
	return runtimeusage.NewQuery(
		values.Get("from"), values.Get("until"), values.Get("timeZone"),
	)
}

func (handler *RuntimeUsersHandler) update(
	writer http.ResponseWriter,
	request *http.Request,
) {
	id := runtimeuser.UserID(strings.TrimPrefix(request.URL.Path, RuntimeUsersPath+"/"))
	if !id.Valid() {
		writeProblem(writer, http.StatusNotFound, "runtime_user_not_found")
		return
	}
	if handler.sessions.IsOwner(id) {
		writeProblem(writer, http.StatusConflict, "runtime_owner_protected")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_update")
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxRuntimeUserBodyBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxRuntimeUserBodyBytes {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_update")
		return
	}
	var input RuntimeUserUpdate
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil ||
		input.Schema != RuntimeUserUpdateSchema || input.State != string(runtimeuser.StateDisabled) {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_update")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_update")
		return
	}
	updated, err := handler.users.Disable(request.Context(), id)
	if err != nil {
		if errors.Is(err, runtimeuser.ErrInvalidUser) {
			writeProblem(writer, http.StatusNotFound, "runtime_user_not_found")
		} else {
			writeProblem(writer, http.StatusServiceUnavailable, "runtime_user_update_unavailable")
		}
		return
	}
	handler.sessions.RevokeUserSessions(id)
	writeServerJSON(writer, http.StatusOK, handler.runtimeUserAdminView(updated))
}

func (handler *RuntimeUsersHandler) replacePassword(
	writer http.ResponseWriter,
	request *http.Request,
) {
	rawID := strings.TrimSuffix(
		strings.TrimPrefix(request.URL.Path, RuntimeUsersPath+"/"),
		"/password",
	)
	id := runtimeuser.UserID(rawID)
	if !id.Valid() || rawID == "" || strings.Contains(rawID, "/") {
		writeProblem(writer, http.StatusNotFound, "runtime_user_not_found")
		return
	}
	if handler.sessions.IsOwner(id) && !handler.allowOwnerPasswordReset {
		writeProblem(writer, http.StatusConflict, "runtime_owner_password_requires_verification")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_password")
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxRuntimeUserBodyBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxRuntimeUserBodyBytes {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_password")
		return
	}
	var input RuntimeUserPassword
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil ||
		input.Schema != RuntimeUserPasswordSchema {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_password")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_password")
		return
	}
	password := []byte(input.Password)
	input.Password = ""
	defer clear(password)
	updated, err := handler.users.ReplacePassword(request.Context(), id, password)
	if err != nil {
		if errors.Is(err, runtimeuser.ErrInvalidUser) {
			writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_password")
		} else {
			writeProblem(writer, http.StatusServiceUnavailable, "runtime_user_password_unavailable")
		}
		return
	}
	handler.sessions.RevokeUserSessions(id)
	writeServerJSON(writer, http.StatusOK, handler.runtimeUserAdminView(updated))
}

func (handler *RuntimeUsersHandler) list(
	writer http.ResponseWriter,
	request *http.Request,
) {
	users, err := handler.users.List(request.Context())
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "runtime_user_list_unavailable")
		return
	}
	items := make([]RuntimeUserAdminView, len(users))
	for index, user := range users {
		items[index] = handler.runtimeUserAdminView(user)
	}
	writeServerJSON(writer, http.StatusOK, RuntimeUserList{
		Schema: RuntimeUserListSchema,
		Items:  items,
	})
}

func (handler *RuntimeUsersHandler) create(
	writer http.ResponseWriter,
	request *http.Request,
) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user")
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxRuntimeUserBodyBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxRuntimeUserBodyBytes {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user")
		return
	}
	var input RuntimeUserCreate
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Schema != RuntimeUserCreateSchema {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user")
		return
	}
	password := []byte(input.Password)
	input.Password = ""
	created, err := handler.users.Create(request.Context(), runtimeuser.CreateCommand{
		Username: input.Username,
		Password: password,
	})
	clear(password)
	if err != nil {
		switch {
		case errors.Is(err, runtimeuser.ErrUsernameConflict):
			writeProblem(writer, http.StatusConflict, "runtime_username_conflict")
		case errors.Is(err, runtimeuser.ErrInvalidUser):
			writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user")
		default:
			writeProblem(writer, http.StatusServiceUnavailable, "runtime_user_create_unavailable")
		}
		return
	}
	if _, err := handler.sessions.EnsureOwner(created.ID); err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "runtime_owner_create_unavailable")
		return
	}
	writeServerJSON(writer, http.StatusCreated, handler.runtimeUserAdminView(created))
}

func (handler *RuntimeUsersHandler) runtimeUserAdminView(
	user runtimeuser.User,
) RuntimeUserAdminView {
	role := string(serveradmin.RoleMember)
	if handler.sessions.IsOwner(user.ID) {
		role = string(serveradmin.RoleOwner)
	}
	return RuntimeUserAdminView{
		ID: string(user.ID), Username: user.Username, State: string(user.State),
		CreatedAt: user.CreatedAt, UpdatedAt: user.UpdatedAt, Role: role,
	}
}

func writeServerJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
