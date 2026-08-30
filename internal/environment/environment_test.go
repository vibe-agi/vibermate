package environment

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/accountselector"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestClientOriginCanonicalizationAndRejection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"https://EXAMPLE.COM:443", "https://example.com"},
		{"HTTPS://B\u00dcCHER.example", "https://xn--bcher-kva.example"},
		{"https://example.com:8443", "https://example.com:8443"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			origin, err := originidentity.ParseClientOrigin(test.input)
			if err != nil || origin.String() != test.want {
				t.Fatalf("ParseClientOrigin(%q) = %q, %v", test.input, origin, err)
			}
		})
	}
	for _, input := range []string{
		"http://example.com", "https://example.com/", "https://example.com/path",
		"https://example.com?x=1", "https://example.com?", "https://example.com#fragment",
		"https://user@example.com", "https://*.example.com", "https://example.com.",
		"https://example.com:", " https://example.com", "https://exa_mple.com",
		"https://127.0.0.1", "https://[2001:0db8::1]:443",
	} {
		t.Run("reject_"+input, func(t *testing.T) {
			if _, err := originidentity.ParseClientOrigin(input); err == nil {
				t.Fatalf("ParseClientOrigin(%q) succeeded", input)
			}
		})
	}
}

func TestLaunchEnvironmentPolicyIsFrozenAndRejectsLauncherAuthority(
	t *testing.T,
) {
	t.Parallel()
	aggregate := fixture(t, "launch-environment", mustOrigin(t, "https://relay.example"))
	aggregate.LaunchEnvironment = LaunchEnvironmentPolicy{
		SetEnv: map[string]string{
			"TEAM_CONTEXT": "team-a",
			"EMPTY_VALUE":  "",
		},
		DeleteEnv: []string{"REMOVE_SECOND", "REMOVE_FIRST"},
	}
	snapshot := mustCompile(t, aggregate)
	aggregate.LaunchEnvironment.SetEnv["TEAM_CONTEXT"] = "mutated"
	aggregate.LaunchEnvironment.DeleteEnv[0] = "MUTATED"
	frozen := snapshot.LaunchEnvironment()
	if frozen.SetEnv["TEAM_CONTEXT"] != "team-a" ||
		len(frozen.DeleteEnv) != 2 ||
		frozen.DeleteEnv[0] != "REMOVE_FIRST" ||
		frozen.DeleteEnv[1] != "REMOVE_SECOND" {
		t.Fatalf("frozen LaunchEnvironmentPolicy = %+v", frozen)
	}
	frozen.SetEnv["TEAM_CONTEXT"] = "changed"
	if snapshot.LaunchEnvironment().SetEnv["TEAM_CONTEXT"] != "team-a" {
		t.Fatal("LaunchEnvironment getter exposed mutable snapshot state")
	}

	for _, invalid := range []LaunchEnvironmentPolicy{
		{SetEnv: map[string]string{"1INVALID": "value"}},
		{SetEnv: map[string]string{"HTTP_PROXY": "http://bypass.invalid"}},
		{DeleteEnv: []string{"VIBERMATE_TOKEN"}},
		{SetEnv: map[string]string{"SAME": "value"}, DeleteEnv: []string{"SAME"}},
		{DeleteEnv: []string{"DUPLICATE", "DUPLICATE"}},
		{SetEnv: map[string]string{"VALUE": "contains\x00nul"}},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid LaunchEnvironmentPolicy was accepted: %+v", invalid)
		}
	}
}

func TestRequestPlanFreezesTheSelectedEgressProfileRevision(t *testing.T) {
	t.Parallel()
	aggregate := fixture(t, "network-path", mustOrigin(t, "https://relay.example"))
	want := egressprofile.ProfileRevision{
		ID: "profile.office", Revision: 3, DisplayName: "Office",
		Policy: egressnetwork.Policy{
			Proxy: egressnetwork.ProxyPolicy{
				Kind: egressnetwork.ProxySOCKS5, Endpoint: "proxy.example:1080",
			},
			Resolver: egressnetwork.ResolverPolicy{
				Kind: egressnetwork.ResolverDoH, DoHURL: "https://dns.example/dns-query",
				Transport: egressnetwork.ResolverTransportProxy,
			},
		},
		PublishedAt: time.Date(2026, time.August, 27, 1, 2, 3, 0, time.UTC),
	}
	aggregate.ClientEndpoints[0].ProtocolPlans[0].EgressProfile = want
	snapshot := mustCompile(t, aggregate)
	aggregate.ClientEndpoints[0].ProtocolPlans[0].EgressProfile = egressprofile.Direct()

	plan, err := snapshot.ResolveRequest(
		mustOrigin(t, "https://relay.example"),
		RequestFacts{
			Target: protocolspec.RequestTarget{
				Method: "POST", Path: "/v1/messages",
				Transport: protocolspec.ClientOperationTransportHTTP,
			},
			DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
		},
	)
	if err != nil {
		t.Fatalf("ResolveRequest(): %v", err)
	}
	if got := plan.EgressProfile(); !got.Equal(want) {
		t.Fatalf("RequestPlan EgressProfile = %#v, want %#v", got, want)
	}
	if got := plan.ProtocolPlan().EgressProfile(); !got.Equal(want) {
		t.Fatalf("CompiledProtocolPlan EgressProfile = %#v, want %#v", got, want)
	}
	if got := plan.EgressPolicy(); got != want.Policy {
		t.Fatalf("RequestPlan EgressPolicy = %#v, want %#v", got, want.Policy)
	}

	invalid := fixture(t, "invalid-network-path", mustOrigin(t, "https://invalid.example"))
	invalid.ClientEndpoints[0].ProtocolPlans[0].EgressProfile = egressprofile.ProfileRevision{
		ID: "profile.invalid", Revision: 1, DisplayName: "Invalid",
		Policy: egressnetwork.Policy{
			Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxyDirect},
			Resolver: egressnetwork.ResolverPolicy{
				Kind: egressnetwork.ResolverDoH, DoHURL: "https://dns.example/dns-query",
				Transport: egressnetwork.ResolverTransportProxy,
			},
		},
		PublishedAt: time.Now(),
	}
	if err := invalid.Validate(); err == nil {
		t.Fatal("Environment accepted an unusable egress profile")
	}
}

func TestRequestPlanFreezesOrderedPublishedTransformRevisions(t *testing.T) {
	t.Parallel()
	aggregate := fixture(t, "message-transform", mustOrigin(t, "https://relay.example"))
	publishedAt := time.Date(2026, time.August, 27, 10, 0, 0, 0, time.UTC)
	aggregate.ClientEndpoints[0].ProtocolPlans[0].Transforms = []codelibrary.TransformRevision{
		{
			ID: "remember-client", Revision: 3, CollectionID: "privacy",
			DisplayName: "Remember client model", PublishedAt: publishedAt,
			Policy: messagetransform.Policy{
				RequestJavaScript:  `context.requested = JSON.parse(request.body).model; request.headers["x-request-order"] = "one";`,
				ResponseJavaScript: `response.headers["x-requested-model"] = context.requested; response.headers["x-response-order"] = (response.headers["x-response-order"] || "") + ",one";`,
			},
		},
		{
			ID: "rewrite-model", Revision: 8, CollectionID: "routing",
			DisplayName: "Rewrite model", PublishedAt: publishedAt,
			Policy: messagetransform.Policy{
				RequestJavaScript:  `request.headers["x-request-order"] += ",two"; request.body = "{\"model\":\"upstream\"}";`,
				ResponseJavaScript: `response.headers["x-response-order"] = "two";`,
			},
		},
	}
	snapshot := mustCompile(t, aggregate)
	aggregate.ClientEndpoints[0].ProtocolPlans[0].Transforms[0].Policy = messagetransform.Policy{}

	plan, err := snapshot.ResolveRequest(
		mustOrigin(t, "https://relay.example"),
		RequestFacts{
			Target: protocolspec.RequestTarget{
				Method: "POST", Path: "/v1/messages",
				Transport: protocolspec.ClientOperationTransportHTTP,
			},
			DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
		},
	)
	if err != nil {
		t.Fatalf("ResolveRequest(): %v", err)
	}
	turn := plan.TransformPipeline().NewTurn()
	request, err := turn.ApplyRequest(context.Background(), messagetransform.RequestMessage{
		Method: "POST", Path: "/v1/messages",
		Headers: make(map[string][]string),
		Body:    []byte(`{"model":"client"}`),
	})
	if err != nil {
		t.Fatalf("ApplyRequest(): %v", err)
	}
	if got := string(request.Body); got != `{"model":"upstream"}` {
		t.Fatalf("transformed request Body = %s", got)
	}
	if got := request.Headers.Get("X-Request-Order"); got != "one,two" {
		t.Fatalf("request Transform order = %q, want one,two", got)
	}
	response, err := turn.ApplyResponse(context.Background(), messagetransform.ResponseMessage{
		StatusCode: 200, Headers: make(map[string][]string), Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("ApplyResponse(): %v", err)
	}
	if got := response.Headers.Get("X-Requested-Model"); got != "client" {
		t.Fatalf("shared Turn Context = %q, want client", got)
	}
	if got := response.Headers.Get("X-Response-Order"); got != "two,one" {
		t.Fatalf("response Transform order = %q, want two,one", got)
	}

	invalid := fixture(t, "invalid-message-transform", mustOrigin(t, "https://invalid.example"))
	invalid.ClientEndpoints[0].ProtocolPlans[0].Transforms = []codelibrary.TransformRevision{{
		ID: "invalid", Revision: 1, CollectionID: "examples", DisplayName: "Invalid",
		PublishedAt: publishedAt, Policy: messagetransform.Policy{RequestJavaScript: `if (`},
	}}
	if err := invalid.Validate(); err == nil {
		t.Fatal("Environment accepted invalid JavaScript syntax")
	}
}

func TestProviderOriginKeepsCanonicalBasePathAndConstrainsCleartext(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input     string
		canonical string
		basePath  string
		transport originidentity.ProviderTransport
	}{
		{"https://RELAY.example:443/v1", "https://relay.example/v1", "/v1", originidentity.ProviderTransportStrictTLS},
		{"https://b\u00fccher.example/anthropic", "https://xn--bcher-kva.example/anthropic", "/anthropic", originidentity.ProviderTransportStrictTLS},
		{"http://127.0.0.1:8080/v1", "http://127.0.0.1:8080/v1", "/v1", originidentity.ProviderTransportLoopbackCleartext},
		{"http://spark-2a59:8888", "http://spark-2a59:8888", "", originidentity.ProviderTransportPrivateCleartext},
		{"http://192.168.50.12:8888/v1", "http://192.168.50.12:8888/v1", "/v1", originidentity.ProviderTransportPrivateCleartext},
	} {
		origin, err := originidentity.ParseProviderOrigin(test.input)
		if err != nil || origin.String() != test.canonical || origin.BasePath() != test.basePath || origin.Transport() != test.transport {
			t.Fatalf("ParseProviderOrigin(%q) = %+v, %v", test.input, origin, err)
		}
	}
	for _, input := range []string{
		"http://203.0.113.7:8888/v1", "http://[::ffff:127.0.0.1]/v1",
		"https://relay.example/v1/", "https://relay.example/a/../v1", "https://relay.example/%76%31",
		"https://relay.example/v1?x=1", "https://relay.example/v1#fragment",
	} {
		if _, err := originidentity.ParseProviderOrigin(input); err == nil {
			t.Fatalf("ParseProviderOrigin(%q) succeeded", input)
		}
	}
}

