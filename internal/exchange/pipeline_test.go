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

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/ssewire"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
)

func TestPipelineExecutesCompleteResponseFromOneFrozenPlan(t *testing.T) {
	t.Parallel()

	accessID := mustAccessID(t, "access-complete")
	snapshot := compileTestSnapshot(t, accessID, 1, "gpt-plan-one")
	resolver := &resolverDouble{snapshots: []access.AccessPlanSnapshot{snapshot}}
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(http.StatusOK, completeProviderResponse("gpt-plan-one")),
		evidence: providertransport.CredentialEvidence{
			DriverRef:  access.AuthDriverStaticHeaderValue,
			HeaderName: "authorization",
			SecretRead: true,
		},
	}}}
	decisions := &decisionDouble{decision: ToolDecision{
		Outcome: ToolDecisionApproved,
	}}
	pipeline := newTestPipeline(t, resolver, provider, decisions)
	t.Cleanup(func() { shutdownPipeline(t, pipeline) })

	inputBody := []byte(`{
		"model":"claude-client-alias",
		"max_tokens":32,
		"metadata":{"user_id":"test-only"},
		"messages":[{"role":"user","content":"hello"}]
	}`)
	request, err := NewClientRequest(
		"exchange-complete",
		snapshot.IngressBinding(),
		inputBody,
		ReplayGenerationCostOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	inputBody[0] = '!'
	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(context.Background(), request, downstream)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if resolver.callCount() != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.callCount())
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 1 {
		t.Fatalf("provider request count = %d, want 1", len(requests))
	}
	if requests[0].RelativePath() != anthropicchat.ProviderRelativePath {
		t.Fatalf("provider path = %q", requests[0].RelativePath())
	}
	var providerWire struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(requests[0].Body(), &providerWire); err != nil {
		t.Fatalf("decode provider request: %v", err)
	}
	if providerWire.Model != "gpt-plan-one" || providerWire.Stream {
		t.Fatalf("provider request = %+v", providerWire)
	}
	if decisions.callCount() != 0 {
		t.Fatalf("tool decision calls = %d, want 0", decisions.callCount())
	}
	if downstream.modesSnapshot()[0] != ResponseModeJSON {
		t.Fatalf("downstream modes = %v", downstream.modesSnapshot())
	}
	if result.Outcome != AttemptSucceeded ||
		result.AccessRevision != 1 ||
		result.PlanHash != snapshot.PlanHash().String() ||
		!result.Ledger.DownstreamOrdinaryHeaders ||
		!result.Ledger.DownstreamTerminal ||
		result.Ledger.UpstreamSends != 1 ||
		result.Ledger.UpstreamResponses != 1 ||
		!translationHasNotice(
			result.Translation,
			protocolcore.NoticeMetadataNotForwarded,
		) {
		t.Fatalf("result = %+v", result)
	}
}

func TestPipelineResendsSameFrozenRepresentationBeforeSemanticCommit(t *testing.T) {
	t.Parallel()

	accessID := mustAccessID(t, "access-resend")
	snapshot := compileTestSnapshot(t, accessID, 1, "gpt-frozen")
	resolver := &resolverDouble{snapshots: []access.AccessPlanSnapshot{snapshot}}
	provider := &providerDouble{results: []providerResult{
		{response: streamResponse(http.StatusServiceUnavailable, nil)},
		{response: streamResponse(http.StatusOK, normalProviderStream(t, "gpt-frozen"))},
	}}
	waiter := &retryWaiterDouble{}
	pipeline := newTestPipelineWithWaiter(
		t,
		resolver,
		provider,
		&decisionDouble{decision: ToolDecision{Outcome: ToolDecisionApproved}},
		waiter,
	)
	t.Cleanup(func() { shutdownPipeline(t, pipeline) })
	request := mustClientRequest(
		t,
		"exchange-resend",
		accessID,
		streamingClientRequest(),
		ReplayGenerationCostOnly,
	)
	downstream := &downstreamRecorder{}

	result, err := pipeline.Execute(context.Background(), request, downstream)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 2 {
		t.Fatalf("provider request count = %d, want 2", len(requests))
	}
	if !bytes.Equal(requests[0].Body(), requests[1].Body()) ||
		requests[0].RequestID() != requests[1].RequestID() ||
		requests[0].TargetRef() != requests[1].TargetRef() {
		t.Fatal("transport resend did not replay the frozen representation")
	}
	if got := downstream.modesSnapshot(); !slices.Equal(
		got,
		[]ResponseMode{ResponseModeEventStream},
	) {
		t.Fatalf("downstream envelopes = %v, want exactly one", got)
	}
	if waiter.callCount() != 1 ||
		result.TransportResends != 1 ||
		result.Ledger.UpstreamSends != 2 ||
		result.Ledger.UpstreamResponses != 2 ||
		!result.Ledger.DownstreamHoldEnvelope ||
		!result.Ledger.DownstreamTerminal ||
		!translationHasNotice(
			result.Translation,
			protocolcore.NoticeLateUsageAccounting,
		) {
		t.Fatalf("resend result = %+v; waiter calls=%d", result, waiter.callCount())
	}
}

func translationHasNotice(
	report protocolcore.TranslationReport,
	code protocolcore.NoticeCode,
) bool {
	return slices.ContainsFunc(
		report.Notices(),
		func(notice protocolcore.TranslationNotice) bool {
			return notice.Code == code
		},
	)
}

func TestPipelineNeverResolvesAgainDuringAnActiveExchange(t *testing.T) {
	t.Parallel()

	accessID := mustAccessID(t, "access-frozen")
	revisionOne := compileTestSnapshot(t, accessID, 1, "gpt-revision-one")
	revisionTwo := compileTestSnapshot(t, accessID, 2, "gpt-revision-two")
	resolver := &resolverDouble{
		snapshots: []access.AccessPlanSnapshot{revisionOne, revisionTwo},
	}
	provider := &providerDouble{results: []providerResult{
		{response: jsonResponse(http.StatusOK, completeProviderResponse("reported-one"))},
		{response: jsonResponse(http.StatusOK, completeProviderResponse("reported-two"))},
	}}
	pipeline := newTestPipeline(
		t,
		resolver,
		provider,
		&decisionDouble{decision: ToolDecision{Outcome: ToolDecisionApproved}},
	)
	t.Cleanup(func() { shutdownPipeline(t, pipeline) })

	first, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(
			t,
			"exchange-frozen-one",
			accessID,
			completeClientRequest(),
			ReplayGenerationCostOnly,
		),
		&downstreamRecorder{},
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(
			t,
			"exchange-frozen-two",
			accessID,
			completeClientRequest(),
			ReplayGenerationCostOnly,
		),
		&downstreamRecorder{},
	)
	if err != nil {
		t.Fatal(err)
	}
	requests := provider.requestsSnapshot()
	var models []string
	for _, request := range requests {
		var wire struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(request.Body(), &wire); err != nil {
			t.Fatal(err)
		}
		models = append(models, wire.Model)
	}
	if !slices.Equal(models, []string{"gpt-revision-one", "gpt-revision-two"}) ||
		first.AccessRevision != 1 ||
		second.AccessRevision != 2 ||
		resolver.callCount() != 2 {
		t.Fatalf(
			"models=%v first=%+v second=%+v resolverCalls=%d",
			models,
			first,
			second,
			resolver.callCount(),
		)
	}
}

