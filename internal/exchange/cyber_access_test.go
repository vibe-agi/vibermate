package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/vibe-agi/vibermate/internal/cyberaccess"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

func TestCyberDiscoveryFailureRecoversOnNextRequestWithoutChangingAccount(t *testing.T) {
	for _, failure := range []struct {
		name  string
		cause error
	}{
		{"transport", errors.New("synthetic private network detail")},
		{"discovery deadline", context.DeadlineExceeded},
		{"invalid catalog", cyberaccess.ErrInvalidCatalog},
	} {
		t.Run(failure.name, func(t *testing.T) {
			account := testAccount{id: "account.selected", revision: 3, epoch: 7}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
				destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com",
				backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
				accounts: []testAccount{account}, preferred: account.id,
			})
			provider := &cyberModeProvider{providerDouble: &providerDouble{results: []providerResult{{response: jsonResponse(http.StatusOK, completeResponsesProviderResponse("client-model"))}}}, err: failure.cause}
			pipeline := newTestPipeline(t, cyberModeAuthority{newAccountAuthority(t, account), cyberaccess.Blue}, provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			body := []byte(`{"model":"client-model","stream":false,"input":"hi"}`)
			_, err := pipeline.Execute(context.Background(), mustClientRequest(t, "capability-first", plan, body), &downstreamRecorder{})
			if ReasonOf(err) != ReasonCode("cyber_access_discovery_unavailable") || !errors.Is(err, failure.cause) || len(provider.requestsSnapshot()) != 0 || len(provider.queries) != 1 {
				t.Fatalf("discovery failure classification/send boundary: reason=%s, model requests=%d, reads=%d, err=%v", ReasonOf(err), len(provider.requestsSnapshot()), len(provider.queries), err)
			}
			provider.err = nil
			provider.catalog = cyberaccess.Catalog{"client-model": {"standard", "daybreak_blue"}}
			result, err := pipeline.Execute(context.Background(), mustClientRequest(t, "capability-recovered", plan, body), &downstreamRecorder{})
			requests := provider.requestsSnapshot()
			if err != nil || result.Outcome != AttemptSucceeded || len(requests) != 1 || len(provider.queries) != 2 {
				t.Fatalf("same-scope recovery did not resume explicitly: result=%+v, sends=%d, reads=%d, err=%v", result, len(requests), len(provider.queries), err)
			}
			actual, ok := requests[0].AccountRef()
			if !ok || actual.ID != account.id || actual.Revision != uint64(account.revision) || actual.CredentialEpoch != account.epoch || actual.CyberAccessProgram != cyberaccess.Blue {
				t.Fatal("recovery changed the frozen account or saved mode")
			}
			var wire struct {
				Access struct {
					Cyber string `json:"cyber"`
				} `json:"access_programs"`
			}
			if json.Unmarshal(requests[0].Body(), &wire) != nil || wire.Access.Cyber != "daybreak_blue" {
				t.Fatal("recovery silently changed the selected mode")
			}
		})
	}
}

func TestCyberDiscoveryPreservesParentCancellation(t *testing.T) {
	account := testAccount{id: "account.selected", revision: 3, epoch: 7}
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
		destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com",
		backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
		accounts: []testAccount{account}, preferred: account.id,
	})
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &cyberModeProvider{providerDouble: &providerDouble{}, discover: func(ctx context.Context) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}}
	pipeline := newTestPipeline(t, cyberModeAuthority{newAccountAuthority(t, account), cyberaccess.Blue}, provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	result, err := pipeline.Execute(parent, mustClientRequest(t, "capability-cancelled", plan, []byte(`{"model":"client-model","input":"hi"}`)), &downstreamRecorder{})
	if ReasonOf(err) != ReasonExchangeCanceled || !errors.Is(err, context.Canceled) || result.Outcome != AttemptCanceled || len(provider.requestsSnapshot()) != 0 {
		t.Fatalf("parent cancellation changed to discovery failure: reason=%s outcome=%s err=%v", ReasonOf(err), result.Outcome, err)
	}
}

type cyberModeAuthority struct {
	AccountLeaseAuthority
	mode cyberaccess.Preference
}

func (a cyberModeAuthority) Acquire(ctx context.Context, request AccountLeaseRequest) (providerauth.Lease, error) {
	lease, err := a.AccountLeaseAuthority.Acquire(ctx, request)
	if err != nil {
		return nil, err
	}
	return cyberModeLease{Lease: lease, mode: a.mode}, nil
}

type cyberModeLease struct {
	providerauth.Lease
	mode cyberaccess.Preference
}