func TestEnvironmentScopedLookupAllowsTheSameOrigin(t *testing.T) {
	t.Parallel()
	origin := mustOrigin(t, "https://shared.example")
	first := mustCompile(t, fixture(t, "work", origin))
	secondAggregate := fixture(t, "personal", origin)
	secondAggregate.ClientEndpoints[0].ID = "endpoint.personal"
	second := mustCompile(t, secondAggregate)
	projection := NewAtomicProjection()
	if err := projection.Restore(mustSystemSnapshot(t), []EnvironmentSnapshot{first, second}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []EnvironmentID{"work", "personal"} {
		endpoint, err := projection.ResolveClientOrigin(id, origin)
		if err != nil || endpoint.ClientOrigin() != origin {
			t.Fatalf("resolve %q = %+v, %v", id, endpoint, err)
		}
	}
}

func TestOneEndpointCarriesClaudeAndCodexPlans(t *testing.T) {
	t.Parallel()
	snapshot := mustCompile(t, fixture(t, "work", mustOrigin(t, "https://relay.example")))
	endpoints := snapshot.ClientEndpoints()
	if len(endpoints) != 1 {
		t.Fatalf("endpoints = %d", len(endpoints))
	}
	plans := endpoints[0].ProtocolPlans()
	if len(plans) != 2 || plans[0].ClientProtocol != ClientProtocolAnthropicMessages ||
		plans[1].ClientProtocol != ClientProtocolOpenAIResponses {
		t.Fatalf("plans = %+v", plans)
	}
}

func TestRequestResolutionSelectsOneProtocolAndFreezesDefaultRoute(t *testing.T) {
	t.Parallel()
	snapshot := mustCompile(t, fixture(t, "work", mustOrigin(t, "https://relay.example")))
	for _, test := range []struct {
		path      string
		wantPlan  ClientProtocolPlanID
		wantRoute UpstreamRouteID
		wantOp    string
	}{
		{"/v1/messages", "plan.anthropic", "route.anthropic", operationcatalog.AnthropicMessagesCreateID},
		{"/v1/responses", "plan.responses", "route.responses", operationcatalog.OpenAIResponsesCreateID},
	} {
		plan, err := snapshot.ResolveRequest(
			mustOrigin(t, "https://relay.example"),
			RequestFacts{
				Target: protocolspec.RequestTarget{
					Method: "POST", Path: test.path,
					Transport: protocolspec.ClientOperationTransportHTTP,
				},
				DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
			},
		)
		route, hasRoute := plan.UpstreamRoute()
		if err != nil || plan.ProtocolPlan().ID() != test.wantPlan ||
			!hasRoute || route.ID() != test.wantRoute ||
			plan.Operation().ID().String() != test.wantOp ||
			plan.EnvironmentRevision() != 1 {
			t.Fatalf("ResolveRequest(%q) = %+v, %v", test.path, plan, err)
		}
	}
	if _, err := snapshot.ResolveRequest(
		mustOrigin(t, "https://relay.example"),
		RequestFacts{
			Target: protocolspec.RequestTarget{
				Method: "POST", Path: "/v1/unknown",
				Transport: protocolspec.ClientOperationTransportHTTP,
			},
			DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
		},
	); !errors.Is(err, protocolspec.ErrOperationNotCatalogued) {
		t.Fatalf("unknown operation = %v", err)
	}
}

func TestRequestResolutionRejectsAmbiguousProtocolEvidence(t *testing.T) {
	t.Parallel()
	value := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	compiler := ambiguousTestCompiler(t, accountCatalogFor(value))
	snapshot, err := compiler.Compile(value)
	if err != nil {
		t.Fatal(err)
	}
	_, err = snapshot.ResolveRequest(
		mustOrigin(t, "https://relay.example"),
		RequestFacts{
			Target: protocolspec.RequestTarget{
				Method: "POST", Path: "/v1/shared",
				Transport: protocolspec.ClientOperationTransportHTTP,
			},
			DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
		},
	)
	if !errors.Is(err, ErrClientProtocolAmbiguous) {
		t.Fatalf("ambiguous request = %v", err)
	}
}

func TestRouteSetAndAccountCandidateOrderRemainFrozen(t *testing.T) {
	t.Parallel()
	value := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	plan := &value.ClientEndpoints[0].ProtocolPlans[0]
	second := cloneRoute(plan.Destination.Upstream.Routes[0])
	second.ID = "route.anthropic.backup"
	second.Revision = 1
	second.ProviderTarget.ID = "target.anthropic.backup"
	setTestRouteAccount(&second, "account.anthropic.backup")
	plan.Destination.Upstream.Routes = append(plan.Destination.Upstream.Routes, second)
	plan.Destination.Upstream.RouteSet.CandidateRouteIDs = []UpstreamRouteID{second.ID, plan.Destination.Upstream.DefaultRouteID}
	snapshot := mustCompile(t, value)
	endpoint, ok := snapshot.LookupCompiledClientOrigin(mustOrigin(t, "https://relay.example"))
	if !ok {
		t.Fatal("compiled endpoint is missing")
	}
	routeSet, exists := endpoint.ProtocolPlans()[0].UpstreamRouteSet()
	if !exists {
		t.Fatal("compiled upstream RouteSet is missing")
	}
	candidates := routeSet.CandidateRouteIDs()
	if len(candidates) != 2 || candidates[0] != second.ID || candidates[1] != "route.anthropic" {
		t.Fatalf("candidate order = %v", candidates)
	}
	candidates[0] = "mutated"
	if routeSet.CandidateRouteIDs()[0] != second.ID {
		t.Fatal("compiled RouteSet was aliased")
	}
	defaultRoute, err := routeSet.DefaultRoute()
	if err != nil || defaultRoute.ID() != "route.anthropic" {
		t.Fatalf("default route = %+v, %v", defaultRoute, err)
	}
}

func TestNamedWireProfileCannotSilentlyChangeApplicationProtocol(t *testing.T) {
	t.Parallel()
	value := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	value.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].WireProfileRef =
		wireprofile.UpstreamWireProfileClaudeCodeValue
	snapshot := mustCompile(t, value)
	_, err := snapshot.ResolveRequest(
		mustOrigin(t, "https://relay.example"),
		RequestFacts{
			Target: protocolspec.RequestTarget{
				Method: "POST", Path: "/v1/messages",
				Transport: protocolspec.ClientOperationTransportHTTP,
			},
			DownstreamProtocol: wireprofile.ApplicationProtocolHTTP2,
		},
	)
	if !errors.Is(err, wireprofile.ErrInvalidProfile) {
		t.Fatalf("missing H2 variant = %v", err)
	}
}

