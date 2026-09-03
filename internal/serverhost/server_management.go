package serverhost

import (
	"net/http"
	"strings"

	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

// serverManagementRouter is the credential-free inner handler shared with a
// co-located Desktop Host. Authentication terminates at the outer Desktop or
// Server router; this adapter only dispatches the already-authorized request.
type serverManagementRouter struct {
	access       http.Handler
	runtimeUsers http.Handler
}

func (router serverManagementRouter) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request == nil {
		serverProblem(writer, http.StatusNotFound, "server_route_not_found")
		return
	}
	switch {
	case request.URL.Path == servercontrol.ServerAccessPath:
		router.access.ServeHTTP(writer, request)
	case request.URL.Path == servercontrol.RuntimeUsersPath ||
		strings.HasPrefix(request.URL.Path, servercontrol.RuntimeUsersPath+"/"):
		router.runtimeUsers.ServeHTTP(writer, request)
	default:
		serverProblem(writer, http.StatusNotFound, "server_route_not_found")
	}
}
