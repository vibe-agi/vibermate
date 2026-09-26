package exchange

import (
	"context"
	"crypto/sha256"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/accountselector"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestDryRunMatchesExecutedFrozenDecisionWithoutSideEffects(t *testing.T) {
	const upstreamModel = "provider-model"
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap, mappedModel: upstreamModel,
		accounts:  []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
		preferred: "account.primary",
		transform: messagetransform.Policy{RequestJavaScript: `
			const payload = JSON.parse(request.body);
			payload.transform_marker = "dry-run";
			request.body = JSON.stringify(payload);
			request.headers["x-transform-request"] = "yes";
		`},
	})
	authority := newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7})
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(http.StatusOK, completeProviderResponse(upstreamModel)),
	}}}
	observer := &attemptObserverDouble{}
	pipeline := newTestPipeline(t, authority, provider, approvedDecisions(), observer)
	defer shutdownPipeline(t, pipeline)
	pipeline.now = func() time.Time { return time.Date(2026, 9, 25, 1, 2, 3, 0, time.UTC) }
	request := mustClientRequest(t, "exchange-dry-run", plan, completeClientRequest())

	preview, err := pipeline.DryRun(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if preview.EnvironmentID != "environment.test" || preview.EnvironmentRevision != 9 ||
		preview.RouteID != "route.primary" || preview.AccountID != "account.primary" ||
		preview.AccountRevision != 3 || preview.RequestedModel != "claude-client-alias" ||
		preview.EffectiveModel != upstreamModel || !preview.ModelMapped ||
		preview.NetworkExitID != "profile.direct" || preview.ProviderMethod != http.MethodPost ||
		!preview.BodyChanged || !slices.Contains(preview.ChangedTopLevelFields, "transform_marker") ||
		!slices.Contains(preview.ProtocolChangedTopLevelFields, "model") ||
		!slices.Contains(preview.ChangedHeaderNames, "X-Transform-Request") ||
		!slices.Contains(preview.Unverified, "credential") ||
		!slices.Contains(preview.Unverified, "runtime_identity") || preview.EvaluatedAt.IsZero() {
		t.Fatalf("dry-run decision = %+v", preview)
	}
	if len(authority.snapshot()) != 0 || provider.callCount() != 0 || len(observer.snapshot()) != 0 {
		t.Fatal("dry run acquired a credential, contacted the provider, or recorded an attempt")
	}
	result, err := pipeline.Execute(context.Background(), request, &downstreamRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	actual := provider.requestsSnapshot()
	if len(actual) != 1 || preview.AccountID != result.AccountID ||
		preview.RouteID != result.RouteID ||
		preview.bodyDigest != sha256.Sum256(actual[0].Body()) ||
		actual[0].Headers().Get("X-Transform-Request") != "yes" {
		t.Fatalf("dry-run and executed decision diverged: preview=%+v result=%+v", preview, result)
	}
}

func TestDryRunUsesPublishedAccountSelectorAndDoesNotLease(t *testing.T) {
	accounts := []testAccount{
		{id: "account.primary", revision: 3, epoch: 7},
		{id: "account.backup", revision: 4, epoch: 5},
	}
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap, mappedModel: "provider-model",
		accounts: accounts,
		selector: &codelibrary.AccountSelectorRevision{
			ID: "model-account", Revision: 2, CollectionID: "routing", DisplayName: "Model account",
			Policy:      accountselector.Policy{JavaScript: `selection.accountId = request.requestedModel === "claude-client-alias" ? "account.backup" : "account.primary";`},
			PublishedAt: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC),
		},
	})
	authority := newAccountAuthority(t, accounts...)
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(http.StatusOK, completeProviderResponse("provider-model")),
	}}}
	pipeline := newTestPipeline(t, authority, provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	request := mustClientRequest(t, "exchange-selector-dry-run", plan, completeClientRequest())
	preview, err := pipeline.DryRun(context.Background(), request)
	if err != nil || preview.AccountID != "account.backup" || len(authority.snapshot()) != 0 || provider.callCount() != 0 {
		t.Fatalf("selector dry run = %+v, error=%v", preview, err)
	}
	result, err := pipeline.Execute(context.Background(), request, &downstreamRecorder{})
	if err != nil || result.AccountID != preview.AccountID {
		t.Fatalf("selector execution = %+v, error=%v", result, err)
	}
}

