package servercontrol

import (
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/serverconnection"
)

const (
	ServerAccessPath                  = "/api/v1/server/access"
	ServerAccessSchema                = "vibermate-server-access-v1"
	RuntimeUserPasswordAuthentication = "runtime_user_password"
	ReusableLoginSessionPolicy        = "reusable_until_logout_disable_or_expiry"
)

type ServerAccessOptions struct {
	Transport string
	Targets   []string
	TLS       func() ServerTLS
}

type ServerTLS struct {
	Mode        string `json:"mode"`
	State       string `json:"state"`
	ServerName  string `json:"serverName,omitempty"`
	Challenge   string `json:"challenge,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Issuer      string `json:"issuer,omitempty"`
	NotBefore   string `json:"notBefore,omitempty"`
	NotAfter    string `json:"notAfter,omitempty"`
	LastError   string `json:"lastError,omitempty"`
}

type ServerAccess struct {
	Schema         string    `json:"schema"`
	Transport      string    `json:"transport"`
	Authentication string    `json:"authentication"`
	SessionPolicy  string    `json:"sessionPolicy"`
	Targets        []string  `json:"targets"`
	TLS            ServerTLS `json:"tls"`
}

type ServerAccessHandler struct {
	access ServerAccess
	tls    func() ServerTLS
}

func NewServerAccess(options ServerAccessOptions) (*ServerAccessHandler, error) {
	if (options.Transport != "http" && options.Transport != "https") ||
		!validServerTargets(options.Targets) {
		return nil, errors.New("Runtime Server access transport is invalid")
	}
	tlsStatus := options.TLS
	if tlsStatus == nil {
		tlsStatus = func() ServerTLS {
			if options.Transport == "http" {
				return ServerTLS{Mode: "http", State: "disabled"}
			}
			return ServerTLS{Mode: "unknown", State: "unavailable"}
		}
	}
	if !validServerTLS(options.Transport, tlsStatus()) {
		return nil, errors.New("Runtime Server TLS status is invalid")
	}
	return &ServerAccessHandler{access: ServerAccess{
		Schema: ServerAccessSchema, Transport: options.Transport,
		Authentication: RuntimeUserPasswordAuthentication,
		SessionPolicy:  ReusableLoginSessionPolicy,
		Targets:        slices.Clone(options.Targets),
	}, tls: tlsStatus}, nil
}

func validServerTLS(transport string, status ServerTLS) bool {
	if status.Mode == "" || status.State == "" ||
		!validOptionalFingerprint(status.Fingerprint) ||
		!validOptionalTLSRange(status.NotBefore, status.NotAfter) {
		return false
	}
	if transport == "http" {
		return status.Mode == "http" && status.State == "disabled" &&
			status.ServerName == "" && status.Challenge == "" &&
			status.Fingerprint == "" && status.Issuer == "" &&
			status.NotBefore == "" && status.NotAfter == "" && status.LastError == ""
	}
	switch status.Mode {
	case "private_ca_tls", "self_signed_tls", "tls_files":
		return status.State == "ready" && status.Challenge == "" &&
			status.ServerName == "" && status.LastError == "" && status.Fingerprint != ""
	case "automatic_tls":
		if status.ServerName == "" ||
			(status.Challenge != "http_01" && status.Challenge != "tls_alpn_01") {
			return false
		}
		switch status.State {
		case "pending":
			return status.Fingerprint == "" && status.LastError == ""
		case "ready", "renewing":
			return status.Fingerprint != "" && status.LastError == ""
		case "renewal_failed":
			return status.LastError != ""
		default:
			return false
		}
	case "unknown":
		return status.State == "unavailable" && status.ServerName == "" &&
			status.Challenge == "" && status.Fingerprint == "" &&
			status.Issuer == "" && status.NotBefore == "" &&
			status.NotAfter == "" && status.LastError == ""
	default:
		return false
	}
}

func validOptionalFingerprint(value string) bool {
	if value == "" {
		return true
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}

func validOptionalTLSRange(notBefore, notAfter string) bool {
	if notBefore == "" && notAfter == "" {
		return true
	}
	if notBefore == "" || notAfter == "" {
		return false
	}
	before, beforeErr := time.Parse(time.RFC3339, notBefore)
	after, afterErr := time.Parse(time.RFC3339, notAfter)
	return beforeErr == nil && afterErr == nil && after.After(before)
}

func validServerTargets(targets []string) bool {
	if len(targets) == 0 || len(targets) > 32 {
		return false
	}
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		address, err := serverconnection.ParseAddress(target)
		if err != nil {
			return false
		}
		if target != address.String() {
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
	access := handler.access
	access.TLS = handler.tls()
	if !validServerTLS(access.Transport, access.TLS) {
		writeProblem(writer, http.StatusServiceUnavailable, "server_tls_status_unavailable")
		return
	}
	writeServerJSON(writer, http.StatusOK, access)
}
