package exchange

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/responseschat"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/ssewire"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestManagedRequestUsesOnlyFrozenEnvironmentRouteAndAccount(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		mode:           environment.PlanModeManaged,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      modelModeFixed,
		fixedModel:     "gpt-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
		preferred:      "account.primary",
	})
	authority := newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7})
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(http.StatusOK, completeProviderResponse("gpt-provider")),
	}}}
	observer := &attemptObserverDouble{}
	pipeline := newTestPipeline(t, authority, provider, approvedDecisions(), observer)
	defer shutdownPipeline(t, pipeline)

	request := mustClientRequest(t, "exchange-managed", plan, completeClientRequest())
	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(context.Background(), request, downstream)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != AttemptSucceeded || result.EnvironmentID != "environment.test" ||
		result.EnvironmentRevision != 9 || result.EndpointID != "endpoint.anthropic" ||
		result.ProtocolPlanID != "protocol.anthropic" || result.RouteID != "route.primary" ||
		result.AccountID != "account.primary" || result.AccountRevision != 3 ||
		result.CredentialEpoch != 7 {
		t.Fatalf("result = %+v", result)
	}
	if !bytes.Contains(downstream.bytesSnapshot(), []byte("Done.")) {
		t.Fatalf("downstream body = %s", downstream.bytesSnapshot())
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d", len(requests))
	}
	frozen := requests[0]
	account, ok := frozen.AccountRef()
	if !ok || frozen.CredentialMode() != providerauth.CredentialManaged ||
		account.ID != "account.primary" || account.Revision != 3 ||
		frozen.Provenance().EnvironmentID().String() != "environment.test" ||
		frozen.Provenance().EnvironmentRevision() != 9 ||
		frozen.Provenance().RouteID().String() != "route.primary" ||
		frozen.Target().Origin().String() != "https://provider.example/v1" ||
		frozen.RelativePath() != anthropicchat.ProviderRelativePath {
		t.Fatalf("provider request authority = %+v account=%+v", frozen, account)
	}
	var providerBody map[string]any
	if err := json.Unmarshal(frozen.Body(), &providerBody); err != nil {
		t.Fatal(err)
	}
	if providerBody["model"] != "gpt-provider" {
		t.Fatalf("provider model = %#v", providerBody["model"])
	}
	if got := authority.snapshot(); len(got) != 1 || got[0].AccountID() != "account.primary" ||
		got[0].EnvironmentID().String() != "environment.test" || got[0].RouteID().String() != "route.primary" {
		t.Fatalf("account lease requests = %+v", got)
	}
	if authority.releaseCount("account.primary") != 1 {
		t.Fatal("managed account lease was not released exactly once")
	}
	observations := observer.snapshot()
	if len(observations) != 1 || observations[0].EnvironmentID.String() != "environment.test" ||
		observations[0].RouteID.String() != "route.primary" || observations[0].AccountID != "account.primary" {
		t.Fatalf("attempt observations = %+v", observations)
	}
}

func TestManagedRequestPublishesFrozenConversationEvidence(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		mode:           environment.PlanModeManaged,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      modelModeFixed,
		fixedModel:     "gpt-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
		preferred:      "account.primary",
	})
	content := &contentObserverDouble{}
	pipeline := newTestPipelineWithContentObserver(
		t,
		newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7}),
		&providerDouble{results: []providerResult{{
			response: jsonResponse(http.StatusOK, completeToolProviderResponse("gpt-provider")),
		}}},
		approvedDecisions(),
		&attemptObserverDouble{},
		content,
	)
	defer shutdownPipeline(t, pipeline)

	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-content", plan, toolClientRequest()),
		&downstreamRecorder{},
	)
	if err != nil || result.Outcome != AttemptSucceeded {
		t.Fatalf("Execute() = %+v, %v", result, err)
	}
	observation, ok := content.latest()
	if !ok || observation.ExchangeID != "exchange-content" ||
		observation.EnvironmentID.String() != "environment.test" ||
		observation.EnvironmentRevision != 9 ||
		observation.EndpointID.String() != "endpoint.anthropic" ||
		observation.EndpointRevision != 7 ||
		observation.ProtocolPlanID.String() != "protocol.anthropic" ||
		observation.ProtocolPlanRevision != 8 ||
		observation.RouteID.String() != "route.primary" ||
		observation.RouteRevision != 6 ||
		observation.Recording.Mode != environment.ContentRecordingFull {
		t.Fatalf("content observation = %+v", observation)
	}
	if len(observation.Request.Messages) != 1 ||
		observation.Request.Messages[0].Role != "user" ||
		observation.Request.Messages[0].Blocks[0].Text != "hello" ||
		len(observation.Request.Tools) != 1 ||
		observation.Request.Tools[0].Name != "shell" {
		t.Fatalf("captured request = %+v", observation.Request)
	}
	if observation.Response == nil || len(observation.Response.Blocks) != 1 ||
		observation.Response.Blocks[0].Kind != "tool_call" ||
		observation.Response.Blocks[0].ToolCall.Name != "shell" ||
		observation.Response.StopReason != "tool_use" ||
		!observation.Response.Usage.Output.Known ||
		observation.Response.Usage.Output.Tokens != 2 {
		t.Fatalf("captured response = %+v", observation.Response)
	}
}

