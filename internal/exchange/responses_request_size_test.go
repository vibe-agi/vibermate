package exchange

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

func TestManagedResponsesNearRequestLimitWithoutTransforms(t *testing.T) {
	account := testAccount{id: "account.primary", revision: 1, epoch: 1}
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		clientProtocol: environment.ClientProtocolOpenAIResponses,
		chatGPTClient:  true,
		destination:    environment.DestinationKindUpstream,
		providerOrigin: "https://chatgpt.com",
		backend:        protocolspec.DialectOpenAIResponses,
		modelMode:      environment.ModelModePassthrough,
		accounts:       []testAccount{account}, preferred: account.id,
		recording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff},
	})
	policy, err := DefaultResourcePolicy()
	if err != nil {
		t.Fatal(err)
	}
	gate, err := NewBodyAdmission(policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ encoding, program string }{
		{"identity", "standard"}, {"gzip", "standard"}, {"zstd", "standard"}, {"identity", "daybreak_blue"},
	} {
		t.Run(test.encoding+"/"+test.program, func(t *testing.T) {
			prefix := `{"model":"client-model","access_programs":{"cyber":"` + test.program + `"},"input":[{"role":"user","content":"<html>&</html>`
			middle := `"},{"role":"user","content":"`
			suffix := `"}]}`
			padding := providertransport.MaxProviderRequestBytes - len(prefix) - len(middle) - len(suffix)
			body := []byte(prefix + strings.Repeat("x", padding/2) + middle + strings.Repeat("x", padding-padding/2) + suffix)
			provider := &providerDouble{results: []providerResult{{response: jsonResponse(http.StatusOK, completeResponsesProviderResponse("client-model"))}}}
			pipeline := newTestPipeline(t, newAccountAuthority(t, account), provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			pipeline.bodyAdmission = gate
			lease, err := gate.AcquirePlan(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			wire := body
			if test.encoding != "identity" {
				wire = compressedRequestFixture(t, test.encoding, body)
			}
			request := mustClientRequestWithOptions(t, "long-session-"+test.encoding, plan, wire,
				WithBodyLease(lease), WithOriginalHeaders(http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {test.encoding}}))
			downstream := &downstreamRecorder{}
			result, err := pipeline.Execute(t.Context(), request, downstream)
			if err != nil || result.Outcome != AttemptSucceeded {
				t.Fatalf("valid long session failed without transforms: outcome=%s error=%v", result.Outcome, err)
			}
			forwarded := provider.requestsSnapshot()
			if len(forwarded) != 1 {
				t.Fatalf("provider requests = %d, want 1", len(forwarded))
			}
			if test.program == "standard" && !bytes.Equal(forwarded[0].Body(), body) {
				t.Fatal("provider did not receive the complete original JSON")
			}
			var before, after map[string]json.RawMessage
			if err := json.Unmarshal(body, &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(forwarded[0].Body(), &after); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before["input"], after["input"]) || string(after["access_programs"]) != `{"cyber":"standard"}` {
				t.Fatal("account preference changed the conversation history or selected the wrong program")
			}
			if !bytes.Contains(downstream.bytesSnapshot(), []byte(`"id":"resp_complete"`)) {
				t.Fatal("client did not receive the provider response")
			}
		})
	}
}
