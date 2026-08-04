// Package capturecontrol exposes the narrow capture-grant and per-run
// lifecycle routes. A Host-authenticated ControlPrincipal can request only its
// allowed grant kinds; the returned run capability can operate only that run.
package capturecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturegrant"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/controlprincipal"
)

const (
	RunCapabilityHeader = "X-Vibermate-Run-Capability"
	maxCreateBytes      = 64 << 10
)

type ReasonCode string

const (
	ReasonControlPrincipalUnauthorized ReasonCode = "control_principal_unauthorized"
	ReasonCaptureGrantNotAllowed       ReasonCode = "capture_grant_not_allowed"
	ReasonInvalidCaptureRun            ReasonCode = "invalid_capture_run"
	ReasonAdapterVerification          ReasonCode = "adapter_verification_failed"
	ReasonProjectionUnavailable        ReasonCode = "access_projection_unavailable"
	ReasonCaptureRunCreate             ReasonCode = "capture_run_create_failed"
	ReasonWorkspaceUnavailable         ReasonCode = "workspace_identity_unavailable"
	ReasonInvalidManualCapture         ReasonCode = "invalid_manual_capture"
	ReasonManualCaptureNotFound        ReasonCode = "manual_capture_not_found"
	ReasonManualCaptureConflict        ReasonCode = "manual_capture_conflict"
	ReasonManualCaptureUnavailable     ReasonCode = "manual_capture_unavailable"
	ReasonRunCapabilityRejected        ReasonCode = "run_capability_rejected"
	ReasonInvalidProcess               ReasonCode = "invalid_process_attachment"
	ReasonInvalidRoute                 ReasonCode = "control_route_not_found"
)

type PrincipalAuthenticator interface {
	Authenticate(string) (controlprincipal.Principal, bool)
}

type CaptureRunIssuer interface {
	IssueCaptureRun(
		context.Context,
		controlprincipal.Principal,
		capturegrant.CaptureRunRequest,
	) (capturegrant.CaptureRunGrant, error)
}

type Options struct {
	Runs        capturerun.Controller
	Principals  PrincipalAuthenticator
	Issuer      CaptureRunIssuer
	Manual      *ManualHandler
	RunLifetime time.Duration
}

type Handler struct {
	runs        capturerun.Controller
	principals  PrincipalAuthenticator
	issuer      CaptureRunIssuer
	manual      *ManualHandler
	runLifetime time.Duration
	mux         *http.ServeMux
}

type CreateRequest struct {
	CWD            string   `json:"cwd"`
	Command        []string `json:"command"`
	ExecutablePath string   `json:"executablePath"`
	LocalUserLabel string   `json:"localUserLabel,omitempty"`
}

type AttachRequest struct {
	ProcessID int `json:"processId"`
}

