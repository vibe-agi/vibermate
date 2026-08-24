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

	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

const (
	RuntimeUsersPath        = "/api/v1/server/runtime-users"
	RuntimeUserUsagePath    = RuntimeUsersPath + "/usage"
	RuntimeUserCreateSchema = "vibermate-runtime-user-create-v1"
	RuntimeUserUpdateSchema = "vibermate-runtime-user-update-v1"
	RuntimeUserListSchema   = "vibermate-runtime-user-list-v1"
	maxRuntimeUserBodyBytes = 16 << 10
)

type RuntimeUsageReader interface {
	Report(context.Context) (runtimeusage.Report, error)
}

type RuntimeUsersOptions struct {
	Users *runtimeuser.Manager
	Usage RuntimeUsageReader
}

type RuntimeUsersHandler struct {
	users *runtimeuser.Manager
	usage RuntimeUsageReader
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

type RuntimeUserAdminView struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type RuntimeUserList struct {
	Schema string                 `json:"schema"`
	Items  []RuntimeUserAdminView `json:"items"`
}

func NewRuntimeUsers(options RuntimeUsersOptions) (*RuntimeUsersHandler, error) {
	if options.Users == nil || options.Usage == nil {
		return nil, errors.New("Runtime User management dependencies are incomplete")
	}
	return &RuntimeUsersHandler{users: options.Users, usage: options.Usage}, nil
}

func (handler *RuntimeUsersHandler) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if handler == nil || request == nil || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
		return
	}
	if request.URL.Path == RuntimeUserUsagePath {
		if request.Method != http.MethodGet {
			writeProblem(writer, http.StatusNotFound, "server_route_not_found")
			return
		}
		report, err := handler.usage.Report(request.Context())
		if err != nil {
			writeProblem(writer, http.StatusServiceUnavailable, "runtime_user_usage_unavailable")
			return
		}
		writeServerJSON(writer, http.StatusOK, report)
		return
	}
	if strings.HasPrefix(request.URL.Path, RuntimeUsersPath+"/") {
		if request.Method != http.MethodPatch {
			writeProblem(writer, http.StatusNotFound, "server_route_not_found")
			return
		}
		handler.update(writer, request)
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

func (handler *RuntimeUsersHandler) update(
	writer http.ResponseWriter,
	request *http.Request,
) {
	id := runtimeuser.UserID(strings.TrimPrefix(request.URL.Path, RuntimeUsersPath+"/"))
	if !id.Valid() {
		writeProblem(writer, http.StatusNotFound, "runtime_user_not_found")
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
	writeServerJSON(writer, http.StatusOK, runtimeUserAdminView(updated))
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
		items[index] = runtimeUserAdminView(user)
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
	writeServerJSON(writer, http.StatusCreated, runtimeUserAdminView(created))
}

func runtimeUserAdminView(user runtimeuser.User) RuntimeUserAdminView {
	return RuntimeUserAdminView{
		ID: string(user.ID), Username: user.Username, State: string(user.State),
		CreatedAt: user.CreatedAt, UpdatedAt: user.UpdatedAt,
	}
}

func writeServerJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
