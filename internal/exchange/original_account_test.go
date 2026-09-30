package exchange

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
)

func TestOriginalAccountPreservesCredentialsWithoutDiscardingRoutePolicies(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://api.anthropic.com", backend: protocolspec.DialectAnthropicMessages,
		originalAccount: true, modelMode: environment.ModelModeMap, mappedModel: "mapped-model",
	})
	provider := &providerDouble{results: []providerResult{{response: jsonResponse(http.StatusOK, []byte(`{
		"id":"msg_original_account","type":"message","role":"assistant","model":"mapped-model",
		"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",
		"usage":{"input_tokens":4,"output_tokens":2}
	}`))}}}
	// No managed account authority is installed: a lease would fail this run.
	pipeline := newTestPipeline(t, nil, provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	client := mustClientRequestWithOptions(t,
		"exchange-original-account", plan, completeClientRequest(), WithOriginalHeaders(http.Header{
			"X-Api-Key": {"synthetic-client-account"}, "Authorization": {"Bearer synthetic-client"},
			"Cookie": {"session=synthetic"}, "Proxy-Authorization": {"must-not-leave"},
		}))
	preview, err := pipeline.DryRun(context.Background(), client)
	if err != nil || preview.AccountID != "" || preview.RouteID != "route.primary" || preview.EffectiveModel != "mapped-model" || provider.callCount() != 0 {
		t.Fatalf("original account dry run: %+v, %v", preview, err)
	}
	result, err := pipeline.Execute(context.Background(), client, &downstreamRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	requests := provider.requestsSnapshot()
	if result.AccountID != "" || len(requests) != 1 {
		t.Fatalf("unexpected account selection: %+v", result)
	}
	request := requests[0]
	if preview.bodyDigest != sha256.Sum256(request.Body()) {
		t.Fatal("dry run did not preserve route policy parity")
	}
	if request.CredentialMode() != providerauth.CredentialClientPassthrough ||
		request.Provenance().DestinationKind() != environment.DestinationKindUpstream ||
		request.Provenance().RouteID() != "route.primary" ||
		request.Headers().Get("X-Api-Key") != "synthetic-client-account" ||
		request.Headers().Get("Authorization") != "Bearer synthetic-client" ||
		request.Headers().Get("Cookie") != "session=synthetic" ||
		request.Headers().Get("Proxy-Authorization") != "" ||
		!bytes.Contains(request.Body(), []byte(`"model":"mapped-model"`)) {
		t.Fatalf("original account lost its identity or route policies: %+v", request)
	}
}