func TestOriginalDestinationHasNoSyntheticUpstreamAuthority(t *testing.T) {
	t.Parallel()
	value := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	for index := range value.ClientEndpoints[0].ProtocolPlans {
		value.ClientEndpoints[0].ProtocolPlans[index].Destination =
			DestinationPlan{Kind: DestinationKindOriginal}
	}
	snapshot, err := testCompiler(t, nil).Compile(value)
	if err != nil {
		t.Fatalf("compile original destination = %v", err)
	}
	requestPlan, err := snapshot.ResolveRequest(
		mustOrigin(t, "https://relay.example"),
		RequestFacts{
			Target: protocolspec.RequestTarget{
				Method: "POST", Path: "/v1/messages",
				Transport: protocolspec.ClientOperationTransportHTTP,
			},
			DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
		},
	)
	if err != nil || !requestPlan.PreservesOriginalDestination() ||
		requestPlan.UsesUpstreamDestination() {
		t.Fatalf("resolve original destination = %+v, %v", requestPlan, err)
	}
	if _, exists := requestPlan.UpstreamRoute(); exists {
		t.Fatal("Original Destination resolved a synthetic Upstream Route")
	}
	persisted := snapshot.Aggregate().ClientEndpoints[0].ProtocolPlans[0].Destination
	if persisted.Upstream != nil {
		t.Fatalf("Original Destination retained upstream authority: %+v", persisted.Upstream)
	}
}

func TestSystemTransparentInterceptsKnownAgentAPIsAndPreservesOriginalDestination(
	t *testing.T,
) {
	t.Parallel()
	snapshot, err := testCompiler(t, nil).CompileSystemTransparent()
	if err != nil {
		t.Fatalf("compile system_transparent = %v", err)
	}
	if !snapshot.SystemOwned() || snapshot.BlindOnly() ||
		snapshot.ContentRecording() != DefaultContentRecordingPolicy() {
		t.Fatalf("system_transparent is not an intercepting evidence Environment: %+v", snapshot)
	}
	for _, testCase := range []struct {
		origin  string
		path    string
		dialect protocolspec.Dialect
	}{
		{
			origin: "https://api.anthropic.com", path: "/v1/messages",
			dialect: protocolspec.DialectAnthropicMessages,
		},
		{
			origin: "https://api.openai.com", path: "/v1/responses",
			dialect: protocolspec.DialectOpenAIResponses,
		},
		{
			origin: "https://chatgpt.com", path: "/backend-api/codex/responses",
			dialect: protocolspec.DialectOpenAIResponses,
		},
	} {
		origin := mustOrigin(t, testCase.origin)
		binding, bindErr := snapshot.BeginConnection(origin)
		if bindErr != nil || binding.Mode != ConnectionModeSemantic {
			t.Fatalf("BeginConnection(%q) = %+v, %v", testCase.origin, binding, bindErr)
		}
		plan, resolveErr := snapshot.ResolveRequest(origin, RequestFacts{
			Target: protocolspec.RequestTarget{
				Method: "POST", Path: testCase.path,
				Transport: protocolspec.ClientOperationTransportHTTP,
			},
			DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
		})
		if resolveErr != nil || !plan.PreservesOriginalDestination() ||
			plan.ProtocolPlan().ClientDialect() != testCase.dialect {
			t.Fatalf("ResolveRequest(%q) = %+v, %v", testCase.origin, plan, resolveErr)
		}
	}
	unknown := mustOrigin(t, "https://unrelated.example")
	binding, err := snapshot.BeginConnection(unknown)
	if err != nil || binding.Mode != ConnectionModeBlind {
		t.Fatalf("unrelated destination was not kept blind: %+v, %v", binding, err)
	}
}

func TestModelMappingsAreCanonicalAndCompiledWithoutAliasing(t *testing.T) {
	t.Parallel()
	value := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	policy := &value.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].ModelPolicy
	policy.Mode = "map"
	policy.Mappings = []ModelMapping{
		{RequestedModel: "z-client", UpstreamModel: "relay:opaque-z"},
		{RequestedModel: "a-client", UpstreamModel: "relay:opaque-a"},
	}

	snapshot := mustCompile(t, value)
	compiled := snapshot.Aggregate().ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].ModelPolicy
	if got := compiled.Mappings; len(got) != 2 ||
		got[0].RequestedModel != "a-client" || got[0].UpstreamModel != "relay:opaque-a" ||
		got[1].RequestedModel != "z-client" || got[1].UpstreamModel != "relay:opaque-z" {
		t.Fatalf("canonical mappings = %+v", got)
	}

	endpoint, ok := snapshot.LookupCompiledClientOrigin(mustOrigin(t, "https://relay.example"))
	if !ok {
		t.Fatal("compiled endpoint is missing")
	}
	compiledRoute, exists := endpoint.ProtocolPlans()[0].UpstreamRouteSet()
	if !exists {
		t.Fatal("compiled upstream RouteSet is missing")
	}
	route, err := compiledRoute.DefaultRoute()
	if err != nil {
		t.Fatal(err)
	}
	if upstream, mapped := route.ResolveModelMapping("a-client"); !mapped || upstream != "relay:opaque-a" {
		t.Fatalf("compiled exact mapping = %q, %t", upstream, mapped)
	}
	if _, mapped := route.ResolveModelMapping("A-client"); mapped {
		t.Fatal("compiled mapping performed a non-exact match")
	}
}

func TestModelMappingsRejectDuplicateRequestedModels(t *testing.T) {
	t.Parallel()
	value := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	policy := &value.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].ModelPolicy
	policy.Mode = "map"
	policy.Mappings = []ModelMapping{
		{RequestedModel: "claude-client", UpstreamModel: "relay-one"},
		{RequestedModel: "claude-client", UpstreamModel: "relay-two"},
	}
	if _, err := testCompiler(t, nil).Compile(value); !errors.Is(err, ErrInvalidEnvironment) {
		t.Fatalf("duplicate requested model = %v", err)
	}
}

func TestModelMappingsPreservePrintableEdgeWhitespace(t *testing.T) {
	t.Parallel()
	value := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	policy := &value.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].ModelPolicy
	policy.Mode = ModelModeMap
	policy.Mappings = []ModelMapping{{
		RequestedModel: " client model ",
		UpstreamModel:  " relay/custom:model ",
	}}

	compiled := mustCompile(t, value).Aggregate().ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].ModelPolicy
	if upstream, mapped := compiled.ResolveMapping(" client model "); !mapped || upstream != " relay/custom:model " {
		t.Fatalf("exact opaque mapping = %q, %t", upstream, mapped)
	}
	if _, mapped := compiled.ResolveMapping("client model"); mapped {
		t.Fatal("trimmed request must not match an exact opaque mapping")
	}
}

func TestObservePolicyIsTheCanonicalDefaultAuthority(t *testing.T) {
	t.Parallel()
	implicit := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	explicit := implicit.Clone()
	explicit.PolicySet = &PolicySet{ToolMode: ToolPolicyObserve}

	implicitJSON, err := CanonicalJSON(implicit)
	if err != nil {
		t.Fatal(err)
	}
	explicitJSON, err := CanonicalJSON(explicit)
	if err != nil {
		t.Fatal(err)
	}
	implicitDigest, err := Digest(implicit)
	if err != nil {
		t.Fatal(err)
	}
	explicitDigest, err := Digest(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if string(implicitJSON) != string(explicitJSON) || implicitDigest != explicitDigest {
		t.Fatalf("default Observe changed authority: implicit=%s explicit=%s", implicitJSON, explicitJSON)
	}
	decoded, err := DecodeCanonicalJSON(implicitJSON)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.PolicySet != nil || decoded.EffectivePolicySet() != DefaultPolicySet() {
		t.Fatalf("canonical default policy = %+v effective=%+v", decoded.PolicySet, decoded.EffectivePolicySet())
	}

	review := implicit.Clone()
	review.PolicySet = &PolicySet{ToolMode: ToolPolicyReview}
	reviewDigest, err := Digest(review)
	if err != nil {
		t.Fatal(err)
	}
	if reviewDigest == implicitDigest {
		t.Fatal("Review policy did not change the Environment authority digest")
	}
}

func TestGraphValidationRejectsUnsafeIdentityAndReferences(t *testing.T) {
	t.Parallel()
	base := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	cases := []struct {
		name   string
		mutate func(*Environment)
	}{
		{"duplicate origin", func(value *Environment) {
			duplicate := cloneEndpoint(value.ClientEndpoints[0])
			duplicate.ID = "endpoint.two"
			value.ClientEndpoints = append(value.ClientEndpoints, duplicate)
		}},
		{"duplicate protocol", func(value *Environment) {
			duplicate := cloneProtocolPlan(value.ClientEndpoints[0].ProtocolPlans[0])
			duplicate.ID = "plan.other"
			value.ClientEndpoints[0].ProtocolPlans = append(value.ClientEndpoints[0].ProtocolPlans, duplicate)
		}},
		{"duplicate stable route ID", func(value *Environment) {
			value.ClientEndpoints[0].ProtocolPlans[1].Destination.Upstream.Routes[0].ID = value.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].ID
		}},
		{"zero child revision", func(value *Environment) { value.ClientEndpoints[0].Revision = 0 }},
		{"bad default route", func(value *Environment) {
			value.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.DefaultRouteID = "route.missing"
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := base.Clone()
			test.mutate(&candidate)
			if _, err := testCompiler(t, nil).Compile(candidate); !errors.Is(err, ErrInvalidEnvironment) {
				t.Fatalf("compile error = %v", err)
			}
		})
	}
}

