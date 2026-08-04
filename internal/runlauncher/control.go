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
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/launcherdiscovery"
	"github.com/vibe-agi/vibermate/internal/loopbackclient"
)

const maxControlResponseBytes = 128 << 10

type ControlFailure struct {
	Status     int
	ReasonCode capturecontrol.ReasonCode
}

func (failure *ControlFailure) Error() string {
	return fmt.Sprintf(
		"launcher control request failed with status %d and reason %s",
		failure.Status,
		failure.ReasonCode,
	)
}

type controlClient struct {
	origin     string
	launcher   string
	httpClient *loopbackclient.Client
}

func newControlClient(
	session launcherdiscovery.Session,
) (*controlClient, error) {
	client, err := loopbackclient.New(session.BaseURL, 10*time.Second)
	if err != nil {
		return nil, err
	}
	if session.LauncherToken == "" {
		client.Close()
		return nil, errors.New("launcher capability is missing")
	}
	return &controlClient{
		origin:     session.BaseURL,
		launcher:   session.LauncherToken,
		httpClient: client,
	}, nil
}

func (client *controlClient) close() {
	if client != nil && client.httpClient != nil {
		client.httpClient.Close()
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
		client.launcher,
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
		return errors.New("launcher control client and context are required")
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode launcher control request: %w", err)
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
		return fmt.Errorf("call launcher control API: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(
		response.Body,
		maxControlResponseBytes+1,
	))
	if err != nil {
		return fmt.Errorf("read launcher control response: %w", err)
	}
	if len(payload) > maxControlResponseBytes {
		return errors.New("launcher control response exceeds the size limit")
	}
	if response.StatusCode != wantStatus {
		return decodeControlFailure(response.StatusCode, payload)
	}
	if output == nil {
		if len(bytes.TrimSpace(payload)) != 0 {
			return errors.New("launcher control response body is unexpected")
		}
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode launcher control response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("launcher control response contains trailing JSON")
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
			"launcher control returned invalid error response with status %d",
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
