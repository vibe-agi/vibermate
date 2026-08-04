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
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accessapply"
	"github.com/vibe-agi/vibermate/internal/accesscredential"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/desktopbootstrap"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/loopbackclient"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

const (
	controlResponseLimit             = 2 << 20
	maximumUnresolvedAccessMutations = 16
)

var controlReasonCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type controlClient struct {
	baseURL    string
	readToken  string
	writeToken string
	client     *loopbackclient.Client

	accessMu                  sync.Mutex
	unresolvedAccessMutations map[[sha256.Size]byte]string
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
		baseURL:                   session.BaseURL,
		readToken:                 session.ReadToken,
		writeToken:                session.WriteToken,
		client:                    client,
		unresolvedAccessMutations: make(map[[sha256.Size]byte]string),
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

func (client *controlClient) applyAccess(
	ctx context.Context,
	config config,
	expected uint64,
) (desktopcontrol.AccessApplyResponse, int, controlProblem, error) {
	if ctx == nil {
		return desktopcontrol.AccessApplyResponse{},
			0,
			controlProblem{},
			errors.New("Access apply context is nil")
	}
	if err := ctx.Err(); err != nil {
		return desktopcontrol.AccessApplyResponse{}, 0, controlProblem{}, err
	}
	input, err := assemblyAccess(config, expected)
	if err != nil {
		return desktopcontrol.AccessApplyResponse{}, 0, controlProblem{}, err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return desktopcontrol.AccessApplyResponse{}, 0, controlProblem{}, err
	}
	path := "/api/v1/accesses/" +
		url.PathEscape(config.accessID) +
		"/actions/apply"
	fingerprint := sha256.Sum256(bytes.Join(
		[][]byte{
			[]byte(http.MethodPut),
			[]byte(path),
			[]byte(strconv.FormatUint(expected, 10)),
			encoded,
		},
		[]byte{0},
	))
	key, err := client.accessMutationKey(fingerprint)
	if err != nil {
		return desktopcontrol.AccessApplyResponse{}, 0, controlProblem{}, err
	}
	attempt := func() (
		desktopcontrol.AccessApplyResponse,
		int,
		controlProblem,
		error,
		bool,
	) {
		var payload json.RawMessage
		status, problem, requestErr := client.requestEncoded(
			ctx,
			http.MethodPut,
			path,
			true,
			&expected,
			encoded,
			true,
			key,
			&payload,
		)
		if requestErr != nil {
			return desktopcontrol.AccessApplyResponse{},
				status, problem, requestErr, false
		}
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			return desktopcontrol.AccessApplyResponse{}, status, problem, nil, true
		}
		if status != http.StatusOK {
			return desktopcontrol.AccessApplyResponse{},
				status,
				problem,
				errors.New("Access apply success status is invalid"),
				false
		}
		result, decodeErr := decodeAccessApplyResponse(payload)
		if decodeErr == nil &&
			(expected == ^uint64(0) || uint64(result.Revision) != expected+1) {
			decodeErr = errors.New("Access apply response revision is invalid")
		}
		if decodeErr != nil {
			return desktopcontrol.AccessApplyResponse{},
				status, problem, decodeErr, false
		}
		return result, status, problem, nil, true
	}
	result, status, problem, err, authoritative := attempt()
	if authoritative {
		client.settleAccessMutation(fingerprint)
		return result, status, problem, err
	}
	if ctx.Err() != nil {
		return result, status, problem, err
	}
	result, status, problem, err, authoritative = attempt()
	if authoritative {
		client.settleAccessMutation(fingerprint)
	}
	return result, status, problem, err
}

func (client *controlClient) accessMutationKey(
	fingerprint [sha256.Size]byte,
) (string, error) {
	if client == nil {
		return "", errors.New("control client is unavailable")
	}
	client.accessMu.Lock()
	defer client.accessMu.Unlock()
	if key := client.unresolvedAccessMutations[fingerprint]; key != "" {
		return key, nil
	}
	if len(client.unresolvedAccessMutations) >= maximumUnresolvedAccessMutations {
		return "", errors.New("too many unresolved Access apply commands")
	}
	key, err := idempotencyKey()
	if err != nil {
		return "", err
	}
	client.unresolvedAccessMutations[fingerprint] = key
	return key, nil
}

