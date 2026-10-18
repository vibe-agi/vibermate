package exchange

import (
	"context"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

func TestNativeModelCatalogHeaderUsesSameScopeAsDirectory(t *testing.T) {
	for _, original := range []bool{false, true} {
		name := "managed"
		if original {
			name = "original-account"
		}
		t.Run(name, func(t *testing.T) {
			account := testAccount{id: "account.selected", revision: 3, epoch: 7}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true, originalAccount: original,
				destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com", backend: protocolspec.DialectOpenAIResponses,
				modelMode: environment.ModelModePassthrough, accounts: []testAccount{account}, preferred: account.id})
			response := jsonResponse(http.StatusOK, completeResponsesProviderResponse("codex-client-alias"))
			response.Header.Set("X-Models-Etag", `"catalog-global"`)
			provider := &providerDouble{results: []providerResult{{response: response}}}
			pipeline := newTestPipeline(t, newAccountAuthority(t, account), provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			downstream := &downstreamRecorder{}
			request := mustClientRequestWithOptions(t, "catalog-tag", plan, []byte(`{"model":"codex-client-alias","stream":false,"input":"hi"}`), WithOriginalHeaders(http.Header{"Authorization": {"Bearer original"}}))
			if _, err := pipeline.Execute(context.Background(), request, downstream); err != nil {
				t.Fatal(err)
			}
			want := `"catalog-global"`
			if !original {
				frozen, ok := provider.requestsSnapshot()[0].AccountRef()
				if !ok {
					t.Fatal("missing managed account")
				}
				route, _ := plan.UpstreamRoute()
				target := route.ProviderTarget()
				want = upstreamservice.ModelCatalogETag(want, upstreamservice.ModelCatalogScope{EnvironmentID: plan.EnvironmentID().String(), EnvironmentRevision: uint64(plan.EnvironmentRevision()), RouteID: route.ID().String(), RouteRevision: uint64(route.Revision()), EndpointID: target.ID, EndpointRevision: uint64(target.Revision), Origin: target.Origin.String(), Account: frozen})
				if want == "" {
					t.Fatal("missing expected managed tag")
				}
			}
			if got := downstream.envelopesSnapshot()[0].Headers().Get("X-Models-Etag"); got != want {
				t.Fatalf("catalog refresh header=%q want=%q", got, want)
			}
			if response.Header.Get("X-Models-Etag") != `"catalog-global"` {
				t.Fatal("raw upstream metadata was changed")
			}
		})
	}
}