func TestDisabledContentRecordingCreatesNoConversationEvidence(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		mode:           environment.PlanModeManaged,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      modelModeFixed,
		fixedModel:     "gpt-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
		preferred:      "account.primary",
		recording: environment.ContentRecordingPolicy{
			Mode: environment.ContentRecordingOff,
		},
	})
	content := &contentObserverDouble{}
	pipeline := newTestPipelineWithContentObserver(
		t,
		newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7}),
		&providerDouble{results: []providerResult{{
			response: jsonResponse(http.StatusOK, completeProviderResponse("gpt-provider")),
		}}},
		approvedDecisions(),
		&attemptObserverDouble{},
		content,
	)
	defer shutdownPipeline(t, pipeline)

	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-no-content", plan, completeClientRequest()),
		&downstreamRecorder{},
	)
	if err != nil || result.Outcome != AttemptSucceeded {
		t.Fatalf("Execute() = %+v, %v", result, err)
	}
	if _, ok := content.latest(); ok {
		t.Fatal("recording-off Environment emitted conversation evidence")
	}
}

func TestContentObserverFailureDoesNotChangeCommittedExchange(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		mode:           environment.PlanModeManaged,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      modelModeFixed,
		fixedModel:     "gpt-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
		preferred:      "account.primary",
	})
	content := &contentObserverDouble{err: errors.New("content store unavailable")}
	pipeline := newTestPipelineWithContentObserver(
		t,
		newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7}),
		&providerDouble{results: []providerResult{{
			response: jsonResponse(http.StatusOK, completeProviderResponse("gpt-provider")),
		}}},
		approvedDecisions(),
		&attemptObserverDouble{},
		content,
	)
	defer shutdownPipeline(t, pipeline)

	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-observer-failure", plan, completeClientRequest()),
		downstream,
	)
	if err != nil || result.Outcome != AttemptSucceeded ||
		!bytes.Contains(downstream.bytesSnapshot(), []byte("Done.")) {
		t.Fatalf("Execute() = %+v, %v; downstream=%q", result, err, downstream.bytesSnapshot())
	}
	if _, ok := content.latest(); !ok {
		t.Fatal("failing observer was not called")
	}
}

func TestOriginalPassthroughPreservesClientEnvelopeAndResponse(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		mode:           environment.PlanModeOriginalPassthrough,
		providerOrigin: "https://api.anthropic.com",
		backend:        protocolspec.DialectAnthropicMessages,
		modelMode:      modelModePreserve,
	})
	responseBody := []byte(`{
		"id":"msg_original","type":"message","role":"assistant","model":"claude-client-alias",
		"content":[{"type":"text","text":"provider-compatible"}],"stop_reason":"end_turn",
		"stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":2}
	}`)
	provider := &providerDouble{results: []providerResult{{response: &http.Response{
		StatusCode: http.StatusCreated,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Upstream":   []string{"kept"},
			"Connection":   []string{"X-Hop"},
			"X-Hop":        []string{"removed"},
		},
		Body: io.NopCloser(bytes.NewReader(responseBody)),
	}}}}
	content := &contentObserverDouble{}
	pipeline := newTestPipelineWithContentObserver(
		t, nil, provider, approvedDecisions(), &attemptObserverDouble{}, content,
	)
	defer shutdownPipeline(t, pipeline)
	body := completeClientRequest()
	request := mustClientRequestWithOptions(
		t,
		"exchange-original",
		plan,
		body,
		WithOriginalHeaders(http.Header{
			"Authorization":       []string{"Bearer client-owned"},
			"Anthropic-Version":   []string{"2023-06-01"},
			"Proxy-Authorization": []string{"must-not-leave"},
		}),
	)
	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(context.Background(), request, downstream)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != AttemptSucceeded || result.AccountID != "" ||
		!bytes.Equal(downstream.bytesSnapshot(), responseBody) {
		t.Fatalf("result=%+v downstream=%s", result, downstream.bytesSnapshot())
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 1 || requests[0].CredentialMode() != providerauth.CredentialClientPassthrough ||
		!bytes.Equal(requests[0].Body(), body) || requests[0].Headers().Get("Authorization") != "Bearer client-owned" ||
		requests[0].Headers().Get("Proxy-Authorization") != "" ||
		requests[0].Target().Origin().String() != "https://api.anthropic.com" ||
		requests[0].RelativePath() != "v1/messages" {
		t.Fatalf("original provider request = %+v", requests)
	}
	envelopes := downstream.envelopesSnapshot()
	if len(envelopes) != 1 || envelopes[0].StatusCode() != http.StatusCreated ||
		envelopes[0].Headers().Get("X-Upstream") != "kept" || envelopes[0].Headers().Get("X-Hop") != "" {
		t.Fatalf("downstream envelopes = %+v", envelopes)
	}
	observation, ok := content.latest()
	if !ok || observation.Request.Messages[0].Blocks[0].Text != "hello" ||
		observation.Response == nil || observation.Response.Blocks[0].Text != "provider-compatible" ||
		observation.Response.Usage.Output.Tokens != 2 {
		t.Fatalf("original passthrough content = %+v", observation)
	}
}

