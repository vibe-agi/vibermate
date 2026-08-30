package exchange

import (
	"bytes"
	"compress/gzip"
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

	"github.com/vibe-agi/vibermate/internal/accountselector"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/responseschat"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/ssewire"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestManagedRequestUsesOnlyFrozenEnvironmentRouteAndAccount(t *testing.T) {
	const exactUpstreamModel = "dashscope_deepseek-v4-flash-0731"
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    exactUpstreamModel,
		accounts:       []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
		preferred:      "account.primary",
	})
	authority := newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7})
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(http.StatusOK, completeProviderResponse(exactUpstreamModel)),
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
	if providerBody["model"] != exactUpstreamModel {
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

func TestManagedCompleteExchangeTransformsWireMessagesWithOneTurnContext(t *testing.T) {
	const mappedModel = "opaque:upstream-model"
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    mappedModel,
		accounts:       []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
		preferred:      "account.primary",
		transform: messagetransform.Policy{
			RequestJavaScript: `
				const payload = JSON.parse(request.body);
				context.originalModel = payload.model;
				request.headers["x-transform-request"] = "yes";
				payload.transform_marker = "request";
				request.body = JSON.stringify(payload);
			`,
			ResponseJavaScript: `
				const payload = JSON.parse(response.body);
				response.headers["x-transform-response"] = context.originalModel;
				payload.choices[0].message.content = "transformed:" + context.originalModel;
				response.body = JSON.stringify(payload);
			`,
		},
	})
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(http.StatusOK, completeProviderResponse(mappedModel)),
	}}}
	pipeline := newTestPipeline(
		t,
		newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7}),
		provider,
		approvedDecisions(),
		&attemptObserverDouble{},
	)
	raw := &rawObserverDouble{}
	pipeline.rawEvidence = raw
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}
	_, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-transform", plan, completeClientRequest()),
		downstream,
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 1 || requests[0].Headers().Get("X-Transform-Request") != "yes" {
		t.Fatalf("provider request Headers = %#v", requests)
	}
	if !bytes.Contains(requests[0].Body(), []byte(`"transform_marker":"request"`)) {
		t.Fatalf("provider request Body = %s", requests[0].Body())
	}
	if !bytes.Contains(downstream.bytesSnapshot(), []byte("transformed:"+mappedModel)) {
		t.Fatalf("downstream Body = %s", downstream.bytesSnapshot())
	}
	envelopes := downstream.envelopesSnapshot()
	if len(envelopes) != 1 || envelopes[0].Headers().Get("X-Transform-Response") != mappedModel {
		t.Fatalf("downstream response Headers = %#v", envelopes)
	}
	rawMessages := raw.snapshot()
	if len(rawMessages) != 2 ||
		rawMessages[0].Layer != rawevidence.LayerTransformRequestInput ||
		bytes.Contains(rawMessages[0].Body, []byte("transform_marker")) ||
		rawMessages[0].Headers.Get("Authorization") != "" ||
		rawMessages[1].Layer != rawevidence.LayerTransformResponseInput ||
		bytes.Contains(rawMessages[1].Body, []byte("transformed:")) {
		t.Fatalf("transform input evidence = %+v", rawMessages)
	}
}

func TestResponseOnlyTransformRecordsAPairedRequestInput(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "opaque:upstream-model",
		accounts:       []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
		preferred:      "account.primary",
		transform: messagetransform.Policy{ResponseJavaScript: `
			response.headers["x-transform-response"] = "yes";
		`},
	})
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(http.StatusOK, completeProviderResponse("opaque:upstream-model")),
	}}}
	pipeline := newTestPipeline(
		t,
		newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7}),
		provider,
		approvedDecisions(),
		&attemptObserverDouble{},
	)
	raw := &rawObserverDouble{}
	pipeline.rawEvidence = raw
	defer shutdownPipeline(t, pipeline)

	_, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-response-only-transform", plan, completeClientRequest()),
		&downstreamRecorder{},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	evidence := raw.snapshot()
	if len(evidence) != 2 ||
		evidence[0].Layer != rawevidence.LayerTransformRequestInput ||
		evidence[1].Layer != rawevidence.LayerTransformResponseInput {
		t.Fatalf("paired Transform inputs = %+v", evidence)
	}
}

func TestManagedMessageTransformReceivesLauncherRuntimeMetadata(t *testing.T) {
	t.Parallel()
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "opaque:upstream-model",
		accounts:       []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
		preferred:      "account.primary",
		transform: messagetransform.Policy{
			RequestJavaScript: `
				request.headers["x-runtime-user"] = [runtime.user.name];
				context.home = runtime.user.homeDirectory;
			`,
			ResponseJavaScript: `
				response.headers["x-runtime"] = [runtime.device.timeZone, runtime.workspace.root, context.home];
			`,
		},
	})
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(http.StatusOK, completeProviderResponse("opaque:upstream-model")),
	}}}
	pipeline := newTestPipeline(
		t,
		newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7}),
		provider,
		approvedDecisions(),
		&attemptObserverDouble{},
	)
	defer shutdownPipeline(t, pipeline)
	admission, err := captureadmission.NewManagedRun(captureadmission.ManagedRunEvidence{
		CaptureRunID:  "capture-runtime-metadata",
		SourceLabel:   "codex",
		WorkspaceRoot: "/Users/jack/Code/vibermate",
		Runtime: captureadmission.ManagedRuntimeMetadata{
			LocalUserName: "jack", HomeDirectory: "/Users/jack",
			OperatingSystem: "darwin", OperatingSystemVersion: "15.6",
			Architecture: "arm64", TimeZone: "Asia/Singapore",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	downstream := &downstreamRecorder{}
	_, err = pipeline.Execute(
		context.Background(),
		mustClientRequestWithOptions(
			t, "exchange-runtime-metadata", plan, completeClientRequest(),
			WithIngressCorrelation(admission, "connection-runtime-metadata"),
		),
		downstream,
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 1 || requests[0].Headers().Get("X-Runtime-User") != "jack" {
		t.Fatalf("provider runtime metadata = %#v", requests)
	}
	envelopes := downstream.envelopesSnapshot()
	want := []string{"Asia/Singapore", "/Users/jack/Code/vibermate", "/Users/jack"}
	if len(envelopes) != 1 || !slices.Equal(envelopes[0].Headers().Values("X-Runtime"), want) {
		t.Fatalf("downstream runtime metadata = %#v, want %#v", envelopes, want)
	}
}

func TestManagedMessageTransformReceivesFrozenTurnTimeAndAnnotationSigner(t *testing.T) {
	fixed := time.Date(2026, 8, 27, 6, 5, 4, 0, time.UTC)
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "opaque:upstream-model",
		accounts:       []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
		preferred:      "account.primary",
		transform: messagetransform.Policy{ResponseJavaScript: `
			const payload = JSON.parse(response.body);
			payload.choices[0].message.content = runtime.annotations.create(
				"turn-time", runtime.turn.startedAt
			);
			response.body = JSON.stringify(payload);
		`},
	})
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(http.StatusOK, completeProviderResponse("opaque:upstream-model")),
	}}}
	pipeline := newTestPipeline(
		t,
		newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7}),
		provider,
		approvedDecisions(),
		&attemptObserverDouble{},
	)
	pipeline.now = func() time.Time { return fixed }
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}
	if _, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-turn-time", plan, completeClientRequest()),
		downstream,
	); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	body := string(downstream.bytesSnapshot())
	if !strings.Contains(body, "vibermate:annotation:v1:turn-time:") ||
		!strings.Contains(body, fixed.Format(time.RFC3339Nano)) {
		t.Fatalf("downstream body = %s", body)
	}
}

