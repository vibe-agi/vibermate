package serverhost

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/serveradmin"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

type router struct {
	scheme        string
	userSessions  http.Handler
	runtimeUsers  http.Handler
	access        http.Handler
	capture       http.Handler
	proxy         http.Handler
	adminSessions http.Handler
	admin         *serveradmin.Authority
	application   *desktopcontrol.Handler
	managementUI  http.Handler
}

func (handler router) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request == nil || !handler.matchesTransport(request) {
		serverProblem(writer, http.StatusForbidden, "server_transport_mismatch")
		return
	}
	if request.Method == http.MethodConnect || request.URL.IsAbs() {
		handler.proxy.ServeHTTP(writer, request)
		return
	}
	switch {
	case request.URL.Path == servercontrol.RuntimeUserSessionPath ||
		request.URL.Path == servercontrol.RuntimeUserCurrentSessionPath:
		handler.userSessions.ServeHTTP(writer, request)
	case request.URL.Path == servercontrol.RuntimeUsersPath ||
		strings.HasPrefix(request.URL.Path, servercontrol.RuntimeUsersPath+"/"):
		if !validAdminTransport(request, handler.scheme) ||
			!handler.authorizeAdmin(request, runtimeUsersScope(request.Method)) {
			serverProblem(writer, http.StatusUnauthorized, "server_admin_unauthorized")
			return
		}
		handler.runtimeUsers.ServeHTTP(writer, request)
	case request.URL.Path == servercontrol.AdminSessionPath:
		if !validAdminTransport(request, handler.scheme) {
			serverProblem(writer, http.StatusForbidden, "server_admin_transport_rejected")
			return
		}
		handler.adminSessions.ServeHTTP(writer, request)
	case request.URL.Path == servercontrol.ServerAccessPath:
		if !validAdminTransport(request, handler.scheme) ||
			!handler.authorizeAdmin(request, serveradmin.ScopeRead) {
			serverProblem(writer, http.StatusUnauthorized, "server_admin_unauthorized")
			return
		}
		handler.access.ServeHTTP(writer, request)
	case request.URL.Path == "/api/v1/capture-runs" ||
		strings.HasPrefix(request.URL.Path, "/api/v1/capture-runs/"):
		handler.capture.ServeHTTP(writer, request)
	case strings.HasPrefix(request.URL.Path, "/api/v1/"):
		if !validAdminTransport(request, handler.scheme) || handler.application == nil {
			serverProblem(writer, http.StatusForbidden, "server_admin_transport_rejected")
			return
		}
		scope := applicationScope(handler.application.RequiredScope(request))
		if !handler.authorizeAdmin(request, scope) {
			serverProblem(writer, http.StatusUnauthorized, "server_admin_unauthorized")
			return
		}
		handler.application.ServeHTTP(writer, request)
	case handler.managementUI != nil:
		handler.managementUI.ServeHTTP(writer, request)
	default:
		serverProblem(writer, http.StatusNotFound, "server_route_not_found")
	}
}

func (handler router) matchesTransport(request *http.Request) bool {
	if request == nil {
		return false
	}
	switch handler.scheme {
	case "http":
		return request.TLS == nil
	case "https":
		return request.TLS != nil
	default:
		return false
	}
}

func applicationScope(scope desktopcontrol.Scope) serveradmin.Scope {
	switch scope {
	case desktopcontrol.ScopeRead:
		return serveradmin.ScopeRead
	case desktopcontrol.ScopeWrite:
		return serveradmin.ScopeWrite
	default:
		return ""
	}
}

func (handler router) authorizeAdmin(request *http.Request, scope serveradmin.Scope) bool {
	if handler.admin == nil || request == nil || !scope.Valid() {
		return false
	}
	values := request.Header.Values("Authorization")
	request.Header.Del("Authorization")
	if len(values) != 1 {
		return false
	}
	value, found := strings.CutPrefix(values[0], "Bearer ")
	return found && handler.admin.Authorize(value, scope)
}

func runtimeUsersScope(method string) serveradmin.Scope {
	if method == http.MethodGet {
		return serveradmin.ScopeRead
	}
	if method == http.MethodPost || method == http.MethodPatch {
		return serveradmin.ScopeWrite
	}
	return ""
}

func validAdminTransport(request *http.Request, scheme string) bool {
	if request == nil || (scheme != "http" && scheme != "https") || request.Host == "" ||
		request.Header.Get("Proxy-Authorization") != "" {
		return false
	}
	for _, name := range []string{
		"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Original-Host",
	} {
		if len(request.Header.Values(name)) != 0 {
			return false
		}
	}
	origin := request.Header.Get("Origin")
	if origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != scheme || parsed.Host != request.Host ||
			parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return false
		}
	}
	site := request.Header.Get("Sec-Fetch-Site")
	return site == "" || site == "none" || site == "same-origin"
}

func serverProblem(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"type":   "urn:vibermate:error:" + strings.ReplaceAll(code, "_", "-"),
		"title":  http.StatusText(status),
		"status": status,
		"code":   code,
	})
}
