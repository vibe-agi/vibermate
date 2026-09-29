package servercontrol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/ipallowlist"
)

const (
	ServerIPAllowlistPath   = "/api/v1/server/ip-allowlist"
	ServerIPAllowlistSchema = "vibermate-server-ip-allowlist-v1"
	maxIPAllowlistBodyBytes = 32 << 10
)

// IPAllowlist is the Server's allowlist as the API sees it: saving also puts
// the list in effect for new and existing connections.
type IPAllowlist interface {
	Current() ipallowlist.Snapshot
	Replace(expected int64, list ipallowlist.List) (ipallowlist.Snapshot, error)
	Refusals() IPRefusals
	// TrustedProxies are the load balancers whose PROXY protocol header names
	// the client. They are deployment configuration, shown read-only.
	TrustedProxies() ipallowlist.List
}

// IPRefusals counts connections refused since the Server started.
type IPRefusals struct {
	Count       uint64
	LastAddress netip.Addr
	LastAt      time.Time
	// ProxyHeaderProblems counts connections from a trusted load balancer
	// that arrived without a valid PROXY protocol header.
	ProxyHeaderProblems uint64
}

type ServerIPAllowlistOptions struct {
	Allowlist IPAllowlist
	// Local serves the co-located App. Its requests never cross the Server
	// listener, so there is no remote requester that a list could lock out.
	Local bool
}

type ServerIPAllowlistHandler struct {
	allowlist IPAllowlist
	local     bool
}

type serverIPAllowlistView struct {
	Schema        string             `json:"schema"`
	Revision      int64              `json:"revision"`
	Ranges        []string           `json:"ranges"`
	UpdatedAt     string             `json:"updatedAt,omitempty"`
	MaxRanges     int                `json:"maxRanges"`
	ClientAddress string             `json:"clientAddress,omitempty"`
	Refused       ipRefusalsResponse `json:"refused"`
	// TrustedProxies are set when the Server starts, never through this API.
	TrustedProxies      []string `json:"trustedProxies"`
	ProxyHeaderProblems uint64   `json:"proxyHeaderProblems"`
}

type ipRefusalsResponse struct {
	Count       uint64 `json:"count"`
	LastAddress string `json:"lastAddress,omitempty"`
	LastAt      string `json:"lastAt,omitempty"`
}

func NewServerIPAllowlist(options ServerIPAllowlistOptions) (*ServerIPAllowlistHandler, error) {
	if options.Allowlist == nil {
		return nil, errors.New("Runtime Server IP allowlist is unavailable")
	}
	return &ServerIPAllowlistHandler{allowlist: options.Allowlist, local: options.Local}, nil
}

func (handler *ServerIPAllowlistHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler == nil || request == nil || request.URL.Path != ServerIPAllowlistPath ||
		request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
		return
	}
	switch request.Method {
	case http.MethodGet:
		writeServerJSON(writer, http.StatusOK, handler.view(handler.allowlist.Current(), request))
	case http.MethodPut:
		handler.replace(writer, request)
	default:
		writeProblem(writer, http.StatusNotFound, "server_route_not_found")
	}
}

func (handler *ServerIPAllowlistHandler) replace(writer http.ResponseWriter, request *http.Request) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_ip_allowlist")
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxIPAllowlistBodyBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxIPAllowlistBodyBytes {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_ip_allowlist")
		return
	}
	var input struct {
		Revision *int64   `json:"revision"`
		Ranges   []string `json:"ranges"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var trailing any
	if err := decoder.Decode(&input); err != nil || input.Revision == nil || *input.Revision < 0 ||
		input.Ranges == nil || !errors.Is(decoder.Decode(&trailing), io.EOF) {
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_ip_allowlist")
		return
	}
	list, err := ipallowlist.Parse(input.Ranges)
	var entryErr *ipallowlist.EntryError
	switch {
	case errors.As(err, &entryErr):
		details := map[string]any{"entry": entryErr.Index, "reason": entryErr.Reason}
		if entryErr.Suggestion != "" {
			details["suggestion"] = entryErr.Suggestion
		}
		writeDetailedProblem(writer, http.StatusUnprocessableEntity, "ip_allowlist_entry_invalid", details)
		return
	case errors.Is(err, ipallowlist.ErrTooManyRanges):
		writeDetailedProblem(writer, http.StatusUnprocessableEntity, "ip_allowlist_too_long",
			map[string]any{"maxRanges": ipallowlist.MaxRanges})
		return
	case err != nil:
		writeProblem(writer, http.StatusUnprocessableEntity, "invalid_ip_allowlist")
		return
	}
	if !handler.local {
		// Refuse a list that would disconnect the person saving it. Loopback
		// is always allowed, so a Server-local session is never refused here.
		client := requesterAddress(request)
		if !list.Allows(client) {
			details := map[string]any{}
			if client.IsValid() {
				details["clientAddress"] = client.String()
			}
			writeDetailedProblem(writer, http.StatusUnprocessableEntity,
				"ip_allowlist_excludes_requester", details)
			return
		}
	}
	saved, err := handler.allowlist.Replace(*input.Revision, list)
	if errors.Is(err, ipallowlist.ErrConflict) {
		writeProblem(writer, http.StatusConflict, "ip_allowlist_conflict")
		return
	}
	if err != nil {
		writer.Header().Set("Retry-After", "1")
		writeProblem(writer, http.StatusServiceUnavailable, "ip_allowlist_unavailable")
		return
	}
	writeServerJSON(writer, http.StatusOK, handler.view(saved, request))
}

func (handler *ServerIPAllowlistHandler) view(
	snapshot ipallowlist.Snapshot,
	request *http.Request,
) serverIPAllowlistView {
	view := serverIPAllowlistView{
		Schema:    ServerIPAllowlistSchema,
		Revision:  snapshot.Revision,
		Ranges:    snapshot.List.Entries(),
		MaxRanges: ipallowlist.MaxRanges,
	}
	if !snapshot.UpdatedAt.IsZero() {
		view.UpdatedAt = snapshot.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if !handler.local {
		if client := requesterAddress(request); client.IsValid() {
			view.ClientAddress = client.String()
		}
	}
	refusals := handler.allowlist.Refusals()
	view.TrustedProxies = handler.allowlist.TrustedProxies().Entries()
	view.ProxyHeaderProblems = refusals.ProxyHeaderProblems
	view.Refused.Count = refusals.Count
	if refusals.LastAddress.IsValid() {
		view.Refused.LastAddress = refusals.LastAddress.String()
	}
	if !refusals.LastAt.IsZero() {
		view.Refused.LastAt = refusals.LastAt.UTC().Format(time.RFC3339)
	}
	return view
}

// requesterAddress is the TCP peer of the request. The Server rejects
// forwarded headers on management routes, so nothing else is trusted.
func requesterAddress(request *http.Request) netip.Addr {
	parsed, err := netip.ParseAddrPort(strings.TrimSpace(request.RemoteAddr))
	if err != nil {
		return netip.Addr{}
	}
	return parsed.Addr().Unmap().WithZone("")
}