func TestManagedSameOriginClientCredentialPathRemainsSemantic(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		mode:           environment.PlanModeManaged,
		providerOrigin: "https://api.anthropic.com",
		backend:        protocolspec.DialectAnthropicMessages,
		modelMode:      modelModePassthrough,
		clientAccount:  true,
	})
	providerBody := []byte(`{
		"id":"msg_provider","type":"message","role":"assistant","model":"claude-provider",
		"content":[{"type":"text","text":"same dialect"}],"stop_reason":"end_turn",
		"stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":2}
	}`)
	provider := &providerDouble{results: []providerResult{{response: jsonResponse(http.StatusOK, providerBody)}}}
	pipeline := newTestPipeline(t, nil, provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	request := mustClientRequestWithOptions(
		t,
		"exchange-client-credential",
		plan,
		completeClientRequest(),
		WithOriginalHeaders(http.Header{"X-Api-Key": []string{"client-secret"}}),
	)
	downstream := &downstreamRecorder{}
	if _, err := pipeline.Execute(context.Background(), request, downstream); err != nil {
		t.Fatal(err)
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 1 || requests[0].Headers().Get("X-Api-Key") != "client-secret" ||
		requests[0].Headers().Get("Anthropic-Version") == "" ||
		requests[0].CredentialMode() != providerauth.CredentialClientPassthrough {
		t.Fatalf("same-origin request = %+v", requests)
	}
	if !bytes.Equal(downstream.bytesSnapshot(), providerBody) {
		t.Fatalf("same-origin response changed: %s", downstream.bytesSnapshot())
	}
}

func TestStreamingStillPublishesIncrementalClientEvents(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		mode:           environment.PlanModeManaged,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      modelModeFixed,
		fixedModel:     "gpt-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 1, epoch: 1}},
		preferred:      "account.primary",
	})
	authority := newAccountAuthority(t, testAccount{id: "account.primary", revision: 1, epoch: 1})
	provider := &providerDouble{results: []providerResult{{response: streamResponse(
		http.StatusOK,
		normalProviderStream(t, "gpt-provider"),
	)}}}
	content := &contentObserverDouble{}
	pipeline := newTestPipelineWithContentObserver(
		t, authority, provider, approvedDecisions(), &attemptObserverDouble{}, content,
	)
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-stream", plan, streamingClientRequest()),
		downstream,
	)
	if err != nil {
		t.Fatal(err)
	}
	wire := downstream.bytesSnapshot()
	if result.Outcome != AttemptSucceeded || result.Ledger.DownstreamSemanticWrites < 2 ||
		!bytes.Contains(wire, []byte("Hello")) || !bytes.Contains(wire, []byte("message_stop")) {
		t.Fatalf("result=%+v stream=%s", result, wire)
	}
	observation, ok := content.latest()
	if !ok || observation.Response == nil ||
		len(observation.Response.Blocks) != 1 ||
		observation.Response.Blocks[0].Text != "Hello" ||
		!observation.Response.Usage.Output.Known ||
		observation.Response.Usage.Output.Tokens != 1 {
		t.Fatalf("stream content observation = %+v", observation)
	}
}

