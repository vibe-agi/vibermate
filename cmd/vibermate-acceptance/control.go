package main

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/loopbackclient"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

const controlResponseLimit = 2 << 20

var controlReasonCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type controlClient struct {
	baseURL    string
	readToken  string
	writeToken string
	client     *loopbackclient.Client
}

type controlProblem struct {
	Type        string
	Title       string
	Status      int
	ReasonCode  string
	OperationID string
}

type controlProblemWire struct {
	Type        string          `json:"type"`
	Title       string          `json:"title"`
	Status      int             `json:"status"`
	ReasonCode  string          `json:"code"`
	OperationID json.RawMessage `json:"operationId"`
}

type activityPageWire struct {
	Items      json.RawMessage `json:"items"`
	NextCursor json.RawMessage `json:"nextCursor"`
}

func newControlClient(
	session desktopbootstrap.Session,
) (*controlClient, error) {
	parsed, err := url.Parse(session.BaseURL)
	if err != nil ||
		parsed.Scheme != "http" ||
		parsed.Hostname() != "127.0.0.1" ||
		parsed.Port() == "" ||
		parsed.Path != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, errors.New("control session base URL is invalid")
	}
	client, err := loopbackclient.New(session.BaseURL, 35*time.Second)
	if err != nil {
		return nil, err
	}
	return &controlClient{
		baseURL: session.BaseURL, readToken: session.ReadToken,
		writeToken: session.WriteToken, client: client,
	}, nil
}

func (client *controlClient) request(
	ctx context.Context,
	method string,
	path string,
	write bool,
	expectedRevision *uint64,
	body any,
	output any,
) (int, controlProblem, error) {
	var encoded []byte
	var err error
	hasBody := body != nil
	if hasBody {
		encoded, err = json.Marshal(body)
		if err != nil {
			return 0, controlProblem{}, err
		}
	}
	key := ""
	if expectedRevision != nil {
		key, err = idempotencyKey()
		if err != nil {
			return 0, controlProblem{}, err
		}
	}
	return client.requestEncoded(
		ctx,
		method,
		path,
		write,
		expectedRevision,
		encoded,
		hasBody,
		key,
		output,
	)
}

