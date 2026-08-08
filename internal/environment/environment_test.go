package environment

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
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

func TestProviderOriginKeepsCanonicalBasePathAndBoundsCleartext(t *testing.T) {
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
	} {
		origin, err := originidentity.ParseProviderOrigin(test.input)
		if err != nil || origin.String() != test.canonical || origin.BasePath() != test.basePath || origin.Transport() != test.transport {
			t.Fatalf("ParseProviderOrigin(%q) = %+v, %v", test.input, origin, err)
		}
	}
	for _, input := range []string{
		"http://relay.example/v1", "http://localhost/v1", "http://[::ffff:127.0.0.1]/v1",
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
	if err := projection.Restore([]EnvironmentSnapshot{first, second}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []EnvironmentID{"work", "personal"} {
		endpoint, err := projection.ResolveClientOrigin(id, origin)
		if err != nil || endpoint.ClientOrigin() != origin {
			t.Fatalf("resolve %q = %+v, %v", id, endpoint, err)
		}
	}
}

func TestConnectionCompatibilityDistinguishesHotSwitchFromReconnect(t *testing.T) {
	t.Parallel()
	current := mustCompile(t, fixture(t, "work", mustOrigin(t, "https://shared.example")))
	targetAggregate := current.Aggregate()
	targetAggregate.ID = "personal"
	targetAggregate.Name = "Personal"
	target := mustCompile(t, targetAggregate)
	semantic, err := current.BeginConnection(mustOrigin(t, "https://shared.example"))
	if err != nil {
		t.Fatal(err)
	}
	if classification, err := ClassifyConnectionTransition(current, target, semantic); err != nil || classification != CompatibilityHotSwitch {
		t.Fatalf("semantic hot switch = %q, %v", classification, err)
	}
	changed := targetAggregate.Clone()
	changed.ClientEndpoints[0].ProtocolPlans[0].ClientAdapterPolicy.ID = "adapter.replacement"
	changedTarget := mustCompile(t, changed)
	if classification, err := ClassifyConnectionTransition(current, changedTarget, semantic); err != nil || classification != CompatibilityReconnectRequired {
		t.Fatalf("semantic incompatible switch = %q, %v", classification, err)
	}
	blindOrigin := mustOrigin(t, "https://blind.example")
	blind := ConnectionBinding{Mode: ConnectionModeBlind, ClientOrigin: blindOrigin}
	if classification, err := ClassifyConnectionTransition(current, target, blind); err != nil || classification != CompatibilityHotSwitch {
		t.Fatalf("blind hot switch = %q, %v", classification, err)
	}
	interceptingTarget := targetAggregate.Clone()
	interceptingTarget.ClientEndpoints[0].ClientOrigin = blindOrigin
	for planIndex := range interceptingTarget.ClientEndpoints[0].ProtocolPlans {
		interceptingTarget.ClientEndpoints[0].ProtocolPlans[planIndex].UpstreamPlan.Routes[0].ProviderTarget.Origin =
			mustProviderOrigin(t, blindOrigin.String())
	}
	if classification, err := ClassifyConnectionTransition(current, mustCompile(t, interceptingTarget), blind); err != nil || classification != CompatibilityReconnectRequired {
		t.Fatalf("blind-to-semantic switch = %q, %v", classification, err)
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
		if err != nil || plan.ProtocolPlan().ID() != test.wantPlan ||
			plan.Route().ID() != test.wantRoute || plan.Operation().ID().String() != test.wantOp ||
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
	compiler := ambiguousTestCompiler(t)
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
	second := cloneRoute(plan.UpstreamPlan.Routes[0])
	second.ID = "route.anthropic.backup"
	second.Revision = 1
	second.ProviderTarget.ID = "target.anthropic.backup"
	plan.UpstreamPlan.Routes = append(plan.UpstreamPlan.Routes, second)
	plan.UpstreamPlan.RouteSet.CandidateRouteIDs = []UpstreamRouteID{second.ID, plan.UpstreamPlan.DefaultRouteID}
	snapshot := mustCompile(t, value)
	endpoint, ok := snapshot.LookupCompiledClientOrigin(mustOrigin(t, "https://relay.example"))
	if !ok {
		t.Fatal("compiled endpoint is missing")
	}
	routeSet := endpoint.ProtocolPlans()[0].RouteSet()
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
	value.ClientEndpoints[0].ProtocolPlans[0].UpstreamPlan.Routes[0].WireProfileRef =
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

func TestOriginalPassthroughCannotRetargetOrChangeSemantics(t *testing.T) {
	t.Parallel()
	value := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	plan := &value.ClientEndpoints[0].ProtocolPlans[0]
	plan.Mode = PlanModeOriginalPassthrough
	plan.UpstreamPlan.Routes[0].ModelPolicy = ModelPolicy{Revision: 1, Mode: "preserve"}
	if _, err := testCompiler(t, nil).Compile(value); err != nil {
		t.Fatalf("valid original passthrough = %v", err)
	}
	value.ClientEndpoints[0].ProtocolPlans[0].UpstreamPlan.Routes[0].ProviderTarget.Origin =
		mustProviderOrigin(t, "https://other.example")
	if _, err := testCompiler(t, nil).Compile(value); !errors.Is(err, ErrInvalidEnvironment) {
		t.Fatalf("retargeted original passthrough = %v", err)
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
			value.ClientEndpoints[0].ProtocolPlans[1].UpstreamPlan.Routes[0].ID = value.ClientEndpoints[0].ProtocolPlans[0].UpstreamPlan.Routes[0].ID
		}},
		{"zero child revision", func(value *Environment) { value.ClientEndpoints[0].Revision = 0 }},
		{"bad default route", func(value *Environment) {
			value.ClientEndpoints[0].ProtocolPlans[0].UpstreamPlan.DefaultRouteID = "route.missing"
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
	route := &candidate.ClientEndpoints[0].ProtocolPlans[0].UpstreamPlan.Routes[0]
	route.AccountPolicy = RouteAccountPolicy{
		Revision: 1, Mode: AccountModeManaged, AllowedRealmIDs: []string{"realm.anthropic"},
		PreferredAccountID: "account.work", CandidateAccountIDs: []string{"account.work"},
		AccountRevisions: map[string]Revision{"account.work": 2}, FailoverPolicy: FailoverOff,
	}
	catalog := accountCatalog{"account.work": {ID: "account.work", Revision: 2, RealmID: "realm.other", Active: true, BackendProtocols: []string{"anthropic_messages"}}}
	if _, err := testCompiler(t, catalog).Compile(candidate); err == nil {
		t.Fatal("incompatible realm was accepted")
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
	next.ClientEndpoints[0].ProtocolPlans[0].UpstreamPlan.Routes[0].WireProfileRef = "wire.changed"
	if err := ValidateTransition(previous, next); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("unchanged child revisions error = %v", err)
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
	route := firstPlan.UpstreamPlan.Routes[0]
	firstPlan.UpstreamPlan.Routes = secondPlan.UpstreamPlan.Routes
	firstPlan.UpstreamPlan.DefaultRouteID = firstPlan.UpstreamPlan.Routes[0].ID
	firstPlan.UpstreamPlan.RouteSet.CandidateRouteIDs = []UpstreamRouteID{firstPlan.UpstreamPlan.Routes[0].ID}
	secondPlan.UpstreamPlan.Routes = []UpstreamRoute{route}
	secondPlan.UpstreamPlan.DefaultRouteID = route.ID
	secondPlan.UpstreamPlan.RouteSet.CandidateRouteIDs = []UpstreamRouteID{route.ID}
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
	aggregate.ClientEndpoints[0].ProtocolPlans[0].UpstreamPlan.Routes[0].AccountPolicy.AllowedRealmIDs[0] = "mutated"
	copyOne := snapshot.Aggregate()
	copyOne.ClientEndpoints[0].ClientOrigin = mustOrigin(t, "https://mutated.example")
	copyOne.ClientEndpoints[0].ProtocolPlans[0].UpstreamPlan.Routes[0].AccountPolicy.AllowedRealmIDs[0] = "mutated"
	copyTwo := snapshot.Aggregate()
	if copyTwo.ClientEndpoints[0].ClientOrigin.String() != "https://relay.example" ||
		copyTwo.ClientEndpoints[0].ProtocolPlans[0].UpstreamPlan.Routes[0].AccountPolicy.AllowedRealmIDs[0] != "realm.anthropic" {
		t.Fatalf("snapshot was aliased: %+v", copyTwo)
	}
	system := SystemTransparentSnapshot()
	if !system.SystemOwned() || !system.BlindOnly() || len(system.ClientEndpoints()) != 0 {
		t.Fatalf("system snapshot = %+v", system)
	}
	projection := NewAtomicProjection()
	if err := projection.Restore(nil); err != nil {
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
	if err := projection.Restore([]EnvironmentSnapshot{mustCompile(t, first), mustCompile(t, second)}); err != nil {
		t.Fatal(err)
	}
	if err := projection.Restore(nil); !errors.Is(err, ErrProjectionRestored) {
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

func TestDraftPreviewPublishRevalidatesEveryBinding(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	candidate := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	inspector := fixedInspector{references: []CaptureReference{{
		Capture:         captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "run.one"},
		LaunchAuthority: mustLaunchAuthority(t, candidate),
		Bindings:        []ConnectionBinding{semanticCaptureBinding(t, candidate, "https://relay.example")},
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
	if previewOne.Classification != CompatibilityReconnectRequired || previewOne.ReconnectRequiredCount != 1 {
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

func TestImpactPreviewClassifiesIngressCompatibleEditAsHotSwitch(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	active := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	repository.active[active.ID] = active.Clone()
	inspector := fixedInspector{references: []CaptureReference{{
		Capture:         captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "run.one"},
		LaunchAuthority: mustLaunchAuthority(t, active),
		Bindings:        []ConnectionBinding{semanticCaptureBinding(t, active, "https://relay.example")},
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
	if preview.Classification != CompatibilityHotSwitch || preview.HotSwitchCount != 1 || preview.ReconnectRequiredCount != 0 {
		t.Fatalf("impact = %+v", preview)
	}
}

func TestPublishRejectsLaunchAuthorityExpansionBeforeMutation(t *testing.T) {
	t.Parallel()

	repository := newMemoryRepository()
	active := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	repository.active[active.ID] = active.Clone()
	inspector := fixedInspector{references: []CaptureReference{{
		Capture:         captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "run.one"},
		LaunchAuthority: mustLaunchAuthority(t, active),
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
		plan.UpstreamPlan.RouteSet.ID = fmt.Sprintf("routes.added.%d", index)
		route := &plan.UpstreamPlan.Routes[0]
		route.ID = UpstreamRouteID(fmt.Sprintf("route.added.%d", index))
		plan.UpstreamPlan.DefaultRouteID = route.ID
		plan.UpstreamPlan.RouteSet.CandidateRouteIDs = []UpstreamRouteID{route.ID}
		route.ProviderTarget.ID = fmt.Sprintf("target.added.%d", index)
		route.ProviderTarget.Origin = mustProviderOrigin(t, added.ClientOrigin.String())
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
	if preview.Classification != CompatibilityRestartRequired || preview.RestartRequiredCount != 1 {
		t.Fatalf("impact = %+v", preview)
	}
	result, err := manager.Publish(context.Background(), preview)
	if !errors.Is(err, ErrLaunchAuthorityRestartRequired) || result.Outcome != CommitOutcomeNotCommitted {
		t.Fatalf("Publish = %+v, %v", result, err)
	}
	current, err := manager.Get(context.Background(), active.ID)
	if err != nil || current.Revision() != 1 || len(current.ClientEndpoints()) != 1 {
		t.Fatalf("active Environment mutated = %+v, %v", current.Aggregate(), err)
	}
}

func TestImpactPreviewChecksEveryActiveCaptureBinding(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	active := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	second := cloneEndpoint(active.ClientEndpoints[0])
	second.ProtocolPlans = second.ProtocolPlans[:1]
	second.ID = "endpoint.second"
	second.ClientOrigin = mustOrigin(t, "https://second.example")
	second.ProtocolPlans[0].ID = "plan.second"
	second.ProtocolPlans[0].ClientAdapterPolicy.ID = "adapter.second"
	second.ProtocolPlans[0].UpstreamPlan.RouteSet.ID = "routes.second"
	second.ProtocolPlans[0].UpstreamPlan.DefaultRouteID = "route.second"
	second.ProtocolPlans[0].UpstreamPlan.Routes[0].ID = "route.second"
	second.ProtocolPlans[0].UpstreamPlan.RouteSet.CandidateRouteIDs = []UpstreamRouteID{"route.second"}
	second.ProtocolPlans[0].UpstreamPlan.Routes[0].ProviderTarget.ID = "target.second"
	second.ProtocolPlans[0].UpstreamPlan.Routes[0].ProviderTarget.Origin =
		mustProviderOrigin(t, second.ClientOrigin.String())
	active.ClientEndpoints = append(active.ClientEndpoints, second)
	repository.active[active.ID] = active.Clone()
	inspector := fixedInspector{references: []CaptureReference{{
		Capture:         captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "run.one"},
		LaunchAuthority: mustLaunchAuthority(t, active),
		Bindings: []ConnectionBinding{
			semanticCaptureBinding(t, active, "https://relay.example"),
			semanticCaptureBinding(t, active, "https://second.example"),
		},
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
	if preview.Classification != CompatibilityReconnectRequired || preview.ReconnectRequiredCount != 1 || preview.HotSwitchCount != 0 {
		t.Fatalf("multi-binding impact = %+v", preview)
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
	if err := projection.Restore([]EnvironmentSnapshot{mustCompile(t, base)}); err != nil {
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
		return ClientProtocolPlan{
			ID: ClientProtocolPlanID(planID), Revision: 1, ClientProtocol: protocol,
			ClientAdapterPolicy: ClientAdapterPolicy{ID: "adapter." + planID, Revision: 1}, Mode: PlanModeManaged,
			UpstreamPlan: UpstreamPlan{
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
					BackendProtocol: string(protocol), AccountPolicy: RouteAccountPolicy{Revision: 1, Mode: AccountModeClientPassthrough, AllowedRealmIDs: []string{realm}, FailoverPolicy: FailoverOff},
					ModelPolicy:    ModelPolicy{Revision: 1, Mode: "passthrough"},
					WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue}},
			},
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
	snapshot, err := testCompiler(t, nil).Compile(value)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
func mustManager(t *testing.T, repository *memoryRepository, inspector CaptureInspector) *Manager {
	t.Helper()
	manager, err := NewManager(context.Background(), repository, testCompiler(t, nil), NewAtomicProjection(), inspector)
	if err != nil {
		t.Fatal(err)
	}
	return manager
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
	compiler, err := NewCompiler(accounts, protocols, wires)
	if err != nil {
		t.Fatal(err)
	}
	return compiler
}

func ambiguousTestCompiler(t *testing.T) Compiler {
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
	compiler, err := NewCompiler(nil, protocols, wires)
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

func (inspector fixedInspector) AffectedCaptures(_ context.Context, _ EnvironmentID, limit int) ([]CaptureReference, error) {
	if len(inspector.references) > limit {
		return nil, ErrImpactLimitExceeded
	}
	return append([]CaptureReference(nil), inspector.references...), nil
}

func (fixedInspector) PrepareEnvironmentTransition(_ context.Context, _ EnvironmentTransition) (EnvironmentTransitionLease, error) {
	return noOpTransitionLease{}, nil
}

type noOpTransitionLease struct{}

func (noOpTransitionLease) Commit()  {}
func (noOpTransitionLease) Release() {}

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
