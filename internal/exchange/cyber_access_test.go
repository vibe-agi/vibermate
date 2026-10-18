package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/cyberaccess"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

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
	catalog cyberaccess.Catalog
	err     error
	queries []providertransport.Request
}

func (p *cyberModeProvider) AccountCyberPrograms(_ context.Context, r providertransport.Request) (cyberaccess.Catalog, error) {
	p.queries = append(p.queries, r)
	return p.catalog, p.err
}

// The directory lookup and preference must follow the selected account and the
// final upstream model after the route applies its model mapping.
func TestAccountCyberModeUsesSelectedAccountAndFinalModel(t *testing.T) {
	for _, test := range []struct {
		name                              string
		mode                              cyberaccess.Preference
		supported, transform, unavailable bool
		origin, current, want             string
	}{
		{name: "Blue overrides standard", mode: cyberaccess.Blue, supported: true, current: "standard", want: "daybreak_blue"},
		{name: "Standard overrides client Blue", current: "daybreak_blue", want: "standard"},
		{name: "Red overrides client Blue", mode: cyberaccess.Red, supported: true, current: "daybreak_blue", want: "daybreak_red"},
		{name: "model unsupported fails", mode: cyberaccess.Blue, current: "standard"},
		{name: "unknown capability fails", mode: cyberaccess.Blue, unavailable: true, current: "standard"},
		{name: "mapped model", mode: cyberaccess.Blue, supported: true, transform: true, current: "standard", want: "daybreak_blue"},
		{name: "other upstream", mode: cyberaccess.Blue, supported: true, origin: "https://api.openai.com", current: "standard", want: "standard"},
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
			if test.supported {
				catalog[model] = []string{"standard", test.mode.WireValue()}
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
				if ReasonOf(err) != ReasonCode("cyber_access_program_unavailable") {
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
