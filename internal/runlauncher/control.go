package runlauncher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/loopbackclient"
)

const maxControlResponseBytes = 128 << 10

type ControlFailure struct {
	Status     int
	ReasonCode capturecontrol.ReasonCode
}

func (failure *ControlFailure) Error() string {
	return fmt.Sprintf(
		"local control request failed with status %d and reason %s",
		failure.Status,
		failure.ReasonCode,
	)
}

type controlClient struct {
	origin      string
	credential  string
	httpClient  requestDoer
	closeHTTP   func()
	description string
}

type requestDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type RuntimeInspection struct {
	Origin     string
	ProcessID  int
	Ready      bool
	APIVersion string
	State      string
	Host       string
	Storage    string
}

func InspectLocal(
	ctx context.Context,
	discovery Discovery,
	timeout time.Duration,
) (RuntimeInspection, error) {
	if ctx == nil || discovery == nil || timeout <= 0 {
		return RuntimeInspection{}, errors.New("local Runtime inspection is incomplete")
	}
	session, err := discovery.Load()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, localdiscovery.ErrExpired) {
			return RuntimeInspection{}, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		return RuntimeInspection{}, fmt.Errorf("load local control discovery: %w", err)
	}
	client, err := newControlClient(session, timeout)
	if err != nil {
		return RuntimeInspection{}, fmt.Errorf("construct local control client: %w", err)
	}
	defer client.close()
	var response desktopcontrol.StatusResponse
	if err := client.jsonRequest(
		ctx,
		http.MethodGet,
		"/api/v1/status",
		client.credential,
		"",
		nil,
		http.StatusOK,
		&response,
	); err != nil {
		return RuntimeInspection{}, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
	}
	if response.Generation != session.InstanceID ||
		response.Runtime.InstanceID != session.InstanceID ||
		response.APIVersion != "v1" ||
		response.StatusKey != "runtime.state."+string(response.Runtime.State) ||
		response.Runtime.StartedAt.IsZero() {
		return RuntimeInspection{}, errors.New("local Runtime status is invalid")
	}
	switch response.Runtime.State {
	case "starting", "initialized", "degraded", "stopping", "stopped", "stop_failed":
	default:
		return RuntimeInspection{}, errors.New("local Runtime state is invalid")
	}
	switch response.Runtime.Host {
	case "desktop", "server":
	default:
		return RuntimeInspection{}, errors.New("local Runtime host is invalid")
	}
	switch response.Runtime.Storage {
	case "healthy", "unavailable":
	default:
		return RuntimeInspection{}, errors.New("local Runtime storage state is invalid")
	}
	return RuntimeInspection{
		Origin: session.BaseURL, ProcessID: session.ProcessID,
		Ready: response.Ready, APIVersion: response.APIVersion,
		State: string(response.Runtime.State), Host: string(response.Runtime.Host),
		Storage: string(response.Runtime.Storage),
	}, nil
}

func newControlClient(
	session localdiscovery.Session,
	timeout time.Duration,
) (*controlClient, error) {
	client, err := loopbackclient.New(session.BaseURL, timeout)
	if err != nil {
		return nil, err
	}
	if session.ControlCredential == "" {
		client.Close()
		return nil, errors.New("local control credential is missing")
	}
	return &controlClient{
		origin:      session.BaseURL,
		credential:  session.ControlCredential,
		httpClient:  client,
		closeHTTP:   client.Close,
		description: "local",
	}, nil
}

func newRemoteControlClient(
	origin string,
	credential string,
	client requestDoer,
	closeHTTP func(),
) (*controlClient, error) {
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" ||
		parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" ||
		parsed.Fragment != "" || credential == "" || client == nil || closeHTTP == nil {
		return nil, errors.New("remote control session is invalid")
	}
	return &controlClient{
		origin: origin, credential: credential, httpClient: client,
		closeHTTP: closeHTTP, description: "remote",
	}, nil
}

func (client *controlClient) close() {
	if client != nil && client.closeHTTP != nil {
		client.closeHTTP()
	}
}

func (client *controlClient) create(
	ctx context.Context,
	input capturecontrol.CreateRequest,
) (capturecontrol.LaunchGrant, error) {
	var output capturecontrol.LaunchGrant
	err := client.jsonRequest(
		ctx,
		http.MethodPost,
		"/api/v1/capture-runs",
		client.credential,
		"",
		input,
		http.StatusCreated,
		&output,
	)
	return output, err
}