func TestPipelineRejectsConnectionBindingAfterAgentEndpointChange(
	t *testing.T,
) {
	t.Parallel()

	accessID := mustAccessID(t, "access-stale-ingress")
	connectionPlan := compileTestSnapshot(
		t,
		accessID,
		1,
		"gpt-connection-plan",
	)
	currentPlan := compileTestSnapshotWithEndpoint(
		t,
		accessID,
		2,
		"gpt-current-plan",
		accessID.String()+"-replacement-endpoint",
	)
	resolver := &resolverDouble{
		snapshots: []access.AccessPlanSnapshot{currentPlan},
	}
	provider := &providerDouble{}
	pipeline := newTestPipeline(
		t,
		resolver,
		provider,
		&decisionDouble{decision: ToolDecision{Outcome: ToolDecisionApproved}},
	)
	t.Cleanup(func() { shutdownPipeline(t, pipeline) })
	request, err := NewClientRequest(
		"exchange-stale-ingress",
		connectionPlan.IngressBinding(),
		completeClientRequest(),
		ReplayGenerationCostOnly,
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = pipeline.Execute(
		context.Background(),
		request,
		&downstreamRecorder{},
	)
	if ReasonOf(err) != ReasonIngressBindingStale {
		t.Fatalf("Execute() error = %v, want stale ingress failure", err)
	}
	if resolver.callCount() != 1 || provider.callCount() != 0 {
		t.Fatalf(
			"stale ingress resolver calls=%d provider calls=%d",
			resolver.callCount(),
			provider.callCount(),
		)
	}
}

func TestPipelineObservesSanitizedProviderRejection(t *testing.T) {
	t.Parallel()

	accessID := mustAccessID(t, "access-provider-rejection")
	snapshot := compileTestSnapshot(t, accessID, 1, "gpt-rejection")
	observer := &attemptObserverDouble{}
	protocolPath, err := anthropicchat.NewProtocolPath(
		anthropicchat.DefaultOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := New(Options{
		OwnerContext: context.Background(),
		Actions:      newTestActionGate(t),
		Resolver: &resolverDouble{
			snapshots: []access.AccessPlanSnapshot{snapshot},
		},
		ProtocolPath: protocolPath,
		Provider: &providerDouble{results: []providerResult{{
			response: streamResponse(
				http.StatusUnprocessableEntity,
				io.NopCloser(strings.NewReader(
					`{"detail":[{"loc":["body","tools"],"msg":"private"}]}`,
				)),
			),
		}}},
		ToolDecisions:      &decisionDouble{},
		RetryWaiter:        &retryWaiterDouble{},
		Observer:           observer,
		ObservationTimeout: time.Second,
		Hold:               DefaultHoldPolicy(),
		Stream:             DefaultStreamBudgets(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownPipeline(t, pipeline) })
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(
			t,
			"exchange-provider-rejection",
			accessID,
			streamingClientRequest(),
			ReplayGenerationCostOnly,
		),
		&downstreamRecorder{},
	)
	if ReasonOf(err) != ReasonProviderStatusRejected ||
		result.Outcome != AttemptFailed {
		t.Fatalf("Execute() result=%+v error=%v", result, err)
	}
	observations := observer.snapshot()
	if len(observations) != 1 ||
		observations[0].ReasonCode != ReasonProviderStatusRejected ||
		observations[0].ProviderStatus != http.StatusUnprocessableEntity ||
		observations[0].ProviderField != ProviderFieldTools {
		t.Fatalf("Attempt observations = %+v", observations)
	}
}

func TestPipelineClassifiesProviderBodyIdleAfterResponseHeaders(t *testing.T) {
	t.Parallel()

	accessID := mustAccessID(t, "access-provider-idle")
	snapshot := compileTestSnapshot(t, accessID, 1, "gpt-idle")
	provider := &providerDouble{results: []providerResult{{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: idleProviderBody{},
		},
	}}}
	pipeline := newTestPipeline(
		t,
		&resolverDouble{snapshots: []access.AccessPlanSnapshot{snapshot}},
		provider,
		&decisionDouble{decision: ToolDecision{
			Outcome: ToolDecisionApproved,
		}},
	)
	t.Cleanup(func() { shutdownPipeline(t, pipeline) })
	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(
			t,
			"exchange-provider-idle",
			accessID,
			streamingClientRequest(),
			ReplayNonReplayable,
		),
		downstream,
	)
	var failure *Failure
	if !errors.As(err, &failure) ||
		failure.Code != ReasonProviderResponseIdle ||
		failure.ProviderStatus != http.StatusOK {
		t.Fatalf("Execute() result=%+v error=%v", result, err)
	}
	if len(downstream.aborts) != 1 ||
		downstream.aborts[0].ReasonCode != ReasonProviderResponseIdle ||
		downstream.aborts[0].ProviderStatus != http.StatusOK {
		t.Fatalf("downstream aborts = %+v", downstream.aborts)
	}
}

func TestPipelinePublishesStableProtocolReasonForMalformedStream(t *testing.T) {
	t.Parallel()

	accessID := mustAccessID(t, "access-malformed-stream")
	snapshot := compileTestSnapshot(t, accessID, 1, "gpt-malformed")
	provider := &providerDouble{results: []providerResult{{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(
				"data: {invalid-json}\n\ndata: [DONE]\n\n",
			)),
		},
	}}}
	pipeline := newTestPipeline(
		t,
		&resolverDouble{snapshots: []access.AccessPlanSnapshot{snapshot}},
		provider,
		&decisionDouble{decision: ToolDecision{
			Outcome: ToolDecisionApproved,
		}},
	)
	t.Cleanup(func() { shutdownPipeline(t, pipeline) })
	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(
			t,
			"exchange-malformed-stream",
			accessID,
			streamingClientRequest(),
			ReplayNonReplayable,
		),
		downstream,
	)
	var failure *Failure
	if !errors.As(err, &failure) ||
		failure.Code != ReasonProviderResponseInvalid ||
		failure.ProtocolReason != protocolcore.ReasonMalformedEventStream ||
		result.Outcome != AttemptFailed {
		t.Fatalf("Execute() result=%+v error=%v", result, err)
	}
	if len(downstream.aborts) != 1 ||
		downstream.aborts[0].ReasonCode != ReasonProviderResponseInvalid ||
		downstream.aborts[0].ProtocolReason !=
			protocolcore.ReasonMalformedEventStream {
		t.Fatalf("downstream aborts = %+v", downstream.aborts)
	}
}