func TestManagedResponsesRequestUsesTheSameFrozenEnvironmentAuthority(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		clientProtocol: environment.ClientProtocolOpenAIResponses,
		mode:           environment.PlanModeManaged,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      modelModeFixed,
		fixedModel:     "gpt-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
		preferred:      "account.primary",
	})
	authority := newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7})
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(http.StatusOK, completeProviderResponse("gpt-provider")),
	}}}
	pipeline := newTestPipeline(t, authority, provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)

	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-responses", plan, completeResponsesClientRequest()),
		downstream,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != AttemptSucceeded || result.EnvironmentID != "environment.test" ||
		result.RouteID != "route.primary" || result.AccountID != "account.primary" {
		t.Fatalf("result = %+v", result)
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 1 || requests[0].RelativePath() != anthropicchat.ProviderRelativePath ||
		requests[0].Provenance().EnvironmentID().String() != "environment.test" ||
		requests[0].Provenance().RouteID().String() != "route.primary" {
		t.Fatalf("provider requests = %+v", requests)
	}
	var providerWire struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(requests[0].Body(), &providerWire); err != nil {
		t.Fatal(err)
	}
	if providerWire.Model != "gpt-provider" || providerWire.Stream {
		t.Fatalf("provider wire = %+v", providerWire)
	}
	var clientWire struct {
		Object string `json:"object"`
		Status string `json:"status"`
		Model  string `json:"model"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(downstream.bytesSnapshot(), &clientWire); err != nil {
		t.Fatalf("decode Responses body: %v", err)
	}
	if clientWire.Object != "response" || clientWire.Status != "completed" ||
		clientWire.Model != "codex-client-alias" || len(clientWire.Output) != 1 ||
		clientWire.Output[0].Type != "message" || len(clientWire.Output[0].Content) != 1 ||
		clientWire.Output[0].Content[0].Type != "output_text" ||
		clientWire.Output[0].Content[0].Text != "Done." {
		t.Fatalf("Responses body = %#v", clientWire)
	}
}

func TestManagedResponsesStreamingKeepsIncrementalClientSemantics(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		clientProtocol: environment.ClientProtocolOpenAIResponses,
		mode:           environment.PlanModeManaged,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      modelModeFixed,
		fixedModel:     "gpt-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 1, epoch: 1}},
		preferred:      "account.primary",
	})
	authority := newAccountAuthority(t, testAccount{id: "account.primary", revision: 1, epoch: 1})
	provider := &providerDouble{results: []providerResult{{response: streamResponse(
		http.StatusOK,
		normalProviderStream(t, "gpt-provider"),
	)}}}
	pipeline := newTestPipeline(t, authority, provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)

	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-responses-stream", plan, streamingResponsesClientRequest()),
		downstream,
	)
	if err != nil {
		t.Fatal(err)
	}
	wire := downstream.bytesSnapshot()
	if result.Outcome != AttemptSucceeded || result.Ledger.DownstreamSemanticWrites < 2 ||
		!bytes.Contains(wire, []byte(`"type":"response.output_text.delta"`)) ||
		!bytes.Contains(wire, []byte(`"type":"response.completed"`)) {
		t.Fatalf("result=%+v stream=%s", result, wire)
	}
}

func TestAccountFailoverCannotEscapeFrozenRouteCandidateSet(t *testing.T) {
	accounts := []testAccount{
		{id: "account.backup", revision: 4, epoch: 5},
		{id: "account.primary", revision: 2, epoch: 3},
	}
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		mode:           environment.PlanModeManaged,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      modelModeFixed,
		fixedModel:     "gpt-provider",
		accounts:       accounts,
		preferred:      "account.primary",
		failover:       environment.FailoverAccountScopedSafe,
	})
	authority := newAccountAuthority(t, accounts...)
	provider := &providerDouble{results: []providerResult{
		{response: jsonResponse(http.StatusTooManyRequests, []byte(`{"error":{"type":"rate_limit"}}`))},
		{response: jsonResponse(http.StatusOK, completeProviderResponse("gpt-provider"))},
	}}
	pipeline := newTestPipeline(t, authority, provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-failover", plan, completeClientRequest()),
		&downstreamRecorder{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountID != "account.backup" || result.AccountRevision != 4 || result.CredentialEpoch != 5 {
		t.Fatalf("final account = %+v", result)
	}
	leaseRequests := authority.snapshot()
	if len(leaseRequests) != 2 || leaseRequests[0].AccountID() != "account.primary" ||
		leaseRequests[1].AccountID() != "account.backup" {
		t.Fatalf("lease order = %+v", leaseRequests)
	}
	providerRequests := provider.requestsSnapshot()
	if len(providerRequests) != 2 {
		t.Fatalf("provider attempts = %d", len(providerRequests))
	}
	for _, request := range providerRequests {
		if request.Provenance().RouteID().String() != "route.primary" ||
			request.Provenance().RouteRevision() != 6 {
			t.Fatalf("attempt escaped frozen route = %+v", request.Provenance())
		}
	}
}

func TestToolDecisionRejectsBeforeAnyToolBytesReachClient(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		mode:           environment.PlanModeManaged,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      modelModeFixed,
		fixedModel:     "gpt-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 1, epoch: 1}},
		preferred:      "account.primary",
	})
	authority := newAccountAuthority(t, testAccount{id: "account.primary", revision: 1, epoch: 1})
	provider := &providerDouble{results: []providerResult{{response: jsonResponse(
		http.StatusOK,
		completeToolProviderResponse("gpt-provider"),
	)}}}
	decisions := &decisionDouble{decision: ToolDecision{Outcome: ToolDecisionRejected, ReasonCode: "user_rejected"}}
	pipeline := newTestPipeline(t, authority, provider, decisions, &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-tool-reject", plan, toolClientRequest()),
		downstream,
	)
	if ReasonOf(err) != ReasonToolDecisionRejected || result.Outcome != AttemptAborted ||
		len(downstream.bytesSnapshot()) != 0 || decisions.callCount() != 1 {
		t.Fatalf("result=%+v err=%v body=%s decisions=%d", result, err, downstream.bytesSnapshot(), decisions.callCount())
	}
	decision := decisions.lastRequest()
	if decision.EnvironmentID().String() != "environment.test" || decision.RouteID().String() != "route.primary" {
		t.Fatalf("tool decision authority = %+v", decision)
	}
}

func TestOperationEvidenceMismatchFailsBeforeAccountOrProvider(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		mode: environment.PlanModeManaged, providerOrigin: "https://provider.example/v1",
		backend: protocolspec.DialectOpenAIChat, modelMode: modelModeFixed, fixedModel: "gpt-provider",
		accounts: []testAccount{{id: "account.primary", revision: 1, epoch: 1}}, preferred: "account.primary",
	})
	wrongID, err := protocolspec.NewClientOperationID(operationcatalog.AnthropicMessagesCountTokensID)
	if err != nil {
		t.Fatal(err)
	}
	wrongEvidence, err := NewClientOperationEvidence(wrongID, 1, http.MethodPost, "/v1/messages", "")
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewClientRequest(
		"exchange-wrong-operation", plan, wrongEvidence, completeClientRequest(),
		ReplayGenerationCostOnly, wireprofile.ApplicationProtocolHTTP1,
	)
	if err != nil {
		t.Fatal(err)
	}
	authority := newAccountAuthority(t, testAccount{id: "account.primary", revision: 1, epoch: 1})
	provider := &providerDouble{}
	pipeline := newTestPipeline(t, authority, provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	_, err = pipeline.Execute(context.Background(), request, &downstreamRecorder{})
	if ReasonOf(err) != ReasonEnvironmentPlanInvalid || len(authority.snapshot()) != 0 || provider.callCount() != 0 {
		t.Fatalf("err=%v leases=%d provider=%d", err, len(authority.snapshot()), provider.callCount())
	}
}

func TestShutdownCancelsAndDrainsActiveFrozenRequest(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		mode: environment.PlanModeManaged, providerOrigin: "https://provider.example/v1",
		backend: protocolspec.DialectOpenAIChat, modelMode: modelModeFixed, fixedModel: "gpt-provider",
		accounts: []testAccount{{id: "account.primary", revision: 1, epoch: 1}}, preferred: "account.primary",
	})
	authority := newAccountAuthority(t, testAccount{id: "account.primary", revision: 1, epoch: 1})
	provider := &blockingProvider{started: make(chan struct{})}
	pipeline := newTestPipeline(t, authority, provider, approvedDecisions(), &attemptObserverDouble{})
	done := make(chan error, 1)
	go func() {
		_, err := pipeline.Execute(
			context.Background(),
			mustClientRequest(t, "exchange-shutdown", plan, completeClientRequest()),
			&downstreamRecorder{},
		)
		done <- err
	}()
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	pipeline.BeginShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pipeline.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; ReasonOf(err) != ReasonExchangeRuntimeStopping {
		t.Fatalf("active request error = %v", err)
	}
	if authority.releaseCount("account.primary") != 1 {
		t.Fatal("shutdown did not release the account lease")
	}
	if _, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-after-shutdown", plan, completeClientRequest()),
		&downstreamRecorder{},
	); ReasonOf(err) != ReasonExchangeRuntimeStopping {
		t.Fatalf("post-shutdown error = %v", err)
	}
}

type testPlanOptions struct {
	clientProtocol environment.ClientProtocol
	mode           environment.PlanMode
	providerOrigin string
	backend        protocolspec.Dialect
	modelMode      string
	fixedModel     string
	clientAccount  bool
	accounts       []testAccount
	preferred      string
	failover       environment.FailoverPolicy
	recording      environment.ContentRecordingPolicy
}

type testAccount struct {
	id       string
	revision environment.Revision
	epoch    uint64
}

type testAccountCatalog map[string]environment.AccountDescriptor

func (catalog testAccountCatalog) LookupAccount(id string) (environment.AccountDescriptor, bool) {
	account, ok := catalog[id]
	return account, ok
}

func mustEnvironmentRequestPlan(t *testing.T, options testPlanOptions) environment.RequestPlan {
	t.Helper()
	clientProtocol := options.clientProtocol
	if clientProtocol == "" {
		clientProtocol = environment.ClientProtocolAnthropicMessages
	}
	clientOriginValue := "https://api.anthropic.com"
	requestPath := "/v1/messages"
	if clientProtocol == environment.ClientProtocolOpenAIResponses {
		clientOriginValue = "https://api.openai.com"
		requestPath = "/v1/responses"
	}
	clientOrigin := mustClientOrigin(t, clientOriginValue)
	providerOrigin := mustProviderOrigin(t, options.providerOrigin)
	realm := "realm.provider"
	accountMode := environment.AccountModeManaged
	if options.clientAccount || options.mode == environment.PlanModeOriginalPassthrough {
		accountMode = environment.AccountModeClientPassthrough
	}
	failover := options.failover
	if failover == "" {
		failover = environment.FailoverOff
	}
	accountPolicy := environment.RouteAccountPolicy{
		Revision: 5, Mode: accountMode, AllowedRealmIDs: []string{realm}, FailoverPolicy: failover,
	}
	catalog := make(testAccountCatalog, len(options.accounts))
	for _, account := range options.accounts {
		accountPolicy.CandidateAccountIDs = append(accountPolicy.CandidateAccountIDs, account.id)
		if accountPolicy.AccountRevisions == nil {
			accountPolicy.AccountRevisions = make(map[string]environment.Revision)
		}
		accountPolicy.AccountRevisions[account.id] = account.revision
		catalog[account.id] = environment.AccountDescriptor{
			ID: account.id, Revision: account.revision, RealmID: realm, Active: true,
			BackendProtocols: []string{string(options.backend)},
		}
	}
	accountPolicy.PreferredAccountID = options.preferred
	recording := options.recording
	if recording.Mode == "" {
		recording = environment.DefaultContentRecordingPolicy()
	}
	aggregate := environment.Environment{
		ID: "environment.test", Name: "Test", State: environment.StateActive, Revision: 9,
		ContentRecording: recording,
		ClientEndpoints: []environment.ClientEndpoint{{
			ID: "endpoint.anthropic", Revision: 7, ClientOrigin: clientOrigin,
			ProtocolPlans: []environment.ClientProtocolPlan{{
				ID: "protocol.anthropic", Revision: 8,
				ClientProtocol:      clientProtocol,
				ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: "adapter.anthropic", Revision: 2},
				Mode:                options.mode,
				UpstreamPlan: environment.UpstreamPlan{
					DefaultRouteID: "route.primary",
					RouteSet:       environment.RouteSet{ID: "routes.primary", Revision: 4, CandidateRouteIDs: []environment.UpstreamRouteID{"route.primary"}},
					Routes: []environment.UpstreamRoute{{
						ID: "route.primary", Revision: 6,
						ProviderTarget: environment.ProviderTarget{
							ID: "target.primary", Revision: 3, Origin: providerOrigin, RealmID: realm,
							Capabilities: []protocolspec.ProviderCapability{
								protocolspec.ProviderCapabilityMessages,
								protocolspec.ProviderCapabilityStreaming,
								protocolspec.ProviderCapabilityToolCalls,
							},
						},
						BackendProtocol: string(options.backend), AccountPolicy: accountPolicy,
						ModelPolicy:    environment.ModelPolicy{Revision: 4, Mode: options.modelMode, FixedModel: options.fixedModel},
						WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue,
					}},
				},
			}},
		}},
	}
	compiler := mustEnvironmentCompiler(t, catalog)
	snapshot, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatalf("compile Environment: %v", err)
	}
	plan, err := snapshot.ResolveRequest(clientOrigin, environment.RequestFacts{
		Target: protocolspec.RequestTarget{
			Method: http.MethodPost, Path: requestPath,
			Transport: protocolspec.ClientOperationTransportHTTP,
		},
		DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
	})
	if err != nil {
		t.Fatalf("resolve Environment request: %v", err)
	}
	return plan
}

func mustEnvironmentCompiler(t *testing.T, accounts environment.AccountCatalog) environment.Compiler {
	t.Helper()
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	newPair := func(
		id string,
		revision protocolspec.Revision,
		client protocolspec.Dialect,
		provider protocolspec.Dialect,
	) protocolspec.CodecPairDefinition {
		pairID, pairErr := protocolspec.NewCodecPairID(id)
		if pairErr != nil {
			t.Fatal(pairErr)
		}
		return protocolspec.CodecPairDefinition{
			ID: pairID, Revision: revision,
			ClientDialect: client, ProviderDialect: provider,
			ClientOperationIDs: operations.SemanticOperationIDs(client),
			RequiredCapabilities: []protocolspec.ProviderCapability{
				protocolspec.ProviderCapabilityMessages,
				protocolspec.ProviderCapabilityStreaming,
				protocolspec.ProviderCapabilityToolCalls,
			},
		}
	}
	protocols, err := protocolspec.NewCatalog(operations.Definitions(), []protocolspec.CodecPairDefinition{
		newPair(anthropicchat.CodecPairID, anthropicchat.CodecRevision, protocolspec.DialectAnthropicMessages, protocolspec.DialectOpenAIChat),
		newPair(anthropicchat.MessagesCodecPairID, anthropicchat.MessagesCodecRevision, protocolspec.DialectAnthropicMessages, protocolspec.DialectAnthropicMessages),
		newPair(responseschat.CodecPairID, responseschat.CodecRevision, protocolspec.DialectOpenAIResponses, protocolspec.DialectOpenAIChat),
	})
	if err != nil {
		t.Fatal(err)
	}
	wires, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := environment.NewCompiler(accounts, protocols, wires)
	if err != nil {
		t.Fatal(err)
	}
	return compiler
}

func mustClientOrigin(t *testing.T, value string) originidentity.ClientOrigin {
	t.Helper()
	origin, err := originidentity.ParseClientOrigin(value)
	if err != nil {
		t.Fatal(err)
	}
	return origin
}

func mustProviderOrigin(t *testing.T, value string) originidentity.ProviderOrigin {
	t.Helper()
	origin, err := originidentity.ParseProviderOrigin(value)
	if err != nil {
		t.Fatal(err)
	}
	return origin
}

func mustClientRequest(t *testing.T, exchangeID string, plan environment.RequestPlan, body []byte) ClientRequest {
	t.Helper()
	return mustClientRequestWithOptions(t, exchangeID, plan, body)
}

func mustClientRequestWithOptions(
	t *testing.T,
	exchangeID string,
	plan environment.RequestPlan,
	body []byte,
	options ...ClientRequestOption,
) ClientRequest {
	t.Helper()
	operation := plan.Operation()
	evidence, err := NewClientOperationEvidence(
		operation.ID(), operation.Revision(), http.MethodPost, operation.PathPattern(), "",
	)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := exchangeReplayClass(operation.ReplayClass())
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewClientRequest(
		exchangeID, plan, evidence, body, replay, wireprofile.ApplicationProtocolHTTP1, options...,
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func mustProtocolPathSelector(t *testing.T) *protocolpath.Selector {
	t.Helper()
	managed, err := anthropicchat.NewProtocolPath(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	compatible, err := anthropicchat.NewMessagesProtocolPath(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	responses, err := responseschat.NewProtocolPath(responseschat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	selector, err := protocolpath.NewSelector(managed, compatible, responses)
	if err != nil {
		t.Fatal(err)
	}
	return selector
}

func newTestPipeline(
	t *testing.T,
	accounts AccountLeaseAuthority,
	provider Provider,
	decisions ToolDecisionGate,
	observer AttemptObserver,
) *Pipeline {
	return newTestPipelineWithContentObserver(
		t,
		accounts,
		provider,
		decisions,
		observer,
		&contentObserverDouble{},
	)
}

func newTestPipelineWithContentObserver(
	t *testing.T,
	accounts AccountLeaseAuthority,
	provider Provider,
	decisions ToolDecisionGate,
	observer AttemptObserver,
	content ContentObserver,
) *Pipeline {
	t.Helper()
	pipeline, err := New(Options{
		OwnerContext: context.Background(), Actions: newTestActionGate(t), Accounts: accounts,
		ProtocolPaths: mustProtocolPathSelector(t), Provider: provider, ToolDecisions: decisions,
		RetryWaiter: &retryWaiterDouble{}, Observer: observer, ContentObserver: content,
		ObservationTimeout: time.Second,
		Hold:               HoldPolicy{MaxTransportResends: 0, RetryDelay: 0, MaxDuration: time.Second},
		Stream:             DefaultStreamBudgets(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return pipeline
}

func newTestActionGate(t *testing.T) *offlinehold.Gate {
	t.Helper()
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Start(context.Background(), offlinehold.RuntimeBinding{InstanceID: "exchange-test-runtime"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		gate.BeginShutdown()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := gate.Drain(ctx); err != nil {
			t.Errorf("drain offline-hold Gate: %v", err)
		}
	})
	return gate
}

func shutdownPipeline(t *testing.T, pipeline *Pipeline) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pipeline.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

type accountLeaseDouble struct {
	account providerauth.AccountRef
	driver  providerauth.DriverRef
	secret  secretstore.Reference
	release func()
	once    sync.Once
}

func (lease *accountLeaseDouble) Mode() providerauth.CredentialMode {
	return providerauth.CredentialManaged
}
func (lease *accountLeaseDouble) Driver() providerauth.DriverRef { return lease.driver }
func (lease *accountLeaseDouble) Secret() secretstore.Reference  { return lease.secret }
func (lease *accountLeaseDouble) Account() (providerauth.AccountRef, bool) {
	return lease.account, true
}
func (lease *accountLeaseDouble) Release() { lease.once.Do(lease.release) }

type accountAuthorityDouble struct {
	mu       sync.Mutex
	accounts map[string]testAccount
	requests []AccountLeaseRequest
	releases map[string]int
}

func newAccountAuthority(t *testing.T, accounts ...testAccount) *accountAuthorityDouble {
	t.Helper()
	authority := &accountAuthorityDouble{accounts: make(map[string]testAccount), releases: make(map[string]int)}
	for _, account := range accounts {
		authority.accounts[account.id] = account
	}
	return authority
}

func (authority *accountAuthorityDouble) Acquire(
	ctx context.Context,
	request AccountLeaseRequest,
) (providerauth.Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	authority.mu.Lock()
	account, ok := authority.accounts[request.AccountID()]
	authority.requests = append(authority.requests, request)
	authority.mu.Unlock()
	if !ok {
		return nil, errors.New("account is unavailable")
	}
	secret, err := secretstore.ParseReference("secret://provider/" + account.id)
	if err != nil {
		return nil, err
	}
	return &accountLeaseDouble{
		account: providerauth.AccountRef{
			ID: account.id, Revision: uint64(account.revision), CredentialEpoch: account.epoch,
			RealmID: request.RealmID(),
		},
		driver: providerauth.StaticHeaderDriverRef(), secret: secret,
		release: func() {
			authority.mu.Lock()
			authority.releases[account.id]++
			authority.mu.Unlock()
		},
	}, nil
}

func (authority *accountAuthorityDouble) snapshot() []AccountLeaseRequest {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return slices.Clone(authority.requests)
}

func (authority *accountAuthorityDouble) releaseCount(accountID string) int {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return authority.releases[accountID]
}

type providerResult struct {
	response *http.Response
	err      error
}

type providerDouble struct {
	mu       sync.Mutex
	results  []providerResult
	requests []providertransport.Request
}

func (provider *providerDouble) Do(
	_ context.Context,
	request providertransport.Request,
) (*http.Response, providertransport.Evidence, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.requests = append(provider.requests, request)
	if len(provider.results) == 0 {
		return nil, providertransport.Evidence{}, errors.New("provider result queue is empty")
	}
	result := provider.results[0]
	provider.results = provider.results[1:]
	return result.response, providertransport.Evidence{}, result.err
}

func (provider *providerDouble) requestsSnapshot() []providertransport.Request {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return slices.Clone(provider.requests)
}

func (provider *providerDouble) callCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return len(provider.requests)
}

type blockingProvider struct {
	once    sync.Once
	started chan struct{}
}

func (provider *blockingProvider) Do(
	ctx context.Context,
	_ providertransport.Request,
) (*http.Response, providertransport.Evidence, error) {
	provider.once.Do(func() { close(provider.started) })
	<-ctx.Done()
	return nil, providertransport.Evidence{}, context.Cause(ctx)
}

type downstreamRecorder struct {
	mu        sync.Mutex
	envelopes []ResponseEnvelope
	body      bytes.Buffer
	aborts    []FailureNotice
}

func (downstream *downstreamRecorder) Begin(_ context.Context, envelope ResponseEnvelope) error {
	downstream.mu.Lock()
	defer downstream.mu.Unlock()
	downstream.envelopes = append(downstream.envelopes, envelope)
	return nil
}

func (downstream *downstreamRecorder) Write(_ context.Context, body []byte) (int, error) {
	downstream.mu.Lock()
	defer downstream.mu.Unlock()
	return downstream.body.Write(body)
}

func (downstream *downstreamRecorder) Keepalive(context.Context) error { return nil }

func (downstream *downstreamRecorder) Abort(_ context.Context, notice FailureNotice) error {
	downstream.mu.Lock()
	defer downstream.mu.Unlock()
	downstream.aborts = append(downstream.aborts, notice)
	return nil
}

func (downstream *downstreamRecorder) bytesSnapshot() []byte {
	downstream.mu.Lock()
	defer downstream.mu.Unlock()
	return bytes.Clone(downstream.body.Bytes())
}

func (downstream *downstreamRecorder) envelopesSnapshot() []ResponseEnvelope {
	downstream.mu.Lock()
	defer downstream.mu.Unlock()
	return slices.Clone(downstream.envelopes)
}

type decisionDouble struct {
	mu       sync.Mutex
	decision ToolDecision
	err      error
	requests []ToolDecisionRequest
}

func approvedDecisions() *decisionDouble {
	return &decisionDouble{decision: ToolDecision{Outcome: ToolDecisionApproved}}
}

func (decisions *decisionDouble) Decide(
	_ context.Context,
	request ToolDecisionRequest,
) (ToolDecision, error) {
	decisions.mu.Lock()
	defer decisions.mu.Unlock()
	decisions.requests = append(decisions.requests, request)
	return decisions.decision, decisions.err
}

func (decisions *decisionDouble) callCount() int {
	decisions.mu.Lock()
	defer decisions.mu.Unlock()
	return len(decisions.requests)
}

func (decisions *decisionDouble) lastRequest() ToolDecisionRequest {
	decisions.mu.Lock()
	defer decisions.mu.Unlock()
	return decisions.requests[len(decisions.requests)-1]
}

type attemptObserverDouble struct {
	mu           sync.Mutex
	observations []AttemptObservation
}

type contentObserverDouble struct {
	mu           sync.Mutex
	observations []ContentObservation
	err          error
}

func (observer *contentObserverDouble) ObserveContent(
	_ context.Context,
	observation ContentObservation,
) error {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.observations = append(observer.observations, observation)
	return observer.err
}

func (observer *contentObserverDouble) latest() (ContentObservation, bool) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.observations) == 0 {
		return ContentObservation{}, false
	}
	return observer.observations[len(observer.observations)-1], true
}

func (observer *attemptObserverDouble) Observe(_ context.Context, observation AttemptObservation) error {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.observations = append(observer.observations, observation)
	return nil
}

func (observer *attemptObserverDouble) snapshot() []AttemptObservation {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return slices.Clone(observer.observations)
}

type retryWaiterDouble struct{}

func (*retryWaiterDouble) WaitForRetry(context.Context, RetryObservation) error { return nil }

func jsonResponse(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func streamResponse(status int, body io.Reader) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(body),
	}
}

func completeProviderResponse(model string) []byte {
	return []byte(`{
		"id":"chatcmpl-complete","object":"chat.completion","created":1,"model":"` + model + `",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Done.","refusal":null},"finish_reason":"stop","logprobs":null}],
		"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}
	}`)
}

func completeToolProviderResponse(model string) []byte {
	return []byte(`{
		"id":"chatcmpl-tool","object":"chat.completion","created":1,"model":"` + model + `",
		"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-shell","type":"function","function":{"name":"shell","arguments":"{}"}}]},"finish_reason":"tool_calls","logprobs":null}],
		"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}
	}`)
}

func normalProviderStream(t *testing.T, model string) io.Reader {
	t.Helper()
	return bytes.NewReader(joinProviderEvents(
		t,
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}}`,
		`[DONE]`,
	))
}