func New(options Options) (*Handler, error) {
	if options.Runs == nil ||
		options.Principals == nil ||
		options.Issuer == nil ||
		options.Manual == nil ||
		options.RunLifetime <= 0 {
		return nil, errors.New("CaptureRun control dependencies are incomplete")
	}
	handler := &Handler{
		runs:        options.Runs,
		principals:  options.Principals,
		issuer:      options.Issuer,
		manual:      options.Manual,
		runLifetime: options.RunLifetime,
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
	handler.mux.HandleFunc("/api/v1/manual-captures", handler.manualCapture)
	handler.mux.HandleFunc("/api/v1/manual-captures/", handler.manualCapture)
	handler.mux.HandleFunc("/api/v1/capture-runs", handler.invalidRoute)
	handler.mux.HandleFunc(
		"/api/v1/capture-runs/{runId}/actions/{action}",
		handler.invalidRoute,
	)
	handler.mux.HandleFunc("/", handler.invalidRoute)
	return handler, nil
}

func (handler *Handler) manualCapture(
	writer http.ResponseWriter,
	request *http.Request,
) {
	principal, ok := handler.authenticatePrincipal(request)
	if !ok {
		writeProblem(
			writer,
			http.StatusUnauthorized,
			ReasonControlPrincipalUnauthorized,
		)
		return
	}
	handler.manual.ServeHTTP(writer, request, principal)
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
	principal, ok := handler.authenticatePrincipal(request)
	if !ok {
		writeProblem(
			writer,
			http.StatusUnauthorized,
			ReasonControlPrincipalUnauthorized,
		)
		return
	}
	var input CreateRequest
	if err := decodeJSON(request, &input, maxCreateBytes); err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidCaptureRun)
		return
	}
	grant, err := handler.issuer.IssueCaptureRun(
		request.Context(),
		principal,
		capturegrant.CaptureRunRequest{
			CWD:            input.CWD,
			Command:        append([]string(nil), input.Command...),
			ExecutablePath: input.ExecutablePath,
			LocalUserLabel: input.LocalUserLabel,
		},
	)
	if err != nil {
		handler.writeIssueFailure(writer, err)
		return
	}
	runView := CaptureRunViewOf(grant.Run.Run)
	writeJSON(writer, http.StatusCreated, LaunchGrant{
		Run:                  runView,
		CatalogRevision:      grant.CatalogRevision,
		LaunchRecipe:         grant.LaunchRecipe,
		Recognition:          grant.Recognition,
		Adapter:              clientLaunchAdapterViewOf(grant.Adapter),
		Signer:               clientSignerViewOf(grant.Signer),
		ExecutablePath:       grant.ExecutablePath,
		ProxyAddress:         grant.ProxyAddress,
		ProxyToken:           grant.Run.ProxyCapability.Value(),
		RunCapability:        grant.Run.ControlCapability.Value(),
		RootPEMPath:          grant.RootPEMPath,
		ProtectedAuthorities: append([]string{}, grant.ProtectedAuthorities...),
		ManagedCredentialAuthorities: append(
			[]string{},
			grant.ManagedCredentialAuthorities...,
		),
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
	writeJSON(writer, http.StatusOK, CaptureRunViewOf(view))
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
	writeJSON(writer, http.StatusOK, CaptureRunViewOf(view))
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

func (handler *Handler) authenticatePrincipal(
	request *http.Request,
) (controlprincipal.Principal, bool) {
	if request == nil {
		return controlprincipal.Principal{}, false
	}
	authorization := request.Header.Values("Authorization")
	runCapabilities := request.Header.Values(RunCapabilityHeader)
	request.Header.Del("Authorization")
	request.Header.Del(RunCapabilityHeader)
	if len(authorization) != 1 || len(runCapabilities) != 0 ||
		!strings.HasPrefix(authorization[0], "Bearer ") {
		return controlprincipal.Principal{}, false
	}
	credential := strings.TrimPrefix(authorization[0], "Bearer ")
	return handler.principals.Authenticate(credential)
}

func consumeRunCapability(
	request *http.Request,
) (capturerun.ControlCapability, bool) {
	values := request.Header.Values(RunCapabilityHeader)
	authorization := request.Header.Values("Authorization")
	request.Header.Del(RunCapabilityHeader)
	request.Header.Del("Authorization")
	if len(values) != 1 || len(authorization) != 0 {
		return capturerun.ControlCapability{}, false
	}
	capability, err := capturerun.NewControlCapability(values[0])
	return capability, err == nil
}

func (handler *Handler) writeIssueFailure(
	writer http.ResponseWriter,
	err error,
) {
	switch {
	case errors.Is(err, capturegrant.ErrPrincipalUnauthorized):
		writeProblem(writer, http.StatusForbidden, ReasonCaptureGrantNotAllowed)
	case errors.Is(err, capturegrant.ErrInvalidCaptureRun):
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidCaptureRun)
	case errors.Is(err, capturegrant.ErrAdapterVerification):
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonAdapterVerification)
	case errors.Is(err, capturegrant.ErrProjectionUnavailable):
		writeProblem(writer, http.StatusServiceUnavailable, ReasonProjectionUnavailable)
	case errors.Is(err, capturegrant.ErrWorkspaceUnavailable):
		writeProblem(writer, http.StatusServiceUnavailable, ReasonWorkspaceUnavailable)
	default:
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonCaptureRunCreate)
	}
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

func writeProblem(
	writer http.ResponseWriter,
	status int,
	reason ReasonCode,
) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(struct {
		Type   string     `json:"type"`
		Title  string     `json:"title"`
		Status int        `json:"status"`
		Code   ReasonCode `json:"code"`
	}{
		Type:   "urn:vibermate:error:" + strings.ReplaceAll(string(reason), "_", "-"),
		Title:  http.StatusText(status),
		Status: status,
		Code:   reason,
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