func TestPipelineClassifiesUnexpectedStreamContentTypeWithoutBodyText(
	t *testing.T,
) {
	t.Parallel()

	accessID := mustAccessID(t, "access-stream-content-type")
	snapshot := compileTestSnapshot(t, accessID, 1, "gpt-content-type")
	provider := &providerDouble{results: []providerResult{{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body: io.NopCloser(strings.NewReader(
				`{"error":{"param":"tools","message":"private provider text"}}`,
			)),
		},
	}}}
	pipeline := newTestPipeline(
		t,
		&resolverDouble{snapshots: []access.AccessPlanSnapshot{snapshot}},
		provider,
		&decisionDouble{decision: ToolDecision{
			Outcome: ToolDecisionApproved,
		}},
	)
	t.Cleanup(func() { shutdownPipeline(t, pipeline) })
	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(
			t,
			"exchange-stream-content-type",
			accessID,
			streamingClientRequest(),
			ReplayNonReplayable,
		),
		downstream,
	)
	var failure *Failure
	if !errors.As(err, &failure) ||
		failure.Code != ReasonProviderResponseInvalid ||
		failure.ProviderField != ProviderFieldTools ||
		failure.ProtocolReason != protocolcore.ReasonInvalidProviderResponse ||
		failure.ResponseIssue != ProviderResponseIssueContentType ||
		strings.Contains(failure.Error(), "private provider text") ||
		result.Outcome != AttemptFailed {
		t.Fatalf("Execute() result=%+v error=%v", result, err)
	}
	if len(downstream.aborts) != 1 ||
		downstream.aborts[0].ProviderField != ProviderFieldTools ||
		downstream.aborts[0].ProtocolReason !=
			protocolcore.ReasonInvalidProviderResponse ||
		downstream.aborts[0].ResponseIssue !=
			ProviderResponseIssueContentType {
		t.Fatalf("downstream aborts = %+v", downstream.aborts)
	}
}

func TestPipelineProviderCommentsDoNotResetSemanticProgressTimeout(t *testing.T) {
	t.Parallel()

	accessID := mustAccessID(t, "access-semantic-idle")
	snapshot := compileTestSnapshot(t, accessID, 1, "gpt-semantic-idle")
	body := newCommentingProviderBody(2 * time.Millisecond)
	provider := &providerDouble{results: []providerResult{{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: body,
		},
	}}}
	pipeline := newTestPipeline(
		t,
		&resolverDouble{snapshots: []access.AccessPlanSnapshot{snapshot}},
		provider,
		&decisionDouble{decision: ToolDecision{
			Outcome: ToolDecisionApproved,
		}},
	)
	pipeline.hold.MaxTransportResends = 0
	pipeline.streamBudgets = StreamBudgets{
		KeepaliveInterval:       5 * time.Millisecond,
		ProviderProgressTimeout: 30 * time.Millisecond,
	}
	t.Cleanup(func() { shutdownPipeline(t, pipeline) })
	downstream := &downstreamRecorder{}
	started := time.Now()
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(
			t,
			"exchange-semantic-idle",
			accessID,
			streamingClientRequest(),
			ReplayNonReplayable,
		),
		downstream,
	)
	var failure *Failure
	if !errors.As(err, &failure) ||
		failure.Code != ReasonProviderResponseIdle ||
		failure.ProviderStatus != http.StatusOK {
		t.Fatalf("Execute() result=%+v error=%v", result, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("semantic idle timeout converged after %s", elapsed)
	}
	if downstream.keepaliveCount() == 0 {
		t.Fatal("provider silence emitted no downstream keepalive")
	}
	select {
	case <-body.closed:
	default:
		t.Fatal("semantic idle did not close the provider body")
	}
}

func TestPipelineKeepaliveFailureCancelsProviderRead(t *testing.T) {
	t.Parallel()

	accessID := mustAccessID(t, "access-downstream-disconnect")
	snapshot := compileTestSnapshot(t, accessID, 1, "gpt-disconnect")
	body := newBlockingProviderBody()
	provider := &providerDouble{results: []providerResult{{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: body,
		},
	}}}
	pipeline := newTestPipeline(
		t,
		&resolverDouble{snapshots: []access.AccessPlanSnapshot{snapshot}},
		provider,
		&decisionDouble{decision: ToolDecision{
			Outcome: ToolDecisionApproved,
		}},
	)
	pipeline.hold.MaxTransportResends = 0
	pipeline.streamBudgets = StreamBudgets{
		KeepaliveInterval:       5 * time.Millisecond,
		ProviderProgressTimeout: time.Second,
	}
	t.Cleanup(func() { shutdownPipeline(t, pipeline) })
	downstream := &downstreamRecorder{
		keepaliveErr: errors.New("downstream connection closed"),
	}
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(
			t,
			"exchange-downstream-disconnect",
			accessID,
			streamingClientRequest(),
			ReplayNonReplayable,
		),
		downstream,
	)
	if ReasonOf(err) != ReasonDownstreamDisconnected {
		t.Fatalf("Execute() result=%+v error=%v", result, err)
	}
	select {
	case <-body.closed:
	default:
		t.Fatal("downstream disconnect did not close the provider body")
	}
}

func TestStreamBudgetsKeepIndependentPositiveDurations(t *testing.T) {
	t.Parallel()

	if err := DefaultStreamBudgets().Validate(); err != nil {
		t.Fatalf("default stream budgets: %v", err)
	}
	for _, budgets := range []StreamBudgets{
		{},
		{
			KeepaliveInterval:       time.Second,
			ProviderProgressTimeout: time.Second,
		},
		{
			KeepaliveInterval:       2 * time.Second,
			ProviderProgressTimeout: time.Second,
		},
	} {
		if err := budgets.Validate(); err == nil {
			t.Fatalf("invalid stream budgets were accepted: %+v", budgets)
		}
	}
}

