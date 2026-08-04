package desktopcontrol

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/vibe-agi/vibermate/internal/controlprincipal"
)

type RouterOptions struct {
	Authority        string
	AllowedOrigins   []string
	Authenticator    *Authenticator
	Application      *Handler
	Bootstrap        http.Handler
	CLIControl       http.Handler
	ManualCaptures   ManualCaptureHandler
	DesktopPrincipal controlprincipal.Principal
}

type ManualCaptureHandler interface {
	ServeHTTP(
		http.ResponseWriter,
		*http.Request,
		controlprincipal.Principal,
	)
}

type Router struct {
	authority        string
	origins          map[string]struct{}
	authenticator    *Authenticator
	application      *Handler
	bootstrap        http.Handler
	cliControl       http.Handler
	manualCaptures   ManualCaptureHandler
	desktopPrincipal controlprincipal.Principal
	closing          atomic.Bool
}

func NewRouter(options RouterOptions) (*Router, error) {
	if options.Authority == "" ||
		options.Authenticator == nil ||
		options.Application == nil ||
		options.Bootstrap == nil ||
		options.CLIControl == nil ||
		options.ManualCaptures == nil ||
		!options.DesktopPrincipal.Valid() ||
		options.DesktopPrincipal.Kind() != controlprincipal.KindDesktopApp ||
		!options.DesktopPrincipal.Allows(controlprincipal.GrantManualCapture) ||
		len(options.AllowedOrigins) == 0 {
		return nil, errors.New("Desktop control router dependencies are incomplete")
	}
	host, port, err := net.SplitHostPort(options.Authority)
	if err != nil || host != "127.0.0.1" || port == "" {
		return nil, errors.New("Desktop control authority must be literal IPv4 loopback")
	}
	origins := make(map[string]struct{}, len(options.AllowedOrigins))
	for _, raw := range options.AllowedOrigins {
		parsed, err := url.Parse(raw)
		if err != nil ||
			parsed.Scheme == "" ||
			parsed.Host == "" ||
			parsed.User != nil ||
			parsed.Path != "" ||
			parsed.RawQuery != "" ||
			parsed.Fragment != "" {
			return nil, errors.New("Desktop control Origin allowlist is invalid")
		}
		if _, duplicate := origins[raw]; duplicate {
			return nil, errors.New("Desktop control Origin allowlist contains a duplicate")
		}
		origins[raw] = struct{}{}
	}
	return &Router{
		authority:        options.Authority,
		origins:          origins,
		authenticator:    options.Authenticator,
		application:      options.Application,
		bootstrap:        options.Bootstrap,
		cliControl:       options.CLIControl,
		manualCaptures:   options.ManualCaptures,
		desktopPrincipal: options.DesktopPrincipal,
	}, nil
}

func (router *Router) BeginShutdown() {
	if router != nil {
		router.closing.Store(true)
	}
}

func (router *Router) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if router == nil ||
		router.closing.Load() ||
		!router.validTransport(request) {
		writeProblem(writer, http.StatusForbidden, ReasonUnauthorized)
		return
	}
	if request.URL.Path == "/api/v1/auth/sessions" {
		if !router.validCLIControlTransport(request) {
			writeProblem(writer, http.StatusForbidden, ReasonUnauthorized)
			return
		}
		router.bootstrap.ServeHTTP(writer, request)
		return
	}
	// The verb chooses the authority here, because they are different acts on
	// the same resource. Creating a run and controlling one belong to the
	// local CLI and its per-run capability; reading the list is a person
	// looking at their own machine, and it reaches the app the same way every
	// other read does. Design 15 lists them that way.
	if capturePath(request.URL.Path) && request.Method != http.MethodGet {
		if !router.validCLIControlTransport(request) {
			writeProblem(writer, http.StatusForbidden, ReasonUnauthorized)
			return
		}
		router.cliControl.ServeHTTP(writer, request)
		return
	}
	// A GET below the collection is still a control path shape, and nothing
	// serves it. It must not fall through to the app and read as a route the
	// app declined.
	if capturePath(request.URL.Path) &&
		request.URL.Path != "/api/v1/capture-runs" {
		if !router.validCLIControlTransport(request) {
			writeProblem(writer, http.StatusForbidden, ReasonUnauthorized)
			return
		}
		router.cliControl.ServeHTTP(writer, request)
		return
	}
	if manualCapturePath(request.URL.Path) &&
		router.validCLIControlTransport(request) {
		router.cliControl.ServeHTTP(writer, request)
		return
	}
	origin := request.Header.Get("Origin")
	if _, allowed := router.origins[origin]; !allowed ||
		!validFetchMetadata(request) {
		writeProblem(writer, http.StatusForbidden, ReasonUnauthorized)
		return
	}
	setCORS(writer, origin)
	if request.Method == http.MethodOptions {
		router.preflight(writer, request)
		return
	}
	if request.Method == http.MethodGet && request.URL.Path == SessionStatePath {
		state, err := router.authenticator.InspectSession(request)
		if err != nil {
			if errors.Is(err, errSessionInvalid) {
				writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			} else {
				writeProblem(writer, http.StatusUnauthorized, ReasonUnauthorized)
			}
			return
		}
		writeJSON(writer, http.StatusOK, state)
		return
	}
	if request.Method == http.MethodPost && request.URL.Path == SessionRenewalPath {
		rotation, err := router.authenticator.RenewSession(request)
		if err != nil {
			switch {
			case errors.Is(err, errSessionUnauthorized):
				writeProblem(writer, http.StatusUnauthorized, ReasonUnauthorized)
			case errors.Is(err, errSessionInvalid):
				writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			case errors.Is(err, errSessionConflict):
				writeProblem(writer, http.StatusConflict, ReasonRevisionConflict)
			default:
				writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
			}
			return
		}
		writeJSON(writer, http.StatusOK, rotation)
		return
	}
	if manualCapturePath(request.URL.Path) {
		scope := requestScope(request)
		if scope == "" || !router.authenticator.Authorize(request, scope) {
			writeProblem(writer, http.StatusUnauthorized, ReasonUnauthorized)
			return
		}
		// Authentication terminates at this router. The shared business handler
		// receives an authenticated principal, never the credential that proved
		// it, even though its current implementation does not forward requests.
		request.Header.Del("Authorization")
		router.manualCaptures.ServeHTTP(
			writer,
			request,
			router.desktopPrincipal,
		)
		return
	}
	scope := router.application.RequiredScope(request)
	if scope == "" || !router.authenticator.Authorize(request, scope) {
		writeProblem(writer, http.StatusUnauthorized, ReasonUnauthorized)
		return
	}
	router.application.ServeHTTP(writer, request)
}

