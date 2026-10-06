package desktopcontrol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

// Dropping the optional permission field breaks actual draft/publish clients.
func TestEnvironmentClientToolAccessWireRoundTrip(t *testing.T) {
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(),
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(),
		Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(),
		Approvals: runtime.ToolApprovals(), Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(),
		Offline: runtime, Clock: desktopcontrol.SystemClock{}, ManualCaptures: runtime.ManualCaptures(),
	})
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{
		"expectedDraftRevision": 0, "name": "Work", "state": "active",
		"clientEndpoints": []any{}, "pluginBindings": []any{},
		"budgetPolicy":     map[string]any{"id": "", "revision": 0},
		"contentRecording": map[string]any{"mode": "off", "retentionDays": 0},
	}
	base["clientToolAccess"] = map[string]any{"enabled": false}
	body, _ := json.Marshal(base)
	response := environmentRequest(t, application, http.MethodPut, "/api/v1/environments/work/draft", 0, "client-tool-off-draft-001", body)
	if response.Code != http.StatusOK {
		t.Fatalf("OFF draft: %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("clientToolAccess")) {
		t.Fatalf("OFF must canonicalize to omission: %s", response.Body.String())
	}
	for _, action := range []string{"preview", "publish"} {
		result := environmentRequest(t, application, http.MethodPost, "/api/v1/environments/work/draft/actions/"+action, 1, "client-tool-off-"+action+"-001", nil)
		if result.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", action, result.Code, result.Body.String())
		}
	}
	base["clientToolAccess"] = map[string]any{"enabled": true}
	body, _ = json.Marshal(base)
	unsupported := environmentRequest(t, application, http.MethodPut, "/api/v1/environments/work/draft", 1, "client-tool-no-builder-001", body)
	if unsupported.Code != http.StatusUnprocessableEntity || !bytes.Contains(unsupported.Body.Bytes(), []byte("environment_client_tool_access_unsupported")) {
		t.Fatalf("missing registration must explain unsupported: %d %s", unsupported.Code, unsupported.Body.String())
	}
}

