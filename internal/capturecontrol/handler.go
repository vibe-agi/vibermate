// Package capturecontrol exposes the narrow launcher and per-run lifecycle
// routes. Launcher capability can only create a CaptureRun; the returned
// control capability can only operate that run.
package capturecontrol

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/localca"
)

const (
	RunCapabilityHeader  = "X-Vibermate-Run-Capability"
	maxCreateBytes       = 64 << 10
	maxArguments         = 256
	maxArgumentBytes     = 32 << 10
	launcherDigestDomain = "vibermate:launcher-control:v1:"
)

type ReasonCode string

const (
	ReasonLauncherUnauthorized  ReasonCode = "launcher_unauthorized"
	ReasonInvalidCaptureRun     ReasonCode = "invalid_capture_run"
	ReasonAdapterVerification   ReasonCode = "adapter_verification_failed"
	ReasonProjectionUnavailable ReasonCode = "access_projection_unavailable"
	ReasonCaptureRunCreate      ReasonCode = "capture_run_create_failed"
	ReasonRunCapabilityRejected ReasonCode = "run_capability_rejected"
	ReasonInvalidProcess        ReasonCode = "invalid_process_attachment"
	ReasonInvalidRoute          ReasonCode = "control_route_not_found"
)

type Clock interface {
	Now() time.Time
}

type Options struct {
	Runs        capturerun.Controller
	Verifier    clientadapter.Verifier
	Authorities access.IngressCatalogReader
	ProxyOrigin string
	Root        localca.Root
	Launcher    *LauncherAuthority
	RunLifetime time.Duration
	Clock       Clock
}

type Handler struct {
	runs        capturerun.Controller
	verifier    clientadapter.Verifier
	authorities access.IngressCatalogReader
	proxyOrigin string
	root        localca.Root
	launcher    *LauncherAuthority
	runLifetime time.Duration
	clock       Clock
	mux         *http.ServeMux
}

type CreateRequest struct {
	CWD            string   `json:"cwd"`
	Command        []string `json:"command"`
	ExecutablePath string   `json:"executablePath"`
}

type LaunchGrant struct {
	Run                  capturerun.View               `json:"run"`
	CatalogRevision      clientadapter.CatalogRevision `json:"catalogRevision"`
	LaunchRecipe         clientadapter.LaunchRecipe    `json:"launchRecipe"`
	Adapter              *clientadapter.Evidence       `json:"adapter,omitempty"`
	ExecutablePath       string                        `json:"executablePath"`
	ProxyOrigin          string                        `json:"proxyOrigin"`
	ProxyCapability      string                        `json:"proxyCapability"`
	RunCapability        string                        `json:"runCapability"`
	RootPEMPath          string                        `json:"rootPemPath,omitempty"`
	ProtectedAuthorities []string                      `json:"protectedAuthorities"`
}

type AttachRequest struct {
	ProcessID int `json:"processId"`
}

func New(options Options) (*Handler, error) {
	if options.Runs == nil ||
		options.Verifier == nil ||
		options.Authorities == nil ||
		options.Clock == nil ||
		options.Launcher == nil ||
		options.RunLifetime <= 0 {
		return nil, errors.New("CaptureRun control dependencies are incomplete")
	}
	if err := validateLoopbackOrigin(options.ProxyOrigin); err != nil {
		return nil, err
	}
	if options.Root.Path() == "" || len(options.Root.CertificatePEM()) == 0 {
		return nil, errors.New("CaptureRun control Root export is incomplete")
	}
	handler := &Handler{
		runs:        options.Runs,
		verifier:    options.Verifier,
		authorities: options.Authorities,
		proxyOrigin: options.ProxyOrigin,
		root:        options.Root,
		launcher:    options.Launcher,
		runLifetime: options.RunLifetime,
		clock:       options.Clock,
		mux:         http.NewServeMux(),
	}
	handler.mux.HandleFunc("POST /api/v1/capture-runs", handler.create)
	handler.mux.HandleFunc(
		"POST /api/v1/capture-runs/{runId}/actions/attach-process",
		handler.attach,
	)
	handler.mux.HandleFunc(
		"POST /api/v1/capture-runs/{runId}/actions/heartbeat",
		handler.heartbeat,
	)
	handler.mux.HandleFunc(
		"POST /api/v1/capture-runs/{runId}/actions/finish",
		handler.finish,
	)
	handler.mux.HandleFunc("/api/v1/capture-runs", handler.invalidRoute)
	handler.mux.HandleFunc(
		"/api/v1/capture-runs/{runId}/actions/{action}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc("/", handler.invalidRoute)
	return handler, nil
}

func (handler *Handler) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	handler.mux.ServeHTTP(writer, request)
}