func TestClientRequestFieldClassificationIsFiniteAndValueFree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    []byte
		failure error
		want    ClientField
	}{
		{
			name: "known unsupported root field",
			body: []byte(`{
				"model":"claude-client-alias",
				"max_tokens":32,
				"messages":[],
				"output_config":{"effort":"private-value"}
			}`),
			failure: errors.New("root decode failed"),
			want:    ClientFieldOutputConfigEffortInvalid,
		},
		{
			name: "known none effort extension",
			body: []byte(`{
				"output_config":{"effort":"none"}
			}`),
			failure: protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$.output_config.effort",
				errors.New("effort decode failed"),
			),
			want: ClientFieldOutputConfigEffortNone,
		},
		{
			name: "known minimal effort extension",
			body: []byte(`{
				"output_config":{"effort":"minimal"}
			}`),
			failure: protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$.output_config.effort",
				errors.New("effort decode failed"),
			),
			want: ClientFieldOutputConfigEffortMinimal,
		},
		{
			name: "unknown nested field name is not disclosed",
			body: []byte(`{
				"output_config":{
					"effort":"high",
					"private_customer_field":"private-value"
				}
			}`),
			failure: errors.New("root decode failed"),
			want:    ClientFieldOutputConfigUnknown,
		},
		{
			name: "valid output config does not mask unsupported root capability",
			body: []byte(`{
				"output_config":{"effort":"high"},
				"mcp_servers":[{"private":"value"}]
			}`),
			failure: errors.New("root decode failed"),
			want:    ClientFieldMCPServers,
		},
		{
			name: "valid output config is not blamed for another unknown field",
			body: []byte(`{
				"output_config":{"effort":"high"},
				"private_customer_field":"private-value"
			}`),
			failure: errors.New("root decode failed"),
			want:    ClientFieldUnknown,
		},
		{
			name: "known task budget path",
			body: []byte(`{
				"output_config":{
					"task_budget":{"type":"private-value"}
				}
			}`),
			failure: protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$.output_config.task_budget.type",
				errors.New("nested decode failed"),
			),
			want: ClientFieldOutputConfigTaskBudget,
		},
		{
			name: "known format is classified without its schema",
			body: []byte(`{
				"output_config":{
					"format":{"type":"private-value","schema":{"private":"value"}}
				}
			}`),
			failure: errors.New("root decode failed"),
			want:    ClientFieldOutputConfigFormat,
		},
		{
			name: "known nested path",
			body: []byte(`{"thinking":{"type":"enabled","budget_tokens":1024}}`),
			failure: protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$.thinking",
				errors.New("nested decode failed"),
			),
			want: ClientFieldThinking,
		},
		{
			name:    "arbitrary field is not disclosed",
			body:    []byte(`{"private_customer_field":"private-value"}`),
			failure: errors.New("root decode failed"),
			want:    ClientFieldUnknown,
		},
		{
			name:    "invalid JSON is not inspected",
			body:    []byte(`{"output_config":`),
			failure: errors.New("root decode failed"),
			want:    ClientFieldUnknown,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyClientRequestField(test.body, test.failure); got != test.want {
				t.Fatalf("classifyClientRequestField() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPipelineObservesUnsupportedClientCapability(t *testing.T) {
	t.Parallel()

	accessID := mustAccessID(t, "access-client-capability")
	snapshot := compileTestSnapshot(t, accessID, 1, "gpt-client-capability")
	observer := &attemptObserverDouble{}
	provider := &providerDouble{}
	protocolPath, err := anthropicchat.NewProtocolPath(
		anthropicchat.DefaultOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := New(Options{
		OwnerContext: context.Background(),
		Actions:      newTestActionGate(t),
		Resolver: &resolverDouble{
			snapshots: []access.AccessPlanSnapshot{snapshot},
		},
		ProtocolPath:       protocolPath,
		Provider:           provider,
		ToolDecisions:      &decisionDouble{},
		RetryWaiter:        &retryWaiterDouble{},
		Observer:           observer,
		ObservationTimeout: time.Second,
		Hold:               DefaultHoldPolicy(),
		Stream:             DefaultStreamBudgets(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownPipeline(t, pipeline) })
	body := []byte(`{
		"model":"claude-client-alias",
		"max_tokens":32,
		"messages":[{"role":"user","content":"hello"}],
		"output_config":{
			"format":{"type":"regex","schema":{"type":"object"}}
		}
	}`)
	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(
			t,
			"exchange-client-capability",
			accessID,
			body,
			ReplayGenerationCostOnly,
		),
		&downstreamRecorder{},
	)
	if ReasonOf(err) != ReasonUnsupportedClientInput ||
		result.Outcome != AttemptFailed ||
		provider.callCount() != 0 {
		t.Fatalf("Execute() result=%+v error=%v providerCalls=%d",
			result,
			err,
			provider.callCount(),
		)
	}
	observations := observer.snapshot()
	if len(observations) != 1 ||
		observations[0].ReasonCode != ReasonUnsupportedClientInput ||
		observations[0].ClientField != ClientFieldOutputConfigFormat {
		t.Fatalf("Attempt observations = %+v", observations)
	}
}

func TestPipelineClassifiesSecretAuthorityFailuresSeparatelyFromTransport(
	t *testing.T,
) {
	t.Parallel()

	pipeline := &Pipeline{}
	for _, cause := range []error{
		secretstore.ErrNotFound,
		secretstore.ErrLocked,
		secretstore.ErrDenied,
		secretstore.ErrUnavailable,
	} {
		failure := pipeline.classifyProviderError(
			context.Background(),
			"exchange-secret",
			errors.Join(errors.New("authenticate provider"), cause),
		)
		if failure.Code != ReasonProviderCredentialUnavailable {
			t.Fatalf("classifyProviderError(%v) = %s", cause, failure.Code)
		}
	}
	transportFailure := pipeline.classifyProviderError(
		context.Background(),
		"exchange-transport",
		errors.New("dial provider"),
	)
	if transportFailure.Code != ReasonProviderTransportFailed {
		t.Fatalf("transport failure code = %s", transportFailure.Code)
	}
}

func TestPipelineDoesNotRetryAfterClientVisibleSemantics(t *testing.T) {
	t.Parallel()

	accessID := mustAccessID(t, "access-commit-barrier")
	snapshot := compileTestSnapshot(t, accessID, 1, "gpt-commit-barrier")
	body := &scriptedReader{
		first: firstTextProviderEvent(t, "gpt-commit-barrier"),
		err:   temporaryNetworkError{},
	}
	provider := &providerDouble{results: []providerResult{{
		response: streamResponse(http.StatusOK, body),
	}}}
	pipeline := newTestPipeline(
		t,
		&resolverDouble{snapshots: []access.AccessPlanSnapshot{snapshot}},
		provider,
		&decisionDouble{decision: ToolDecision{Outcome: ToolDecisionApproved}},
	)
	t.Cleanup(func() { shutdownPipeline(t, pipeline) })
	downstream := &downstreamRecorder{}

	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(
			t,
			"exchange-commit-barrier",
			accessID,
			streamingClientRequest(),
			ReplayGenerationCostOnly,
		),
		downstream,
	)
	if ReasonOf(err) != ReasonProviderTransportFailed {
		t.Fatalf("Execute() error = %v", err)
	}
	if provider.callCount() != 1 ||
		result.TransportResends != 0 ||
		result.Ledger.DownstreamSemanticBytes == 0 ||
		!result.Ledger.DownstreamFailure ||
		result.Ledger.DownstreamTerminal {
		t.Fatalf("result = %+v; provider calls=%d", result, provider.callCount())
	}
}

func TestPipelineToolDecisionBarrierRejectsWithoutToolOrTerminalLeak(t *testing.T) {
	t.Parallel()

	accessID := mustAccessID(t, "access-tool-reject")
	snapshot := compileTestSnapshot(t, accessID, 1, "gpt-tool")
	decisions := &decisionDouble{decision: ToolDecision{
		Outcome:    ToolDecisionRejected,
		ReasonCode: "approval_required",
	}}
	provider := &providerDouble{results: []providerResult{{
		response: streamResponse(http.StatusOK, toolProviderStream(t, "gpt-tool")),
	}}}
	pipeline := newTestPipeline(
		t,
		&resolverDouble{snapshots: []access.AccessPlanSnapshot{snapshot}},
		provider,
		decisions,
	)
	t.Cleanup(func() { shutdownPipeline(t, pipeline) })
	downstream := &downstreamRecorder{}

	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(
			t,
			"exchange-tool-reject",
			accessID,
			streamingToolClientRequest(),
			ReplayGenerationCostOnly,
		),
		downstream,
	)
	if ReasonOf(err) != ReasonToolDecisionRejected {
		t.Fatalf("Execute() error = %v", err)
	}
	if decisions.callCount() != 1 {
		t.Fatalf("decision calls = %d, want 1", decisions.callCount())
	}
	intents := decisions.lastRequest().ToolIntents()
	if len(intents) != 1 ||
		intents[0].Call.Key.WireID() != "call-hidden" ||
		string(intents[0].Call.Arguments.Bytes()) != `{"cmd":"pwd"}` {
		t.Fatalf("decision intents = %#v", intents)
	}
	wire := downstream.bytesSnapshot()
	for _, forbidden := range [][]byte{
		[]byte("call-hidden"),
		[]byte(`"name":"shell"`),
		[]byte(`"partial_json"`),
		[]byte(`"message_stop"`),
	} {
		if bytes.Contains(wire, forbidden) {
			t.Fatalf("rejected stream leaked %q: %s", forbidden, wire)
		}
	}
	if len(result.Ledger.DownstreamToolKeys()) != 0 ||
		result.Ledger.DownstreamTerminal ||
		!result.Ledger.DownstreamFailure ||
		result.Outcome != AttemptAborted {
		t.Fatalf("result = %+v", result)
	}
}

func TestPipelineToolDecisionApprovalReleasesCompleteToolThenTerminal(t *testing.T) {
	t.Parallel()

	accessID := mustAccessID(t, "access-tool-approve")
	snapshot := compileTestSnapshot(t, accessID, 1, "gpt-tool")
	provider := &providerDouble{results: []providerResult{{
		response: streamResponse(http.StatusOK, toolProviderStream(t, "gpt-tool")),
	}}}
	pipeline := newTestPipeline(
		t,
		&resolverDouble{snapshots: []access.AccessPlanSnapshot{snapshot}},
		provider,
		&decisionDouble{decision: ToolDecision{Outcome: ToolDecisionApproved}},
	)
	t.Cleanup(func() { shutdownPipeline(t, pipeline) })
	downstream := &downstreamRecorder{}

	result, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(
			t,
			"exchange-tool-approve",
			accessID,
			streamingToolClientRequest(),
			ReplayGenerationCostOnly,
		),
		downstream,
	)
	if err != nil {
		t.Fatal(err)
	}
	wire := downstream.bytesSnapshot()
	if !bytes.Contains(wire, []byte("call-hidden")) ||
		!bytes.Contains(wire, []byte(`"partial_json":"{\"cmd\":\"pwd\"}"`)) ||
		!bytes.Contains(wire, []byte(`"message_stop"`)) {
		t.Fatalf("approved stream is incomplete: %s", wire)
	}
	if !slices.Equal(
		result.Ledger.DownstreamToolKeys(),
		[]string{anthropicchat.CallNamespace + ":call-hidden"},
	) || !result.Ledger.DownstreamTerminal {
		t.Fatalf("result = %+v", result)
	}
}

