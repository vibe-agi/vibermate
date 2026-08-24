package servercontrol

import (
	"errors"
	"net/http"
)

const (
	ServerAccessPath                  = "/api/v1/server/access"
	ServerAccessSchema                = "vibermate-server-access-v1"
	RuntimeUserPasswordAuthentication = "runtime_user_password"
	ReusableLoginSessionPolicy        = "reusable_until_logout_disable_or_expiry"
)

type ServerAccessOptions struct {
	Transport string
}

type ServerAccess struct {
	Schema         string `json:"schema"`
	Transport      string `json:"transport"`
	Authentication string `json:"authentication"`
	SessionPolicy  string `json:"sessionPolicy"`
}

type ServerAccessHandler struct {
	access ServerAccess
}

func NewServerAccess(options ServerAccessOptions) (*ServerAccessHandler, error) {
	if options.Transport != "http" && options.Transport != "https" {
		return nil, errors.New("Runtime Server access transport is invalid")
	}
	return &ServerAccessHandler{access: ServerAccess{
		Schema: ServerAccessSchema, Transport: options.Transport,
		Authentication: RuntimeUserPasswordAuthentication,
		SessionPolicy:  ReusableLoginSessionPolicy,
	}}, nil
}

func (handler *ServerAccessHandler) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if handler == nil || request == nil || request.Method != http.MethodGet ||
		request.URL.Path != ServerAccessPath || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
		return
	}
	writeServerJSON(writer, http.StatusOK, handler.access)
}
