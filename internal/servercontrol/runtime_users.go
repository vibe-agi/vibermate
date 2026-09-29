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
	"sort"
	"strconv"
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
	RuntimeUserPolicySchema   = "vibermate-runtime-user-policy-v1"
	RuntimeUserListSchema     = "vibermate-runtime-user-list-v1"
	maxRuntimeUserBodyBytes   = 16 << 10
)

type RuntimeUsageReader interface {
	Report(context.Context, runtimeusage.AggregationQuery) (runtimeusage.Report, error)
	SetCollectionPolicy(context.Context, runtimeusage.CollectionPolicy) (runtimeusage.CollectionPolicy, error)
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

type RuntimeUserPolicyUpdate struct {
	Schema                   string   `json:"schema"`
	AllEnvironments          bool     `json:"allEnvironments"`
	AllowedEnvironmentIDs    []string `json:"allowedEnvironmentIds"`
	DailyAgentAPICallWarning int64    `json:"dailyAgentApiCallWarning"`
	DailyTokenWarning        int64    `json:"dailyTokenWarning"`
}

type RuntimeUserAdminView struct {
	ID                       string    `json:"id"`
	Username                 string    `json:"username"`
	State                    string    `json:"state"`
	CreatedAt                time.Time `json:"createdAt"`
	UpdatedAt                time.Time `json:"updatedAt"`
	Role                     string    `json:"role"`
	AllEnvironments          bool      `json:"allEnvironments"`
	AllowedEnvironmentIDs    []string  `json:"allowedEnvironmentIds"`
	DailyAgentAPICallWarning int64     `json:"dailyAgentApiCallWarning"`
	DailyTokenWarning        int64     `json:"dailyTokenWarning"`
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
	if request.URL.Path == RuntimeUserUsagePath+"/collection" && request.Method == http.MethodPatch && request.URL.RawQuery == "" {
		handler.setUsageCollection(writer, request)
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
		if errors.Is(err, runtimeusage.ErrSnapshotChanged) {
			writeProblem(writer, http.StatusConflict, "usage_snapshot_changed")
			return
		}
		if errors.Is(err, runtimeusage.ErrInvalidQuery) {
			writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_usage_query")
			return
		}
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
		} else if strings.HasSuffix(request.URL.Path, "/policy") {
			handler.updatePolicy(writer, request)
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

func (handler *RuntimeUsersHandler) setUsageCollection(writer http.ResponseWriter, request *http.Request) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_usage_collection_policy")
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxRuntimeUserBodyBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxRuntimeUserBodyBytes {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_usage_collection_policy")
		return
	}
	var input struct {
		Enabled       *bool `json:"enabled"`
		RetentionDays int   `json:"retentionDays"`
		Revision      int64 `json:"revision"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var trailing any
	if err := decoder.Decode(&input); err != nil || input.Enabled == nil || !errors.Is(decoder.Decode(&trailing), io.EOF) {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_usage_collection_policy")
		return
	}
	policy := runtimeusage.CollectionPolicy{Enabled: *input.Enabled, RetentionDays: input.RetentionDays, Revision: input.Revision}
	if err := policy.Validate(); err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_usage_collection_policy")
		return
	}
	updated, err := handler.usage.SetCollectionPolicy(request.Context(), policy)
	if errors.Is(err, runtimeusage.ErrPolicyConflict) {
		writeProblem(writer, http.StatusConflict, "usage_collection_policy_conflict")
		return
	}
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "usage_collection_policy_unavailable")
		return
	}
	writeServerJSON(writer, http.StatusOK, updated)
}

func (handler *RuntimeUsersHandler) updatePolicy(
	writer http.ResponseWriter,
	request *http.Request,
) {
	rawID := strings.TrimSuffix(
		strings.TrimPrefix(request.URL.Path, RuntimeUsersPath+"/"),
		"/policy",
	)
	id := runtimeuser.UserID(rawID)
	if !id.Valid() || rawID == "" || strings.Contains(rawID, "/") {
		writeProblem(writer, http.StatusNotFound, "runtime_user_not_found")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_policy")
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxRuntimeUserBodyBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxRuntimeUserBodyBytes {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_policy")
		return
	}
	var input RuntimeUserPolicyUpdate
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Schema != RuntimeUserPolicySchema {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_policy")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_policy")
		return
	}
	policy, err := runtimeuser.NewPolicy(
		input.AllEnvironments,
		input.AllowedEnvironmentIDs,
		input.DailyAgentAPICallWarning,
		input.DailyTokenWarning,
	)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_policy")
		return
	}
	updated, err := handler.users.SetPolicy(request.Context(), id, policy)
	if err != nil {
		if errors.Is(err, runtimeuser.ErrInvalidUser) {
			writeProblem(writer, http.StatusNotFound, "runtime_user_not_found")
		} else {
			writeProblem(writer, http.StatusServiceUnavailable, "runtime_user_policy_unavailable")
		}
		return
	}
	writeServerJSON(writer, http.StatusOK, handler.runtimeUserAdminView(updated))
}

func runtimeUsageQuery(rawQuery string) (runtimeusage.AggregationQuery, error) {
	if len(rawQuery) > 16<<10 {
		return runtimeusage.AggregationQuery{}, runtimeusage.ErrInvalidQuery
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil ||
		len(values["from"]) != 1 || len(values["until"]) != 1 ||
		len(values["timeZone"]) != 1 {
		return runtimeusage.AggregationQuery{}, runtimeusage.ErrInvalidQuery
	}
	period, err := runtimeusage.NewQuery(values.Get("from"), values.Get("until"), values.Get("timeZone"))
	if err != nil {
		return runtimeusage.AggregationQuery{}, err
	}
	query := runtimeusage.AggregationQuery{Period: period, Dimension: values.Get("groupBy"), Cursor: values.Get("cursor"), Snapshot: values.Get("snapshot")}
	if query.Dimension != "" {
		query.Limit = runtimeusage.MaxGroupPage
	}
	for key, items := range values {
		if len(items) != 1 {
			return runtimeusage.AggregationQuery{}, runtimeusage.ErrInvalidQuery
		}
		switch key {
		case "from", "until", "timeZone", "groupBy", "cursor", "snapshot":
		case "limit":
			query.Limit, err = strconv.Atoi(items[0])
			if err != nil || query.Dimension == "" {
				return runtimeusage.AggregationQuery{}, runtimeusage.ErrInvalidQuery
			}
		default:
			if !strings.HasPrefix(key, "filter.") {
				return runtimeusage.AggregationQuery{}, runtimeusage.ErrInvalidQuery
			}
			query.Filters = append(query.Filters, runtimeusage.Filter{Dimension: strings.TrimPrefix(key, "filter."), ID: items[0]})
		}
	}
	sort.Slice(query.Filters, func(i, j int) bool { return query.Filters[i].Dimension < query.Filters[j].Dimension })
	return query, query.Validate()
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
		input.Schema != RuntimeUserUpdateSchema || (input.State != string(runtimeuser.StateDisabled) && input.State != string(runtimeuser.StateActive)) {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_update")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_user_update")
		return
	}
	var updated runtimeuser.User
	if input.State == string(runtimeuser.StateActive) {
		updated, err = handler.users.Enable(request.Context(), id)
	} else {
		updated, err = handler.users.Disable(request.Context(), id)
	}
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
		AllEnvironments:          user.Policy.AllEnvironments(),
		AllowedEnvironmentIDs:    user.Policy.EnvironmentIDs(),
		DailyAgentAPICallWarning: user.Policy.DailyAgentAPICallWarning,
		DailyTokenWarning:        user.Policy.DailyTokenWarning,
	}
}

func writeServerJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
