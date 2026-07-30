package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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

const controlResponseLimit = 2 << 20

type controlClient struct {
	baseURL    string
	readToken  string
	writeToken string
	client     *loopbackclient.Client
}

type controlProblem struct {
	Type       string `json:"type"`
	Status     int    `json:"status"`
	ReasonCode string `json:"reasonCode"`
	MessageKey string `json:"messageKey"`
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
		baseURL:    session.BaseURL,
		readToken:  session.ReadToken,
		writeToken: session.WriteToken,
		client:     client,
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
	if client == nil || ctx == nil || !strings.HasPrefix(path, "/api/v1/") {
		return 0, controlProblem{}, errors.New("control request is incomplete")
	}
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return 0, controlProblem{}, err
		}
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
	token := client.readToken
	if write {
		token = client.writeToken
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if expectedRevision != nil {
		request.Header.Set(
			"If-Match",
			strconv.FormatUint(*expectedRevision, 10),
		)
		key, err := idempotencyKey()
		if err != nil {
			return 0, controlProblem{}, err
		}
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
		var problem controlProblem
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&problem); err != nil {
			return response.StatusCode, controlProblem{}, errors.New(
				"control failure response is invalid",
			)
		}
		return response.StatusCode, problem, nil
	}
	if output == nil {
		return response.StatusCode, controlProblem{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return response.StatusCode, controlProblem{}, fmt.Errorf(
			"decode control response: %w",
			err,
		)
	}
	return response.StatusCode, controlProblem{}, nil
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
	input := assemblyAccess(config, expected)
	var result desktopcontrol.AccessApplyResponse
	status, problem, err := client.request(
		ctx,
		http.MethodPut,
		"/api/v1/accesses/"+url.PathEscape(config.accessID)+"/actions/apply",
		true,
		&expected,
		input,
		&result,
	)
	return result, status, problem, err
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
) (activity.Page, error) {
	var page activity.Page
	status, problem, err := client.request(
		ctx,
		http.MethodGet,
		"/api/v1/activities?limit=50",
		false,
		nil,
		nil,
		&page,
	)
	if err != nil {
		return activity.Page{}, err
	}
	if status != http.StatusOK {
		return activity.Page{}, fmt.Errorf(
			"read Activity timeline: %s",
			problem.ReasonCode,
		)
	}
	return page, nil
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
		"/api/v1/approvals?state=pending&limit=10",
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

func idempotencyKey() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func assemblyAccess(config config, expected uint64) accessapply.Input {
	identifiers := assemblyIdentifiers(config.accessID)
	return accessapply.Input{
		ExpectedRevision: expected,
		Access: accessapply.AccessInput{
			ID:                identifiers.access,
			Name:              "M0 Assembly Access",
			Description:       "Fixed Claude Code assembly acceptance",
			Status:            string(access.AccessStatusEnabled),
			AgentEndpointID:   identifiers.endpoint,
			DefaultRouteSetID: identifiers.routeSet,
			ProfileIDs:        []string{identifiers.profile},
			EgressPolicyID:    identifiers.egress,
		},
		AgentEndpoint: accessapply.AgentEndpointInput{
			ID:            identifiers.endpoint,
			ClientOrigin:  "https://api.anthropic.com",
			ClientDialect: string(access.DialectAnthropicMessages),
		},
		Profiles: []accessapply.ProfileInput{{
			ID:                  identifiers.profile,
			Name:                "M0 Assembly Profile",
			Description:         "Fixed Anthropic to OpenAI Chat path",
			BackendDialect:      string(access.DialectOpenAIChat),
			TargetID:            identifiers.target,
			TransportProfileRef: access.TransportProfileObservedClientH1Value,
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
			Label:         "M0 Assembly Account",
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
	}
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