func (client *controlClient) settleAccessMutation(
	fingerprint [sha256.Size]byte,
) {
	if client == nil {
		return
	}
	client.accessMu.Lock()
	delete(client.unresolvedAccessMutations, fingerprint)
	client.accessMu.Unlock()
}

func decodeAccessApplyResponse(
	payload []byte,
) (desktopcontrol.AccessApplyResponse, error) {
	var fields map[string]json.RawMessage
	if err := decodeClosedJSON(payload, &fields); err != nil || fields == nil {
		return desktopcontrol.AccessApplyResponse{}, errors.New(
			"Access apply response is not a closed object",
		)
	}
	statePayload, exists := fields["applicationState"]
	if !exists {
		return desktopcontrol.AccessApplyResponse{}, errors.New(
			"Access apply response application state is missing",
		)
	}
	var state desktopcontrol.AccessApplicationState
	if err := decodeClosedJSON(statePayload, &state); err != nil {
		return desktopcontrol.AccessApplyResponse{}, errors.New(
			"Access apply response application state is invalid",
		)
	}
	baseFields := []string{"outcome", "revision", "applicationState"}
	switch state {
	case desktopcontrol.AccessApplicationStateActive:
		if !hasExactJSONFields(
			fields,
			"outcome",
			"revision",
			"applicationState",
			"planHash",
		) {
			return desktopcontrol.AccessApplyResponse{}, errors.New(
				"active Access apply response fields are invalid",
			)
		}
	case desktopcontrol.AccessApplicationStateUnavailable:
		if !hasExactJSONFields(fields, baseFields...) {
			return desktopcontrol.AccessApplyResponse{}, errors.New(
				"unavailable Access apply response fields are invalid",
			)
		}
	default:
		return desktopcontrol.AccessApplyResponse{}, errors.New(
			"Access apply response application state is invalid",
		)
	}
	var result desktopcontrol.AccessApplyResponse
	if err := decodeClosedJSON(payload, &result); err != nil {
		return desktopcontrol.AccessApplyResponse{}, err
	}
	if err := validateAccessApplyResponse(result); err != nil {
		return desktopcontrol.AccessApplyResponse{}, err
	}
	return result, nil
}

func hasExactJSONFields(
	fields map[string]json.RawMessage,
	expected ...string,
) bool {
	if len(fields) != len(expected) {
		return false
	}
	for _, field := range expected {
		if _, exists := fields[field]; !exists {
			return false
		}
	}
	return true
}