func TestManagedMessageTransformFailsClosedBeforeItsCommitBoundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		transform messagetransform.Policy
		wantCalls int
	}{
		{
			name: "request",
			transform: messagetransform.Policy{
				RequestJavaScript: `throw new Error("request rejected");`,
			},
			wantCalls: 0,
		},
		{
			name: "response",
			transform: messagetransform.Policy{
				ResponseJavaScript: `throw new Error("response rejected");`,
			},
			wantCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				destination:    environment.DestinationKindUpstream,
				providerOrigin: "https://provider.example/v1",
				backend:        protocolspec.DialectOpenAIChat,
				modelMode:      environment.ModelModeMap,
				mappedModel:    "opaque:upstream-model",
				accounts:       []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
				preferred:      "account.primary",
				transform:      test.transform,
			})
			provider := &providerDouble{results: []providerResult{{
				response: jsonResponse(http.StatusOK, completeProviderResponse("opaque:upstream-model")),
			}}}
			pipeline := newTestPipeline(
				t,
				newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7}),
				provider,
				approvedDecisions(),
				&attemptObserverDouble{},
			)
			defer shutdownPipeline(t, pipeline)
			downstream := &downstreamRecorder{}
			_, err := pipeline.Execute(
				context.Background(),
				mustClientRequest(t, "exchange-transform-failure-"+test.name, plan, completeClientRequest()),
				downstream,
			)
			if ReasonOf(err) != ReasonMessageTransformFailed {
				t.Fatalf("Execute() error = %v, want %s", err, ReasonMessageTransformFailed)
			}
			if provider.callCount() != test.wantCalls {
				t.Fatalf("provider calls = %d, want %d", provider.callCount(), test.wantCalls)
			}
			if len(downstream.envelopesSnapshot()) != 0 || len(downstream.bytesSnapshot()) != 0 {
				t.Fatalf("failed transform committed downstream: envelopes=%d body=%q", len(downstream.envelopesSnapshot()), downstream.bytesSnapshot())
			}
		})
	}
}

func TestManagedMessagesSecondTurnPreservesCitedAssistantHistory(t *testing.T) {
	const mappedModel = "dashscope:deepseek-v4-flash-0731"
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "http://127.0.0.1:23333",
		backend:        protocolspec.DialectAnthropicMessages,
		modelMode:      environment.ModelModeMap,
		mappedModel:    mappedModel,
		accounts: []testAccount{{
			id: "account.primary", revision: 3, epoch: 7,
		}},
		preferred: "account.primary",
	})
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(http.StatusOK, []byte(`{
			"id":"msg_second_turn","type":"message","role":"assistant",
			"model":"`+mappedModel+`",
			"content":[{"type":"text","text":"second answer","citations":[]}],
			"stop_reason":"end_turn","stop_sequence":null,
			"usage":{"input_tokens":8,"output_tokens":3}
		}`)),
	}}}
	content := &contentObserverDouble{}
	pipeline := newTestPipelineWithContentObserver(
		t,
		newAccountAuthority(
			t,
			testAccount{id: "account.primary", revision: 3, epoch: 7},
		),
		provider,
		approvedDecisions(),
		&attemptObserverDouble{},
		content,
	)
	defer shutdownPipeline(t, pipeline)

	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(
			t,
			"exchange-messages-second-turn",
			plan,
			secondTurnCitedClientRequest(),
		),
		downstream,
	)
	if err != nil || result.Outcome != AttemptSucceeded {
		t.Fatalf("Execute() = %+v, %v", result, err)
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 1 ||
		requests[0].RelativePath() != anthropicchat.MessagesProviderRelativePath ||
		!bytes.Contains(requests[0].Body(), []byte(`"citations":[]`)) ||
		!bytes.Contains(requests[0].Body(), []byte(`"model":"`+mappedModel+`"`)) {
		t.Fatalf("provider request = %+v", requests)
	}
	var clientBody map[string]any
	if err := json.Unmarshal(downstream.bytesSnapshot(), &clientBody); err != nil {
		t.Fatal(err)
	}
	if clientBody["model"] != "claude-client-alias" {
		t.Fatalf("client response model = %#v", clientBody["model"])
	}
	observation, ok := content.latest()
	if !ok || observation.Response == nil ||
		observation.Response.RequestedModel != "claude-client-alias" ||
		observation.Response.EffectiveModel != mappedModel ||
		observation.Response.ReportedModel != mappedModel {
		t.Fatalf("content observation = %+v", observation)
	}
}

func TestUnmappedClientModelIsPreservedExactly(t *testing.T) {
	const unrelatedUpstreamModel = "relay:only-for-another-client-model"
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		modelMappings: []environment.ModelMapping{{
			RequestedModel: "another-client-model",
			UpstreamModel:  unrelatedUpstreamModel,
		}},
		accounts:  []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
		preferred: "account.primary",
	})
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(http.StatusOK, completeProviderResponse("claude-client-alias")),
	}}}
	pipeline := newTestPipeline(
		t,
		newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7}),
		provider,
		approvedDecisions(),
		&attemptObserverDouble{},
	)
	defer shutdownPipeline(t, pipeline)

	if _, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-unmapped-model", plan, completeClientRequest()),
		&downstreamRecorder{},
	); err != nil {
		t.Fatal(err)
	}
	var providerBody map[string]any
	if err := json.Unmarshal(provider.requestsSnapshot()[0].Body(), &providerBody); err != nil {
		t.Fatal(err)
	}
	if providerBody["model"] != "claude-client-alias" {
		t.Fatalf("unmapped provider model = %#v", providerBody["model"])
	}
}

func TestManagedRequestPublishesFrozenConversationEvidence(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
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
	content.mu.Lock()
	observations := append([]ContentObservation(nil), content.observations...)
	content.mu.Unlock()
	if len(observations) != 2 || observations[0].Response != nil ||
		observations[1].Response == nil {
		t.Fatalf("content lifecycle observations = %+v", observations)
	}
}

