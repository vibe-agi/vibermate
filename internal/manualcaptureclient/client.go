// Package manualcaptureclient is the bounded local-CLI transport for the
// authenticated ManualCapture control contract. It never retries a mutation:
// losing a one-time grant response leaves an ambiguous result that must be
// resolved by listing and explicitly rotating or revoking the capture.
package manualcaptureclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/loopbackclient"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

const (
	maximumResponseBytes = 256 << 10
	defaultTimeout       = 10 * time.Second
)

type Failure struct {
	Status     int
	ReasonCode capturecontrol.ReasonCode
}

func (failure *Failure) Error() string {
	return fmt.Sprintf(
		"ManualCapture control request failed with status %d and reason %s",
		failure.Status,
		failure.ReasonCode,
	)
}

type Client struct {
	origin     string
	credential string
	httpClient *loopbackclient.Client
}

type GrantResult struct {
	Grant capturecontrol.ManualCaptureGrant
	ETag  string
}

type Resource struct {
	Capture capturecontrol.ManualCaptureView
	ETag    string
}

func New(session localdiscovery.Session) (*Client, error) {
	if session.ControlCredential == "" {
		return nil, errors.New("local control credential is missing")
	}
	client, err := loopbackclient.New(session.BaseURL, defaultTimeout)
	if err != nil {
		return nil, err
	}
	return &Client{
		origin:     session.BaseURL,
		credential: session.ControlCredential,
		httpClient: client,
	}, nil
}

func (client *Client) Close() {
	if client != nil && client.httpClient != nil {
		client.httpClient.Close()
	}
}

func (client *Client) Context(
	ctx context.Context,
) (capturecontrol.ManualCaptureContext, error) {
	var output capturecontrol.ManualCaptureContext
	_, err := client.request(
		ctx,
		http.MethodGet,
		"/api/v1/manual-captures/context",
		"",
		nil,
		http.StatusOK,
		&output,
	)
	if err == nil {
		err = validateContext(output)
	}
	return output, err
}

func (client *Client) Create(
	ctx context.Context,
	input capturecontrol.ManualCaptureCreateRequest,
) (GrantResult, error) {
	var output capturecontrol.ManualCaptureGrant
	etag, err := client.request(
		ctx,
		http.MethodPost,
		"/api/v1/manual-captures",
		"",
		input,
		http.StatusCreated,
		&output,
	)
	if err == nil {
		err = validateGrant(output)
	}
	if err == nil && !validManualCaptureETag(etag) {
		err = errors.New("ManualCapture grant ETag is invalid")
	}
	return GrantResult{Grant: output, ETag: etag}, err
}

func (client *Client) List(ctx context.Context) (capturecontrol.ManualCapturePage, error) {
	var output capturecontrol.ManualCapturePage
	_, err := client.request(
		ctx,
		http.MethodGet,
		"/api/v1/manual-captures",
		"",
		nil,
		http.StatusOK,
		&output,
	)
	if err == nil {
		if output.Items == nil {
			err = errors.New("ManualCapture page is invalid")
		} else {
			for _, item := range output.Items {
				if itemErr := validateManualCaptureView(item); itemErr != nil {
					err = itemErr
					break
				}
			}
		}
	}
	return output, err
}

func (client *Client) Get(
	ctx context.Context,
	id manualcapture.ID,
) (Resource, error) {
	if !id.Valid() {
		return Resource{}, errors.New("ManualCapture ID is invalid")
	}
	var output capturecontrol.ManualCaptureView
	etag, err := client.request(
		ctx,
		http.MethodGet,
		manualPath(id),
		"",
		nil,
		http.StatusOK,
		&output,
	)
	if err == nil {
		err = validateManualCaptureView(output)
	}
	if err == nil && !validManualCaptureETag(etag) {
		err = errors.New("ManualCapture resource ETag is invalid")
	}
	return Resource{Capture: output, ETag: etag}, err
}

func (client *Client) Rotate(
	ctx context.Context,
	id manualcapture.ID,
	expectedETag string,
) (GrantResult, error) {
	if !id.Valid() || !validManualCaptureETag(expectedETag) {
		return GrantResult{}, errors.New(
			"ManualCapture rotation coordinates are invalid",
		)
	}
	var output capturecontrol.ManualCaptureGrant
	etag, err := client.request(
		ctx,
		http.MethodPost,
		manualPath(id)+"/actions/rotate-credential",
		expectedETag,
		nil,
		http.StatusOK,
		&output,
	)
	if err == nil {
		err = validateGrant(output)
	}
	if err == nil && !validManualCaptureETag(etag) {
		err = errors.New("ManualCapture rotation ETag is invalid")
	}
	return GrantResult{Grant: output, ETag: etag}, err
}

func (client *Client) Revoke(
	ctx context.Context,
	id manualcapture.ID,
	expectedETag string,
) error {
	if !id.Valid() || !validManualCaptureETag(expectedETag) {
		return errors.New("ManualCapture revocation coordinates are invalid")
	}
	_, err := client.request(
		ctx,
		http.MethodPost,
		manualPath(id)+"/actions/revoke",
		expectedETag,
		nil,
		http.StatusNoContent,
		nil,
	)
	return err
}