func TestPipelineShutdownCancelsAndDrainsActiveExchange(t *testing.T) {
	t.Parallel()

	accessID := mustAccessID(t, "access-shutdown")
	snapshot := compileTestSnapshot(t, accessID, 1, "gpt-shutdown")
	provider := &blockingProvider{
		started: make(chan struct{}),
	}
	pipeline := newTestPipeline(
		t,
		&resolverDouble{snapshots: []access.AccessPlanSnapshot{snapshot}},
		provider,
		&decisionDouble{decision: ToolDecision{Outcome: ToolDecisionApproved}},
	)
	resultChannel := make(chan error, 1)
	go func() {
		_, executeErr := pipeline.Execute(
			context.Background(),
			mustClientRequest(
				t,
				"exchange-shutdown",
				accessID,
				streamingClientRequest(),
				ReplayGenerationCostOnly,
			),
			&downstreamRecorder{},
		)
		resultChannel <- executeErr
	}()
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pipeline.Shutdown(shutdownContext); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-resultChannel:
		if ReasonOf(err) != ReasonExchangeCanceled {
			t.Fatalf("Execute() after shutdown = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("active Exchange did not drain")
	}
	_, err := pipeline.Execute(
		context.Background(),
		mustClientRequest(
			t,
			"exchange-after-shutdown",
			accessID,
			completeClientRequest(),
			ReplayGenerationCostOnly,
		),
		&downstreamRecorder{},
	)
	if ReasonOf(err) != ReasonExchangeRuntimeStopping {
		t.Fatalf("new Execute() after shutdown = %v", err)
	}
}

func TestPipelinePlannedHoldResumesThroughGatedProviderClient(t *testing.T) {
	t.Parallel()

	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Start(context.Background(), offlinehold.RuntimeBinding{
		InstanceID: "exchange-hold-runtime",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Enter(
		context.Background(),
		gate.Snapshot().Revision,
	); err != nil {
		t.Fatal(err)
	}
	secrets := &integrationSecretReader{}
	authenticator, err := providertransport.NewStaticBearerAuthenticator(secrets)
	if err != nil {
		t.Fatal(err)
	}
	transport := &integrationRoundTripper{
		body: normalProviderStream(t, "gpt-held"),
	}
	provider, err := providertransport.NewClient(
		providertransport.ClientOptions{
			Coordinator:   gate,
			Authenticator: authenticator,
			Transport:     transport,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	accessID := mustAccessID(t, "access-planned-hold")
	snapshot := compileTestSnapshot(t, accessID, 1, "gpt-held")
	pipeline := newTestPipelineWithActions(
		t,
		&resolverDouble{snapshots: []access.AccessPlanSnapshot{snapshot}},
		provider,
		&decisionDouble{decision: ToolDecision{Outcome: ToolDecisionApproved}},
		&retryWaiterDouble{},
		gate,
	)
	t.Cleanup(func() {
		shutdownPipeline(t, pipeline)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			t.Fatalf("provider Shutdown() error = %v", err)
		}
		gate.BeginShutdown()
		if err := gate.Drain(ctx); err != nil {
			t.Fatalf("gate Drain() error = %v", err)
		}
	})
	downstream := &downstreamRecorder{}
	resultChannel := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		result, executeErr := pipeline.Execute(
			context.Background(),
			mustClientRequest(
				t,
				"exchange-planned-hold",
				accessID,
				streamingClientRequest(),
				ReplayGenerationCostOnly,
			),
			downstream,
		)
		resultChannel <- struct {
			result Result
			err    error
		}{result: result, err: executeErr}
	}()

	waitForQueuedEgress(t, gate, 1)
	if secrets.callCount() != 0 ||
		transport.callCount() != 0 ||
		!slices.Equal(
			downstream.modesSnapshot(),
			[]ResponseMode{ResponseModeEventStream},
		) {
		t.Fatalf(
			"held Exchange secret=%d transport=%d modes=%v",
			secrets.callCount(),
			transport.callCount(),
			downstream.modesSnapshot(),
		)
	}
	if _, err := gate.Resume(
		context.Background(),
		gate.Snapshot().Revision,
		offlinehold.ResumeRequest{Targets: gate.PendingProbeTargets()},
		integrationProber{},
	); err != nil {
		t.Fatal(err)
	}
	select {
	case completed := <-resultChannel:
		if completed.err != nil {
			t.Fatalf("Execute() error = %v", completed.err)
		}
		if completed.result.Outcome != AttemptSucceeded ||
			!completed.result.Ledger.DownstreamTerminal {
			t.Fatalf("result = %+v", completed.result)
		}
	case <-time.After(time.Second):
		t.Fatal("held Exchange did not resume")
	}
	if secrets.callCount() != 1 || transport.callCount() != 1 {
		t.Fatalf(
			"resumed Exchange secret=%d transport=%d",
			secrets.callCount(),
			transport.callCount(),
		)
	}
}

func newTestPipeline(
	t *testing.T,
	resolver access.SnapshotResolver,
	provider Provider,
	decisions ToolDecisionGate,
) *Pipeline {
	t.Helper()
	return newTestPipelineWithWaiter(
		t,
		resolver,
		provider,
		decisions,
		&retryWaiterDouble{},
	)
}

func newTestPipelineWithWaiter(
	t *testing.T,
	resolver access.SnapshotResolver,
	provider Provider,
	decisions ToolDecisionGate,
	waiter RetryWaiter,
) *Pipeline {
	t.Helper()
	actions := newTestActionGate(t)
	return newTestPipelineWithActions(
		t,
		resolver,
		provider,
		decisions,
		waiter,
		actions,
	)
}

func newTestPipelineWithActions(
	t *testing.T,
	resolver access.SnapshotResolver,
	provider Provider,
	decisions ToolDecisionGate,
	waiter RetryWaiter,
	actions offlinehold.ActionAdmission,
) *Pipeline {
	t.Helper()
	protocolPath, err := anthropicchat.NewProtocolPath(
		anthropicchat.DefaultOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := New(Options{
		OwnerContext:       context.Background(),
		Actions:            actions,
		Resolver:           resolver,
		ProtocolPath:       protocolPath,
		Provider:           provider,
		ToolDecisions:      decisions,
		RetryWaiter:        waiter,
		Observer:           &attemptObserverDouble{},
		ObservationTimeout: time.Second,
		Hold: HoldPolicy{
			MaxTransportResends: 1,
			RetryDelay:          0,
			MaxDuration:         time.Second,
		},
		Stream: DefaultStreamBudgets(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return pipeline
}

func newTestActionGate(t *testing.T) *offlinehold.Gate {
	t.Helper()
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatalf("new offline-hold Gate: %v", err)
	}
	if err := gate.Start(
		context.Background(),
		offlinehold.RuntimeBinding{InstanceID: "exchange-test-runtime"},
	); err != nil {
		t.Fatalf("start offline-hold Gate: %v", err)
	}
	t.Cleanup(func() {
		gate.BeginShutdown()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := gate.Drain(ctx); err != nil {
			t.Fatalf("drain offline-hold Gate: %v", err)
		}
	})
	return gate
}

func shutdownPipeline(t *testing.T, pipeline *Pipeline) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pipeline.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

type attemptObserverDouble struct {
	mu           sync.Mutex
	observations []AttemptObservation
}

func (observer *attemptObserverDouble) Observe(
	_ context.Context,
	observation AttemptObservation,
) error {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.observations = append(
		observer.observations,
		observation,
	)
	return nil
}

func (observer *attemptObserverDouble) snapshot() []AttemptObservation {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return slices.Clone(observer.observations)
}

type resolverDouble struct {
	mu        sync.Mutex
	snapshots []access.AccessPlanSnapshot
	calls     int
}

func (resolver *resolverDouble) ResolveAccess(
	_ access.AccessID,
) (access.AccessPlanSnapshot, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	index := resolver.calls
	resolver.calls++
	if len(resolver.snapshots) == 0 {
		return access.AccessPlanSnapshot{}, access.ErrAccessNotConfigured
	}
	if index >= len(resolver.snapshots) {
		index = len(resolver.snapshots) - 1
	}
	return resolver.snapshots[index], nil
}

func (resolver *resolverDouble) callCount() int {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return resolver.calls
}

type providerResult struct {
	response *http.Response
	evidence providertransport.CredentialEvidence
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
		return nil, providertransport.Evidence{},
			errors.New("provider result queue is empty")
	}
	result := provider.results[0]
	provider.results = provider.results[1:]
	return result.response, providertransport.Evidence{
		Credential: result.evidence,
	}, result.err
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
	mu           sync.Mutex
	modes        []ResponseMode
	body         bytes.Buffer
	aborts       []FailureNotice
	keepalives   int
	keepaliveErr error
	writeN       int
	writeErr     error
}

type idleProviderBody struct{}

func (idleProviderBody) Read([]byte) (int, error) {
	return 0, providertransport.ErrProviderResponseIdle
}

func (idleProviderBody) Close() error {
	return nil
}

type commentingProviderBody struct {
	interval time.Duration
	closed   chan struct{}
	once     sync.Once
}

func newCommentingProviderBody(interval time.Duration) *commentingProviderBody {
	return &commentingProviderBody{
		interval: interval,
		closed:   make(chan struct{}),
	}
}

func (body *commentingProviderBody) Read(destination []byte) (int, error) {
	timer := time.NewTimer(body.interval)
	defer timer.Stop()
	select {
	case <-body.closed:
		return 0, io.EOF
	case <-timer.C:
		return copy(destination, []byte(": provider keepalive\n\n")), nil
	}
}

func (body *commentingProviderBody) Close() error {
	body.once.Do(func() {
		close(body.closed)
	})
	return nil
}

type blockingProviderBody struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingProviderBody() *blockingProviderBody {
	return &blockingProviderBody{closed: make(chan struct{})}
}

func (body *blockingProviderBody) Read([]byte) (int, error) {
	<-body.closed
	return 0, io.EOF
}

func (body *blockingProviderBody) Close() error {
	body.once.Do(func() {
		close(body.closed)
	})
	return nil
}

func (downstream *downstreamRecorder) Begin(
	_ context.Context,
	mode ResponseMode,
) error {
	downstream.mu.Lock()
	defer downstream.mu.Unlock()
	downstream.modes = append(downstream.modes, mode)
	return nil
}

func (downstream *downstreamRecorder) Write(
	_ context.Context,
	body []byte,
) (int, error) {
	downstream.mu.Lock()
	defer downstream.mu.Unlock()
	count := len(body)
	if downstream.writeN > 0 && downstream.writeN < count {
		count = downstream.writeN
	}
	_, _ = downstream.body.Write(body[:count])
	return count, downstream.writeErr
}

func (downstream *downstreamRecorder) Keepalive(
	_ context.Context,
) error {
	downstream.mu.Lock()
	defer downstream.mu.Unlock()
	downstream.keepalives++
	return downstream.keepaliveErr
}

func (downstream *downstreamRecorder) Abort(
	_ context.Context,
	notice FailureNotice,
) error {
	downstream.mu.Lock()
	defer downstream.mu.Unlock()
	downstream.aborts = append(downstream.aborts, notice)
	return nil
}

func (downstream *downstreamRecorder) keepaliveCount() int {
	downstream.mu.Lock()
	defer downstream.mu.Unlock()
	return downstream.keepalives
}

func (downstream *downstreamRecorder) modesSnapshot() []ResponseMode {
	downstream.mu.Lock()
	defer downstream.mu.Unlock()
	return slices.Clone(downstream.modes)
}

func (downstream *downstreamRecorder) bytesSnapshot() []byte {
	downstream.mu.Lock()
	defer downstream.mu.Unlock()
	return bytes.Clone(downstream.body.Bytes())
}

type decisionDouble struct {
	mu       sync.Mutex
	decision ToolDecision
	err      error
	requests []ToolDecisionRequest
}

func (decisions *decisionDouble) Decide(
	_ context.Context,
	request ToolDecisionRequest,
) (ToolDecision, error) {
	decisions.mu.Lock()
	defer decisions.mu.Unlock()
	request.intents = cloneToolIntents(request.intents)
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
	if len(decisions.requests) == 0 {
		return ToolDecisionRequest{}
	}
	return decisions.requests[len(decisions.requests)-1]
}

type retryWaiterDouble struct {
	mu           sync.Mutex
	observations []RetryObservation
	err          error
}

func (waiter *retryWaiterDouble) WaitForRetry(
	_ context.Context,
	observation RetryObservation,
) error {
	waiter.mu.Lock()
	defer waiter.mu.Unlock()
	waiter.observations = append(waiter.observations, observation)
	return waiter.err
}

func (waiter *retryWaiterDouble) callCount() int {
	waiter.mu.Lock()
	defer waiter.mu.Unlock()
	return len(waiter.observations)
}

type temporaryNetworkError struct{}

func (temporaryNetworkError) Error() string   { return "temporary network failure" }
func (temporaryNetworkError) Timeout() bool   { return false }
func (temporaryNetworkError) Temporary() bool { return true }

type scriptedReader struct {
	first []byte
	err   error
	read  bool
}

type integrationSecretReader struct {
	mu    sync.Mutex
	calls int
}

func (reader *integrationSecretReader) Read(
	_ context.Context,
	_ secretstore.Reference,
) (*secretstore.Value, error) {
	reader.mu.Lock()
	reader.calls++
	reader.mu.Unlock()
	return secretstore.NewValue([]byte("provider-token"))
}

func (reader *integrationSecretReader) callCount() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.calls
}

type integrationRoundTripper struct {
	mu    sync.Mutex
	calls int
	body  io.Reader
}

func (transport *integrationRoundTripper) RoundTrip(
	_ *http.Request,
	_ providertransport.TransportDispatch,
) (*http.Response, transportprofile.Evidence, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.calls++
	return streamResponse(http.StatusOK, transport.body),
		transportprofile.Evidence{},
		nil
}

func (transport *integrationRoundTripper) callCount() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.calls
}

type integrationProber struct{}

func (integrationProber) Probe(
	context.Context,
	offlinehold.ProbeRequest,
) error {
	return nil
}

func waitForQueuedEgress(
	t *testing.T,
	gate *offlinehold.Gate,
	want int,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if gate.Snapshot().QueuedRequests == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queued egress = %d, want %d", gate.Snapshot().QueuedRequests, want)
}

func (reader *scriptedReader) Read(destination []byte) (int, error) {
	if !reader.read {
		reader.read = true
		return copy(destination, reader.first), nil
	}
	return 0, reader.err
}

func jsonResponse(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(bytes.NewReader(body)),
	}
}

func streamResponse(status int, body io.Reader) *http.Response {
	if body == nil {
		body = bytes.NewReader(nil)
	}
	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
		},
		Body: io.NopCloser(body),
	}
}