func TestDryRunOriginalDestinationMatchesTheExecutedBodyWithoutInventingAccount(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindOriginal,
		providerOrigin: "https://api.anthropic.com",
		backend:        protocolspec.DialectAnthropicMessages,
		modelMode:      environment.ModelModePassthrough,
		transform: messagetransform.Policy{RequestJavaScript: `
			const value = JSON.parse(request.body);
			value.metadata = {previewed: true};
			request.body = JSON.stringify(value);
		`},
	})
	provider := &providerDouble{results: []providerResult{{
		response: jsonResponse(http.StatusOK, []byte(`{
			"id":"msg_original_preview","type":"message","role":"assistant",
			"model":"claude-client-alias","content":[{"type":"text","text":"synthetic"}],
			"stop_reason":"end_turn","stop_sequence":null,
			"usage":{"input_tokens":1,"output_tokens":1}
		}`)),
	}}}
	pipeline := newTestPipeline(t, nil, provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	request := mustClientRequestWithOptions(t, "exchange-original-dry-run", plan,
		completeClientRequest(), WithOriginalHeaders(http.Header{
			"Authorization": {"Bearer synthetic-client-secret"},
		}))
	preview, err := pipeline.DryRun(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if preview.DestinationKind != "original" || preview.RouteID != "" ||
		preview.AccountID != "" || !preview.BodyChanged ||
		!slices.Contains(preview.ChangedTopLevelFields, "metadata") ||
		provider.callCount() != 0 {
		t.Fatalf("Original Destination preview = %+v", preview)
	}
	if _, err := pipeline.Execute(context.Background(), request, &downstreamRecorder{}); err != nil {
		t.Fatal(err)
	}
	actual := provider.requestsSnapshot()
	if len(actual) != 1 || preview.bodyDigest != sha256.Sum256(actual[0].Body()) ||
		actual[0].Headers().Get("Authorization") != "Bearer synthetic-client-secret" {
		t.Fatal("Original Destination preview diverged from the executed request")
	}
}

func TestDryRunReportsUnmatchedModelAndTransformFailureWithoutEgress(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap,
		modelMappings:  []environment.ModelMapping{{RequestedModel: "other-model", UpstreamModel: "provider-other"}},
		accounts:       []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
		preferred:      "account.primary",
	})
	provider := &providerDouble{}
	pipeline := newTestPipeline(t,
		newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7}),
		provider, approvedDecisions(), &attemptObserverDouble{},
	)
	defer shutdownPipeline(t, pipeline)
	preview, err := pipeline.DryRun(context.Background(),
		mustClientRequest(t, "exchange-model-unmatched", plan, completeClientRequest()))
	if err != nil || preview.ModelMapped || preview.EffectiveModel != "claude-client-alias" {
		t.Fatalf("unmatched model = %+v, error=%v", preview, err)
	}
	if provider.callCount() != 0 {
		t.Fatal("unmatched model preview sent provider traffic")
	}

	failingPlan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://provider.example/v1",
		backend:        protocolspec.DialectOpenAIChat,
		modelMode:      environment.ModelModeMap, mappedModel: "provider-model",
		accounts:  []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
		preferred: "account.primary",
		transform: messagetransform.Policy{RequestJavaScript: `throw new Error("private-secret");`},
	})
	_, err = pipeline.DryRun(context.Background(),
		mustClientRequest(t, "exchange-transform-failed-dry-run", failingPlan, completeClientRequest()))
	if ReasonOf(err) != ReasonMessageTransformFailed || provider.callCount() != 0 {
		t.Fatalf("transform dry run failure = %v; provider calls=%d", err, provider.callCount())
	}
}

func TestDryRunChangedFieldNamesNeverEchoValues(t *testing.T) {
	before := []byte(`{"public":"before","access_token":"before","secret token value":"before","nested":{"a":1,"b":2}}`)
	after := []byte(`{"public":"top-secret-value","access_token":"top-secret-value","secret token value":"top-secret-value","nested":{"b":2,"a":1}}`)
	changed := changedTopLevelFields(before, after)
	if !slices.Equal(changed, []string{"[redacted]", "[redacted]", "public"}) {
		t.Fatalf("changed fields = %v", changed)
	}
	if got := changedTopLevelFields(
		[]byte(`{"count":9007199254740992}`),
		[]byte(`{"count":9007199254740993}`),
	); !slices.Equal(got, []string{"count"}) {
		t.Fatalf("large numeric field change = %v", got)
	}
}