func (router *Router) validTransport(request *http.Request) bool {
	if request == nil ||
		request.Host != router.authority ||
		request.Header.Get("Proxy-Authorization") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || host != "127.0.0.1" {
		return false
	}
	for _, name := range []string{
		"Forwarded",
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Proto",
		"X-Original-Host",
	} {
		if len(request.Header.Values(name)) != 0 {
			return false
		}
	}
	return true
}

// capturePath reports the capture-run collection and everything below it.
func capturePath(path string) bool {
	return path == "/api/v1/capture-runs" ||
		strings.HasPrefix(path, "/api/v1/capture-runs/")
}

func manualCapturePath(path string) bool {
	return path == "/api/v1/manual-captures" ||
		strings.HasPrefix(path, "/api/v1/manual-captures/")
}

func requestScope(request *http.Request) Scope {
	if request == nil {
		return ""
	}
	if request.Method == http.MethodGet {
		return ScopeRead
	}
	if request.Method == http.MethodPost {
		return ScopeWrite
	}
	return ""
}

func (router *Router) validCLIControlTransport(request *http.Request) bool {
	return request.Header.Get("Origin") == "" &&
		request.Header.Get("Sec-Fetch-Site") == "" &&
		request.Header.Get("Sec-Fetch-Mode") == "" &&
		request.Header.Get("Sec-Fetch-Dest") == ""
}

func (router *Router) preflight(
	writer http.ResponseWriter,
	request *http.Request,
) {
	method := request.Header.Get("Access-Control-Request-Method")
	switch method {
	case http.MethodGet,
		http.MethodPut,
		http.MethodPost,
		http.MethodPatch:
	default:
		writeProblem(writer, http.StatusForbidden, ReasonUnauthorized)
		return
	}
	requested := strings.ToLower(
		request.Header.Get("Access-Control-Request-Headers"),
	)
	for _, header := range strings.Split(requested, ",") {
		switch strings.TrimSpace(header) {
		case "", "authorization", "content-type", "idempotency-key", "if-match":
		default:
			writeProblem(writer, http.StatusForbidden, ReasonUnauthorized)
			return
		}
	}
	writer.Header().Set("Access-Control-Allow-Methods", method)
	writer.Header().Set(
		"Access-Control-Allow-Headers",
		"Authorization, Content-Type, Idempotency-Key, If-Match",
	)
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func validFetchMetadata(request *http.Request) bool {
	site := request.Header.Get("Sec-Fetch-Site")
	if site != "" && site != "cross-site" {
		return false
	}
	mode := request.Header.Get("Sec-Fetch-Mode")
	if mode != "" && mode != "cors" {
		return false
	}
	destination := request.Header.Get("Sec-Fetch-Dest")
	return destination == "" || destination == "empty"
}

func setCORS(writer http.ResponseWriter, origin string) {
	writer.Header().Set("Access-Control-Allow-Origin", origin)
	writer.Header().Add("Vary", "Origin")
}