func TestLocalIncrementalEvidenceNeverTruncatesTheUpstreamRequest(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
		preferred:      "account.primary",
	})
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(http.StatusOK, completeProviderResponse("gpt-provider")),
	}}}
	content := &contentObserverDouble{}
	pipeline := newTestPipelineWithContentObserver(
		t,
		newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7}),
		provider,
		approvedDecisions(),
		&attemptObserverDouble{},
		content,
	)
	defer shutdownPipeline(t, pipeline)

	admission, err := captureadmission.NewManagedRun(captureadmission.ManagedRunEvidence{
		CaptureRunID: "capture-full-context",
		SourceLabel:  "claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
		"model":"claude-client-alias","max_tokens":32,
		"messages":[
			{"role":"user","content":"first"},
			{"role":"assistant","content":"second"},
			{"role":"user","content":"third"}
		]
	}`)
	request := mustClientRequestWithOptions(
		t,
		"exchange-full-context",
		plan,
		body,
		WithIngressCorrelation(admission, "connection-full-context"),
	)
	result, err := pipeline.Execute(context.Background(), request, &downstreamRecorder{})
	if err != nil || result.Outcome != AttemptSucceeded {
		t.Fatalf("Execute() = %+v, %v", result, err)
	}

	requests := provider.requestsSnapshot()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d", len(requests))
	}
	var upstream struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(requests[0].Body(), &upstream); err != nil || len(upstream.Messages) != 3 {
		t.Fatalf("upstream received a truncated request: body=%s err=%v", requests[0].Body(), err)
	}
	observation, ok := content.latest()
	if !ok || observation.CaptureRunID != "capture-full-context" ||
		observation.ManualCaptureID != "" || len(observation.Request.Messages) != 3 ||
		observation.Request.Messages[2].Blocks[0].Text != "third" {
		t.Fatalf("local full evidence = %+v", observation)
	}
}

func TestDisabledContentRecordingCreatesNoConversationEvidence(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
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
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
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

func TestOriginalDestinationPreservesClientEnvelopeAndResponse(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindOriginal,
		providerOrigin: "https://api.anthropic.com",
		backend:        protocolspec.DialectAnthropicMessages,
		modelMode:      environment.ModelModePassthrough,
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

func TestOriginalDestinationTransformsCompleteMessagesWithoutExposingCredentials(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindOriginal,
		providerOrigin: "https://api.anthropic.com",
		backend:        protocolspec.DialectAnthropicMessages,
		modelMode:      environment.ModelModePassthrough,
		transform: messagetransform.Policy{
			RequestJavaScript: `
				if (request.headers.authorization !== undefined || request.headers["x-api-key"] !== undefined) {
					throw new Error("credential exposed");
				}
				const payload = JSON.parse(request.body);
				context.marker = payload.model;
				payload.metadata = {transformed: true};
				request.body = JSON.stringify(payload);
			`,
			ResponseJavaScript: `
				const payload = JSON.parse(response.body);
				payload.content[0].text = "transformed:" + context.marker;
				response.headers["x-transform-response"] = "yes";
				response.body = JSON.stringify(payload);
			`,
		},
	})
	responseBody := []byte(`{
		"id":"msg_original","type":"message","role":"assistant","model":"claude-client-alias",
		"content":[{"type":"text","text":"provider-compatible"}],"stop_reason":"end_turn",
		"stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":2}
	}`)
	provider := &providerDouble{results: []providerResult{{response: &http.Response{
		StatusCode: http.StatusCreated,
		Header: http.Header{
			"Content-Type": {"application/json"},
			"X-Upstream":   {"kept"},
			"Set-Cookie":   {"provider-secret=hidden"},
		},
		Body: io.NopCloser(bytes.NewReader(responseBody)),
	}}}}
	pipeline := newTestPipeline(t, nil, provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}
	_, err := pipeline.Execute(
		context.Background(),
		mustClientRequestWithOptions(
			t,
			"exchange-original-transform",
			plan,
			completeClientRequest(),
			WithOriginalHeaders(http.Header{
				"Authorization": {"Bearer client-owned"},
				"X-Api-Key":     {"client-owned-key"},
			}),
		),
		downstream,
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 1 ||
		requests[0].Headers().Get("Authorization") != "Bearer client-owned" ||
		requests[0].Headers().Get("X-Api-Key") != "client-owned-key" ||
		!bytes.Contains(requests[0].Body(), []byte(`"transformed":true`)) {
		t.Fatalf("transformed original request = %#v", requests)
	}
	if !bytes.Contains(downstream.bytesSnapshot(), []byte("transformed:claude-client-alias")) {
		t.Fatalf("transformed original response = %s", downstream.bytesSnapshot())
	}
	envelopes := downstream.envelopesSnapshot()
	if len(envelopes) != 1 ||
		envelopes[0].StatusCode() != http.StatusCreated ||
		envelopes[0].Headers().Get("X-Upstream") != "kept" ||
		envelopes[0].Headers().Get("X-Transform-Response") != "yes" ||
		envelopes[0].Headers().Get("Set-Cookie") != "provider-secret=hidden" {
		t.Fatalf("transformed original response envelope = %#v", envelopes)
	}
}

func TestOriginalDestinationPreservesGzipStreamAndRecordsDecodedResponse(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindOriginal,
		providerOrigin: "https://api.anthropic.com",
		backend:        protocolspec.DialectAnthropicMessages,
		modelMode:      environment.ModelModePassthrough,
	})
	wire := []byte(strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_gzip","type":"message","role":"assistant","model":"claude-client-alias","usage":{"input_tokens":4}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"compressed evidence"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n") + "\n")
	compressed := gzipFixture(t, wire)
	provider := &providerDouble{results: []providerResult{{response: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     []string{"text/event-stream"},
			"Content-Encoding": []string{"gzip"},
		},
		Body: io.NopCloser(bytes.NewReader(compressed)),
	}}}}
	content := &contentObserverDouble{}
	pipeline := newTestPipelineWithContentObserver(
		t, nil, provider, approvedDecisions(), &attemptObserverDouble{}, content,
	)
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}
	request := mustClientRequestWithOptions(
		t,
		"exchange-original-gzip",
		plan,
		streamingClientRequest(),
		WithOriginalHeaders(http.Header{
			"Authorization":     []string{"Bearer client-owned"},
			"Anthropic-Version": []string{"2023-06-01"},
		}),
	)
	result, err := pipeline.Execute(
		context.Background(),
		request,
		downstream,
	)
	if err != nil || result.Outcome != AttemptSucceeded {
		t.Fatalf("Execute() = %+v, %v", result, err)
	}
	if !bytes.Equal(downstream.bytesSnapshot(), compressed) {
		t.Fatal("original gzip response bytes changed before downstream delivery")
	}
	envelopes := downstream.envelopesSnapshot()
	if len(envelopes) != 1 || envelopes[0].Headers().Get("Content-Encoding") != "gzip" {
		t.Fatalf("downstream envelope = %+v", envelopes)
	}
	observation, ok := content.latest()
	if !ok || observation.Response == nil || len(observation.Response.Blocks) != 1 ||
		observation.Response.Blocks[0].Text != "compressed evidence" ||
		!observation.Response.Usage.Output.Known ||
		observation.Response.Usage.Output.Tokens != 2 {
		t.Fatalf("gzip content observation = %+v", observation)
	}
}

func TestOriginalDestinationTransformsSSEEventsAndPreservesStreaming(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindOriginal,
		providerOrigin: "https://api.anthropic.com",
		backend:        protocolspec.DialectAnthropicMessages,
		modelMode:      environment.ModelModePassthrough,
		transform: messagetransform.Policy{ResponseJavaScript: `
			response.headers["x-stream-transform"] = "yes";
			const payload = JSON.parse(response.body);
			if (payload.type === "content_block_delta") {
				payload.delta.text = "rewritten";
			}
			response.body = JSON.stringify(payload);
		`},
	})
	provider := &providerDouble{results: []providerResult{{response: streamResponse(
		http.StatusOK,
		&boundedChunkReader{reader: anthropicTextProviderStream(), maximum: 37},
	)}}}
	content := &contentObserverDouble{}
	pipeline := newTestPipelineWithContentObserver(
		t,
		nil,
		provider,
		approvedDecisions(),
		&attemptObserverDouble{},
		content,
	)
	raw := &rawObserverDouble{}
	pipeline.rawEvidence = raw
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequestWithOptions(
			t,
			"exchange-original-stream-transform",
			plan,
			streamingClientRequest(),
			WithOriginalHeaders(http.Header{"X-Api-Key": {"client-owned"}}),
		),
		downstream,
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !bytes.Contains(downstream.bytesSnapshot(), []byte("rewritten")) ||
		bytes.Contains(downstream.bytesSnapshot(), []byte(`"text":"hello"`)) ||
		result.Ledger.DownstreamSemanticWrites < 2 {
		t.Fatalf("transformed original stream result=%+v wire=%s", result, downstream.bytesSnapshot())
	}
	envelopes := downstream.envelopesSnapshot()
	if len(envelopes) != 1 || envelopes[0].Headers().Get("X-Stream-Transform") != "yes" {
		t.Fatalf("transformed original stream envelope = %#v", envelopes)
	}
	observation, ok := content.latest()
	if !ok || observation.Response == nil ||
		len(observation.Response.Blocks) != 1 ||
		observation.Response.Blocks[0].Text != "rewritten" {
		t.Fatalf("transformed original stream observation = %+v", observation)
	}
	rawMessages := raw.snapshot()
	if len(rawMessages) != 2 ||
		rawMessages[0].Layer != rawevidence.LayerTransformRequestInput ||
		rawMessages[1].Layer != rawevidence.LayerTransformResponseInput ||
		!bytes.Contains(rawMessages[1].Body, []byte(`"text":"hello"`)) ||
		bytes.Contains(rawMessages[1].Body, []byte("rewritten")) ||
		rawMessages[1].Headers.Get("Content-Encoding") != "" {
		t.Fatalf("stream Transform input evidence = %+v", rawMessages)
	}
}

func TestOriginalDestinationTransformsCompressedSSEAsLogicalMessages(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindOriginal,
		providerOrigin: "https://api.anthropic.com",
		backend:        protocolspec.DialectAnthropicMessages,
		modelMode:      environment.ModelModePassthrough,
		transform: messagetransform.Policy{ResponseJavaScript: `
			response.headers["x-stream-transform"] = "gzip-decoded";
			const payload = JSON.parse(response.body);
			if (payload.type === "content_block_delta") {
				payload.delta.text = "rewritten compressed";
			}
			response.body = JSON.stringify(payload);
		`},
	})
	wire, err := io.ReadAll(anthropicTextProviderStream())
	if err != nil {
		t.Fatalf("read provider stream: %v", err)
	}
	compressed := gzipFixture(t, wire)
	provider := &providerDouble{results: []providerResult{{response: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     {"text/event-stream"},
			"Content-Encoding": {"gzip"},
		},
		Body: io.NopCloser(&boundedChunkReader{
			reader: bytes.NewReader(compressed), maximum: 19,
		}),
	}}}}
	pipeline := newTestPipeline(
		t, nil, provider, approvedDecisions(), &attemptObserverDouble{},
	)
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequestWithOptions(
			t,
			"exchange-original-compressed-stream-transform",
			plan,
			streamingClientRequest(),
			WithOriginalHeaders(http.Header{"X-Api-Key": {"client-owned"}}),
		),
		downstream,
	)
	if err != nil || result.Outcome != AttemptSucceeded {
		t.Fatalf("Execute() = %+v, %v", result, err)
	}
	if !bytes.Contains(downstream.bytesSnapshot(), []byte("rewritten compressed")) {
		t.Fatalf("transformed stream = %s", downstream.bytesSnapshot())
	}
	envelopes := downstream.envelopesSnapshot()
	if len(envelopes) != 1 ||
		envelopes[0].Headers().Get("Content-Encoding") != "" ||
		envelopes[0].Headers().Get("X-Stream-Transform") != "gzip-decoded" {
		t.Fatalf("transformed stream envelope = %#v", envelopes)
	}
}

func TestOriginalResponsesAcceptsCanceledReadAfterProvenTerminal(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		clientProtocol: environment.ClientProtocolOpenAIResponses,
		destination:    environment.DestinationKindOriginal,
		providerOrigin: "https://api.openai.com",
		backend:        protocolspec.DialectOpenAIResponses,
		modelMode:      environment.ModelModePassthrough,
	})
	wire := originalResponsesTerminalWire(t)
	provider := &providerDouble{results: []providerResult{{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(&terminalCanceledReader{body: wire}),
	}}}}
	content := &contentObserverDouble{}
	pipeline := newTestPipelineWithContentObserver(
		t, nil, provider, approvedDecisions(), &attemptObserverDouble{}, content,
	)
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}
	request := mustClientRequestWithOptions(
		t,
		"exchange-original-responses-canceled-eof",
		plan,
		streamingResponsesClientRequest(),
		WithOriginalHeaders(http.Header{"Authorization": []string{"Bearer client-owned"}}),
	)

	result, err := pipeline.Execute(context.Background(), request, downstream)
	if err != nil || result.Outcome != AttemptSucceeded || !result.Ledger.DownstreamTerminal {
		t.Fatalf("Execute() = %+v, %v", result, err)
	}
	if !bytes.Equal(downstream.bytesSnapshot(), wire) {
		t.Fatal("original Responses stream changed before downstream delivery")
	}
	observation, ok := content.latest()
	if !ok || observation.Response == nil || len(observation.Response.Blocks) != 1 ||
		observation.Response.Blocks[0].Text != "provider-compatible" {
		t.Fatalf("Responses content observation = %+v", observation)
	}
}

func TestTransformedOriginalResponsesAcceptsCanceledReadAfterProvenTerminal(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		clientProtocol: environment.ClientProtocolOpenAIResponses,
		destination:    environment.DestinationKindOriginal,
		providerOrigin: "https://api.openai.com",
		backend:        protocolspec.DialectOpenAIResponses,
		modelMode:      environment.ModelModePassthrough,
		transform: messagetransform.Policy{ResponseJavaScript: `
			response.headers["x-transform"] = "applied";
		`},
	})
	wire := originalResponsesTerminalWire(t)
	provider := &providerDouble{results: []providerResult{{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(&terminalCanceledReader{body: wire}),
	}}}}
	content := &contentObserverDouble{}
	pipeline := newTestPipelineWithContentObserver(
		t, nil, provider, approvedDecisions(), &attemptObserverDouble{}, content,
	)
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}
	request := mustClientRequestWithOptions(
		t,
		"exchange-transformed-original-responses-canceled-eof",
		plan,
		streamingResponsesClientRequest(),
		WithOriginalHeaders(http.Header{"Authorization": []string{"Bearer client-owned"}}),
	)

	result, err := pipeline.Execute(context.Background(), request, downstream)
	if err != nil || result.Outcome != AttemptSucceeded || !result.Ledger.DownstreamTerminal {
		t.Fatalf("Execute() = %+v, %v", result, err)
	}
	if len(downstream.envelopesSnapshot()) != 1 ||
		downstream.envelopesSnapshot()[0].Headers().Get("X-Transform") != "applied" {
		t.Fatalf("transformed response envelope = %#v", downstream.envelopesSnapshot())
	}
	observation, ok := content.latest()
	if !ok || observation.Response == nil || len(observation.Response.Blocks) != 1 ||
		observation.Response.Blocks[0].Text != "provider-compatible" {
		t.Fatalf("Responses content observation = %+v", observation)
	}
}

func TestOriginalDestinationClientCredentialPathRemainsSemantic(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination: environment.DestinationKindOriginal,
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
		requests[0].CredentialMode() != providerauth.CredentialClientPassthrough {
		t.Fatalf("same-origin request = %+v", requests)
	}
	if !bytes.Equal(downstream.bytesSnapshot(), providerBody) {
		t.Fatalf("same-origin response changed: %s", downstream.bytesSnapshot())
	}
}

func TestStreamingStillPublishesIncrementalClientEvents(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
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

func TestStreamingResponseTransformRewritesEachSSEEventWithoutBufferingTheTurn(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 1, epoch: 1}},
		preferred:      "account.primary",
		transform: messagetransform.Policy{ResponseJavaScript: `
			if (!response.streaming || response.eventName !== "message") {
				throw new Error("stream metadata missing");
			}
			response.headers["x-stream-transform"] = "yes";
			const payload = JSON.parse(response.body);
			if (payload.choices?.[0]?.delta?.content === "Hello") {
				payload.choices[0].delta.content = "Rewritten";
			}
			response.body = JSON.stringify(payload);
		`},
	})
	provider := &providerDouble{results: []providerResult{{response: streamResponse(
		http.StatusOK,
		normalProviderStream(t, "gpt-provider"),
	)}}}
	content := &contentObserverDouble{}
	pipeline := newTestPipelineWithContentObserver(
		t,
		newAccountAuthority(t, testAccount{id: "account.primary", revision: 1, epoch: 1}),
		provider,
		approvedDecisions(),
		&attemptObserverDouble{},
		content,
	)
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-stream-transform", plan, streamingClientRequest()),
		downstream,
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	wire := downstream.bytesSnapshot()
	if !bytes.Contains(wire, []byte("Rewritten")) || bytes.Contains(wire, []byte(`"Hello"`)) ||
		result.Ledger.DownstreamSemanticWrites < 2 {
		t.Fatalf("transformed stream result=%+v wire=%s", result, wire)
	}
	envelopes := downstream.envelopesSnapshot()
	if len(envelopes) != 1 || envelopes[0].Headers().Get("X-Stream-Transform") != "yes" {
		t.Fatalf("transformed stream envelope = %#v", envelopes)
	}
	observation, ok := content.latest()
	if !ok || observation.Response == nil ||
		len(observation.Response.Blocks) != 1 ||
		observation.Response.Blocks[0].Text != "Rewritten" {
		t.Fatalf("transformed stream content observation = %+v", observation)
	}
}

func TestManagedStreamingResponseTransformDecodesGzipBeforeEditing(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 1, epoch: 1}},
		preferred:      "account.primary",
		transform: messagetransform.Policy{ResponseJavaScript: `
			response.headers["x-stream-transform"] = "gzip-decoded";
			const payload = JSON.parse(response.body);
			if (payload.choices?.[0]?.delta?.content === "Hello") {
				payload.choices[0].delta.content = "Rewritten compressed";
			}
			response.body = JSON.stringify(payload);
		`},
	})
	wire, err := io.ReadAll(normalProviderStream(t, "gpt-provider"))
	if err != nil {
		t.Fatalf("read provider stream: %v", err)
	}
	compressed := gzipFixture(t, wire)
	provider := &providerDouble{results: []providerResult{{response: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     {"text/event-stream"},
			"Content-Encoding": {"gzip"},
		},
		Body: io.NopCloser(&boundedChunkReader{
			reader: bytes.NewReader(compressed), maximum: 17,
		}),
	}}}}
	pipeline := newTestPipeline(
		t,
		newAccountAuthority(t, testAccount{id: "account.primary", revision: 1, epoch: 1}),
		provider,
		approvedDecisions(),
		&attemptObserverDouble{},
	)
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-compressed-stream-transform", plan, streamingClientRequest()),
		downstream,
	)
	if err != nil || result.Outcome != AttemptSucceeded {
		t.Fatalf("Execute() = %+v, %v", result, err)
	}
	if !bytes.Contains(downstream.bytesSnapshot(), []byte("Rewritten compressed")) {
		t.Fatalf("transformed stream = %s", downstream.bytesSnapshot())
	}
	envelopes := downstream.envelopesSnapshot()
	if len(envelopes) != 1 ||
		envelopes[0].Headers().Get("Content-Encoding") != "" ||
		envelopes[0].Headers().Get("X-Stream-Transform") != "gzip-decoded" {
		t.Fatalf("transformed stream envelope = %#v", envelopes)
	}
}

func TestStreamingResponseTransformFailureBeforeFirstEventCommitsNothing(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 1, epoch: 1}},
		preferred:      "account.primary",
		transform: messagetransform.Policy{
			ResponseJavaScript: `throw new Error("must stay private");`,
		},
	})
	provider := &providerDouble{results: []providerResult{{response: streamResponse(
		http.StatusOK,
		normalProviderStream(t, "gpt-provider"),
	)}}}
	pipeline := newTestPipeline(
		t,
		newAccountAuthority(t, testAccount{id: "account.primary", revision: 1, epoch: 1}),
		provider,
		approvedDecisions(),
		&attemptObserverDouble{},
	)
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}

	_, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-stream-transform-before-commit", plan, streamingClientRequest()),
		downstream,
	)
	if ReasonOf(err) != ReasonMessageTransformFailed {
		t.Fatalf("Execute() error = %v, want %s", err, ReasonMessageTransformFailed)
	}
	if len(downstream.envelopesSnapshot()) != 0 || len(downstream.bytesSnapshot()) != 0 ||
		len(downstream.abortsSnapshot()) != 0 {
		t.Fatalf(
			"pre-commit failure reached downstream: envelopes=%d body=%q aborts=%+v",
			len(downstream.envelopesSnapshot()),
			downstream.bytesSnapshot(),
			downstream.abortsSnapshot(),
		)
	}
}

func TestStreamingResponseTransformFailureAfterFirstEventAbortsCommittedStream(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 1, epoch: 1}},
		preferred:      "account.primary",
		transform: messagetransform.Policy{ResponseJavaScript: `
			context.events = (context.events ?? 0) + 1;
			if (context.events === 2) throw new Error("must stay private");
		`},
	})
	first := `{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1,"model":"gpt-provider","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`
	second := `{"id":"chatcmpl-stream","object":"chat.completion.chunk","created":1,"model":"gpt-provider","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	provider := &providerDouble{results: []providerResult{{response: streamResponse(
		http.StatusOK,
		&chunkSequenceReader{chunks: [][]byte{
			joinProviderEvents(t, first),
			joinProviderEvents(t, second),
		}},
	)}}}
	pipeline := newTestPipeline(
		t,
		newAccountAuthority(t, testAccount{id: "account.primary", revision: 1, epoch: 1}),
		provider,
		approvedDecisions(),
		&attemptObserverDouble{},
	)
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}

	_, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-stream-transform-after-commit", plan, streamingClientRequest()),
		downstream,
	)
	if ReasonOf(err) != ReasonMessageTransformFailed {
		t.Fatalf("Execute() error = %v, want %s", err, ReasonMessageTransformFailed)
	}
	if len(downstream.envelopesSnapshot()) != 1 ||
		!bytes.Contains(downstream.bytesSnapshot(), []byte("Hello")) {
		t.Fatalf(
			"first transformed event was not committed: envelopes=%d body=%q",
			len(downstream.envelopesSnapshot()),
			downstream.bytesSnapshot(),
		)
	}
	aborts := downstream.abortsSnapshot()
	if len(aborts) != 1 || aborts[0].ReasonCode != ReasonMessageTransformFailed {
		t.Fatalf("post-commit failure aborts = %+v", aborts)
	}
}

func TestManagedCompatibleTextStreamCompletesAfterEmptyTerminalRelease(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://api.anthropic.com",
		backend:        protocolspec.DialectAnthropicMessages,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "claude-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 1, epoch: 1}},
		preferred:      "account.primary",
	})
	authority := newAccountAuthority(t, testAccount{id: "account.primary", revision: 1, epoch: 1})
	provider := &providerDouble{results: []providerResult{{response: streamResponse(
		http.StatusOK,
		anthropicTextProviderStream(),
	)}}}
	pipeline := newTestPipeline(t, authority, provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)

	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-compatible-stream", plan, streamingClientRequest()),
		downstream,
	)
	if err != nil || result.Outcome != AttemptSucceeded || !result.Ledger.DownstreamTerminal {
		t.Fatalf("Execute() = %+v, %v", result, err)
	}
	wire := downstream.bytesSnapshot()
	if !bytes.Contains(wire, []byte(`"text":"hello"`)) ||
		!bytes.Contains(wire, []byte(`"type":"message_stop"`)) ||
		!bytes.Contains(wire, []byte(`"model":"claude-client-alias"`)) ||
		bytes.Contains(wire, []byte(`"model":"claude-provider"`)) {
		t.Fatalf("downstream stream = %s", wire)
	}
}

func TestManagedResponsesRequestUsesTheSameFrozenEnvironmentAuthority(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		clientProtocol: environment.ClientProtocolOpenAIResponses,
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
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

func TestManagedResponsesHTTP2ClientUsesLoopbackHTTP1Provider(t *testing.T) {
	const mappedModel = "dashscope:deepseek-v4-flash-0731"
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		clientProtocol:     environment.ClientProtocolOpenAIResponses,
		downstreamProtocol: wireprofile.ApplicationProtocolHTTP2,
		destination:        environment.DestinationKindUpstream,
		providerOrigin:     "http://127.0.0.1:23333/v1",
		backend:            protocolspec.DialectOpenAIResponses,
		modelMode:          environment.ModelModeMap,
		mappedModel:        mappedModel,
		accounts: []testAccount{{
			id: "account.primary", revision: 3, epoch: 7,
		}},
		preferred: "account.primary",
	})
	authority := newAccountAuthority(
		t,
		testAccount{id: "account.primary", revision: 3, epoch: 7},
	)
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(
			http.StatusOK,
			completeResponsesProviderResponse(mappedModel),
		),
	}}}
	content := &contentObserverDouble{}
	pipeline := newTestPipelineWithContentObserver(
		t,
		authority,
		provider,
		approvedDecisions(),
		&attemptObserverDouble{},
		content,
	)
	defer shutdownPipeline(t, pipeline)

	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequestWithHTTPProtocol(
			t,
			"exchange-responses-loopback-h2",
			plan,
			completeResponsesClientRequest(),
			wireprofile.ApplicationProtocolHTTP2,
		),
		downstream,
	)
	if err != nil {
		t.Fatal(err)
	}
	requests := provider.requestsSnapshot()
	if result.Outcome != AttemptSucceeded || len(requests) != 1 ||
		requests[0].RelativePath() != responseschat.ResponsesProviderRelativePath ||
		result.Presentation.ClientProtocol != wireprofile.ApplicationProtocolHTTP2 ||
		result.Presentation.UpstreamProtocol != wireprofile.ApplicationProtocolHTTP1 {
		t.Fatalf("result = %+v, requests = %+v", result, requests)
	}
	var clientBody struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(downstream.bytesSnapshot(), &clientBody); err != nil {
		t.Fatal(err)
	}
	if clientBody.Model != "codex-client-alias" {
		t.Fatalf("client response model = %q", clientBody.Model)
	}
	observation, ok := content.latest()
	if !ok || observation.Response == nil ||
		observation.Response.RequestedModel != "codex-client-alias" ||
		observation.Response.EffectiveModel != mappedModel ||
		observation.Response.ReportedModel != mappedModel {
		t.Fatalf("content observation = %+v", observation)
	}
}

func TestManagedResponsesStartGroupsExactCodexSessionBeforeTerminal(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		clientProtocol: environment.ClientProtocolOpenAIResponses,
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIResponses,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "provider-model",
		accounts:       []testAccount{{id: "account.primary", revision: 1, epoch: 1}},
		preferred:      "account.primary",
	})
	provider := &providerDouble{results: []providerResult{
		{response: jsonResponse(http.StatusOK, completeResponsesProviderResponse("provider-model"))},
		{response: jsonResponse(http.StatusOK, completeResponsesProviderResponse("provider-model"))},
		{response: jsonResponse(http.StatusOK, completeResponsesProviderResponse("provider-model"))},
	}}
	observer := &attemptObserverDouble{}
	pipeline := newTestPipeline(
		t,
		newAccountAuthority(t, testAccount{id: "account.primary", revision: 1, epoch: 1}),
		provider,
		approvedDecisions(),
		observer,
	)
	defer shutdownPipeline(t, pipeline)
	admission, err := captureadmission.NewManagedRun(
		captureadmission.ManagedRunEvidence{
			CaptureRunID: "capture-codex-session",
			SourceLabel:  "codex",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		exchangeID string
		sessionID  string
		threadID   string
		turnID     string
	}{
		{exchangeID: "exchange-session-turn-1", sessionID: "session-1", threadID: "thread-1", turnID: "turn-1"},
		{exchangeID: "exchange-session-turn-2", sessionID: "session-1", threadID: "thread-1", turnID: "turn-2"},
		{exchangeID: "exchange-other-session", sessionID: "session-2", threadID: "thread-2", turnID: "turn-1"},
	} {
		result, err := pipeline.Execute(
			context.Background(),
			mustClientRequestWithOptions(
				t,
				test.exchangeID,
				plan,
				responsesClientRequestWithIdentity(
					test.sessionID,
					test.threadID,
					test.turnID,
				),
				WithIngressCorrelation(admission, "connection-codex-session"),
			),
			&downstreamRecorder{},
		)
		if err != nil || result.Outcome != AttemptSucceeded {
			t.Fatalf("Execute(%s) = %+v, %v", test.exchangeID, result, err)
		}
	}

	starts := observer.startSnapshot()
	if len(starts) != 3 ||
		starts[0].Conversation.Kind != agentconversation.KindMain ||
		starts[0].Conversation.Evidence != agentconversation.EvidenceExplicitSession ||
		starts[0].Conversation.ProjectionID != starts[1].Conversation.ProjectionID ||
		starts[0].Conversation.ProjectionID == starts[2].Conversation.ProjectionID {
		t.Fatalf("start Conversations = %#v", starts)
	}
}

func TestManagedResponsesStreamingKeepsIncrementalClientSemantics(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		clientProtocol: environment.ClientProtocolOpenAIResponses,
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
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

func TestAccountSelectorChoosesOneFrozenEndpointAccount(t *testing.T) {
	accounts := []testAccount{
		{id: "account.backup", revision: 4, epoch: 5},
		{id: "account.primary", revision: 2, epoch: 3},
	}
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
		accounts:       accounts,
		preferred:      "account.primary",
		selector: &codelibrary.AccountSelectorRevision{
			ID: "model-account", Revision: 2, CollectionID: "routing", DisplayName: "Model account",
			Policy: accountselector.Policy{JavaScript: `
selection.accountId = request.requestedModel === "claude-client-alias"
  ? "account.backup"
  : "account.primary";
`},
			PublishedAt: time.Date(2026, time.August, 28, 0, 0, 0, 0, time.UTC),
		},
	})
	authority := newAccountAuthority(t, accounts...)
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(http.StatusOK, completeProviderResponse("gpt-provider")),
	}}}
	pipeline := newTestPipeline(t, authority, provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-account-selector", plan, completeClientRequest()),
		&downstreamRecorder{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountID != "account.backup" || result.AccountRevision != 4 || result.CredentialEpoch != 5 {
		t.Fatalf("final account = %+v", result)
	}
	leaseRequests := authority.snapshot()
	if len(leaseRequests) != 1 || leaseRequests[0].AccountID() != "account.backup" {
		t.Fatalf("lease selection = %+v", leaseRequests)
	}
	providerRequests := provider.requestsSnapshot()
	if len(providerRequests) != 1 {
		t.Fatalf("provider attempts = %d", len(providerRequests))
	}
	for _, request := range providerRequests {
		if request.Provenance().RouteID().String() != "route.primary" ||
			request.Provenance().RouteRevision() != 6 {
			t.Fatalf("attempt escaped frozen route = %+v", request.Provenance())
		}
	}
}

func TestManagedAuthenticationRejectionNeverSelectsAnotherAccount(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			accounts := []testAccount{
				{id: "account.primary", revision: 2, epoch: 3},
				{id: "account.backup", revision: 4, epoch: 5},
			}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				destination: environment.DestinationKindUpstream, providerOrigin: "https://provider.example/v1",
				backend: protocolspec.DialectOpenAIChat, modelMode: environment.ModelModeMap,
				mappedModel: "gpt-provider", accounts: accounts,
				preferred: "account.primary",
			})
			authority := newAccountAuthority(t, accounts...)
			provider := &providerDouble{results: []providerResult{
				{response: jsonResponse(status, []byte(`{"error":{"type":"authentication_error"}}`))},
				{response: jsonResponse(http.StatusOK, completeProviderResponse("gpt-provider"))},
			}}
			pipeline := newTestPipeline(
				t, authority, provider, approvedDecisions(), &attemptObserverDouble{},
			)
			defer shutdownPipeline(t, pipeline)

			_, err := pipeline.Execute(
				context.Background(),
				mustClientRequest(t, "exchange-auth-rejected", plan, completeClientRequest()),
				&downstreamRecorder{},
			)
			var failure *Failure
			if !errors.As(err, &failure) ||
				failure.Code != ReasonProviderStatusRejected ||
				failure.ProviderStatus != status {
				t.Fatalf("authentication rejection = %v", err)
			}
			if got := provider.requestsSnapshot(); len(got) != 1 {
				t.Fatalf("provider attempts = %d, want 1", len(got))
			}
			if got := authority.snapshot(); len(got) != 1 ||
				got[0].AccountID() != "account.primary" {
				t.Fatalf("account lease requests = %+v", got)
			}
			if authority.releaseCount("account.primary") != 1 ||
				authority.releaseCount("account.backup") != 0 {
				t.Fatal("authentication rejection crossed the frozen account boundary")
			}
		})
	}
}

func TestAccountSelectorFailureSendsNothing(t *testing.T) {
	accounts := []testAccount{{id: "account.only", revision: 2, epoch: 3}}
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
		accounts:       accounts,
		selector: &codelibrary.AccountSelectorRevision{
			ID: "reject", Revision: 1, CollectionID: "routing", DisplayName: "Reject",
			Policy:      accountselector.Policy{JavaScript: `selection.accountId = "account.outside";`},
			PublishedAt: time.Date(2026, time.August, 28, 0, 0, 0, 0, time.UTC),
		},
	})
	authority := newAccountAuthority(t, accounts...)
	provider := &providerDouble{}
	pipeline := newTestPipeline(t, authority, provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)

	_, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-selector-failed", plan, completeClientRequest()),
		&downstreamRecorder{},
	)
	if ReasonOf(err) != ReasonAccountSelectorFailed {
		t.Fatalf("selector failure = %v", err)
	}
	if len(authority.snapshot()) != 0 || provider.callCount() != 0 {
		t.Fatal("failed selector acquired credentials or sent provider traffic")
	}
}

func TestToolDecisionRejectsBeforeAnyToolBytesReachClient(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
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

func TestToolDecisionExpiryHasDistinctReasonAndNeverReleasesBytes(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 1, epoch: 1}},
		preferred:      "account.primary",
	})
	authority := newAccountAuthority(t, testAccount{id: "account.primary", revision: 1, epoch: 1})
	provider := &providerDouble{results: []providerResult{{response: jsonResponse(
		http.StatusOK,
		completeToolProviderResponse("gpt-provider"),
	)}}}
	decisions := &decisionDouble{decision: ToolDecision{Outcome: ToolDecisionRejected, ReasonCode: "approval_expired"}}
	pipeline := newTestPipeline(t, authority, provider, decisions, &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(t, "exchange-tool-expired", plan, toolClientRequest()),
		downstream,
	)
	if ReasonOf(err) != ReasonToolDecisionExpired || result.Outcome != AttemptAborted ||
		len(downstream.bytesSnapshot()) != 0 || decisions.callCount() != 1 {
		t.Fatalf("result=%+v err=%v body=%s decisions=%d", result, err, downstream.bytesSnapshot(), decisions.callCount())
	}
}

func TestStreamingToolDecisionKeepsTheClientAliveUntilApproval(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		mappedModel:    "gpt-provider",
		accounts:       []testAccount{{id: "account.primary", revision: 1, epoch: 1}},
		preferred:      "account.primary",
	})
	authority := newAccountAuthority(t, testAccount{id: "account.primary", revision: 1, epoch: 1})
	provider := &providerDouble{results: []providerResult{{response: streamResponse(
		http.StatusOK,
		streamingToolProviderResponse(t, "gpt-provider"),
	)}}}
	decisions := newBlockingDecisionDouble()
	pipeline := newTestPipeline(t, authority, provider, decisions, &attemptObserverDouble{})
	pipeline.streamBudgets = StreamBudgets{
		KeepaliveInterval:       10 * time.Millisecond,
		ProviderProgressTimeout: time.Second,
	}
	defer shutdownPipeline(t, pipeline)
	downstream := &downstreamRecorder{}
	completed := make(chan error, 1)
	go func() {
		_, err := pipeline.Execute(
			context.Background(),
			mustClientRequest(t, "exchange-stream-tool-approval", plan, streamingToolClientRequest()),
			downstream,
		)
		completed <- err
	}()
	select {
	case <-decisions.entered:
	case err := <-completed:
		t.Fatalf("stream completed before requesting tool approval: %v", err)
	case <-time.After(time.Second):
		t.Fatal("tool decision was not requested")
	}
	deadline := time.Now().Add(time.Second)
	for downstream.keepaliveCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := downstream.keepaliveCount(); got < 2 {
		t.Fatalf("keepalives while awaiting approval = %d, want at least 2", got)
	}
	close(decisions.approve)
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("approved streaming tool response did not complete")
	}
	if !bytes.Contains(downstream.bytesSnapshot(), []byte("call-shell")) {
		t.Fatalf("approved tool response was not released: %s", downstream.bytesSnapshot())
	}
}

func TestOperationEvidenceMismatchFailsBeforeAccountOrProvider(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination: environment.DestinationKindUpstream, providerOrigin: "https://provider.example/v1",
		backend: protocolspec.DialectOpenAIChat, modelMode: environment.ModelModeMap, mappedModel: "gpt-provider",
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
		destination: environment.DestinationKindUpstream, providerOrigin: "https://provider.example/v1",
		backend: protocolspec.DialectOpenAIChat, modelMode: environment.ModelModeMap, mappedModel: "gpt-provider",
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
	clientProtocol     environment.ClientProtocol
	downstreamProtocol wireprofile.ApplicationProtocol
	destination        environment.DestinationKind
	providerOrigin     string
	backend            protocolspec.Dialect
	modelMode          environment.ModelMode
	mappedModel        string
	modelMappings      []environment.ModelMapping
	accounts           []testAccount
	preferred          string
	selector           *codelibrary.AccountSelectorRevision
	recording          environment.ContentRecordingPolicy
	transform          messagetransform.Policy
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
	realm := "realm.provider"
	accountPolicy := environment.RouteAccountPolicy{
		Revision: 5, Mode: environment.AccountSelectionFixed,
	}
	catalog := make(testAccountCatalog, len(options.accounts))
	for _, account := range options.accounts {
		catalog[account.id] = environment.AccountDescriptor{
			ID: account.id, Revision: account.revision, DisplayName: account.id,
			UpstreamEndpointID: "target.primary", UpstreamEndpointRevision: 3,
			RealmID: realm, Active: true,
			BackendProtocols: []string{string(options.backend)},
		}
	}
	if options.selector != nil {
		selector := *options.selector
		accountPolicy.Mode = environment.AccountSelectionJavaScript
		accountPolicy.Selector = &selector
		for _, account := range options.accounts {
			accountPolicy.Accounts = append(accountPolicy.Accounts, environment.RouteAccountReference{
				ID: account.id, Revision: account.revision, DisplayName: account.id,
			})
		}
	} else if options.preferred != "" {
		accountPolicy.FixedAccountID = options.preferred
		for _, account := range options.accounts {
			if account.id == options.preferred {
				accountPolicy.Accounts = []environment.RouteAccountReference{{
					ID: account.id, Revision: account.revision, DisplayName: account.id,
				}}
				break
			}
		}
	}
	recording := options.recording
	if recording.Mode == "" {
		recording = environment.DefaultContentRecordingPolicy()
	}
	modelPolicy := environment.ModelPolicy{Revision: 4, Mode: options.modelMode}
	if options.modelMode == environment.ModelModeMap {
		if len(options.modelMappings) != 0 {
			modelPolicy.Mappings = slices.Clone(options.modelMappings)
		} else {
			requestedModel := "claude-client-alias"
			if clientProtocol == environment.ClientProtocolOpenAIResponses {
				requestedModel = "codex-client-alias"
			}
			modelPolicy.Mappings = []environment.ModelMapping{{
				RequestedModel: requestedModel,
				UpstreamModel:  options.mappedModel,
			}}
		}
	}
	destination := environment.DestinationPlan{Kind: options.destination}
	if options.destination == environment.DestinationKindUpstream {
		providerOrigin := mustProviderOrigin(t, options.providerOrigin)
		destination.Upstream = &environment.UpstreamPlan{
			DefaultRouteID: "route.primary",
			RouteSet: environment.RouteSet{
				ID: "routes.primary", Revision: 4,
				CandidateRouteIDs: []environment.UpstreamRouteID{"route.primary"},
			},
			Routes: []environment.UpstreamRoute{{
				ID: "route.primary", Revision: 6,
				ProviderTarget: environment.ProviderTarget{
					ID: "target.primary", Revision: 3,
					Origin: providerOrigin, RealmID: realm,
					Capabilities: []protocolspec.ProviderCapability{
						protocolspec.ProviderCapabilityMessages,
						protocolspec.ProviderCapabilityStreaming,
						protocolspec.ProviderCapabilityToolCalls,
					},
				},
				BackendProtocol: string(options.backend), AccountPolicy: accountPolicy,
				ModelPolicy:    modelPolicy,
				WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue,
			}},
		}
	}
	var transforms []codelibrary.TransformRevision
	if options.transform.Enabled() {
		transforms = []codelibrary.TransformRevision{{
			ID: "transform.test", Revision: 1, CollectionID: "tests",
			DisplayName: "Test Transform", Policy: options.transform,
			PublishedAt: time.Date(2026, time.August, 27, 0, 0, 0, 0, time.UTC),
		}}
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
				Destination:         destination,
				EgressProfile:       egressprofile.Direct(),
				Transforms:          transforms,
			}},
		}},
	}
	compiler := mustEnvironmentCompiler(t, catalog)
	snapshot, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatalf("compile Environment: %v", err)
	}
	downstreamProtocol := options.downstreamProtocol
	if downstreamProtocol == "" {
		downstreamProtocol = wireprofile.ApplicationProtocolHTTP1
	}
	plan, err := snapshot.ResolveRequest(clientOrigin, environment.RequestFacts{
		Target: protocolspec.RequestTarget{
			Method: http.MethodPost, Path: requestPath,
			Transport: protocolspec.ClientOperationTransportHTTP,
		},
		DownstreamProtocol: downstreamProtocol,
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
		newPair(responseschat.ResponsesPassthroughCodecPairID, responseschat.ResponsesPassthroughCodecRevision, protocolspec.DialectOpenAIResponses, protocolspec.DialectOpenAIResponses),
	})
	if err != nil {
		t.Fatal(err)
	}
	wires, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := environment.NewCompiler(accounts, nil, protocols, wires)
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
	return mustClientRequestWithHTTPProtocol(
		t,
		exchangeID,
		plan,
		body,
		wireprofile.ApplicationProtocolHTTP1,
	)
}

func mustClientRequestWithHTTPProtocol(
	t *testing.T,
	exchangeID string,
	plan environment.RequestPlan,
	body []byte,
	protocol wireprofile.ApplicationProtocol,
) ClientRequest {
	t.Helper()
	return mustClientRequestWithOptionsAndHTTPProtocol(
		t,
		exchangeID,
		plan,
		body,
		protocol,
	)
}

func mustClientRequestWithOptions(
	t *testing.T,
	exchangeID string,
	plan environment.RequestPlan,
	body []byte,
	options ...ClientRequestOption,
) ClientRequest {
	return mustClientRequestWithOptionsAndHTTPProtocol(
		t,
		exchangeID,
		plan,
		body,
		wireprofile.ApplicationProtocolHTTP1,
		options...,
	)
}

func mustClientRequestWithOptionsAndHTTPProtocol(
	t *testing.T,
	exchangeID string,
	plan environment.RequestPlan,
	body []byte,
	protocol wireprofile.ApplicationProtocol,
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
		exchangeID, plan, evidence, body, replay, protocol, options...,
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
	responsesPassthrough, err := responseschat.NewResponsesPassthroughProtocolPath(
		openairesponses.DefaultOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	selector, err := protocolpath.NewSelector(managed, compatible, responses, responsesPassthrough)
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
	observer ExchangeObserver,
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
	observer ExchangeObserver,
	content ContentObserver,
) *Pipeline {
	t.Helper()
	annotations, err := clientannotation.NewSigner(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(annotations.Destroy)
	pipeline, err := New(Options{
		OwnerContext: context.Background(), Actions: newTestActionGate(t), Accounts: accounts,
		ProtocolPaths: mustProtocolPathSelector(t), Provider: provider, ToolDecisions: decisions,
		RetryWaiter: &retryWaiterDouble{}, Observer: observer, ContentObserver: content,
		ObservationTimeout: time.Second,
		Hold:               HoldPolicy{MaxTransportResends: 0, RetryDelay: 0, MaxDuration: time.Second},
		Stream:             DefaultStreamBudgets(),
		ClientAnnotations:  annotations,
		Now:                time.Now,
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
	mu         sync.Mutex
	envelopes  []ResponseEnvelope
	body       bytes.Buffer
	aborts     []FailureNotice
	keepalives int
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

func (downstream *downstreamRecorder) Keepalive(context.Context) error {
	downstream.mu.Lock()
	defer downstream.mu.Unlock()
	downstream.keepalives++
	return nil
}

func (downstream *downstreamRecorder) keepaliveCount() int {
	downstream.mu.Lock()
	defer downstream.mu.Unlock()
	return downstream.keepalives
}

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

func (downstream *downstreamRecorder) abortsSnapshot() []FailureNotice {
	downstream.mu.Lock()
	defer downstream.mu.Unlock()
	return slices.Clone(downstream.aborts)
}

type decisionDouble struct {
	mu       sync.Mutex
	decision ToolDecision
	err      error
	requests []ToolDecisionRequest
}

type blockingDecisionDouble struct {
	entered chan struct{}
	approve chan struct{}
	once    sync.Once
}

func newBlockingDecisionDouble() *blockingDecisionDouble {
	return &blockingDecisionDouble{
		entered: make(chan struct{}),
		approve: make(chan struct{}),
	}
}

func (decisions *blockingDecisionDouble) Decide(
	ctx context.Context,
	_ ToolDecisionRequest,
) (ToolDecision, error) {
	decisions.once.Do(func() { close(decisions.entered) })
	select {
	case <-decisions.approve:
		return ToolDecision{Outcome: ToolDecisionApproved}, nil
	case <-ctx.Done():
		return ToolDecision{}, context.Cause(ctx)
	}
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
	starts       []StartObservation
	observations []AttemptObservation
}

type rawObserverDouble struct {
	mu           sync.Mutex
	observations []rawevidence.Observation
}

func (observer *rawObserverDouble) Observe(
	_ context.Context,
	observation rawevidence.Observation,
) (rawevidence.Watermark, error) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observation.Headers = observation.Headers.Clone()
	observation.Body = bytes.Clone(observation.Body)
	observer.observations = append(observer.observations, observation)
	return rawevidence.Watermark{
		WriterID: "transform-test", Sequence: uint64(len(observer.observations)),
	}, nil
}

func (observer *rawObserverDouble) snapshot() []rawevidence.Observation {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return slices.Clone(observer.observations)
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

func (observer *attemptObserverDouble) ObserveStart(
	_ context.Context,
	observation StartObservation,
) error {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.starts = append(observer.starts, observation)
	return nil
}

func (observer *attemptObserverDouble) ObserveTerminal(_ context.Context, observation AttemptObservation) error {
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

func (observer *attemptObserverDouble) startSnapshot() []StartObservation {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return slices.Clone(observer.starts)
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

func gzipFixture(t *testing.T, body []byte) []byte {
	t.Helper()
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

type terminalCanceledReader struct {
	body []byte
	done bool
}

type boundedChunkReader struct {
	reader  io.Reader
	maximum int
}

type chunkSequenceReader struct {
	chunks [][]byte
}

func (reader *chunkSequenceReader) Read(destination []byte) (int, error) {
	if len(reader.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := reader.chunks[0]
	count := copy(destination, chunk)
	if count == len(chunk) {
		reader.chunks = reader.chunks[1:]
	} else {
		reader.chunks[0] = chunk[count:]
	}
	return count, nil
}

func (reader *boundedChunkReader) Read(destination []byte) (int, error) {
	if reader.maximum > 0 && len(destination) > reader.maximum {
		destination = destination[:reader.maximum]
	}
	return reader.reader.Read(destination)
}

func (reader *terminalCanceledReader) Read(destination []byte) (int, error) {
	if len(reader.body) > 0 {
		count := copy(destination, reader.body)
		reader.body = reader.body[count:]
		return count, nil
	}
	if !reader.done {
		reader.done = true
		return 0, context.Canceled
	}
	return 0, io.EOF
}

func appendSSEFixture(t *testing.T, name string, payload any) []byte {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := ssewire.Encode(ssewire.Event{Name: name, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func originalResponsesTerminalWire(t *testing.T) []byte {
	t.Helper()
	item := json.RawMessage(`{
		"id":"msg_original_responses",
		"type":"message",
		"status":"completed",
		"role":"assistant",
		"content":[{"type":"output_text","text":"provider-compatible"}]
	}`)
	wire := appendSSEFixture(t, "response.output_item.done", map[string]any{
		"type": "response.output_item.done", "sequence_number": 1,
		"output_index": 0, "item": item,
	})
	return append(wire, appendSSEFixture(t, "response.completed", map[string]any{
		"type": "response.completed", "sequence_number": 2,
		"response": json.RawMessage(`{
			"id":"resp_original","created_at":1,"status":"completed",
			"model":"codex-client-alias","output":[],
			"usage":{"input_tokens":4,"input_tokens_details":{"cached_tokens":0},
			"output_tokens":2,"output_tokens_details":{"reasoning_tokens":0}}
		}`),
	})...)
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

func streamingToolProviderResponse(t *testing.T, model string) io.Reader {
	t.Helper()
	return bytes.NewReader(joinProviderEvents(
		t,
		`{"id":"chatcmpl-stream-tool","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call-shell","type":"function","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-stream-tool","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-stream-tool","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"chatcmpl-stream-tool","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
		`[DONE]`,
	))
}