func (handler *Handler) invalidRoute(
	writer http.ResponseWriter,
	_ *http.Request,
) {
	writeProblem(writer, http.StatusNotFound, ReasonInvalidRoute)
}

func (handler *Handler) create(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if !handler.authorizeLauncher(request) {
		writeProblem(writer, http.StatusUnauthorized, ReasonLauncherUnauthorized)
		return
	}
	var input CreateRequest
	if err := decodeJSON(request, &input, maxCreateBytes); err != nil ||
		validateCreateRequest(input) != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidCaptureRun)
		return
	}
	detection, err := handler.verifier.Verify(
		request.Context(),
		clientadapter.Request{
			Command:        append([]string(nil), input.Command...),
			CWD:            input.CWD,
			ExecutablePath: input.ExecutablePath,
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonAdapterVerification)
		return
	}
	if !detection.CatalogRevision.Valid() ||
		detection.CanonicalPath == "" ||
		(detection.Status != clientadapter.StatusGeneric &&
			detection.Status != clientadapter.StatusVerified) ||
		(detection.Status == clientadapter.StatusGeneric &&
			detection.Evidence != nil) ||
		(detection.Status == clientadapter.StatusVerified &&
			(detection.Evidence == nil ||
				detection.Evidence.Validate() != nil ||
				detection.Evidence.CatalogRevision !=
					detection.CatalogRevision)) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonAdapterVerification)
		return
	}
	authorities, err := handler.authorities.ActiveClientAuthorities()
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonProjectionUnavailable)
		return
	}
	recipe := clientadapter.LaunchGeneric
	var adapter *clientadapter.Evidence
	rootPath := ""
	if detection.Status == clientadapter.StatusVerified &&
		detection.Evidence != nil {
		evidence := *detection.Evidence
		adapter = &evidence
		recipe = evidence.LaunchRecipe
		if recipe.RequiresRoot() {
			rootPath = handler.root.Path()
		}
	}
	grant, err := handler.runs.Create(
		request.Context(),
		capturerun.CreateCommand{
			CWD:             input.CWD,
			ExecutablePath:  detection.CanonicalPath,
			Lifetime:        handler.runLifetime,
			CatalogRevision: detection.CatalogRevision,
			Adapter:         adapter,
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonCaptureRunCreate)
		return
	}
	writeJSON(writer, http.StatusCreated, LaunchGrant{
		Run:                  grant.Run,
		CatalogRevision:      detection.CatalogRevision,
		LaunchRecipe:         recipe,
		Adapter:              adapter,
		ExecutablePath:       detection.CanonicalPath,
		ProxyOrigin:          handler.proxyOrigin,
		ProxyCapability:      grant.ProxyCapability.Value(),
		RunCapability:        grant.ControlCapability.Value(),
		RootPEMPath:          rootPath,
		ProtectedAuthorities: append([]string(nil), authorities...),
	})
}

