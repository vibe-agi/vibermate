package desktopcontrol

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
)

type RouterOptions struct {
	Authority      string
	AllowedOrigins []string
	Authenticator  *Authenticator
	Application    *Handler
	Bootstrap      http.Handler
	CaptureRuns    http.Handler
}

type Router struct {
	authority     string
	origins       map[string]struct{}
	authenticator *Authenticator
	application   *Handler
	bootstrap     http.Handler
	captureRuns   http.Handler
	closing       atomic.Bool
}

func NewRouter(options RouterOptions) (*Router, error) {
	if options.Authority == "" ||
		options.Authenticator == nil ||
		options.Application == nil ||
		options.Bootstrap == nil ||
		options.CaptureRuns == nil ||
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
		authority:     options.Authority,
		origins:       origins,
		authenticator: options.Authenticator,
		application:   options.Application,
		bootstrap:     options.Bootstrap,
		captureRuns:   options.CaptureRuns,
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
		if !router.validLauncherTransport(request) {
			writeProblem(writer, http.StatusForbidden, ReasonUnauthorized)
			return
		}
		router.bootstrap.ServeHTTP(writer, request)
		return
	}
	// The verb chooses the authority here, because they are different acts on
	// the same resource. Creating a run and controlling one belong to the
	// launcher and its per-run capability; reading the list is a person
	// looking at their own machine, and it reaches the app the same way every
	// other read does. Design 15 lists them that way.
	if capturePath(request.URL.Path) && request.Method != http.MethodGet {
		if !router.validLauncherTransport(request) {
			writeProblem(writer, http.StatusForbidden, ReasonUnauthorized)
			return
		}
		router.captureRuns.ServeHTTP(writer, request)
		return
	}
	// A GET below the collection is still a control path shape, and nothing
	// serves it. It must not fall through to the app and read as a route the
	// app declined.
	if capturePath(request.URL.Path) &&
		request.URL.Path != "/api/v1/capture-runs" {
		if !router.validLauncherTransport(request) {
			writeProblem(writer, http.StatusForbidden, ReasonUnauthorized)
			return
		}
		router.captureRuns.ServeHTTP(writer, request)
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

func (router *Router) validLauncherTransport(request *http.Request) bool {
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
		http.MethodPost:
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