func TestAccountCompatibilityAndMutableAliasesFailClosed(t *testing.T) {
	t.Parallel()
	candidate := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	route := &candidate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
	route.AccountPolicy = RouteAccountPolicy{
		Revision: 1, Mode: AccountSelectionFixed, FixedAccountID: "account.work",
		Accounts: []RouteAccountReference{{ID: "account.work", Revision: 2, DisplayName: "Work"}},
	}
	catalog := accountCatalog{"account.work": {
		ID: "account.work", Revision: 2, DisplayName: "Work",
		UpstreamEndpointID: route.ProviderTarget.ID, UpstreamEndpointRevision: route.ProviderTarget.Revision,
		RealmID: "realm.other", Active: true, BackendProtocols: []string{"anthropic_messages"},
	}}
	if _, err := testCompiler(t, catalog).Compile(candidate); err == nil {
		t.Fatal("incompatible realm was accepted")
	}
	catalog["account.work"] = AccountDescriptor{
		ID: "account.work", Revision: 2, DisplayName: "Work",
		UpstreamEndpointID: "target.other", UpstreamEndpointRevision: route.ProviderTarget.Revision,
		RealmID: route.ProviderTarget.RealmID, Active: true,
		BackendProtocols: []string{"anthropic_messages"},
	}
	if _, err := testCompiler(t, catalog).Compile(candidate); err == nil {
		t.Fatal("account from another Endpoint in the same realm was accepted")
	}

	previous := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	next := previous.Clone()
	next.Revision = 2
	next.ClientEndpoints[0].Revision = 2
	next.ClientEndpoints[0].ClientOrigin = mustOrigin(t, "https://other.example")
	if err := ValidateTransition(previous, next); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("mutable endpoint alias error = %v", err)
	}
	next = previous.Clone()
	next.Revision = 2
	next.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].WireProfileRef = "wire.changed"
	if err := ValidateTransition(previous, next); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("unchanged child revisions error = %v", err)
	}
}

func TestAccountSelectorCompilesItsFrozenRevisionAndEndpointAccounts(t *testing.T) {
	t.Parallel()
	candidate := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	route := &candidate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
	route.AccountPolicy = RouteAccountPolicy{
		Revision: 1, Mode: AccountSelectionJavaScript,
		Selector: &codelibrary.AccountSelectorRevision{
			ID: "workspace", Revision: 3, CollectionID: "routing", DisplayName: "Workspace",
			Policy:      accountselector.Policy{JavaScript: `selection.accountId = accounts[1].id;`},
			PublishedAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC),
		},
		Accounts: []RouteAccountReference{
			{ID: "account.basic", Revision: 2, DisplayName: "Basic"},
			{ID: "account.pro", Revision: 4, DisplayName: "Pro"},
		},
	}
	catalog := accountCatalogFor(candidate)
	catalog["account.basic"] = AccountDescriptor{
		ID: "account.basic", Revision: 2, DisplayName: "Basic",
		UpstreamEndpointID:       route.ProviderTarget.ID,
		UpstreamEndpointRevision: route.ProviderTarget.Revision,
		RealmID:                  route.ProviderTarget.RealmID, Active: true,
		BackendProtocols: []string{route.BackendProtocol},
	}
	catalog["account.pro"] = AccountDescriptor{
		ID: "account.pro", Revision: 4, DisplayName: "Pro",
		UpstreamEndpointID:       route.ProviderTarget.ID,
		UpstreamEndpointRevision: route.ProviderTarget.Revision,
		RealmID:                  route.ProviderTarget.RealmID, Active: true,
		BackendProtocols: []string{route.BackendProtocol},
	}
	snapshot, err := testCompiler(t, catalog).Compile(candidate)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	endpoint, ok := snapshot.LookupCompiledClientOrigin(candidate.ClientEndpoints[0].ClientOrigin)
	if !ok || len(endpoint.ProtocolPlans()) == 0 {
		t.Fatal("compiled client Endpoint is missing")
	}
	routes, ok := endpoint.ProtocolPlans()[0].UpstreamRouteSet()
	if !ok {
		t.Fatal("compiled RouteSet is missing")
	}
	compiled, err := routes.Select(route.ID)
	if err != nil {
		t.Fatalf("Select(%q) error = %v", route.ID, err)
	}
	policy := compiled.AccountPolicy()
	if policy.Mode() != AccountSelectionJavaScript || policy.SelectorID() != "workspace" ||
		policy.SelectorRevision() != 3 || len(policy.Accounts()) != 2 {
		t.Fatalf("compiled Account Selector = %+v", policy)
	}
}

func TestUpstreamRouteRequiresAnEndpointAccount(t *testing.T) {
	t.Parallel()
	candidate := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	route := &candidate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
	route.AccountPolicy = RouteAccountPolicy{
		Revision: 1, Mode: AccountSelectionFixed,
	}

	if _, err := testCompiler(t, nil).Compile(candidate); !errors.Is(err, ErrInvalidEnvironment) {
		t.Fatalf("compile error = %v, want %v", err, ErrInvalidEnvironment)
	}
}

func TestAccountDeletionGuardReturnsPublishedRouteReferencesBeforeDeleting(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	manager := mustManager(t, repository, nil)
	candidate := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	for endpointIndex := range candidate.ClientEndpoints {
		for planIndex := range candidate.ClientEndpoints[endpointIndex].ProtocolPlans {
			route := &candidate.ClientEndpoints[endpointIndex].ProtocolPlans[planIndex].Destination.Upstream.Routes[0]
			route.AccountPolicy = RouteAccountPolicy{
				Revision: 1, Mode: AccountSelectionFixed, FixedAccountID: "account.work",
				Accounts: []RouteAccountReference{{ID: "account.work", Revision: 1, DisplayName: "Work"}},
			}
		}
	}
	repository.mu.Lock()
	repository.active[candidate.ID] = candidate.Clone()
	repository.mu.Unlock()

	deleted := false
	references, err := manager.GuardAccountDeletion(
		context.Background(),
		"account.work",
		func() error { deleted = true; return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if deleted || len(references) != 2 ||
		references[0].EnvironmentID != "work" ||
		references[0].EnvironmentName != "Work" ||
		references[0].EnvironmentRevision != 1 ||
		references[0].RouteID != "route.anthropic" ||
		references[1].RouteID != "route.responses" {
		t.Fatalf("account deletion references = %+v deleted=%t", references, deleted)
	}

	for endpointIndex := range candidate.ClientEndpoints {
		for planIndex := range candidate.ClientEndpoints[endpointIndex].ProtocolPlans {
			candidate.ClientEndpoints[endpointIndex].ProtocolPlans[planIndex].Destination =
				DestinationPlan{Kind: DestinationKindOriginal}
		}
	}
	repository.mu.Lock()
	repository.active[candidate.ID] = candidate.Clone()
	repository.mu.Unlock()
	references, err = manager.GuardAccountDeletion(
		context.Background(),
		"account.work",
		func() error { deleted = true; return nil },
	)
	if err != nil || !deleted || len(references) != 0 {
		t.Fatalf("unreferenced account deletion = %+v deleted=%t err=%v", references, deleted, err)
	}
}

func TestStableChildrenCannotMoveAcrossParents(t *testing.T) {
	t.Parallel()
	previous := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	movedPlan := previous.Clone()
	movedPlan.Revision = 2
	movedPlan.ClientEndpoints[0].Revision = 2
	plan := movedPlan.ClientEndpoints[0].ProtocolPlans[0]
	movedPlan.ClientEndpoints[0].ProtocolPlans = movedPlan.ClientEndpoints[0].ProtocolPlans[1:]
	second := cloneEndpoint(movedPlan.ClientEndpoints[0])
	second.ID = "endpoint.second"
	second.Revision = 1
	second.ClientOrigin = mustOrigin(t, "https://second.example")
	second.ProtocolPlans = []ClientProtocolPlan{plan}
	movedPlan.ClientEndpoints = append(movedPlan.ClientEndpoints, second)
	if _, err := materializeIdentityHistory(&previous, movedPlan); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("moved protocol plan = %v", err)
	}

	movedRoute := previous.Clone()
	movedRoute.Revision = 2
	movedRoute.ClientEndpoints[0].Revision = 2
	movedRoute.ClientEndpoints[0].ProtocolPlans[0].Revision = 2
	movedRoute.ClientEndpoints[0].ProtocolPlans[1].Revision = 2
	firstPlan := &movedRoute.ClientEndpoints[0].ProtocolPlans[0]
	secondPlan := &movedRoute.ClientEndpoints[0].ProtocolPlans[1]
	route := firstPlan.Destination.Upstream.Routes[0]
	firstPlan.Destination.Upstream.Routes = secondPlan.Destination.Upstream.Routes
	firstPlan.Destination.Upstream.DefaultRouteID = firstPlan.Destination.Upstream.Routes[0].ID
	firstPlan.Destination.Upstream.RouteSet.CandidateRouteIDs = []UpstreamRouteID{firstPlan.Destination.Upstream.Routes[0].ID}
	secondPlan.Destination.Upstream.Routes = []UpstreamRoute{route}
	secondPlan.Destination.Upstream.DefaultRouteID = route.ID
	secondPlan.Destination.Upstream.RouteSet.CandidateRouteIDs = []UpstreamRouteID{route.ID}
	if _, err := materializeIdentityHistory(&previous, movedRoute); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("moved route = %v", err)
	}
}