func (handler *Handler) attach(
	writer http.ResponseWriter,
	request *http.Request,
) {
	capability, ok := consumeRunCapability(request)
	if !ok {
		writeProblem(writer, http.StatusForbidden, ReasonRunCapabilityRejected)
		return
	}
	var input AttachRequest
	if err := decodeJSON(request, &input, 4096); err != nil ||
		input.ProcessID <= 0 {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidProcess)
		return
	}
	view, err := handler.runs.Attach(
		request.Context(),
		request.PathValue("runId"),
		capability,
		input.ProcessID,
	)
	if err != nil {
		writeProblem(writer, http.StatusForbidden, ReasonRunCapabilityRejected)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (handler *Handler) heartbeat(
	writer http.ResponseWriter,
	request *http.Request,
) {
	capability, ok := consumeRunCapability(request)
	if !ok || !emptyBody(request.Body) {
		writeProblem(writer, http.StatusForbidden, ReasonRunCapabilityRejected)
		return
	}
	view, err := handler.runs.Heartbeat(
		request.Context(),
		request.PathValue("runId"),
		capability,
		handler.runLifetime,
	)
	if err != nil {
		writeProblem(writer, http.StatusForbidden, ReasonRunCapabilityRejected)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (handler *Handler) finish(
	writer http.ResponseWriter,
	request *http.Request,
) {
	capability, ok := consumeRunCapability(request)
	if !ok || !emptyBody(request.Body) {
		writeProblem(writer, http.StatusForbidden, ReasonRunCapabilityRejected)
		return
	}
	if err := handler.runs.Finish(
		request.Context(),
		request.PathValue("runId"),
		capability,
	); err != nil {
		writeProblem(writer, http.StatusForbidden, ReasonRunCapabilityRejected)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) authorizeLauncher(request *http.Request) bool {
	return handler.launcher.Authorize(request)
}

func consumeRunCapability(
	request *http.Request,
) (capturerun.ControlCapability, bool) {
	values := request.Header.Values(RunCapabilityHeader)
	request.Header.Del(RunCapabilityHeader)
	request.Header.Del("Authorization")
	if len(values) != 1 {
		return capturerun.ControlCapability{}, false
	}
	capability, err := capturerun.NewControlCapability(values[0])
	return capability, err == nil
}

func validateCreateRequest(input CreateRequest) error {
	if input.CWD == "" ||
		!filepath.IsAbs(input.CWD) ||
		filepath.Clean(input.CWD) != input.CWD ||
		input.ExecutablePath == "" ||
		!filepath.IsAbs(input.ExecutablePath) ||
		len(input.Command) == 0 ||
		len(input.Command) > maxArguments {
		return errors.New("CaptureRun create request is invalid")
	}
	total := 0
	for _, argument := range input.Command {
		if strings.ContainsRune(argument, '\x00') {
			return errors.New("CaptureRun argument contains NUL")
		}
		total += len(argument)
	}
	if input.Command[0] == "" || total > maxArgumentBytes {
		return errors.New("CaptureRun command is outside the limit")
	}
	return nil
}

func decodeJSON(request *http.Request, output any, limit int64) error {
	if request == nil || request.Body == nil {
		return errors.New("JSON request body is missing")
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("request Content-Type is not application/json")
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return errors.New("JSON request exceeds the limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON request has trailing data")
	}
	return nil
}

func emptyBody(body io.Reader) bool {
	if body == nil {
		return true
	}
	data, err := io.ReadAll(io.LimitReader(body, 1))
	return err == nil && len(data) == 0
}

func validateLoopbackOrigin(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil ||
		parsed.Scheme != "http" ||
		parsed.User != nil ||
		parsed.Path != "" ||
		parsed.RawPath != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return errors.New("control proxy origin is invalid")
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || host != "127.0.0.1" {
		return errors.New("control proxy origin is not literal IPv4 loopback")
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return errors.New("control proxy port is invalid")
	}
	return nil
}

func launcherDigest(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(launcherDigestDomain + token))
}

func decodeCapability(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("capability is invalid")
	}
	return decoded, nil
}

func writeProblem(
	writer http.ResponseWriter,
	status int,
	reason ReasonCode,
) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(struct {
		Type       string     `json:"type"`
		Status     int        `json:"status"`
		ReasonCode ReasonCode `json:"reasonCode"`
	}{
		Type:       "urn:vibermate:error:" + strings.ReplaceAll(string(reason), "_", "-"),
		Status:     status,
		ReasonCode: reason,
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