func completeProviderResponse(model string) []byte {
	return []byte(`{
		"id":"chatcmpl-complete",
		"object":"chat.completion",
		"created":1,
		"model":"` + model + `",
		"choices":[{
			"index":0,
			"message":{"role":"assistant","content":"Done.","refusal":null},
			"finish_reason":"stop",
			"logprobs":null
		}],
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

func firstTextProviderEvent(t *testing.T, model string) []byte {
	t.Helper()
	return joinProviderEvents(
		t,
		`{"id":"chatcmpl-partial","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[{"index":0,"delta":{"role":"assistant","content":"Visible"},"finish_reason":null}]}`,
	)
}

func toolProviderStream(t *testing.T, model string) io.Reader {
	t.Helper()
	return bytes.NewReader(joinProviderEvents(
		t,
		`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[{"index":0,"delta":{"role":"assistant","content":null},"finish_reason":null}]}`,
		`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-hidden","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\""}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"pwd\"}"}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"`+model+`","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":4,"total_tokens":12}}`,
		`[DONE]`,
	))
}

func joinProviderEvents(t *testing.T, payloads ...string) []byte {
	t.Helper()
	var wire bytes.Buffer
	for _, payload := range payloads {
		event, err := ssewire.Encode(ssewire.Event{
			Name: "message",
			Data: []byte(payload),
		})
		if err != nil {
			t.Fatal(err)
		}
		wire.Write(event)
	}
	return wire.Bytes()
}

func completeClientRequest() []byte {
	return []byte(`{
		"model":"claude-client-alias",
		"max_tokens":32,
		"messages":[{"role":"user","content":"hello"}]
	}`)
}

func streamingClientRequest() []byte {
	return []byte(`{
		"model":"claude-client-alias",
		"max_tokens":32,
		"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`)
}

func streamingToolClientRequest() []byte {
	return []byte(`{
		"model":"claude-client-alias",
		"max_tokens":32,
		"stream":true,
		"messages":[{"role":"user","content":"hello"}],
		"tools":[{
			"name":"shell",
			"description":"Run a command.",
			"input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}}
		}]
	}`)
}

func mustClientRequest(
	t *testing.T,
	exchangeID string,
	accessID access.AccessID,
	body []byte,
	replayClass ReplayClass,
) ClientRequest {
	t.Helper()
	request, err := NewClientRequest(
		exchangeID,
		testIngressBinding(t, accessID),
		body,
		replayClass,
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func testIngressBinding(
	t *testing.T,
	accessID access.AccessID,
) access.IngressBinding {
	t.Helper()
	return compileTestSnapshot(
		t,
		accessID,
		1,
		"gpt-ingress-binding",
	).IngressBinding()
}

func compileTestSnapshot(
	t *testing.T,
	accessID access.AccessID,
	revision access.Revision,
	modelValue string,
) access.AccessPlanSnapshot {
	t.Helper()
	return compileTestSnapshotWithEndpoint(
		t,
		accessID,
		revision,
		modelValue,
		accessID.String()+"-endpoint",
	)
}

func compileTestSnapshotWithEndpoint(
	t *testing.T,
	accessID access.AccessID,
	revision access.Revision,
	modelValue string,
	endpointValue string,
) access.AccessPlanSnapshot {
	t.Helper()
	codecID, err := access.NewCodecPairID(anthropicchat.CodecPairID)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := operationcatalog.M0()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := access.NewCatalog(access.CatalogOptions{
		Capabilities: access.PlanCapabilities{
			MaxEndpointProfiles: 1,
			MaxAccountBindings:  1,
			MaxRouteSets:        1,
		},
		ClientOperations: operations.Definitions(),
		CodecPairs: []access.CodecPairDefinition{{
			ID:              codecID,
			Revision:        anthropicchat.CodecRevision,
			ClientDialect:   access.DialectAnthropicMessages,
			ProviderDialect: access.DialectOpenAIChat,
			ClientOperationIDs: operations.SemanticOperationIDs(
				access.DialectAnthropicMessages,
			),
			RequiredCapabilities: []access.ProviderCapability{
				access.ProviderCapabilityMessages,
				access.ProviderCapabilityStreaming,
				access.ProviderCapabilityToolCalls,
			},
		}},
		AuthDrivers: []access.AuthDriverDefinition{{
			Ref:      access.StaticHeaderAuthDriverRef(),
			Revision: 1,
		}},
		EgressModes: []access.EgressModeDefinition{{
			Mode:     access.EgressModeDirect,
			Revision: 1,
		}},
		PluginPlanModes: []access.PluginPlanModeDefinition{{
			Mode:     access.PluginPlanModePassThrough,
			Revision: 1,
		}},
		ModelPolicyModes: []access.ModelPolicyModeDefinition{{
			Mode:     access.ModelPolicyModeFixed,
			Revision: 1,
		}},
		TransportProfiles: []access.TransportFingerprintDefinition{
			access.ObservedClientH1TransportFingerprintDefinition(),
			access.StandardH1TransportFingerprintDefinition(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := access.NewCompiler(catalog)
	if err != nil {
		t.Fatal(err)
	}

	suffix := accessID.String()
	endpointID := mustAgentEndpointID(t, endpointValue)
	profileID := mustEndpointProfileID(t, suffix+"-profile")
	targetID := mustProviderTargetID(t, suffix+"-target")
	accountID := mustAccountBindingID(t, suffix+"-account")
	routeID := mustRouteSetID(t, suffix+"-routes")
	egressID := mustEgressPolicyID(t, suffix+"-egress")
	clientOrigin, err := access.NewClientOrigin("https://api.anthropic.com:443")
	if err != nil {
		t.Fatal(err)
	}
	providerOrigin, err := access.NewProviderOrigin(
		"https://api.openai.com:443/v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	model, err := access.NewModelName(modelValue)
	if err != nil {
		t.Fatal(err)
	}
	secretRef, err := access.NewSecretRef("secret://provider/" + suffix)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := compiler.Compile(access.Aggregate{
		Binding: access.AccessBinding{
			ID:                accessID,
			Revision:          revision,
			Name:              "Pipeline Access",
			Description:       "Executable Exchange pipeline Access",
			Status:            access.AccessStatusEnabled,
			AgentEndpointID:   endpointID,
			DefaultRouteSetID: routeID,
			ProfileIDs:        []access.EndpointProfileID{profileID},
			EgressPolicyID:    egressID,
		},
		AgentEndpoint: access.AgentEndpoint{
			ID:            endpointID,
			Revision:      revision,
			AccessID:      accessID,
			ClientOrigin:  clientOrigin,
			ClientDialect: access.DialectAnthropicMessages,
		},
		Profiles: []access.EndpointProfile{{
			ID:                      profileID,
			Revision:                revision,
			AccessID:                accessID,
			Name:                    "OpenAI Chat",
			Description:             "Fixed provider profile",
			BackendDialect:          access.DialectOpenAIChat,
			TargetID:                targetID,
			TransportProfileRef:     access.ObservedClientH1TransportProfileRef(),
			DefaultAccountBindingID: accountID,
			AccountBindingIDs:       []access.AccountBindingID{accountID},
			DefaultModelPolicy: access.ModelPolicy{
				Revision:   revision,
				Mode:       access.ModelPolicyModeFixed,
				FixedModel: model,
			},
		}},
		ProviderTargets: []access.ProviderTarget{{
			ID:        targetID,
			Revision:  revision,
			AccessID:  accessID,
			ProfileID: profileID,
			Origin:    providerOrigin,
			Protocol:  access.DialectOpenAIChat,
			Capabilities: []access.ProviderCapability{
				access.ProviderCapabilityMessages,
				access.ProviderCapabilityStreaming,
				access.ProviderCapabilityToolCalls,
			},
		}},
		AccountBindings: []access.ProviderAccountBinding{{
			ID:            accountID,
			Revision:      revision,
			AccessID:      accessID,
			ProfileID:     profileID,
			Label:         "Primary",
			SecretRef:     secretRef,
			AuthDriverRef: access.StaticHeaderAuthDriverRef(),
			Enabled:       true,
		}},
		RouteSets: []access.RouteSet{{
			ID:                  routeID,
			Revision:            revision,
			AccessID:            accessID,
			CandidateProfileIDs: []access.EndpointProfileID{profileID},
		}},
		EgressPolicy: access.AccessEgressPolicy{
			ID:       egressID,
			Revision: revision,
			AccessID: accessID,
			Mode:     access.EgressModeDirect,
		},
		PluginPlan: access.PluginPlan{
			Revision: revision,
			AccessID: accessID,
			Mode:     access.PluginPlanModePassThrough,
		},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	return snapshot
}

func mustAccessID(t *testing.T, value string) access.AccessID {
	t.Helper()
	id, err := access.NewAccessID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustAgentEndpointID(t *testing.T, value string) access.AgentEndpointID {
	t.Helper()
	id, err := access.NewAgentEndpointID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustEndpointProfileID(
	t *testing.T,
	value string,
) access.EndpointProfileID {
	t.Helper()
	id, err := access.NewEndpointProfileID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustProviderTargetID(
	t *testing.T,
	value string,
) access.ProviderTargetID {
	t.Helper()
	id, err := access.NewProviderTargetID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustAccountBindingID(
	t *testing.T,
	value string,
) access.AccountBindingID {
	t.Helper()
	id, err := access.NewAccountBindingID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustRouteSetID(t *testing.T, value string) access.RouteSetID {
	t.Helper()
	id, err := access.NewRouteSetID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustEgressPolicyID(
	t *testing.T,
	value string,
) access.EgressPolicyID {
	t.Helper()
	id, err := access.NewEgressPolicyID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestProviderRejectionClassifierReturnsOnlyKnownEmittedFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want ProviderField
	}{
		{
			name: "OpenAI parameter",
			body: `{"error":{"message":"private","param":"max_completion_tokens"}}`,
			want: ProviderFieldMaxCompletionTokens,
		},
		{
			name: "validation location",
			body: `{"detail":[{"loc":["body","reasoning_effort"],"msg":"private"}]}`,
			want: ProviderFieldReasoningEffort,
		},
		{
			name: "structured output parameter",
			body: `{"error":{"message":"private","param":"response_format"}}`,
			want: ProviderFieldResponseFormat,
		},
		{
			name: "unknown provider value",
			body: `{"error":{"param":"private_provider_field"}}`,
			want: ProviderFieldUnknown,
		},
		{
			name: "provider text is not scanned",
			body: `{"error":{"message":"max_tokens is private text"}}`,
			want: ProviderFieldUnknown,
		},
		{
			name: "malformed payload",
			body: `{"error":`,
			want: ProviderFieldUnknown,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := classifyProviderRejection(strings.NewReader(test.body))
			if got != test.want {
				t.Fatalf(
					"classifyProviderRejection() = %q, want %q",
					got,
					test.want,
				)
			}
		})
	}
}

func TestProviderRequestAcceptHeaderMatchesResponseMode(t *testing.T) {
	t.Parallel()

	path, err := anthropicchat.NewProtocolPath(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		body []byte
		want string
	}{
		{body: streamingClientRequest(), want: "text/event-stream"},
		{
			body: []byte(
				`{"model":"client","max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`,
			),
			want: "application/json",
		},
	}
	for _, test := range tests {
		request, _, err := path.Client().DecodeRequest(test.body)
		if err != nil {
			t.Fatal(err)
		}
		request, err = request.WithEffectiveModel("provider")
		if err != nil {
			t.Fatal(err)
		}
		encoded, _, err := path.Backend().EncodeRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		if got := encoded.Headers().Get("Accept"); got != test.want {
			t.Fatalf(
				"provider request Accept = %q, want %q",
				got,
				test.want,
			)
		}
	}
}
