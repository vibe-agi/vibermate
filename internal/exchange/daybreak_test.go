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

type daybreakAuthority struct {
	AccountLeaseAuthority
	enabled bool
}

func (a daybreakAuthority) Acquire(ctx context.Context, request AccountLeaseRequest) (providerauth.Lease, error) {
	lease, err := a.AccountLeaseAuthority.Acquire(ctx, request)
	if err != nil {
		return nil, err
	}
	return daybreakLease{Lease: lease, enabled: a.enabled}, nil
}

type daybreakLease struct {
	providerauth.Lease
	enabled bool
}

func (l daybreakLease) Account() (providerauth.AccountRef, bool) {
	account, ok := l.Lease.Account()
	account.DaybreakBlueEnabled = l.enabled
	return account, ok
}

type daybreakProvider struct {
	*providerDouble
	catalog cyberaccess.Catalog
	err     error
	queries []providertransport.Request
}

func (p *daybreakProvider) AccountCyberPrograms(_ context.Context, r providertransport.Request) (cyberaccess.Catalog, error) {
	p.queries = append(p.queries, r)
	return p.catalog, p.err
}

// The directory lookup and preference must follow the selected account and the
// final upstream model after the route applies its model mapping.
func TestDaybreakPreferenceUsesSelectedAccountAndFinalModel(t *testing.T) {
	for _, test := range []struct {
		name                                       string
		enabled, supported, transform, unavailable bool
		origin, want                               string
	}{
		{name: "manual on", enabled: true, supported: true, want: "daybreak_blue"},
		{name: "off follows client", supported: true, want: "standard"},
		{name: "model unsupported", enabled: true, want: "standard"},
		{name: "unknown capability", enabled: true, unavailable: true, want: "standard"},
		{name: "mapped model", enabled: true, supported: true, transform: true, want: "daybreak_blue"},
		{name: "other upstream", enabled: true, supported: true, origin: "https://api.openai.com", want: "standard"},
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
				catalog[model] = []string{"standard", "daybreak_blue"}
			}
			provider := &daybreakProvider{providerDouble: &providerDouble{results: []providerResult{{response: jsonResponse(http.StatusOK, completeResponsesProviderResponse(model))}}}, catalog: catalog}
			if test.unavailable {
				provider.err = errors.New("directory unavailable")
			}
			pipeline := newTestPipeline(t, daybreakAuthority{newAccountAuthority(t, testAccount{id: "account.selected", revision: 3, epoch: 7}), test.enabled}, provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			body := []byte(`{"model":"client-model","stream":false,"input":"hi","access_programs":{"cyber":"standard"}}`)
			result, err := pipeline.Execute(context.Background(), mustClientRequest(t, "exchange-blue", plan, body), &downstreamRecorder{})
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
			if test.enabled && test.origin == "" {
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