func (client *controlClient) attach(
	ctx context.Context,
	grant capturecontrol.LaunchGrant,
	processID int,
) error {
	var output capturecontrol.CaptureRunView
	return client.jsonRequest(
		ctx,
		http.MethodPost,
		runActionPath(grant.Run.ID, "attach-process"),
		"",
		grant.RunCapability,
		capturecontrol.AttachRequest{ProcessID: processID},
		http.StatusOK,
		&output,
	)
}

func (client *controlClient) heartbeat(
	ctx context.Context,
	grant capturecontrol.LaunchGrant,
) error {
	var output capturecontrol.CaptureRunView
	return client.jsonRequest(
		ctx,
		http.MethodPost,
		runActionPath(grant.Run.ID, "heartbeat"),
		"",
		grant.RunCapability,
		nil,
		http.StatusOK,
		&output,
	)
}

func (client *controlClient) finish(
	ctx context.Context,
	grant capturecontrol.LaunchGrant,
) error {
	return client.jsonRequest(
		ctx,
		http.MethodPost,
		runActionPath(grant.Run.ID, "finish"),
		"",
		grant.RunCapability,
		nil,
		http.StatusNoContent,
		nil,
	)
}

func (client *controlClient) jsonRequest(
	ctx context.Context,
	method string,
	path string,
	bearer string,
	runCapability string,
	input any,
	wantStatus int,
	output any,
) error {
	if client == nil || ctx == nil {
		return errors.New("control client and context are required")
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode %s control request: %w", client.description, err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		method,
		client.origin+path,
		body,
	)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json, application/problem+json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if runCapability != "" {
		request.Header.Set(capturecontrol.RunCapabilityHeader, runCapability)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("call %s control API: %w", client.description, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(
		response.Body,
		maxControlResponseBytes+1,
	))
	if err != nil {
		return fmt.Errorf("read %s control response: %w", client.description, err)
	}
	if len(payload) > maxControlResponseBytes {
		return fmt.Errorf("%s control response exceeds the size limit", client.description)
	}
	if response.StatusCode != wantStatus {
		return decodeControlFailure(response.StatusCode, payload)
	}
	if output == nil {
		if len(bytes.TrimSpace(payload)) != 0 {
			return fmt.Errorf("%s control response body is unexpected", client.description)
		}
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode %s control response: %w", client.description, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s control response contains trailing JSON", client.description)
	}
	return nil
}

func decodeControlFailure(status int, payload []byte) error {
	var problem struct {
		Type        string                    `json:"type"`
		Title       string                    `json:"title"`
		Status      int                       `json:"status"`
		Code        capturecontrol.ReasonCode `json:"code"`
		OperationID string                    `json:"operationId,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&problem); err != nil ||
		problem.Status != status ||
		problem.Title == "" ||
		problem.Code == "" ||
		problem.Type != "urn:vibermate:error:"+
			strings.ReplaceAll(string(problem.Code), "_", "-") {
		return fmt.Errorf(
			"local control returned invalid error response with status %d",
			status,
		)
	}
	return &ControlFailure{
		Status:     status,
		ReasonCode: problem.Code,
	}
}

func runActionPath(runID string, action string) string {
	return "/api/v1/capture-runs/" +
		url.PathEscape(runID) +
		"/actions/" +
		action
}

func validateGrant(grant capturecontrol.LaunchGrant) error {
	if grant.Run.ID == "" ||
		grant.RunCapability == "" ||
		grant.ProxyToken == "" ||
		grant.ProxyToken == grant.RunCapability ||
		grant.ExecutablePath == "" ||
		grant.ProxyAddress == "" ||
		!grant.CatalogRevision.Valid() ||
		!grant.LaunchRecipe.Valid() {
		return errors.New("CaptureRun launch grant is incomplete")
	}
	// The shape is decided by the type that defines it, so producer and
	// consumer cannot drift apart again.
	if err := grant.Validate(); err != nil {
		return err
	}
	for _, authority := range grant.ProtectedAuthorities {
		host, port, err := netSplitAuthority(authority)
		if err != nil || host == "" || port == 0 {
			return errors.New("CaptureRun protected authority is invalid")
		}
	}
	return nil
}

func netSplitAuthority(authority string) (string, uint16, error) {
	parsed, err := url.Parse("https://" + authority)
	if err != nil ||
		parsed.User != nil ||
		parsed.Path != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		parsed.Host != authority {
		return "", 0, errors.New("invalid endpoint authority")
	}
	portText := parsed.Port()
	if portText == "" {
		return "", 0, errors.New("endpoint authority is missing a port")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return "", 0, errors.New("endpoint authority port is invalid")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", 0, errors.New("endpoint authority host is invalid")
	}
	return host, uint16(port), nil
}