func TestEnvironmentClientToolAccessNestedWireRejectsWithoutSaving(t *testing.T) {
	ctx := context.Background()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), ClientToolAccess: runtime,
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(),
		Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), EgressProfiles: runtime.EgressProfiles(),
		Offline: runtime, Clock: desktopcontrol.SystemClock{}, ManualCaptures: runtime.ManualCaptures(),
	})
	if err != nil {
		t.Fatal(err)
	}
	system, err := runtime.Environments().Get(ctx, environment.SystemTransparentID)
	if err != nil {
		t.Fatal(err)
	}
	base := system.Aggregate()
	directChoice, err := egressprofile.Explicit(egressprofile.Direct())
	if err != nil {
		t.Fatal(err)
	}
	// This is a new user environment, not a historical system revision.
	for i := range base.ClientEndpoints {
		base.ClientEndpoints[i].Revision = 1
		for j := range base.ClientEndpoints[i].ProtocolPlans {
			base.ClientEndpoints[i].ProtocolPlans[j].Revision = 1
			// This existing malformed-profile matrix targets the explicit wire.
			base.ClientEndpoints[i].ProtocolPlans[j].EgressProfile = directChoice
		}
	}
	input := desktopcontrol.EnvironmentDraftInput{Name: "Work", State: environment.StateActive, ClientEndpoints: base.ClientEndpoints, PluginBindings: base.PluginBindings, BudgetPolicy: base.BudgetPolicy, ContentRecording: base.ContentRecording, LaunchEnvironment: base.LaunchEnvironment, PolicySet: base.PolicySet, ClientToolAccess: &environment.ClientToolAccessPolicy{Enabled: true}}
	body, marshalErr := json.Marshal(input)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var roundTrip desktopcontrol.EnvironmentDraftInput
	if err := json.Unmarshal(body, &roundTrip); err != nil {
		t.Fatalf("owner input roundtrip: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&roundTrip); err != nil {
		t.Fatalf("strict owner input: %v", err)
	}
	first := environmentRequest(t, application, http.MethodPut, "/api/v1/environments/work/draft", 0, "strict-native-initial-save", body)
	if first.Code != 200 {
		t.Fatalf("prepare: %d %s", first.Code, first.Body.String())
	}
	var prepared desktopcontrol.EnvironmentDraftResponse
	if err := json.Unmarshal(first.Body.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"preview", "publish"} {
		response := environmentRequest(t, application, http.MethodPost, "/api/v1/environments/work/draft/actions/"+action, uint64(prepared.DraftRevision), "strict-native-initial-"+action, nil)
		if response.Code != 200 {
			t.Fatalf("%s: %d %s", action, response.Code, response.Body.String())
		}
	}
	input.ClientToolAccess = prepared.Candidate.ClientToolAccess
	body, _ = json.Marshal(input)
	saved := environmentRequest(t, application, http.MethodPut, "/api/v1/environments/work/draft", 1, "strict-native-trusted-echo", body)
	if saved.Code != 200 {
		t.Fatalf("trusted echo: %d %s", saved.Code, saved.Body.String())
	}
	var existing desktopcontrol.EnvironmentDraftResponse
	if err := json.Unmarshal(saved.Body.Bytes(), &existing); err != nil {
		t.Fatal(err)
	}
	input.ExpectedDraftRevision = existing.DraftRevision
	inputJSON, _ := json.Marshal(input)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(inputJSON, &fields); err != nil {
		t.Fatal(err)
	}
	routeJSON, _ := json.Marshal(prepared.Candidate.ClientToolAccess.Route)
	withRoute := func(route []byte) json.RawMessage {
		return json.RawMessage(`{"enabled":true,"route":` + string(route) + `}`)
	}
	cases := []struct {
		name   string
		policy json.RawMessage
	}{
		{"route alias null", json.RawMessage(`{"enabled":true,"Route":null}`)},
		{"conflicting enabled alias", json.RawMessage(`{"enabled":true,"Enabled":false}`)},
		{"duplicate enabled", json.RawMessage(`{"enabled":false,"enabled":true}`)},
		{"duplicate permission container", json.RawMessage(`{"enabled":true}`)},
		{"duplicate route", json.RawMessage(`{"enabled":true,"route":` + string(routeJSON) + `,"route":` + string(routeJSON) + `}`)},
		{"route field alias", withRoute(bytes.Replace(routeJSON, []byte(`"id":`), []byte(`"ID":`), 1))},
		{"egress field alias", withRoute(bytes.Replace(routeJSON, []byte(`"egressProfile":`), []byte(`"EgressProfile":`), 1))},
		{"egress display name field alias", withRoute(bytes.Replace(routeJSON, []byte(`"displayName":`), []byte(`"DisplayName":`), 1))},
		{"egress policy field alias", withRoute(bytes.Replace(routeJSON, []byte(`"proxy":`), []byte(`"Proxy":`), 1))},
		{"proxy kind field alias", withRoute(bytes.Replace(routeJSON, []byte(`"kind":`), []byte(`"Kind":`), 1))},
		{"resolver transport field alias", withRoute(bytes.Replace(routeJSON, []byte(`"transport":`), []byte(`"Transport":`), 1))},
		{"duplicate proxy kind", withRoute(bytes.Replace(routeJSON, []byte(`"kind":"direct"`), []byte(`"kind":"direct","kind":"direct"`), 1))},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			before := environmentRequest(t, application, http.MethodGet, "/api/v1/environments/work/draft", 0, "", nil)
			var prior desktopcontrol.EnvironmentDraftResponse
			if before.Code != 200 || json.Unmarshal(before.Body.Bytes(), &prior) != nil {
				t.Fatal("saved draft unavailable")
			}
			fields["expectedDraftRevision"], _ = json.Marshal(prior.DraftRevision)
			fields["clientToolAccess"] = test.policy
			requestBody, _ := json.Marshal(fields)
			if test.name == "duplicate permission container" {
				requestBody = bytes.Replace(requestBody, []byte(`"clientToolAccess":{"enabled":true}`), []byte(`"clientToolAccess":{"enabled":true},"clientToolAccess":{"enabled":true}`), 1)
			}
			response := environmentRequest(t, application, http.MethodPut, "/api/v1/environments/work/draft", 1, "strict-native-reject-"+strconv.Itoa(index), requestBody)
			if response.Code != http.StatusUnprocessableEntity {
				t.Errorf("malformed permission accepted: %d", response.Code)
			}
			current := environmentRequest(t, application, http.MethodGet, "/api/v1/environments/work/draft", 0, "", nil)
			if current.Code != 200 || !bytes.Equal(current.Body.Bytes(), before.Body.Bytes()) {
				t.Error("malformed permission changed saved draft")
			}
		})
	}
}