func (client *Client) request(
	ctx context.Context,
	method string,
	path string,
	expectedETag string,
	input any,
	wantStatus int,
	output any,
) (string, error) {
	if client == nil || client.httpClient == nil || ctx == nil ||
		(path != "/api/v1/manual-captures" &&
			!strings.HasPrefix(path, "/api/v1/manual-captures/")) {
		return "", errors.New("ManualCapture control request is incomplete")
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return "", errors.New("encode ManualCapture control request")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.origin+path, body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/json, application/problem+json")
	request.Header.Set("Authorization", "Bearer "+client.credential)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if expectedETag != "" {
		request.Header.Set("If-Match", expectedETag)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("call ManualCapture control API: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil || len(payload) > maximumResponseBytes {
		return "", errors.New("ManualCapture control response exceeded its bound")
	}
	if values := response.Header.Values("Cache-Control"); len(values) != 1 || values[0] != "no-store" {
		return "", errors.New("ManualCapture control response cache policy is invalid")
	}
	if response.StatusCode != wantStatus {
		return "", decodeFailure(response, payload)
	}
	if output == nil {
		if len(bytes.TrimSpace(payload)) != 0 {
			return "", errors.New("ManualCapture control response body is unexpected")
		}
		return responseETag(response), nil
	}
	if !exactContentType(response, "application/json") {
		return "", errors.New("ManualCapture control response Content-Type is invalid")
	}
	if err := decodeClosedJSON(payload, output); err != nil {
		return "", err
	}
	return responseETag(response), nil
}

func decodeFailure(response *http.Response, payload []byte) error {
	if !exactContentType(response, "application/problem+json") {
		return errors.New("ManualCapture control failure Content-Type is invalid")
	}
	var problem struct {
		Type   string                    `json:"type"`
		Title  string                    `json:"title"`
		Status int                       `json:"status"`
		Code   capturecontrol.ReasonCode `json:"code"`
	}
	if err := decodeClosedJSON(payload, &problem); err != nil ||
		problem.Status != response.StatusCode ||
		problem.Title == "" || problem.Code == "" ||
		problem.Type != "urn:vibermate:error:"+
			strings.ReplaceAll(string(problem.Code), "_", "-") {
		return errors.New("ManualCapture control failure is invalid")
	}
	return &Failure{Status: problem.Status, ReasonCode: problem.Code}
}

func decodeClosedJSON(payload []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return errors.New("decode ManualCapture control response")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("ManualCapture control response contains trailing JSON")
	}
	return nil
}

func exactContentType(response *http.Response, want string) bool {
	values := response.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	return err == nil && mediaType == want && len(parameters) == 0
}

func validateContext(context capturecontrol.ManualCaptureContext) error {
	parsed, err := url.Parse(context.ProxyAddress)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" ||
		parsed.Port() == "" || parsed.User != nil || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		!validConfirmationToken(context.ConfirmationToken) ||
		context.Root.Kind != "local_path" || !validDERSHA256(context.Root.DERSHA256) ||
		context.Root.Fingerprint == "" || !filepath.IsAbs(context.Root.PEMPath) ||
		filepath.Clean(context.Root.PEMPath) != context.Root.PEMPath ||
		context.DefaultTemporarySeconds < 60 ||
		context.MaxTemporarySeconds < context.DefaultTemporarySeconds {
		return errors.New("ManualCapture context is invalid")
	}
	return nil
}

func validateGrant(grant capturecontrol.ManualCaptureGrant) error {
	if validateManualCaptureView(grant.Capture) != nil ||
		grant.Capture.State != manualcapture.StateActive ||
		grant.ProxyUsername != manualcapture.ProxyUsername ||
		grant.ProxyPassword == "" {
		return errors.New("ManualCapture grant is invalid")
	}
	if _, err := manualcapture.NewProxyCredential(grant.ProxyPassword); err != nil {
		return errors.New("ManualCapture grant credential is invalid")
	}
	return validateDelivery(grant.ProxyAddress, grant.Root)
}

func validateDelivery(
	proxyAddress string,
	root capturecontrol.RootPublicDelivery,
) error {
	parsed, err := url.Parse(proxyAddress)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" ||
		parsed.Port() == "" || parsed.User != nil || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		root.Kind != "local_path" || !validDERSHA256(root.DERSHA256) ||
		root.Fingerprint == "" || !filepath.IsAbs(root.PEMPath) ||
		filepath.Clean(root.PEMPath) != root.PEMPath {
		return errors.New("ManualCapture delivery is invalid")
	}
	return nil
}

func validateManualCaptureView(view capturecontrol.ManualCaptureView) error {
	if view.ID == "" || view.IngressProfileID == "" || view.DisplayName == "" ||
		!view.ClientClass.Valid() || !view.Lifetime.Valid() || !view.State.Valid() ||
		!view.Observation.Valid() || view.CreatedAt.IsZero() || view.UpdatedAt.IsZero() {
		return errors.New("ManualCapture view is invalid")
	}
	return nil
}

func validConfirmationToken(value string) bool {
	if !strings.HasPrefix(value, "ctx_") {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "ctx_"))
	return err == nil && len(decoded) == sha256.Size
}

func validManualCaptureETag(value string) bool {
	const prefix = `"mc_`
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, `"`) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(
		strings.TrimSuffix(strings.TrimPrefix(value, prefix), `"`),
	)
	return err == nil && len(decoded) == sha256.Size
}

func responseETag(response *http.Response) string {
	values := response.Header.Values("ETag")
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func validDERSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func manualPath(id manualcapture.ID) string {
	return "/api/v1/manual-captures/" + url.PathEscape(id.String())
}
