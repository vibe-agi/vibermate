package servercontrol

import (
	"context"
	"errors"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/serveradmin"
)

const WebSelfUsagePath = "/api/v1/server/me/usage"

type WebSelfUsageProjector interface {
	ReportForUser(
		ctx context.Context,
		query runtimeusage.AggregationQuery,
		userID runtimeuser.UserID,
	) (runtimeusage.Report, error)
}

type WebSelfOptions struct {
	Sessions *serveradmin.Authority
	Usage    WebSelfUsageProjector
}

type WebSelfHandler struct {
	sessions *serveradmin.Authority
	usage    WebSelfUsageProjector
}

func NewWebSelf(options WebSelfOptions) (*WebSelfHandler, error) {
	if options.Sessions == nil || options.Usage == nil {
		return nil, errors.New("Runtime Server self-service dependencies are incomplete")
	}
	return &WebSelfHandler{sessions: options.Sessions, usage: options.Usage}, nil
}

func (handler *WebSelfHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler == nil || request == nil || request.Method != http.MethodGet ||
		request.URL.Path != WebSelfUsagePath {
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
		return
	}
	principal, valid := takeWebPrincipal(writer, request, handler.sessions, serveradmin.ScopeRead)
	if !valid {
		return
	}
	query, err := runtimeUsageQuery(request.URL.RawQuery)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_usage_query")
		return
	}
	report, err := handler.usage.ReportForUser(request.Context(), query, principal.UserID)
	if errors.Is(err, runtimeusage.ErrSnapshotChanged) {
		writeProblem(writer, http.StatusConflict, "usage_snapshot_changed")
		return
	}
	if errors.Is(err, runtimeusage.ErrInvalidQuery) {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_runtime_usage_query")
		return
	}
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, "runtime_usage_unavailable")
		return
	}
	writeServerJSON(writer, http.StatusOK, report)
}