func validateAccessApplyResponse(
	result desktopcontrol.AccessApplyResponse,
) error {
	if result.Outcome != access.WriteOutcomeCommitted || result.Revision == 0 {
		return errors.New("Access apply response commit receipt is invalid")
	}
	switch result.ApplicationState {
	case desktopcontrol.AccessApplicationStateActive:
		decoded, err := hex.DecodeString(result.PlanHash)
		if err != nil ||
			len(decoded) != sha256.Size ||
			strings.ToLower(result.PlanHash) != result.PlanHash {
			return errors.New("active Access apply response plan hash is invalid")
		}
	case desktopcontrol.AccessApplicationStateUnavailable:
		if result.PlanHash != "" {
			return errors.New("unavailable Access apply response exposed a plan hash")
		}
	default:
		return errors.New("Access apply response application state is invalid")
	}
	return nil
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

func (client *controlClient) credential(
	ctx context.Context,
	config config,
) (accesscredential.View, error) {
	identifiers := assemblyIdentifiers(config.accessID)
	var view accesscredential.View
	status, problem, err := client.request(
		ctx,
		http.MethodGet,
		"/api/v1/accesses/"+url.PathEscape(identifiers.access)+
			"/profiles/"+url.PathEscape(identifiers.profile)+
			"/credentials/"+url.PathEscape(identifiers.account),
		false,
		nil,
		nil,
		&view,
	)
	if err != nil {
		return accesscredential.View{}, err
	}
	if status != http.StatusOK {
		return accesscredential.View{}, fmt.Errorf(
			"read provider credential metadata: %s",
			problem.ReasonCode,
		)
	}
	return view, nil
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
		if !validControlIdentity(item.ID, activity.MaxIdentityBytes) ||
			item.OccurredAt.IsZero() ||
			!validExchangeSummaryStatus(item.Status) {
			return desktopcontrol.ActivityPage{}, errors.New(
				"Activity summary is invalid",
			)
		}
		if _, err := access.NewAccessID(item.AccessID); err != nil {
			return desktopcontrol.ActivityPage{}, errors.New(
				"Activity summary Access ID is invalid",
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

func assemblyAccess(
	config config,
	expected uint64,
) (accessapply.Input, error) {
	identifiers := assemblyIdentifiers(config.accessID)
	client, err := selectedAcceptanceClient(config)
	if err != nil {
		return accessapply.Input{}, err
	}
	return accessapply.Input{
		ExpectedRevision: expected,
		Access: accessapply.AccessInput{
			ID:                identifiers.access,
			Name:              "Assembly Access",
			Description:       "Fixed client assembly acceptance",
			Status:            string(access.AccessStatusEnabled),
			AgentEndpointID:   identifiers.endpoint,
			DefaultRouteSetID: identifiers.routeSet,
			ProfileIDs:        []string{identifiers.profile},
			EgressPolicyID:    identifiers.egress,
		},
		AgentEndpoint: accessapply.AgentEndpointInput{
			ID:            identifiers.endpoint,
			ClientOrigin:  client.ClientOrigin,
			ClientDialect: string(client.ClientDialect),
		},
		Profiles: []accessapply.ProfileInput{{
			ID:                     identifiers.profile,
			Name:                   "Assembly Profile",
			Description:            "Fixed client to OpenAI Chat path",
			BackendDialect:         string(access.DialectOpenAIChat),
			TargetID:               identifiers.target,
			UpstreamWireProfileRef: access.UpstreamWireProfileFollowClientValue,
			DefaultModelPolicy: accessapply.ModelPolicyInput{
				Mode:       string(access.ModelPolicyModeFixed),
				FixedModel: config.providerModel,
			},
			AccountBindingIDs:       []string{identifiers.account},
			DefaultAccountBindingID: identifiers.account,
		}},
		ProviderTargets: []accessapply.ProviderTargetInput{{
			ID:        identifiers.target,
			ProfileID: identifiers.profile,
			Origin:    config.providerOrigin,
			Protocol:  string(access.DialectOpenAIChat),
			Capabilities: []string{
				string(access.ProviderCapabilityMessages),
				string(access.ProviderCapabilityStreaming),
				string(access.ProviderCapabilityToolCalls),
			},
		}},
		AccountBindings: []accessapply.AccountBindingInput{{
			ID:            identifiers.account,
			ProfileID:     identifiers.profile,
			Label:         "Assembly Account",
			SecretRef:     config.secretRef,
			AuthDriverRef: access.AuthDriverStaticHeaderValue,
			Enabled:       true,
		}},
		RouteSets: []accessapply.RouteSetInput{{
			ID:                  identifiers.routeSet,
			CandidateProfileIDs: []string{identifiers.profile},
		}},
		EgressPolicy: accessapply.EgressPolicyInput{
			ID:   identifiers.egress,
			Mode: string(access.EgressModeDirect),
		},
		PluginPlan: accessapply.PluginPlanInput{
			Mode:       string(access.PluginPlanModePassThrough),
			BindingIDs: []string{},
		},
	}, nil
}

type assemblyIDs struct {
	access   string
	endpoint string
	profile  string
	target   string
	account  string
	routeSet string
	egress   string
}

func assemblyIdentifiers(accessID string) assemblyIDs {
	return assemblyIDs{
		access:   accessID,
		endpoint: accessID + "-agent",
		profile:  accessID + "-openai",
		target:   accessID + "-target",
		account:  accessID + "-account",
		routeSet: accessID + "-routes",
		egress:   accessID + "-egress",
	}
}