func (l cyberModeLease) Account() (providerauth.AccountRef, bool) {
	account, ok := l.Lease.Account()
	account.CyberAccessProgram = l.mode
	return account, ok
}

type cyberModeProvider struct {
	*providerDouble
	catalog  cyberaccess.Catalog
	err      error
	queries  []providertransport.Request
	discover func(context.Context) error
}

func (p *cyberModeProvider) AccountCyberPrograms(ctx context.Context, r providertransport.Request) (cyberaccess.Catalog, error) {
	p.queries = append(p.queries, r)
	if p.discover != nil {
		return nil, p.discover(ctx)
	}
	return p.catalog, p.err
}

// The directory lookup and preference must follow the selected account and the
// final upstream model after the route applies its model mapping.
func TestAccountCyberModeUsesSelectedAccountAndFinalModel(t *testing.T) {
	for _, test := range []struct {
		name                                 string
		mode                                 cyberaccess.Preference
		transform, unavailable, incomingBlue bool
		programs                             []string
		origin, current, want                string
	}{
		{name: "Blue overrides standard", mode: cyberaccess.Blue, programs: []string{"standard", "daybreak_blue"}, current: "standard", want: "daybreak_blue"},
		{name: "Standard overrides client Blue", current: "daybreak_blue", want: "standard"},
		{name: "Red overrides client Blue", mode: cyberaccess.Red, programs: []string{"standard", "daybreak_red"}, current: "daybreak_blue", want: "daybreak_red"},
		{name: "model unsupported fails", mode: cyberaccess.Blue, current: "standard"},
		{name: "unknown capability fails", mode: cyberaccess.Blue, unavailable: true, current: "standard"},
		{name: "mapped model", mode: cyberaccess.Blue, programs: []string{"standard", "daybreak_blue"}, transform: true, current: "standard", want: "daybreak_blue"},
		{name: "Blue Standard fallback overrides client Red", mode: cyberaccess.Blue, programs: []string{"standard"}, current: "daybreak_red", want: "standard"},
		{name: "Red Standard fallback overrides client Blue", mode: cyberaccess.Red, programs: []string{"standard"}, current: "daybreak_blue", want: "standard"},
		{name: "mapped Standard-only model ignores incoming Blue support", mode: cyberaccess.Blue, transform: true, incomingBlue: true, programs: []string{"standard"}, current: "daybreak_blue", want: "standard"},
		{name: "other upstream", mode: cyberaccess.Blue, programs: []string{"standard", "daybreak_blue"}, origin: "https://custom.example", current: "standard", want: "standard"},
	} {
		t.Run(test.name, func(t *testing.T) {
			origin := test.origin
			if origin == "" {
				origin = "https://chatgpt.com"
			}
			modelMode := environment.ModelModePassthrough
			var mappings []environment.ModelMapping
			if test.transform {
				modelMode = environment.ModelModeMap
				mappings = []environment.ModelMapping{{RequestedModel: "client-model", UpstreamModel: "mapped-final"}}
			}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true, destination: environment.DestinationKindUpstream,
				providerOrigin: origin, backend: protocolspec.DialectOpenAIResponses, modelMode: modelMode, modelMappings: mappings,
				accounts: []testAccount{{id: "account.selected", revision: 3, epoch: 7}}, preferred: "account.selected",
			})
			catalog := cyberaccess.Catalog{}
			model := "client-model"
			if test.transform {
				model = "mapped-final"
			}
			if test.programs != nil {
				catalog[model] = test.programs
			}
			if test.incomingBlue {
				catalog["client-model"] = []string{"daybreak_blue"}
			}
			provider := &cyberModeProvider{providerDouble: &providerDouble{results: []providerResult{{response: jsonResponse(http.StatusOK, completeResponsesProviderResponse(model))}}}, catalog: catalog}
			if test.unavailable {
				provider.err = errors.New("directory unavailable")
			}
			pipeline := newTestPipeline(t, cyberModeAuthority{newAccountAuthority(t, testAccount{id: "account.selected", revision: 3, epoch: 7}), test.mode}, provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			body := []byte(`{"model":"client-model","stream":false,"input":"hi","access_programs":{"cyber":"` + test.current + `"}}`)
			result, err := pipeline.Execute(context.Background(), mustClientRequest(t, "exchange-blue", plan, body), &downstreamRecorder{})
			if test.want == "" {
				wantReason := ReasonCyberAccessProgramUnavailable
				if test.unavailable {
					wantReason = ReasonCyberDiscoveryUnavailable
				}
				if ReasonOf(err) != wantReason {
					t.Fatalf("want explicit mode unavailable, got %v", err)
				}
				if len(provider.requestsSnapshot()) != 0 {
					t.Fatal("incompatible mode reached model transport")
				}
				return
			}
			if err != nil || result.Outcome != AttemptSucceeded {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			requests := provider.requestsSnapshot()
			if len(requests) != 1 {
				t.Fatalf("requests=%d", len(requests))
			}
			selected, ok := requests[0].AccountRef()
			if !ok || selected.ID != "account.selected" || selected.CredentialEpoch != 7 || selected.CyberAccessProgram != test.mode {
				t.Fatal("changed frozen account authority")
			}
			var wire struct {
				Access struct {
					Cyber string `json:"cyber"`
				} `json:"access_programs"`
				Model string `json:"model"`
			}
			if json.Unmarshal(requests[0].Body(), &wire) != nil || wire.Access.Cyber != test.want || wire.Model != model {
				t.Fatalf("upstream body=%s", requests[0].Body())
			}
			wantQueries := 0
			if test.mode != cyberaccess.Standard && test.origin == "" {
				wantQueries = 1
			}
			if len(provider.queries) != wantQueries {
				t.Fatalf("capability queries=%d, want %d", len(provider.queries), wantQueries)
			}
			for _, q := range provider.queries {
				account, ok := q.AccountRef()
				if !ok || account.ID != "account.selected" || account.CredentialEpoch != 7 {
					t.Fatal("queried a different account")
				}
			}
		})
	}
}