func anthropicTextProviderStream() io.Reader {
	return strings.NewReader(strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_text","type":"message","role":"assistant","model":"claude-provider","usage":{"input_tokens":3}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n") + "\n")
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

func secondTurnCitedClientRequest() []byte {
	return []byte(`{
		"model":"claude-client-alias",
		"max_tokens":64,
		"messages":[
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"private state","signature":"signed-state"},
				{"type":"text","text":"first answer","citations":[]}
			]},
			{"role":"user","content":[{"type":"text","text":"follow up"}]}
		],
		"stream":false
	}`)
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

func responsesClientRequestWithIdentity(
	sessionID string,
	threadID string,
	turnID string,
) []byte {
	return []byte(`{
		"model":"codex-client-alias",
		"input":[{
			"type":"message","role":"user",
			"content":[{"type":"input_text","text":"hello"}]
		}],
		"store":false,"stream":false,
		"client_metadata":{
			"session_id":"` + sessionID + `",
			"thread_id":"` + threadID + `",
			"turn_id":"` + turnID + `"
		}
	}`)
}

func completeResponsesProviderResponse(model string) []byte {
	return []byte(`{
		"id":"resp_complete",
		"created_at":1,
		"status":"completed",
		"model":"` + model + `",
		"output":[{
			"id":"msg_complete",
			"type":"message",
			"status":"completed",
			"role":"assistant",
			"content":[{"type":"output_text","text":"Done.","annotations":[]}]
		}],
		"usage":{}
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

func streamingToolClientRequest() []byte {
	return []byte(`{
		"model":"claude-client-alias","max_tokens":32,"stream":true,
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

func TestMergeClientProtocolEvidenceFillsOnlyAbsentNames(t *testing.T) {
	t.Parallel()

	request := protocolcore.Request{ProtocolEvidence: []protocolcore.ProtocolEvidenceValue{
		{Name: "claude.agent_id", Value: "decoder-agent"},
		{Name: "openai_responses.turn_id", Value: "turn-1"},
	}}
	merged := mergeClientProtocolEvidence(
		request,
		[]protocolcore.ProtocolEvidenceValue{
			{Name: "claude.agent_id", Value: "header-agent"},
			{Name: "claude.parent_agent_id", Value: "parent-1"},
			{Name: "claude.session_id", Value: "session-1"},
		},
	)
	want := []protocolcore.ProtocolEvidenceValue{
		{Name: "claude.agent_id", Value: "decoder-agent"},
		{Name: "claude.parent_agent_id", Value: "parent-1"},
		{Name: "claude.session_id", Value: "session-1"},
		{Name: "openai_responses.turn_id", Value: "turn-1"},
	}
	if !slices.Equal(merged.ProtocolEvidence, want) {
		t.Fatalf("merged protocol evidence = %#v", merged.ProtocolEvidence)
	}
	if request.ProtocolEvidence[0].Value != "decoder-agent" ||
		len(request.ProtocolEvidence) != 2 {
		t.Fatalf("merge mutated decoded request: %#v", request.ProtocolEvidence)
	}
}