func joinProviderEvents(t *testing.T, payloads ...string) []byte {
	t.Helper()
	var wire bytes.Buffer
	for _, payload := range payloads {
		event, err := ssewire.Encode(ssewire.Event{Name: "message", Data: []byte(payload)})
		if err != nil {
			t.Fatal(err)
		}
		wire.Write(event)
	}
	return wire.Bytes()
}

func completeClientRequest() []byte {
	return []byte(`{"model":"claude-client-alias","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)
}

func completeResponsesClientRequest() []byte {
	return []byte(`{
		"model":"codex-client-alias",
		"input":[{
			"type":"message",
			"role":"user",
			"content":[{"type":"input_text","text":"hello"}]
		}],
		"store":false,
		"stream":false
	}`)
}

func streamingResponsesClientRequest() []byte {
	return []byte(`{
		"model":"codex-client-alias",
		"input":[{
			"type":"message",
			"role":"user",
			"content":[{"type":"input_text","text":"hello"}]
		}],
		"store":false,
		"stream":true
	}`)
}

func streamingClientRequest() []byte {
	return []byte(`{"model":"claude-client-alias","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hello"}]}`)
}

func toolClientRequest() []byte {
	return []byte(`{
		"model":"claude-client-alias","max_tokens":32,
		"messages":[{"role":"user","content":"hello"}],
		"tools":[{"name":"shell","description":"Run a command.","input_schema":{"type":"object","properties":{}}}]
	}`)
}

func TestProviderRejectionClassifierReturnsOnlyKnownEmittedFields(t *testing.T) {
	tests := []struct {
		body string
		want ProviderField
	}{
		{`{"error":{"message":"private","param":"max_completion_tokens"}}`, ProviderFieldMaxCompletionTokens},
		{`{"detail":[{"loc":["body","reasoning_effort"],"msg":"private"}]}`, ProviderFieldReasoningEffort},
		{`{"error":{"message":"private","param":"response_format"}}`, ProviderFieldResponseFormat},
		{`{"error":{"param":"private_provider_field"}}`, ProviderFieldUnknown},
		{`{"error":{"message":"max_tokens is private text"}}`, ProviderFieldUnknown},
	}
	for _, test := range tests {
		if got := classifyProviderRejection(strings.NewReader(test.body)); got != test.want {
			t.Fatalf("classifyProviderRejection() = %q, want %q", got, test.want)
		}
	}
}