// Guardian's mode is derived from the selected account, not from the client's
// program choice. This exercises the real request pipeline and inspects the
// bytes admitted to the model transport after its account capability read.
func TestAccountCyberModeDerivesGuardianProgramFromSelectedAccount(t *testing.T) {
	testAccountGuardianMode(t, "https://chatgpt.com", true)
}
func TestOfficialAPIAccountCyberModeDerivesGuardianProgramFromSelectedAccount(t *testing.T) {
	testAccountGuardianMode(t, "https://api.openai.com/v1", false)
}
func testAccountGuardianMode(t *testing.T, origin string, chatGPTClient bool) {
	for _, test := range []struct {
		name         string
		mode         cyberaccess.Preference
		headers      http.Header
		metadata     json.RawMessage
		current      string
		model        string
		programs     []string
		otherModel   bool
		directoryErr bool
		withoutTier  bool
		want         string
		wantFailure  ReasonCode
	}{
		{name: "parent Red stays Red", mode: cyberaccess.Red, current: "daybreak_blue", programs: []string{"daybreak_red"}, want: "daybreak_red"},
		{name: "legacy Guardian derives Blue from Red", mode: cyberaccess.Red, headers: http.Header{"X-Openai-Subagent": {"guardian"}}, metadata: json.RawMessage(`{"x-openai-subagent":"guardian","parent_turn_id":"parent"}`), current: "standard", programs: []string{"daybreak_blue"}, want: "daybreak_blue"},
		{name: "Guardian header alone", mode: cyberaccess.Red, headers: http.Header{"X-Openai-Subagent": {"guardian"}}, current: "daybreak_red", programs: []string{"daybreak_blue"}, want: "daybreak_blue"},
		{name: "Guardian metadata alone", mode: cyberaccess.Red, metadata: json.RawMessage(`{"x-openai-subagent":"guardian"}`), current: "daybreak_red", programs: []string{"daybreak_blue"}, want: "daybreak_blue"},
		{name: "reviewer role header", mode: cyberaccess.Red, headers: http.Header{"X-Codex-Guardian": {"reviewer"}}, current: "daybreak_red", programs: []string{"daybreak_blue"}, want: "daybreak_blue"},
		{name: "Guardian does not invent a service tier", mode: cyberaccess.Red, headers: http.Header{"X-Codex-Guardian": {"reviewer"}}, current: "daybreak_red", programs: []string{"daybreak_blue"}, withoutTier: true, want: "daybreak_blue"},
		{name: "classifier role header", mode: cyberaccess.Red, headers: http.Header{"X-Codex-Guardian": {"classifier"}, "X-Openai-Subagent": {"guardian"}}, metadata: json.RawMessage(`{"x-openai-subagent":"guardian"}`), current: "standard", programs: []string{"daybreak_blue"}, want: "daybreak_blue"},
		{name: "Blue account ignores client Red", mode: cyberaccess.Blue, headers: http.Header{"X-Openai-Subagent": {"guardian"}}, current: "daybreak_red", programs: []string{"daybreak_blue"}, want: "daybreak_blue"},
		{name: "Standard account ignores client Blue", mode: cyberaccess.Standard, headers: http.Header{"X-Openai-Subagent": {"guardian"}}, current: "daybreak_blue", want: "standard"},
		{name: "Guardian derived Blue falls back to Standard", mode: cyberaccess.Red, headers: http.Header{"X-Openai-Subagent": {"guardian"}}, current: "daybreak_red", programs: []string{"standard"}, want: "standard"},
		{name: "Guardian derived Blue chooses Standard over Red", mode: cyberaccess.Red, headers: http.Header{"X-Codex-Guardian": {"reviewer"}}, current: "daybreak_red", programs: []string{"standard", "daybreak_red"}, want: "standard"},
		{name: "Guardian still requires Blue support", mode: cyberaccess.Red, headers: http.Header{"X-Openai-Subagent": {"guardian"}}, current: "daybreak_blue", programs: []string{"daybreak_red"}, wantFailure: ReasonCyberAccessProgramUnavailable},
		{name: "different model cannot authorize Guardian", mode: cyberaccess.Red, headers: http.Header{"X-Openai-Subagent": {"guardian"}}, current: "daybreak_blue", programs: []string{"daybreak_blue"}, otherModel: true, wantFailure: ReasonCyberAccessProgramUnavailable},
		{name: "unadvertised internal alias remains unavailable", mode: cyberaccess.Red, headers: http.Header{"X-Openai-Subagent": {"guardian"}}, current: "daybreak_blue", model: "codex-auto-review", wantFailure: ReasonCyberAccessProgramUnavailable},
		{name: "advertised internal alias uses exact evidence", mode: cyberaccess.Red, headers: http.Header{"X-Openai-Subagent": {"guardian"}}, current: "daybreak_red", model: "codex-auto-review", programs: []string{"daybreak_blue"}, want: "daybreak_blue"},
		{name: "Guardian directory failure stays unavailable", mode: cyberaccess.Red, headers: http.Header{"X-Openai-Subagent": {"guardian"}}, current: "daybreak_blue", directoryErr: true, wantFailure: ReasonCyberDiscoveryUnavailable},
		{name: "unrelated subagent stays Red", mode: cyberaccess.Red, headers: http.Header{"X-Openai-Subagent": {"worker"}}, metadata: json.RawMessage(`{"x-openai-subagent":"worker"}`), current: "daybreak_blue", programs: []string{"daybreak_red"}, want: "daybreak_red"},
		{name: "substring is not Guardian", mode: cyberaccess.Red, headers: http.Header{"X-Openai-Subagent": {"guardian-worker"}}, current: "daybreak_blue", programs: []string{"daybreak_red"}, want: "daybreak_red"},
		{name: "role value is case sensitive", mode: cyberaccess.Red, headers: http.Header{"X-Openai-Subagent": {"Guardian"}}, current: "daybreak_blue", programs: []string{"daybreak_red"}, want: "daybreak_red"},
		{name: "hop role marker is not resurrected", mode: cyberaccess.Red, headers: http.Header{"X-Codex-Guardian": {"reviewer"}, "Connection": {"x-codex-guardian"}}, current: "daybreak_blue", programs: []string{"daybreak_red"}, want: "daybreak_red"},
		{name: "conflicting header and metadata fail", mode: cyberaccess.Red, headers: http.Header{"X-Openai-Subagent": {"guardian"}}, metadata: json.RawMessage(`{"x-openai-subagent":"worker"}`), current: "daybreak_blue", programs: []string{"daybreak_blue", "daybreak_red"}, wantFailure: ReasonProviderRequestInvalid},
		{name: "conflicting reviewer and subagent fail", mode: cyberaccess.Red, headers: http.Header{"X-Codex-Guardian": {"reviewer"}, "X-Openai-Subagent": {"worker"}}, current: "daybreak_blue", programs: []string{"daybreak_blue", "daybreak_red"}, wantFailure: ReasonProviderRequestInvalid},
		{name: "duplicate subagent headers fail", mode: cyberaccess.Red, headers: http.Header{"X-Openai-Subagent": {"guardian", "guardian"}}, current: "daybreak_blue", programs: []string{"daybreak_blue", "daybreak_red"}, wantFailure: ReasonProviderRequestInvalid},
		{name: "duplicate role headers cannot hide behind legacy metadata", mode: cyberaccess.Red, headers: http.Header{"X-Codex-Guardian": {"reviewer", "classifier"}}, metadata: json.RawMessage(`{"x-openai-subagent":"guardian"}`), current: "daybreak_blue", programs: []string{"daybreak_blue", "daybreak_red"}, wantFailure: ReasonProviderRequestInvalid},
		{name: "unknown role cannot hide behind legacy metadata", mode: cyberaccess.Red, headers: http.Header{"X-Codex-Guardian": {"other"}}, metadata: json.RawMessage(`{"x-openai-subagent":"guardian"}`), current: "daybreak_blue", programs: []string{"daybreak_blue", "daybreak_red"}, wantFailure: ReasonProviderRequestInvalid},
		{name: "standalone null subagent metadata fails", mode: cyberaccess.Red, metadata: json.RawMessage(`{"x-openai-subagent":null}`), current: "daybreak_blue", programs: []string{"daybreak_blue", "daybreak_red"}, wantFailure: ReasonProviderRequestInvalid},
		{name: "ordinary metadata string stays Red", mode: cyberaccess.Red, metadata: json.RawMessage(`{"x-openai-subagent":"worker"}`), current: "daybreak_blue", programs: []string{"daybreak_red"}, want: "daybreak_red"},
		{name: "nonstring Guardian metadata fails", mode: cyberaccess.Red, headers: http.Header{"X-Openai-Subagent": {"guardian"}}, metadata: json.RawMessage(`{"x-openai-subagent":null}`), current: "daybreak_blue", programs: []string{"daybreak_blue", "daybreak_red"}, wantFailure: ReasonProviderRequestInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := test.model
			if model == "" {
				model = "review-model"
			}
			account := testAccount{id: "account.selected", revision: 3, epoch: 7}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: chatGPTClient,
				destination: environment.DestinationKindUpstream, providerOrigin: origin,
				backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
				accounts: []testAccount{account}, preferred: account.id,
			})
			catalog := cyberaccess.Catalog{}
			if test.programs != nil {
				catalogModel := model
				if test.otherModel {
					catalogModel = "different-model"
				}
				catalog[catalogModel] = test.programs
			}
			provider := &cyberModeProvider{
				providerDouble: &providerDouble{results: []providerResult{{response: jsonResponse(http.StatusOK, completeResponsesProviderResponse(model))}}},
				catalog:        catalog,
			}
			if test.directoryErr {
				provider.err = errors.New("directory unavailable")
			}
			pipeline := newTestPipeline(t, cyberModeAuthority{newAccountAuthority(t, account), test.mode}, provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			body := map[string]any{
				"model": model, "stream": false, "input": "review", "store": false,
				"service_tier": "priority", "reasoning": map[string]any{"effort": "low"},
				"access_programs": map[string]any{"cyber": test.current, "other": map[string]any{"keep": true}},
			}
			if test.withoutTier {
				delete(body, "service_tier")
			}
			if test.metadata != nil {
				body["client_metadata"] = test.metadata
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			result, err := pipeline.Execute(context.Background(), mustClientRequestWithOptions(t, "guardian-account-mode", plan, encoded, WithOriginalHeaders(test.headers)), &downstreamRecorder{})
			requests := provider.requestsSnapshot()
			if len(requests) > 0 && origin == "https://api.openai.com/v1" && (requests[0].Target().Origin().String() != origin || requests[0].RelativePath() != "responses") {
				t.Fatal("API inference destination changed")
			}

			if test.wantFailure != "" {
				if ReasonOf(err) != test.wantFailure || len(requests) != 0 {
					t.Fatalf("failure=%s, want %s; model requests=%d; err=%v", ReasonOf(err), test.wantFailure, len(requests), err)
				}
				if test.wantFailure == ReasonProviderRequestInvalid && len(provider.queries) != 0 {
					t.Fatalf("malformed metadata read capabilities: %d", len(provider.queries))
				}
				return
			}
			if err != nil || result.Outcome != AttemptSucceeded || len(requests) != 1 {
				t.Fatalf("result=%+v model requests=%d err=%v", result, len(requests), err)
			}
			var got, want map[string]any
			if json.Unmarshal(requests[0].Body(), &got) != nil || json.Unmarshal(encoded, &want) != nil {
				t.Fatal("request is not JSON")
			}
			want["access_programs"].(map[string]any)["cyber"] = test.want
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("request changed beyond the account-derived program: got=%s want=%v", requests[0].Body(), want)
			}
			wantQueries := 1
			if test.mode == cyberaccess.Standard {
				wantQueries = 0
			}
			if len(provider.queries) != wantQueries {
				t.Fatalf("capability queries=%d, want %d", len(provider.queries), wantQueries)
			}
			for _, query := range provider.queries {
				owner, ok := query.AccountRef()
				if !ok || owner.ID != account.id || owner.SettingsRevision != 1 || owner.CredentialEpoch != 7 || owner.CyberAccessProgram != test.mode {
					t.Fatal("Guardian derivation changed the frozen account authority")
				}
			}
		})
	}
}