func (client *controlClient) requestEncoded(
	ctx context.Context,
	method string,
	path string,
	write bool,
	expectedRevision *uint64,
	encoded []byte,
	hasBody bool,
	key string,
	output any,
) (int, controlProblem, error) {
	if client == nil ||
		ctx == nil ||
		!strings.HasPrefix(path, "/api/v1/") ||
		((expectedRevision == nil) != (key == "")) ||
		(!hasBody && len(encoded) != 0) {
		return 0, controlProblem{}, errors.New("control request is incomplete")
	}
	request, err := http.NewRequestWithContext(
		ctx,
		method,
		client.baseURL+path,
		bytes.NewReader(encoded),
	)
	if err != nil {
		return 0, controlProblem{}, err
	}
	request.Header.Set("Origin", "tauri://localhost")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	request.Header.Set("Sec-Fetch-Mode", "cors")
	request.Header.Set("Sec-Fetch-Dest", "empty")
	request.Header.Set("Accept", "application/json, application/problem+json")
	token := client.readToken
	if write {
		token = client.writeToken
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if hasBody {
		request.Header.Set("Content-Type", "application/json")
	}
	if expectedRevision != nil {
		request.Header.Set(
			"If-Match",
			strconv.FormatUint(*expectedRevision, 10),
		)
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := client.client.Do(request)
	if err != nil {
		return 0, controlProblem{}, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(
		io.LimitReader(response.Body, controlResponseLimit+1),
	)
	if err != nil || len(payload) > controlResponseLimit {
		return response.StatusCode, controlProblem{}, errors.New(
			"control response exceeded its bound",
		)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if !controlContentType(response, "application/problem+json") {
			return response.StatusCode, controlProblem{}, errors.New(
				"control failure response Content-Type is invalid",
			)
		}
		problem, err := decodeControlProblem(response.StatusCode, payload)
		if err != nil {
			return response.StatusCode, controlProblem{}, errors.New(
				"control failure response is invalid",
			)
		}
		return response.StatusCode, problem, nil
	}
	if output == nil {
		if len(bytes.TrimSpace(payload)) != 0 {
			return response.StatusCode, controlProblem{}, errors.New(
				"control success response body is unexpected",
			)
		}
		return response.StatusCode, controlProblem{}, nil
	}
	if !controlContentType(response, "application/json") {
		return response.StatusCode, controlProblem{}, errors.New(
			"control success response Content-Type is invalid",
		)
	}
	if err := decodeClosedJSON(payload, output); err != nil {
		return response.StatusCode, controlProblem{}, fmt.Errorf(
			"decode control response: %w",
			err,
		)
	}
	return response.StatusCode, controlProblem{}, nil
}

func controlContentType(response *http.Response, expected string) bool {
	if response == nil {
		return false
	}
	values := response.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	return err == nil && mediaType == expected && len(parameters) == 0
}

func decodeClosedJSON(payload []byte, output any) error {
	if !utf8.Valid(payload) {
		return errors.New("control response is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("control response contains trailing JSON")
	}
	return nil
}

func decodeControlProblem(status int, payload []byte) (controlProblem, error) {
	var wire controlProblemWire
	if err := decodeClosedJSON(payload, &wire); err != nil {
		return controlProblem{}, err
	}
	operationID := ""
	if len(wire.OperationID) != 0 {
		if err := decodeClosedJSON(wire.OperationID, &operationID); err != nil ||
			operationID == "" {
			return controlProblem{}, errors.New("operation ID is invalid")
		}
	}
	problem := controlProblem{
		Type:        wire.Type,
		Title:       wire.Title,
		Status:      wire.Status,
		ReasonCode:  wire.ReasonCode,
		OperationID: operationID,
	}
	if problem.Status != status ||
		problem.Title == "" ||
		!controlReasonCodePattern.MatchString(problem.ReasonCode) ||
		problem.Type != "urn:vibermate:error:"+
			strings.ReplaceAll(problem.ReasonCode, "_", "-") {
		return controlProblem{}, errors.New("control problem is inconsistent")
	}
	return problem, nil
}

func (client *controlClient) status(
	ctx context.Context,
) (desktopcontrol.StatusResponse, error) {
	var status desktopcontrol.StatusResponse
	code, problem, err := client.request(
		ctx,
		http.MethodGet,
		"/api/v1/status",
		false,
		nil,
		nil,
		&status,
	)
	if err != nil {
		return desktopcontrol.StatusResponse{}, err
	}
	if code != http.StatusOK || !status.Ready {
		return desktopcontrol.StatusResponse{}, fmt.Errorf(
			"runtime status is unavailable: %s",
			problem.ReasonCode,
		)
	}
	return status, nil
}

type environmentPublication struct {
	Draft   desktopcontrol.EnvironmentDraftResponse
	Preview desktopcontrol.EnvironmentImpactResponse
	Publish desktopcontrol.EnvironmentPublishResponse
}

func (client *controlClient) publishInitialEnvironment(
	ctx context.Context,
	config config,
	expectedBase uint64,
) (environmentPublication, int, controlProblem, error) {
	if ctx == nil {
		return environmentPublication{}, 0, controlProblem{},
			errors.New("Environment publish context is nil")
	}
	if err := ctx.Err(); err != nil {
		return environmentPublication{}, 0, controlProblem{}, err
	}
	candidate, err := assemblyEnvironment(config, environment.Revision(expectedBase+1))
	if err != nil {
		return environmentPublication{}, 0, controlProblem{}, err
	}
	input := desktopcontrol.EnvironmentDraftInput{
		ExpectedDraftRevision: 0,
		Name:                  candidate.Name,
		State:                 candidate.State,
		ClientEndpoints:       candidate.ClientEndpoints,
		PluginBindings:        candidate.PluginBindings,
		BudgetPolicy:          candidate.BudgetPolicy,
		EgressPolicy:          candidate.EgressPolicy,
	}
	path := "/api/v1/environments/" + url.PathEscape(config.environmentID) + "/draft"
	var result environmentPublication
	status, problem, err := client.request(
		ctx, http.MethodPut, path, true, &expectedBase, input, &result.Draft,
	)
	if err != nil || status != http.StatusOK || problem.ReasonCode != "" {
		return environmentPublication{}, status, problem, err
	}
	if err := validateEnvironmentDraft(result.Draft, candidate, expectedBase); err != nil {
		return environmentPublication{}, status, problem, err
	}
	draftRevision := uint64(result.Draft.DraftRevision)
	actionPath := path + "/actions/preview"
	status, problem, err = client.request(
		ctx, http.MethodPost, actionPath, true, &draftRevision, nil, &result.Preview,
	)
	if err != nil || status != http.StatusOK || problem.ReasonCode != "" {
		return environmentPublication{}, status, problem, err
	}
	if err := validateEnvironmentImpact(result.Preview, result.Draft); err != nil {
		return environmentPublication{}, status, problem, err
	}
	actionPath = path + "/actions/publish"
	status, problem, err = client.request(
		ctx, http.MethodPost, actionPath, true, &draftRevision, nil, &result.Publish,
	)
	if err != nil || status != http.StatusOK || problem.ReasonCode != "" {
		return environmentPublication{}, status, problem, err
	}
	if err := validateEnvironmentPublication(result, candidate); err != nil {
		return environmentPublication{}, status, problem, err
	}
	return result, status, problem, nil
}

func validateEnvironmentDraft(result desktopcontrol.EnvironmentDraftResponse, candidate environment.Environment, expectedBase uint64) error {
	if result.EnvironmentID != candidate.ID || uint64(result.BaseRevision) != expectedBase ||
		result.DraftRevision == 0 || result.Candidate.ID != candidate.ID ||
		result.Candidate.Revision != candidate.Revision || !canonicalSHA256(result.CandidateDigest) ||
		result.Candidate.Digest != result.CandidateDigest {
		return errors.New("Environment draft response is inconsistent")
	}
	return nil
}

func validateEnvironmentImpact(result desktopcontrol.EnvironmentImpactResponse, draft desktopcontrol.EnvironmentDraftResponse) error {
	if result.EnvironmentID != draft.EnvironmentID || result.BaseRevision != draft.BaseRevision ||
		result.DraftRevision != draft.DraftRevision || result.CandidateDigest != draft.CandidateDigest ||
		result.HotSwitchCount != 0 || result.ReconnectRequiredCount != 0 || len(result.Affected) != 0 {
		return errors.New("Environment impact preview is inconsistent")
	}
	return nil
}

func validateEnvironmentPublication(result environmentPublication, candidate environment.Environment) error {
	published := result.Publish.Environment
	if result.Publish.Outcome != environment.CommitOutcomeCommitted ||
		published.ID != candidate.ID || published.Revision != candidate.Revision ||
		published.Digest != result.Draft.CandidateDigest || !canonicalSHA256(published.Digest) {
		return errors.New("Environment publish response is inconsistent")
	}
	if err := validateEnvironmentImpact(result.Publish.Impact, result.Draft); err != nil {
		return err
	}
	return nil
}

func canonicalSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func (client *controlClient) connectionRules(
	ctx context.Context,
) (desktopcontrol.ConnectionRuleSetResponse, error) {
	var rules desktopcontrol.ConnectionRuleSetResponse
	status, problem, err := client.request(
		ctx,
		http.MethodGet,
		"/api/v1/policies/connections",
		false,
		nil,
		nil,
		&rules,
	)
	if err != nil {
		return desktopcontrol.ConnectionRuleSetResponse{}, err
	}
	if status != http.StatusOK {
		return desktopcontrol.ConnectionRuleSetResponse{}, fmt.Errorf(
			"read connection rules: %s",
			problem.ReasonCode,
		)
	}
	return rules, nil
}

func (client *controlClient) replaceConnectionRules(
	ctx context.Context,
	expected uint64,
	input desktopcontrol.ConnectionRuleSetInput,
) (desktopcontrol.ConnectionRuleSetResponse, error) {
	var rules desktopcontrol.ConnectionRuleSetResponse
	status, problem, err := client.request(
		ctx,
		http.MethodPatch,
		"/api/v1/policies/connections",
		true,
		&expected,
		input,
		&rules,
	)
	if err != nil {
		return desktopcontrol.ConnectionRuleSetResponse{}, err
	}
	if status != http.StatusOK {
		return desktopcontrol.ConnectionRuleSetResponse{}, fmt.Errorf(
			"replace connection rules: %s",
			problem.ReasonCode,
		)
	}
	return rules, nil
}

func (client *controlClient) environment(
	ctx context.Context,
	environmentID environment.EnvironmentID,
) (desktopcontrol.EnvironmentResponse, error) {
	var view desktopcontrol.EnvironmentResponse
	status, problem, err := client.request(
		ctx, http.MethodGet,
		"/api/v1/environments/"+url.PathEscape(environmentID.String()),
		false, nil, nil, &view,
	)
	if err != nil {
		return desktopcontrol.EnvironmentResponse{}, err
	}
	if status != http.StatusOK {
		return desktopcontrol.EnvironmentResponse{}, fmt.Errorf(
			"read Environment: %s", problem.ReasonCode,
		)
	}
	return view, nil
}

func (client *controlClient) captures(
	ctx context.Context,
) (desktopcontrol.CaptureListResponse, error) {
	var page desktopcontrol.CaptureListResponse
	status, problem, err := client.request(
		ctx, http.MethodGet, "/api/v1/captures?limit=200", false, nil, nil, &page,
	)
	if err != nil {
		return desktopcontrol.CaptureListResponse{}, err
	}
	if status != http.StatusOK {
		return desktopcontrol.CaptureListResponse{}, fmt.Errorf(
			"read Captures: %s", problem.ReasonCode,
		)
	}
	return page, nil
}

func (client *controlClient) captureAssignment(
	ctx context.Context,
	reference captureidentity.Reference,
) (desktopcontrol.CaptureAssignmentResponse, error) {
	if err := reference.Validate(); err != nil {
		return desktopcontrol.CaptureAssignmentResponse{}, err
	}
	var assignment desktopcontrol.CaptureAssignmentResponse
	status, problem, err := client.request(
		ctx, http.MethodGet,
		"/api/v1/captures/"+url.PathEscape(reference.Key())+"/environment-assignment",
		false, nil, nil, &assignment,
	)
	if err != nil {
		return desktopcontrol.CaptureAssignmentResponse{}, err
	}
	if status != http.StatusOK {
		return desktopcontrol.CaptureAssignmentResponse{}, fmt.Errorf(
			"read Capture assignment: %s", problem.ReasonCode,
		)
	}
	return assignment, nil
}

func (client *controlClient) activities(
	ctx context.Context,
	cursor string,
) (desktopcontrol.ActivityPage, error) {
	if cursor != "" && !validActivityCursor(cursor) {
		return desktopcontrol.ActivityPage{}, errors.New(
			"Activity cursor is invalid",
		)
	}
	path := "/api/v1/activities?limit=" + strconv.Itoa(activity.MaxPageSize)
	if cursor != "" {
		path += "&cursor=" + url.QueryEscape(cursor)
	}
	var wire activityPageWire
	status, problem, err := client.request(
		ctx,
		http.MethodGet,
		path,
		false,
		nil,
		nil,
		&wire,
	)
	if err != nil {
		return desktopcontrol.ActivityPage{}, err
	}
	if status != http.StatusOK {
		return desktopcontrol.ActivityPage{}, fmt.Errorf(
			"read Activity page: %s",
			problem.ReasonCode,
		)
	}
	page, err := activityPageFromWire(wire)
	if err != nil {
		return desktopcontrol.ActivityPage{}, fmt.Errorf(
			"decode Activity page: %w",
			err,
		)
	}
	return page, nil
}

func activityPageFromWire(
	wire activityPageWire,
) (desktopcontrol.ActivityPage, error) {
	var items []desktopcontrol.ActivitySummary
	if len(wire.Items) == 0 || decodeClosedJSON(wire.Items, &items) != nil ||
		items == nil || len(items) > activity.MaxPageSize {
		return desktopcontrol.ActivityPage{}, errors.New(
			"Activity items are invalid",
		)
	}
	for _, item := range items {
		if item.Validate() != nil ||
			!validFrozenEnvironmentRef(item.Environment) ||
			!validControlIdentity(item.ID, activity.MaxIdentityBytes) ||
			item.OccurredAt.IsZero() ||
			!validExchangeSummaryStatus(item.Status) {
			return desktopcontrol.ActivityPage{}, errors.New(
				"Activity summary is invalid",
			)
		}
	}
	page := desktopcontrol.ActivityPage{Items: items}
	if len(wire.NextCursor) != 0 {
		if err := decodeClosedJSON(wire.NextCursor, &page.NextCursor); err != nil ||
			!validActivityCursor(page.NextCursor) {
			return desktopcontrol.ActivityPage{}, errors.New(
				"Activity next cursor is invalid",
			)
		}
	}
	return page, nil
}

func validFrozenEnvironmentRef(reference desktopcontrol.FrozenEnvironmentRef) bool {
	if _, err := environment.NewEnvironmentID(reference.ID); err != nil {
		return false
	}
	if _, err := environment.ParseCandidateDigest(reference.Digest); err != nil {
		return false
	}
	if _, err := environment.NewClientEndpointID(reference.ClientEndpointID); err != nil {
		return false
	}
	if _, err := environment.NewClientProtocolPlanID(reference.ProtocolPlanID); err != nil {
		return false
	}
	if _, err := environment.NewUpstreamRouteID(reference.RouteID); err != nil {
		return false
	}
	return true
}

func validExchangeSummaryStatus(value string) bool {
	switch activity.Status(value) {
	case activity.StatusSucceeded, activity.StatusFailed, activity.StatusCanceled:
		return true
	default:
		return false
	}
}

func validActivityCursor(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) != 0 &&
		base64.RawURLEncoding.EncodeToString(decoded) == value
}

func validControlIdentity(value string, maximumBytes int) bool {
	if value == "" ||
		len(value) > maximumBytes ||
		!utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func (client *controlClient) connections(
	ctx context.Context,
	cursor string,
) (connectionevent.Page, error) {
	path := "/api/v1/connections?limit=" +
		strconv.Itoa(connectionevent.MaxPageSize)
	if cursor != "" {
		path += "&cursor=" + url.QueryEscape(cursor)
	}
	var page connectionevent.Page
	status, problem, err := client.request(
		ctx,
		http.MethodGet,
		path,
		false,
		nil,
		nil,
		&page,
	)
	if err != nil {
		return connectionevent.Page{}, err
	}
	if status != http.StatusOK {
		return connectionevent.Page{}, fmt.Errorf(
			"read ConnectionEvent page: %s",
			problem.ReasonCode,
		)
	}
	return page, nil
}

func (client *controlClient) connectionTimeline(
	ctx context.Context,
	connectionID string,
) (connectionevent.Timeline, error) {
	var timeline connectionevent.Timeline
	status, problem, err := client.request(
		ctx,
		http.MethodGet,
		"/api/v1/connections/"+url.PathEscape(connectionID),
		false,
		nil,
		nil,
		&timeline,
	)
	if err != nil {
		return connectionevent.Timeline{}, err
	}
	if status != http.StatusOK {
		return connectionevent.Timeline{}, fmt.Errorf(
			"read ConnectionEvent timeline: %s",
			problem.ReasonCode,
		)
	}
	return timeline, nil
}

func (client *controlClient) offline(
	ctx context.Context,
) (offlinehold.Snapshot, error) {
	var snapshot offlinehold.Snapshot
	status, problem, err := client.request(
		ctx,
		http.MethodGet,
		"/api/v1/offline-hold",
		false,
		nil,
		nil,
		&snapshot,
	)
	if err != nil {
		return offlinehold.Snapshot{}, err
	}
	if status != http.StatusOK {
		return offlinehold.Snapshot{}, fmt.Errorf(
			"read offline hold: %s",
			problem.ReasonCode,
		)
	}
	return snapshot, nil
}

func (client *controlClient) offlineAction(
	ctx context.Context,
	action string,
	expected uint64,
) (offlinehold.Snapshot, error) {
	if action != "enter" && action != "resume" {
		return offlinehold.Snapshot{}, errors.New("offline action is invalid")
	}
	var snapshot offlinehold.Snapshot
	status, problem, err := client.request(
		ctx,
		http.MethodPost,
		"/api/v1/offline-hold/actions/"+action,
		true,
		&expected,
		nil,
		&snapshot,
	)
	if err != nil {
		return offlinehold.Snapshot{}, err
	}
	if status != http.StatusOK {
		return offlinehold.Snapshot{}, fmt.Errorf(
			"offline %s failed: %s",
			action,
			problem.ReasonCode,
		)
	}
	return snapshot, nil
}

func (client *controlClient) pendingApprovals(
	ctx context.Context,
) (toolapproval.Page, error) {
	var page toolapproval.Page
	status, problem, err := client.request(
		ctx,
		http.MethodGet,
		"/api/v1/approvals?state=pending&limit="+
			strconv.Itoa(toolapproval.MaxPageSize),
		false,
		nil,
		nil,
		&page,
	)
	if err != nil {
		return toolapproval.Page{}, err
	}
	if status != http.StatusOK {
		return toolapproval.Page{}, fmt.Errorf(
			"list approvals failed: %s",
			problem.ReasonCode,
		)
	}
	return page, nil
}

func (client *controlClient) allowOnce(
	ctx context.Context,
	approval toolapproval.View,
) (toolapproval.View, error) {
	expected := approval.Revision
	var view toolapproval.View
	status, problem, err := client.request(
		ctx,
		http.MethodPost,
		"/api/v1/approvals/"+approval.ID+"/actions/decide",
		true,
		&expected,
		desktopcontrol.ApprovalDecisionInput{
			Decision: toolapproval.DecisionAllowOnce,
			Scope:    toolapproval.ScopeRequest,
		},
		&view,
	)
	if err != nil {
		return toolapproval.View{}, err
	}
	if status != http.StatusOK {
		return toolapproval.View{}, fmt.Errorf(
			"allow approval failed: %s",
			problem.ReasonCode,
		)
	}
	return view, nil
}

func (client *controlClient) denyOnce(
	ctx context.Context,
	approval toolapproval.View,
) (toolapproval.View, error) {
	expected := approval.Revision
	var view toolapproval.View
	status, problem, err := client.request(
		ctx,
		http.MethodPost,
		"/api/v1/approvals/"+approval.ID+"/actions/decide",
		true,
		&expected,
		desktopcontrol.ApprovalDecisionInput{
			Decision:   toolapproval.DecisionDeny,
			Scope:      toolapproval.ScopeRequest,
			ReasonCode: "acceptance_followup_denied",
		},
		&view,
	)
	if err != nil {
		return toolapproval.View{}, err
	}
	if status != http.StatusOK {
		return toolapproval.View{}, fmt.Errorf(
			"deny approval failed: %s",
			problem.ReasonCode,
		)
	}
	return view, nil
}

func idempotencyKey() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func assemblyEnvironment(
	config config,
	revision environment.Revision,
) (environment.Environment, error) {
	client, err := selectedAcceptanceClient(config)
	if err != nil {
		return environment.Environment{}, err
	}
	if revision == 0 {
		return environment.Environment{}, errors.New("Environment revision is required")
	}
	environmentID, err := environment.NewEnvironmentID(config.environmentID)
	if err != nil {
		return environment.Environment{}, err
	}
	clientOrigin, err := originidentity.ParseClientOrigin(client.ClientOrigin)
	if err != nil {
		return environment.Environment{}, err
	}
	providerOrigin, err := originidentity.ParseProviderOrigin(client.ClientOrigin)
	if err != nil {
		return environment.Environment{}, err
	}
	const (
		endpointID = environment.ClientEndpointID("acceptance.endpoint")
		planID     = environment.ClientProtocolPlanID("acceptance.protocol")
		routeID    = environment.UpstreamRouteID("acceptance.route")
	)
	return environment.Environment{
		ID: environmentID, Name: "Assembly Environment", State: environment.StateActive,
		Revision: revision,
		ClientEndpoints: []environment.ClientEndpoint{{
			ID: endpointID, Revision: revision, ClientOrigin: clientOrigin,
			ProtocolPlans: []environment.ClientProtocolPlan{{
				ID: planID, Revision: revision, ClientProtocol: client.ClientProtocol,
				ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: "acceptance.adapter", Revision: revision},
				Mode:                environment.PlanModeOriginalPassthrough,
				UpstreamPlan: environment.UpstreamPlan{
					DefaultRouteID: routeID,
					RouteSet:       environment.RouteSet{ID: "acceptance.routes", Revision: revision, CandidateRouteIDs: []environment.UpstreamRouteID{routeID}},
					Routes: []environment.UpstreamRoute{{
						ID: routeID, Revision: revision,
						ProviderTarget: environment.ProviderTarget{
							ID: "acceptance.target", Revision: revision, Origin: providerOrigin,
							RealmID: "acceptance.realm", Capabilities: []protocolspec.ProviderCapability{
								protocolspec.ProviderCapabilityMessages,
								protocolspec.ProviderCapabilityStreaming,
								protocolspec.ProviderCapabilityToolCalls,
							},
						},
						BackendProtocol: string(client.ClientProtocol),
						AccountPolicy: environment.RouteAccountPolicy{
							Revision: revision, Mode: environment.AccountModeClientPassthrough,
							FailoverPolicy: environment.FailoverOff,
						},
						ModelPolicy:    environment.ModelPolicy{Revision: revision, Mode: "preserve"},
						WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue,
					}},
				},
			}},
		}},
	}, nil
}