func TestEnvironmentClientToolAccessPreparedCandidateAndFrozenRoute(t *testing.T) {
	ctx := context.Background()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), ClientToolAccess: runtime,
		Assignments: runtime.CaptureAssignments(), Activities: runtime.Activities(), Contents: runtime.ExchangeContents(),
		Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), EgressProfiles: runtime.EgressProfiles(),
		Offline: runtime, Clock: desktopcontrol.SystemClock{}, ManualCaptures: runtime.ManualCaptures(),
	})
	if err != nil {
		t.Fatal(err)
	}
	base, err := runtime.Environments().Get(ctx, environment.SystemTransparentID)
	if err != nil {
		t.Fatal(err)
	}
	aggregate := base.Aggregate()
	for i := range aggregate.ClientEndpoints {
		aggregate.ClientEndpoints[i].Revision = 1
		for j := range aggregate.ClientEndpoints[i].ProtocolPlans {
			aggregate.ClientEndpoints[i].ProtocolPlans[j].Revision = 1
		}
	}
	input := desktopcontrol.EnvironmentDraftInput{Name: "Work", State: environment.StateActive, ClientEndpoints: aggregate.ClientEndpoints, PluginBindings: aggregate.PluginBindings, BudgetPolicy: aggregate.BudgetPolicy, ContentRecording: aggregate.ContentRecording, LaunchEnvironment: aggregate.LaunchEnvironment, PolicySet: aggregate.PolicySet, ClientToolAccess: &environment.ClientToolAccessPolicy{Enabled: true}}
	save := func(baseRevision, draftRevision environment.Revision, key string) desktopcontrol.EnvironmentDraftResponse {
		input.ExpectedDraftRevision = draftRevision
		body, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		var roundTrip desktopcontrol.EnvironmentDraftInput
		if err := json.Unmarshal(body, &roundTrip); err != nil {
			t.Fatalf("owner input roundtrip: %v", err)
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&roundTrip); err != nil {
			t.Fatalf("strict owner input: %v", err)
		}
		response := environmentRequest(t, application, http.MethodPut, "/api/v1/environments/work/draft", uint64(baseRevision), key, body)
		if response.Code != 200 {
			t.Fatalf("save: %d %s", response.Code, response.Body.String())
		}
		var draft desktopcontrol.EnvironmentDraftResponse
		if err := json.Unmarshal(response.Body.Bytes(), &draft); err != nil {
			t.Fatal(err)
		}
		return draft
	}
	publish := func(revision environment.Revision, key string) {
		for _, action := range []string{"preview", "publish"} {
			r := environmentRequest(t, application, http.MethodPost, "/api/v1/environments/work/draft/actions/"+action, uint64(revision), key+action, nil)
			if r.Code != 200 {
				t.Fatalf("%s: %d %s", action, r.Code, r.Body.String())
			}
		}
	}
	first := save(0, 0, "native-on-first-0001")
	publish(first.DraftRevision, "native-on-first-")
	route := *first.Candidate.ClientToolAccess.Route
	if route.TargetOrigin.String() != "https://chatgpt.com" || route.CredentialOwner != "original_client" || route.Revision != 1 {
		t.Fatalf("prepared route %+v", route)
	}
	input.ClientToolAccess = &environment.ClientToolAccessPolicy{Enabled: true, Route: &route}
	unchanged := save(1, 0, "native-on-unchanged-001")
	if *unchanged.Candidate.ClientToolAccess.Route != route {
		t.Fatal("unchanged route identity/revision changed")
	}
	// Saving OFF without publishing cannot revoke or retire the published route.
	input.ClientToolAccess = nil
	offDraft := save(1, unchanged.DraftRevision, "native-off-draft-0001")
	current, _ := runtime.Environments().Get(ctx, "work")
	if current.Aggregate().ClientToolAccess == nil {
		t.Fatal("draft OFF revoked published permission")
	}
	input.ClientToolAccess = &environment.ClientToolAccessPolicy{Enabled: true}
	reenabledDraft := save(1, offDraft.DraftRevision, "native-on-draft-00001")
	if *reenabledDraft.Candidate.ClientToolAccess.Route != route {
		t.Fatal("unpublished OFF retired route")
	}
	// Resolve the complete unsaved matching original-client egress, before compile.
	policy := egressnetwork.DefaultPolicy()
	policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "proxy.synthetic.example:1080"}
	profile, err := runtime.EgressProfiles().Publish(ctx, egressprofile.PublishCommand{ID: "profile.tools", DisplayName: "Tool egress", Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	for ei := range input.ClientEndpoints {
		if input.ClientEndpoints[ei].ClientOrigin.String() == "https://chatgpt.com" {
			input.ClientEndpoints[ei].Revision++
			for pi := range input.ClientEndpoints[ei].ProtocolPlans {
				p := &input.ClientEndpoints[ei].ProtocolPlans[pi]
				p.Revision++
				p.EgressProfile = authoredEgress(t, profile)
			}
		}
	}
	changed := save(1, reenabledDraft.DraftRevision, "native-egress-unsaved-001")
	if changed.Candidate.ClientToolAccess.Route.ID != route.ID || changed.Candidate.ClientToolAccess.Route.Revision != 2 || changed.Candidate.ClientToolAccess.Route.EgressProfile != authoredEgress(t, profile) {
		t.Fatal("unsaved egress was ignored or route identity replaced")
	}
	publish(changed.DraftRevision, "native-egress-")
	// Caller may echo the exact trusted old route; substituted target is refused.
	for index, invalid := range []any{nil, true, map[string]any{"enabled": "true"}, map[string]any{"enabled": false, "route": route}, map[string]any{"enabled": true, "route": map[string]any{"id": "substituted"}}} {
		body, _ := json.Marshal(input)
		var fields map[string]any
		_ = json.Unmarshal(body, &fields)
		fields["clientToolAccess"] = invalid
		fields["expectedDraftRevision"] = 0
		body, _ = json.Marshal(fields)
		r := environmentRequest(t, application, http.MethodPut, "/api/v1/environments/work/draft", 2, "native-invalid-permission-"+strconv.Itoa(index), body)
		if r.Code != 422 {
			t.Fatalf("invalid permission %v: %d %s", invalid, r.Code, r.Body.String())
		}
	}
	body, _ := json.Marshal(input)
	var aliased map[string]any
	_ = json.Unmarshal(body, &aliased)
	delete(aliased, "clientToolAccess")
	aliased["ClientToolAccess"] = map[string]any{"enabled": true}
	aliased["expectedDraftRevision"] = 0
	body, _ = json.Marshal(aliased)
	badAlias := environmentRequest(t, application, http.MethodPut, "/api/v1/environments/work/draft", 2, "native-invalid-permission-alias", body)
	if badAlias.Code != 422 {
		t.Fatalf("permission field alias accepted: %d %s", badAlias.Code, badAlias.Body.String())
	}
	input.ExpectedDraftRevision = 0
	body, _ = json.Marshal(input)
	missingBase := environmentRequest(t, application, http.MethodPut, "/api/v1/environments/work/draft", 99, "native-unpublished-base-001", body)
	if missingBase.Code != http.StatusConflict {
		t.Fatalf("permission preparation hid draft base conflict: %d %s", missingBase.Code, missingBase.Body.String())
	}
	input.ClientToolAccess = nil
	finalOff := save(2, 0, "native-off-publish-001")
	publish(finalOff.DraftRevision, "native-off-")
	current, _ = runtime.Environments().Get(ctx, "work")
	if current.Aggregate().ClientToolAccess != nil {
		t.Fatal("published OFF retained permission")
	}
	input.ClientToolAccess = &environment.ClientToolAccessPolicy{Enabled: true}
	fresh := save(3, 0, "native-fresh-on-0001")
	if fresh.Candidate.ClientToolAccess.Route.ID == route.ID || fresh.Candidate.ClientToolAccess.Route.Revision != 1 {
		t.Fatal("published OFF resurrected prior route authority")
	}
	// Real managed-account creation and draft resolution do not replace native
	// tool login. Both secrets remain in the synthetic test credential store.
	for _, id := range []string{"account.native.one", "account.native.two"} {
		created := environmentRequest(t, application, http.MethodPost, "/api/v1/provider-accounts", 0, "native-create-"+id, []byte(`{"id":"`+id+`","displayName":"`+id+`","upstreamEndpointId":"target.codex.official","kind":"bearer_token","secret":"synthetic-test-only-secret"}`))
		if created.Code != http.StatusCreated {
			t.Fatalf("synthetic account: %d %s", created.Code, created.Body.String())
		}
	}
	target, err := runtime.UpstreamEndpoints().Get(ctx, upstreamendpoint.ChatGPTOfficialID)
	if err != nil {
		t.Fatal(err)
	}
	matching := -1
	for ei := range input.ClientEndpoints {
		if input.ClientEndpoints[ei].ClientOrigin.String() == "https://chatgpt.com" {
			matching = ei
		}
	}
	if matching < 0 {
		t.Fatal("no matching original client")
	}
	input.ClientEndpoints[matching].Revision++
	p := &input.ClientEndpoints[matching].ProtocolPlans[0]
	p.Revision++
	managedRoute := environment.UpstreamRoute{ID: "route.native.model", Revision: 1, ProviderTarget: environment.ProviderTarget{ID: target.ID.String(), Revision: environment.Revision(target.Revision), Origin: target.Origin, RealmID: target.RealmID, Capabilities: target.Capabilities}, BackendProtocol: "openai_responses", AccountPolicy: environment.RouteAccountPolicy{Revision: 1, Mode: environment.AccountSelectionFixed, FixedAccountID: "account.native.one", Accounts: []environment.RouteAccountReference{{ID: "account.native.one", Revision: 1, DisplayName: "account.native.one"}}}, ModelPolicy: environment.ModelPolicy{Revision: 1, Mode: environment.ModelModePassthrough}, WireProfileRef: "follow-client"}
	p.Destination = environment.DestinationPlan{Kind: environment.DestinationKindUpstream, Upstream: &environment.UpstreamPlan{Routes: []environment.UpstreamRoute{managedRoute}, DefaultRouteID: managedRoute.ID, RouteSet: environment.RouteSet{ID: "routes.native.model", Revision: 1, CandidateRouteIDs: []environment.UpstreamRouteID{managedRoute.ID}}}}
	managed := save(3, fresh.DraftRevision, "native-model-account-one")
	p.Destination.Upstream.Routes[0].AccountPolicy.FixedAccountID = "account.native.two"
	p.Destination.Upstream.Routes[0].AccountPolicy.Accounts = []environment.RouteAccountReference{{ID: "account.native.two", Revision: 1, DisplayName: "account.native.two"}}
	otherAccount := save(3, managed.DraftRevision, "native-model-account-two")
	if managed.Candidate.ClientToolAccess.Route.CredentialOwner != "original_client" || otherAccount.Candidate.ClientToolAccess.Route.CredentialOwner != "original_client" || otherAccount.Candidate.ClientToolAccess.Route.TargetOrigin.String() != "https://chatgpt.com" || otherAccount.Candidate.ClientToolAccess.Route.EgressProfile != authoredEgress(t, profile) {
		t.Fatal("model account altered native owner/egress")
	}
	historical := environmentRequest(t, application, http.MethodGet, "/api/v1/environments/work/revisions/1", 0, "", nil)
	var frozen desktopcontrol.EnvironmentResponse
	_ = json.Unmarshal(historical.Body.Bytes(), &frozen)
	if historical.Code != 200 || *frozen.ClientToolAccess.Route != route {
		t.Fatal("frozen permission route mutated")
	}
	system, _ := runtime.Environments().Get(ctx, environment.SystemTransparentID)
	if system.Aggregate().ClientToolAccess != nil {
		t.Fatal("sibling permission changed")
	}
}