func TestDeletedStableChildIDCannotBeReused(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	manager := mustManager(t, repository, nil)
	first := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	firstDraft, err := manager.SaveDraft(context.Background(), DraftCommand{Candidate: first})
	if err != nil {
		t.Fatal(err)
	}
	firstPreview, err := manager.Preview(context.Background(), first.ID, firstDraft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), firstPreview); err != nil {
		t.Fatal(err)
	}

	second := first.Clone()
	second.Revision = 2
	second.ClientEndpoints[0].Revision = 2
	removed := cloneProtocolPlan(second.ClientEndpoints[0].ProtocolPlans[1])
	second.ClientEndpoints[0].ProtocolPlans = second.ClientEndpoints[0].ProtocolPlans[:1]
	secondDraft, err := manager.SaveDraft(context.Background(), DraftCommand{
		ExpectedBaseRevision: 1, ExpectedDraftRevision: 0, Candidate: second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondDraft.Candidate.RetiredChildIdentities) != 2 {
		t.Fatalf("retired identities = %+v", secondDraft.Candidate.RetiredChildIdentities)
	}
	secondPreview, err := manager.Preview(context.Background(), second.ID, secondDraft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), secondPreview); err != nil {
		t.Fatal(err)
	}

	third := secondDraft.Candidate.Clone()
	third.Revision = 3
	third.RetiredChildIdentities = nil // control-plane input does not own tombstones
	third.ClientEndpoints[0].Revision = 3
	third.ClientEndpoints[0].ProtocolPlans = append(third.ClientEndpoints[0].ProtocolPlans, removed)
	if _, err := manager.SaveDraft(context.Background(), DraftCommand{
		ExpectedBaseRevision: 2, ExpectedDraftRevision: 0, Candidate: third,
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("reused retired child = %v", err)
	}
}

func TestSnapshotsAndSystemTransparentResistAliases(t *testing.T) {
	t.Parallel()
	aggregate := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	snapshot := mustCompile(t, aggregate)
	aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].ProviderTarget.Capabilities[0] = protocolspec.ProviderCapability("mutated")
	copyOne := snapshot.Aggregate()
	copyOne.ClientEndpoints[0].ClientOrigin = mustOrigin(t, "https://mutated.example")
	copyOne.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].ProviderTarget.Capabilities[0] = protocolspec.ProviderCapability("mutated")
	copyTwo := snapshot.Aggregate()
	if copyTwo.ClientEndpoints[0].ClientOrigin.String() != "https://relay.example" ||
		copyTwo.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].ProviderTarget.Capabilities[0] != protocolspec.ProviderCapabilityMessages {
		t.Fatalf("snapshot was aliased: %+v", copyTwo)
	}
	system := mustSystemSnapshot(t)
	if !system.SystemOwned() || system.BlindOnly() || len(system.ClientEndpoints()) != 3 {
		t.Fatalf("system snapshot = %+v", system)
	}
	projection := NewAtomicProjection()
	if err := projection.Restore(system, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := projection.Resolve(SystemTransparentID); err != nil {
		t.Fatal(err)
	}
	if err := projection.Publish(system); !errors.Is(err, ErrSystemEnvironment) {
		t.Fatalf("publish system = %v", err)
	}
}

func TestProjectionRestoreMonotonicPoisonAndImmutableReads(t *testing.T) {
	t.Parallel()
	first := fixture(t, "first", mustOrigin(t, "https://first.example"))
	second := fixture(t, "second", mustOrigin(t, "https://second.example"))
	projection := NewAtomicProjection()
	system := mustSystemSnapshot(t)
	if err := projection.Restore(system, []EnvironmentSnapshot{mustCompile(t, first), mustCompile(t, second)}); err != nil {
		t.Fatal(err)
	}
	if err := projection.Restore(system, nil); !errors.Is(err, ErrProjectionRestored) {
		t.Fatalf("second restore = %v", err)
	}
	if err := projection.Publish(mustCompile(t, first)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("regression = %v", err)
	}
	skipped := first.Clone()
	skipped.Revision = 3
	if err := projection.Publish(mustCompile(t, skipped)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("skipped revision = %v", err)
	}
	projection.MarkUnavailable(first.ID)
	if _, err := projection.Resolve(first.ID); !errors.Is(err, ErrProjectionUnavailable) {
		t.Fatalf("poisoned resolve = %v", err)
	}
	if _, err := projection.Resolve(second.ID); err != nil {
		t.Fatalf("unrelated resolve = %v", err)
	}
	health := projection.Health()
	if health.State != ProjectionStateUnavailable || len(health.UnavailableEnvironments) != 1 || health.UnavailableEnvironments[0] != first.ID {
		t.Fatalf("health = %+v", health)
	}
}

