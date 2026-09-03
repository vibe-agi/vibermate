package servercontrol

import (
	"errors"
	"net/http"
	"net/netip"
	"slices"
)

const (
	ServerAccessPath                  = "/api/v1/server/access"
	ServerAccessSchema                = "vibermate-server-access-v2"
	RuntimeUserPasswordAuthentication = "runtime_user_password"
	ReusableLoginSessionPolicy        = "reusable_until_logout_disable_or_expiry"
)

type ServerAccessOptions struct {
	Transport string
	Targets   []string
}

type ServerAccess struct {
	Schema         string   `json:"schema"`
	Transport      string   `json:"transport"`
	Authentication string   `json:"authentication"`
	SessionPolicy  string   `json:"sessionPolicy"`
	Targets        []string `json:"targets"`
}

type ServerAccessHandler struct {
	access ServerAccess
}

func NewServerAccess(options ServerAccessOptions) (*ServerAccessHandler, error) {
	if (options.Transport != "http" && options.Transport != "https") ||
		!validServerTargets(options.Targets) {
		return nil, errors.New("Runtime Server access transport is invalid")
	}
	return &ServerAccessHandler{access: ServerAccess{
		Schema: ServerAccessSchema, Transport: options.Transport,
		Authentication: RuntimeUserPasswordAuthentication,
		SessionPolicy:  ReusableLoginSessionPolicy,
		Targets:        slices.Clone(options.Targets),
	}}, nil
}

func validServerTargets(targets []string) bool {
	if len(targets) == 0 || len(targets) > 32 {
		return false
	}
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		address, err := netip.ParseAddrPort(target)
		if err != nil || !address.Addr().IsValid() || address.Addr().IsUnspecified() ||
			address.Addr().IsMulticast() || address.Port() == 0 {
			return false
		}
		canonical := address.String()
		if target != canonical {
			return false
		}
		if _, duplicate := seen[target]; duplicate {
			return false
		}
		seen[target] = struct{}{}
	}
	return true
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