func TestDraftPreviewPublishRevalidatesTheFrozenCaptureList(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	candidate := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	inspector := fixedInspector{references: []CaptureReference{{
		Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "run.one"},
	}}}
	manager := mustManager(t, repository, inspector)
	draftOne, err := manager.SaveDraft(context.Background(), DraftCommand{Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SaveDraft(context.Background(), DraftCommand{Candidate: candidate}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale draft CAS = %v", err)
	}
	if _, err := manager.Resolve(candidate.ID); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("draft became authority: %v", err)
	}
	previewOne, err := manager.Preview(context.Background(), candidate.ID, draftOne.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(previewOne.ContinuingCaptures) != 1 ||
		previewOne.ContinuingCaptures[0].Capture.ID != "run.one" {
		t.Fatalf("initial impact = %+v", previewOne)
	}
	candidate.Name = "Work replacement"
	draftTwo, err := manager.SaveDraft(context.Background(), DraftCommand{ExpectedDraftRevision: draftOne.Revision, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), previewOne); !errors.Is(err, ErrPreviewStale) {
		t.Fatalf("stale preview publish = %v", err)
	}
	previewTwo, err := manager.Preview(context.Background(), candidate.ID, draftTwo.Revision)
	if err != nil {
		t.Fatal(err)
	}
	tampered := previewTwo
	tampered.CandidateDigest[0]++
	if _, err := manager.Publish(context.Background(), tampered); !errors.Is(err, ErrPreviewStale) {
		t.Fatalf("tampered digest = %v", err)
	}
	result, err := manager.Publish(context.Background(), previewTwo)
	if err != nil || result.Outcome != CommitOutcomeCommitted {
		t.Fatalf("publish = %+v, %v", result, err)
	}
	resolved, err := manager.Resolve(candidate.ID)
	if err != nil || resolved.Name() != "Work replacement" {
		t.Fatalf("resolved = %+v, %v", resolved, err)
	}
	if _, err := manager.Publish(context.Background(), previewTwo); !errors.Is(err, ErrPreviewStale) {
		t.Fatalf("reused publish preview = %v", err)
	}
}

func TestPublishRejectsAnActiveCASThatMovedAfterPreview(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	manager := mustManager(t, repository, nil)
	first := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	draft, err := manager.SaveDraft(context.Background(), DraftCommand{Candidate: first})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(context.Background(), first.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), preview); err != nil {
		t.Fatal(err)
	}

	second := first.Clone()
	second.Revision = 2
	second.Name = "Second"
	draft, err = manager.SaveDraft(context.Background(), DraftCommand{ExpectedBaseRevision: 1, ExpectedDraftRevision: 0, Candidate: second})
	if err != nil {
		t.Fatal(err)
	}
	preview, err = manager.Preview(context.Background(), second.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	moved := first.Clone()
	moved.Revision = 2
	moved.Name = "Concurrent writer"
	repository.active[first.ID] = moved
	repository.mu.Unlock()
	result, err := manager.Publish(context.Background(), preview)
	if !errors.Is(err, ErrRevisionConflict) || result.Outcome != CommitOutcomeConflict {
		t.Fatalf("stale active CAS = %+v, %v", result, err)
	}
}

func TestImpactPreviewReportsThatRunningCapturesKeepTheirFrozenRevision(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	active := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	repository.active[active.ID] = active.Clone()
	inspector := fixedInspector{references: []CaptureReference{{
		Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "run.one"},
	}}}
	manager := mustManager(t, repository, inspector)
	candidate := active.Clone()
	candidate.Revision = 2
	candidate.Name = "Work renamed"
	draft, err := manager.SaveDraft(context.Background(), DraftCommand{ExpectedBaseRevision: 1, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(context.Background(), candidate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.ContinuingCaptures) != 1 ||
		preview.ContinuingCaptures[0].Capture.ID != "run.one" {
		t.Fatalf("impact = %+v", preview)
	}
}

func TestPublishAllowsLaunchAuthorityExpansionForFutureCaptures(t *testing.T) {
	t.Parallel()

	repository := newMemoryRepository()
	active := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	repository.active[active.ID] = active.Clone()
	inspector := fixedInspector{references: []CaptureReference{{
		Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "run.one"},
	}}}
	manager := mustManager(t, repository, inspector)
	candidate := active.Clone()
	candidate.Revision = 2
	added := cloneEndpoint(active.ClientEndpoints[0])
	added.ID = "endpoint.added"
	added.ClientOrigin = mustOrigin(t, "https://added.example")
	for index := range added.ProtocolPlans {
		plan := &added.ProtocolPlans[index]
		plan.ID = ClientProtocolPlanID(fmt.Sprintf("plan.added.%d", index))
		plan.ClientAdapterPolicy.ID = fmt.Sprintf("adapter.added.%d", index)
		plan.Destination.Upstream.RouteSet.ID = fmt.Sprintf("routes.added.%d", index)
		route := &plan.Destination.Upstream.Routes[0]
		route.ID = UpstreamRouteID(fmt.Sprintf("route.added.%d", index))
		plan.Destination.Upstream.DefaultRouteID = route.ID
		plan.Destination.Upstream.RouteSet.CandidateRouteIDs = []UpstreamRouteID{route.ID}
		route.ProviderTarget.ID = fmt.Sprintf("target.added.%d", index)
		route.ProviderTarget.Origin = mustProviderOrigin(t, added.ClientOrigin.String())
		setTestRouteAccount(route, fmt.Sprintf("account.added.%d", index))
	}
	candidate.ClientEndpoints = append(candidate.ClientEndpoints, added)
	draft, err := manager.SaveDraft(context.Background(), DraftCommand{
		ExpectedBaseRevision: 1, Candidate: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(context.Background(), candidate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.ContinuingCaptures) != 1 {
		t.Fatalf("impact = %+v", preview)
	}
	result, err := manager.Publish(context.Background(), preview)
	if err != nil || result.Outcome != CommitOutcomeCommitted || result.Aggregate.Revision != 2 {
		t.Fatalf("Publish = %+v, %v", result, err)
	}
	current, err := manager.Get(context.Background(), active.ID)
	if err != nil || current.Revision() != 2 || len(current.ClientEndpoints()) != 2 {
		t.Fatalf("published Environment = %+v, %v", current.Aggregate(), err)
	}
}

func TestImpactPreviewDoesNotApplyProtocolChangesToRunningCaptures(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	active := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	second := cloneEndpoint(active.ClientEndpoints[0])
	second.ProtocolPlans = second.ProtocolPlans[:1]
	second.ID = "endpoint.second"
	second.ClientOrigin = mustOrigin(t, "https://second.example")
	second.ProtocolPlans[0].ID = "plan.second"
	second.ProtocolPlans[0].ClientAdapterPolicy.ID = "adapter.second"
	second.ProtocolPlans[0].Destination.Upstream.RouteSet.ID = "routes.second"
	second.ProtocolPlans[0].Destination.Upstream.DefaultRouteID = "route.second"
	second.ProtocolPlans[0].Destination.Upstream.Routes[0].ID = "route.second"
	second.ProtocolPlans[0].Destination.Upstream.RouteSet.CandidateRouteIDs = []UpstreamRouteID{"route.second"}
	second.ProtocolPlans[0].Destination.Upstream.Routes[0].ProviderTarget.ID = "target.second"
	second.ProtocolPlans[0].Destination.Upstream.Routes[0].ProviderTarget.Origin =
		mustProviderOrigin(t, second.ClientOrigin.String())
	setTestRouteAccount(
		&second.ProtocolPlans[0].Destination.Upstream.Routes[0],
		"account.second",
	)
	active.ClientEndpoints = append(active.ClientEndpoints, second)
	repository.active[active.ID] = active.Clone()
	inspector := fixedInspector{references: []CaptureReference{{
		Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "run.one"},
	}}}
	manager := mustManager(t, repository, inspector)
	candidate := active.Clone()
	candidate.Revision = 2
	candidate.ClientEndpoints[1].Revision = 2
	candidate.ClientEndpoints[1].ProtocolPlans[0].Revision = 2
	candidate.ClientEndpoints[1].ProtocolPlans[0].ClientAdapterPolicy = ClientAdapterPolicy{
		ID: "adapter.second.replacement", Revision: 2,
	}
	draft, err := manager.SaveDraft(context.Background(), DraftCommand{ExpectedBaseRevision: 1, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(context.Background(), candidate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.ContinuingCaptures) != 1 ||
		preview.ContinuingCaptures[0].Capture.ID != "run.one" {
		t.Fatalf("frozen-Capture impact = %+v", preview)
	}
}

func TestSystemTransparentCannotEnterTheUserDraftPath(t *testing.T) {
	t.Parallel()
	manager := mustManager(t, newMemoryRepository(), nil)
	candidate := fixture(t, SystemTransparentID.String(), mustOrigin(t, "https://relay.example"))
	if _, err := manager.SaveDraft(context.Background(), DraftCommand{Candidate: candidate}); !errors.Is(err, ErrSystemEnvironment) {
		t.Fatalf("system draft = %v", err)
	}
}

func TestControlReaderSeparatesCurrentDraftAndHistoricalRevisions(t *testing.T) {
	t.Parallel()
	manager := mustManager(t, newMemoryRepository(), nil)
	first := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	draft, err := manager.SaveDraft(context.Background(), DraftCommand{Candidate: first})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(context.Background(), first.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), preview); err != nil {
		t.Fatal(err)
	}
	second := first.Clone()
	second.Revision = 2
	second.Name = "Work renamed"
	privateDraft, err := manager.SaveDraft(context.Background(), DraftCommand{
		ExpectedBaseRevision: 1, ExpectedDraftRevision: 0, Candidate: second,
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := manager.Get(context.Background(), first.ID)
	if err != nil || current.Revision() != 1 || current.Name() != "Work" {
		t.Fatalf("current = %+v, %v", current, err)
	}
	readDraft, err := manager.GetDraft(context.Background(), first.ID)
	if err != nil || readDraft.Revision != privateDraft.Revision || readDraft.Candidate.Name != "Work renamed" {
		t.Fatalf("draft = %+v, %v", readDraft, err)
	}
	secondPreview, err := manager.Preview(context.Background(), first.ID, privateDraft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), secondPreview); err != nil {
		t.Fatal(err)
	}
	historical, err := manager.GetRevision(context.Background(), first.ID, 1)
	if err != nil || historical.Name() != "Work" {
		t.Fatalf("historical = %+v, %v", historical, err)
	}
	latest, err := manager.GetRevision(context.Background(), first.ID, 2)
	if err != nil || latest.Name() != "Work renamed" {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
	listed, err := manager.List(context.Background())
	if err != nil || len(listed) != 2 || listed[0].ID() != SystemTransparentID || listed[1].ID() != first.ID {
		t.Fatalf("list = %+v, %v", listed, err)
	}
}

func TestFailedAndIndeterminateTransactionsNeverPublishCandidate(t *testing.T) {
	t.Parallel()
	for _, outcome := range []CommitOutcome{CommitOutcomeNotCommitted, CommitOutcomeIndeterminate} {
		t.Run(string(outcome), func(t *testing.T) {
			repository := newMemoryRepository()
			repository.publishOutcome = outcome
			manager := mustManager(t, repository, nil)
			candidate := fixture(t, "work", mustOrigin(t, "https://relay.example"))
			draft, err := manager.SaveDraft(context.Background(), DraftCommand{Candidate: candidate})
			if err != nil {
				t.Fatal(err)
			}
			preview, err := manager.Preview(context.Background(), candidate.ID, draft.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Publish(context.Background(), preview); err == nil {
				t.Fatal("failed publish reported success")
			}
			_, resolveErr := manager.Resolve(candidate.ID)
			if outcome == CommitOutcomeIndeterminate {
				if !errors.Is(resolveErr, ErrProjectionUnavailable) {
					t.Fatalf("indeterminate resolve = %v", resolveErr)
				}
			} else if !errors.Is(resolveErr, ErrEnvironmentNotFound) {
				t.Fatalf("failed resolve = %v", resolveErr)
			}
		})
	}
}

func TestProjectionConcurrentReadersAndWriters(t *testing.T) {
	projection := NewAtomicProjection()
	base := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	if err := projection.Restore(mustSystemSnapshot(t), []EnvironmentSnapshot{mustCompile(t, base)}); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 1000 {
				snapshot, err := projection.Resolve(base.ID)
				if err != nil || snapshot.ID() != base.ID || snapshot.Aggregate().ClientEndpoints[0].ID != "endpoint.shared" {
					t.Errorf("concurrent resolve = %+v, %v", snapshot, err)
					return
				}
			}
		}()
	}
	for revision := Revision(2); revision <= 100; revision++ {
		candidate := base.Clone()
		candidate.Revision = revision
		candidate.Name = fmt.Sprintf("Work %d", revision)
		if err := projection.Publish(mustCompile(t, candidate)); err != nil {
			t.Fatal(err)
		}
	}
	wait.Wait()
}

type accountCatalog map[string]AccountDescriptor

func (catalog accountCatalog) LookupAccount(id string) (AccountDescriptor, bool) {
	value, ok := catalog[id]
	return value, ok
}

func fixture(t *testing.T, id string, origin originidentity.ClientOrigin) Environment {
	t.Helper()
	providerOrigin := mustProviderOrigin(t, origin.String())
	plan := func(planID string, protocol ClientProtocol, routeID string, realm string) ClientProtocolPlan {
		accountID := "account." + planID
		return ClientProtocolPlan{
			ID: ClientProtocolPlanID(planID), Revision: 1, ClientProtocol: protocol,
			ClientAdapterPolicy: ClientAdapterPolicy{ID: "adapter." + planID, Revision: 1},
			EgressProfile:       egressprofile.Direct(),
			Destination: DestinationPlan{Kind: DestinationKindUpstream, Upstream: &UpstreamPlan{
				DefaultRouteID: UpstreamRouteID(routeID), RouteSet: RouteSet{
					ID: "routes." + planID, Revision: 1,
					CandidateRouteIDs: []UpstreamRouteID{UpstreamRouteID(routeID)},
				},
				Routes: []UpstreamRoute{{ID: UpstreamRouteID(routeID), Revision: 1,
					ProviderTarget: ProviderTarget{
						ID: "target." + planID, Revision: 1, Origin: providerOrigin, RealmID: realm,
						Capabilities: []protocolspec.ProviderCapability{
							protocolspec.ProviderCapabilityMessages,
							protocolspec.ProviderCapabilityStreaming,
							protocolspec.ProviderCapabilityToolCalls,
						},
					},
					BackendProtocol: string(protocol), AccountPolicy: RouteAccountPolicy{
						Revision: 1, Mode: AccountSelectionFixed, FixedAccountID: accountID,
						Accounts: []RouteAccountReference{{ID: accountID, Revision: 1, DisplayName: accountID}},
					},
					ModelPolicy:    ModelPolicy{Revision: 1, Mode: "passthrough"},
					WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue}},
			}},
		}
	}
	return Environment{ID: EnvironmentID(id), Name: "Work", State: StateActive, Revision: 1,
		ContentRecording: DefaultContentRecordingPolicy(),
		ClientEndpoints: []ClientEndpoint{{ID: "endpoint.shared", Revision: 1, ClientOrigin: origin,
			ProtocolPlans: []ClientProtocolPlan{plan("plan.anthropic", ClientProtocolAnthropicMessages, "route.anthropic", "realm.anthropic"), plan("plan.responses", ClientProtocolOpenAIResponses, "route.responses", "realm.openai")}}}}
}

func mustOrigin(t *testing.T, value string) originidentity.ClientOrigin {
	t.Helper()
	origin, err := originidentity.ParseClientOrigin(value)
	if err != nil {
		t.Fatal(err)
	}
	return origin
}

func semanticCaptureBinding(t *testing.T, aggregate Environment, origin string) ConnectionBinding {
	t.Helper()
	binding, err := mustCompile(t, aggregate).BeginConnection(mustOrigin(t, origin))
	if err != nil || binding.Mode != ConnectionModeSemantic {
		t.Fatalf("BeginConnection(%q) = %+v, %v", origin, binding, err)
	}
	return binding
}
func mustProviderOrigin(t *testing.T, value string) originidentity.ProviderOrigin {
	t.Helper()
	origin, err := originidentity.ParseProviderOrigin(value)
	if err != nil {
		t.Fatal(err)
	}
	return origin
}
func mustCompile(t *testing.T, value Environment) EnvironmentSnapshot {
	t.Helper()
	snapshot, err := testCompiler(t, accountCatalogFor(value)).Compile(value)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func mustSystemSnapshot(t *testing.T) EnvironmentSnapshot {
	t.Helper()
	snapshot, err := testCompiler(t, nil).CompileSystemTransparent()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
func mustManager(t *testing.T, repository *memoryRepository, inspector CaptureInspector) *Manager {
	t.Helper()
	accounts := accountCatalogFor(fixture(t, "catalog", mustOrigin(t, "https://relay.example")))
	accounts["account.added.0"] = AccountDescriptor{
		ID: "account.added.0", Revision: 1, DisplayName: "account.added.0",
		UpstreamEndpointID: "target.added.0", UpstreamEndpointRevision: 1,
		RealmID: "realm.anthropic", Active: true,
		BackendProtocols: []string{"anthropic_messages"},
	}
	accounts["account.added.1"] = AccountDescriptor{
		ID: "account.added.1", Revision: 1, DisplayName: "account.added.1",
		UpstreamEndpointID: "target.added.1", UpstreamEndpointRevision: 1,
		RealmID: "realm.openai", Active: true,
		BackendProtocols: []string{"openai_responses"},
	}
	accounts["account.second"] = AccountDescriptor{
		ID: "account.second", Revision: 1, DisplayName: "account.second",
		UpstreamEndpointID: "target.second", UpstreamEndpointRevision: 1,
		RealmID: "realm.anthropic", Active: true,
		BackendProtocols: []string{"anthropic_messages"},
	}
	manager, err := NewManager(context.Background(), repository, testCompiler(t, accounts), NewAtomicProjection(), inspector)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func accountCatalogFor(value Environment) accountCatalog {
	catalog := accountCatalog{}
	for _, endpoint := range value.ClientEndpoints {
		for _, plan := range endpoint.ProtocolPlans {
			for _, route := range destinationRoutes(plan.Destination) {
				for _, frozen := range route.AccountPolicy.Accounts {
					catalog[frozen.ID] = AccountDescriptor{
						ID: frozen.ID, Revision: frozen.Revision, DisplayName: frozen.DisplayName,
						UpstreamEndpointID:       route.ProviderTarget.ID,
						UpstreamEndpointRevision: route.ProviderTarget.Revision,
						RealmID:                  route.ProviderTarget.RealmID, Active: true,
						BackendProtocols: []string{route.BackendProtocol},
					}
				}
			}
		}
	}
	return catalog
}

func setTestRouteAccount(route *UpstreamRoute, accountID string) {
	route.AccountPolicy = RouteAccountPolicy{
		Revision: 1, Mode: AccountSelectionFixed, FixedAccountID: accountID,
		Accounts: []RouteAccountReference{{ID: accountID, Revision: 1, DisplayName: accountID}},
	}
}

func testCompiler(t *testing.T, accounts AccountCatalog) Compiler {
	t.Helper()
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	pair := func(id string, dialect protocolspec.Dialect) protocolspec.CodecPairDefinition {
		pairID, pairErr := protocolspec.NewCodecPairID(id)
		if pairErr != nil {
			t.Fatal(pairErr)
		}
		return protocolspec.CodecPairDefinition{
			ID: pairID, Revision: 1, ClientDialect: dialect, ProviderDialect: dialect,
			ClientOperationIDs: operations.SemanticOperationIDs(dialect),
			RequiredCapabilities: []protocolspec.ProviderCapability{
				protocolspec.ProviderCapabilityMessages,
				protocolspec.ProviderCapabilityStreaming,
				protocolspec.ProviderCapabilityToolCalls,
			},
		}
	}
	protocols, err := protocolspec.NewCatalog(operations.Definitions(), []protocolspec.CodecPairDefinition{
		pair("test.anthropic.passthrough", protocolspec.DialectAnthropicMessages),
		pair("test.responses.passthrough", protocolspec.DialectOpenAIResponses),
	})
	if err != nil {
		t.Fatal(err)
	}
	wires, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := NewCompiler(accounts, nil, protocols, wires)
	if err != nil {
		t.Fatal(err)
	}
	return compiler
}

func ambiguousTestCompiler(t *testing.T, accounts AccountCatalog) Compiler {
	t.Helper()
	definition := func(id string, dialect protocolspec.Dialect) protocolspec.ClientOperationDefinition {
		operationID, err := protocolspec.NewClientOperationID(id)
		if err != nil {
			t.Fatal(err)
		}
		result, err := protocolspec.NewClientOperationDefinition(protocolspec.ClientOperationOptions{
			ID: operationID, Revision: 1, ClientDialect: dialect,
			Methods: []string{"POST"}, PathPattern: "/v1/shared",
			PathMatch:    protocolspec.ClientOperationPathExact,
			Kind:         protocolspec.ClientOperationSemantic,
			Transport:    protocolspec.ClientOperationTransportHTTP,
			BodyKind:     protocolspec.ClientOperationBodyJSON,
			ReplayClass:  protocolspec.ClientReplayGenerationCostOnly,
			CodecFeature: "messages", MaxBodyBytes: 1024,
			PayloadClass: protocolspec.OperationPayloadClientSemantic, EgressBearing: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	definitions := []protocolspec.ClientOperationDefinition{
		definition("shared.anthropic", protocolspec.DialectAnthropicMessages),
		definition("shared.responses", protocolspec.DialectOpenAIResponses),
	}
	pairs := make([]protocolspec.CodecPairDefinition, 0, len(definitions))
	for _, operation := range definitions {
		pairID, err := protocolspec.NewCodecPairID("pair." + operation.ID().String())
		if err != nil {
			t.Fatal(err)
		}
		pairs = append(pairs, protocolspec.CodecPairDefinition{
			ID: pairID, Revision: 1,
			ClientDialect: operation.ClientDialect(), ProviderDialect: operation.ClientDialect(),
			ClientOperationIDs: []protocolspec.ClientOperationID{operation.ID()},
			RequiredCapabilities: []protocolspec.ProviderCapability{
				protocolspec.ProviderCapabilityMessages,
				protocolspec.ProviderCapabilityStreaming,
				protocolspec.ProviderCapabilityToolCalls,
			},
		})
	}
	protocols, err := protocolspec.NewCatalog(definitions, pairs)
	if err != nil {
		t.Fatal(err)
	}
	wires, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := NewCompiler(accounts, nil, protocols, wires)
	if err != nil {
		t.Fatal(err)
	}
	return compiler
}

type fixedInspector struct{ references []CaptureReference }

func mustLaunchAuthority(t *testing.T, aggregate Environment) LaunchAuthorityBoundary {
	t.Helper()
	boundary, err := NewLaunchAuthorityBoundary(mustCompile(t, aggregate))
	if err != nil {
		t.Fatal(err)
	}
	return boundary
}

func (inspector fixedInspector) ActiveCaptures(_ context.Context, _ EnvironmentID, limit int) ([]CaptureReference, error) {
	if len(inspector.references) > limit {
		return nil, ErrImpactLimitExceeded
	}
	return append([]CaptureReference(nil), inspector.references...), nil
}

type memoryRepository struct {
	mu             sync.Mutex
	active         map[EnvironmentID]Environment
	revisions      map[EnvironmentID]map[Revision]Environment
	drafts         map[EnvironmentID]Draft
	draftRevisions map[EnvironmentID]Revision
	publishOutcome CommitOutcome
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		active: map[EnvironmentID]Environment{}, revisions: map[EnvironmentID]map[Revision]Environment{},
		drafts: map[EnvironmentID]Draft{}, draftRevisions: map[EnvironmentID]Revision{},
	}
}
func (repository *memoryRepository) LoadAllActive(context.Context) ([]Environment, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	result := make([]Environment, 0, len(repository.active))
	for _, value := range repository.active {
		result = append(result, value.Clone())
	}
	return result, nil
}
func (repository *memoryRepository) LoadActive(_ context.Context, id EnvironmentID) (Environment, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	value, ok := repository.active[id]
	return value.Clone(), ok, nil
}
func (repository *memoryRepository) LoadRevision(_ context.Context, id EnvironmentID, revision Revision) (Environment, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	values := repository.revisions[id]
	value, ok := values[revision]
	return value.Clone(), ok, nil
}
func (repository *memoryRepository) LoadDraft(_ context.Context, id EnvironmentID) (Draft, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	value, ok := repository.drafts[id]
	return value.Clone(), ok, nil
}
func (repository *memoryRepository) SaveDraft(_ context.Context, mutation DraftMutation) (Draft, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	active, exists := repository.active[mutation.EnvironmentID]
	currentDraft, draftExists := repository.drafts[mutation.EnvironmentID]
	currentDraftRevision := Revision(0)
	if draftExists {
		currentDraftRevision = currentDraft.Revision
	}
	if (exists && active.Revision != mutation.ExpectedBaseRevision) || (!exists && mutation.ExpectedBaseRevision != 0) || currentDraftRevision != mutation.ExpectedDraftRevision {
		return Draft{}, ErrRevisionConflict
	}
	revision := repository.draftRevisions[mutation.EnvironmentID] + 1
	draft := Draft{EnvironmentID: mutation.EnvironmentID, BaseRevision: mutation.ExpectedBaseRevision, Revision: revision, Candidate: mutation.Candidate.Clone(), CandidateDigest: mutation.CandidateDigest}
	repository.draftRevisions[mutation.EnvironmentID] = revision
	repository.drafts[mutation.EnvironmentID] = draft
	return draft.Clone(), nil
}
func (repository *memoryRepository) PublishDraft(_ context.Context, mutation PublishMutation) (CommitResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.publishOutcome != "" && repository.publishOutcome != CommitOutcomeCommitted {
		return CommitResult{Outcome: repository.publishOutcome}, errors.New("injected commit failure")
	}
	active, exists := repository.active[mutation.EnvironmentID]
	draft, draftExists := repository.drafts[mutation.EnvironmentID]
	if (exists && active.Revision != mutation.ExpectedBaseRevision) || (!exists && mutation.ExpectedBaseRevision != 0) || !draftExists || draft.Revision != mutation.DraftRevision || draft.CandidateDigest != mutation.CandidateDigest {
		return CommitResult{Outcome: CommitOutcomeConflict, ActualRevision: active.Revision}, nil
	}
	repository.active[mutation.EnvironmentID] = mutation.Candidate.Clone()
	if repository.revisions[mutation.EnvironmentID] == nil {
		repository.revisions[mutation.EnvironmentID] = map[Revision]Environment{}
	}
	repository.revisions[mutation.EnvironmentID][mutation.Candidate.Revision] = mutation.Candidate.Clone()
	delete(repository.drafts, mutation.EnvironmentID)
	return CommitResult{Outcome: CommitOutcomeCommitted, Aggregate: mutation.Candidate.Clone(), ActualRevision: mutation.Candidate.Revision}, nil
}

// Retire mirrors the store: the active pointer and the draft go, the revisions
// a frozen Exchange resolves stay.
func (repository *memoryRepository) Retire(
	_ context.Context,
	id EnvironmentID,
) (bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, live := repository.active[id]; !live {
		return false, nil
	}
	delete(repository.active, id)
	delete(repository.drafts, id)
	delete(repository.draftRevisions, id)
	return true, nil
}

func retiringManager(
	t *testing.T,
	repository *memoryRepository,
	captures []CaptureReference,
) *Manager {
	t.Helper()
	return mustManager(t, repository, fixedInspector{references: captures})
}

func seedActiveEnvironment(t *testing.T, repository *memoryRepository) Environment {
	t.Helper()
	candidate := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	repository.mu.Lock()
	repository.active[candidate.ID] = candidate.Clone()
	repository.mu.Unlock()
	return candidate
}

// A running Capture has already frozen its admission decisions against this
// Environment. Removing it underneath would leave those decisions pointing at
// an authority that no longer exists, so the delete is refused and the holder
// is named.
func TestDeletingAnEnvironmentIsRefusedWhileACaptureIsRunning(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	candidate := seedActiveEnvironment(t, repository)
	running, err := captureidentity.New(captureidentity.KindManagedRun, "run.abc")
	if err != nil {
		t.Fatal(err)
	}
	manager := retiringManager(t, repository, []CaptureReference{{
		Capture:        running,
		Program:        "claude",
		MachineLabel:   "laptop",
		WorkspaceLabel: "agent-lab",
	}})

	result, err := manager.Delete(context.Background(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted || len(result.Holders) != 1 ||
		result.Holders[0].Kind != resourcedeletion.KindRunningCapture {
		t.Fatalf("result = %+v, want refused with one running Capture", result)
	}
	repository.mu.Lock()
	_, live := repository.active[candidate.ID]
	repository.mu.Unlock()
	if !live {
		t.Fatal("a refused delete still removed the Environment")
	}
}

func TestDeletingAnUnreferencedEnvironmentRetiresIt(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	candidate := seedActiveEnvironment(t, repository)
	manager := retiringManager(t, repository, nil)

	result, err := manager.Delete(context.Background(), candidate.ID)
	if err != nil || !result.Deleted {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	repository.mu.Lock()
	_, live := repository.active[candidate.ID]
	repository.mu.Unlock()
	if live {
		t.Fatal("the Environment is still active after a successful delete")
	}
	if _, err := manager.Resolve(candidate.ID); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("Resolve() after delete = %v, want ErrEnvironmentNotFound", err)
	}
}

// Without an inspector the running-Capture holder cannot be consulted. Deleting
// anyway would be the unchecked delete this guard exists to prevent.
func TestDeletingAnEnvironmentFailsClosedWithoutACaptureInspector(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	candidate := seedActiveEnvironment(t, repository)
	manager := mustManager(t, repository, nil)

	if _, err := manager.Delete(
		context.Background(), candidate.ID,
	); !errors.Is(err, ErrInvalidEnvironment) {
		t.Fatalf("Delete() error = %v, want ErrInvalidEnvironment", err)
	}
	repository.mu.Lock()
	_, live := repository.active[candidate.ID]
	repository.mu.Unlock()
	if !live {
		t.Fatal("an unchecked delete removed the Environment")
	}
}
