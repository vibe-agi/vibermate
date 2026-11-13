package exchange

import (
	"context"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// The completed-request observation says what the script did, for a request
// that went through and for one the script refused.
func TestAttemptObservationReportsScriptOutcomes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, script string
		wantReason   ReasonCode
		wantCalls    int
		want         messagetransform.Outcome
		wantBlock    string
	}{
		{name: "unchanged", script: `const size = request.body.length;`, wantCalls: 1, want: messagetransform.OutcomeUnchanged},
		{name: "changed", script: `request.headers["x-audited"] = ["yes"];`, wantCalls: 1, want: messagetransform.OutcomeChanged},
		{name: "blocked", script: `runtime.block("secret-detected");`, wantReason: ReasonMessageTransformFailed, want: messagetransform.OutcomeBlocked, wantBlock: "secret-detected"},
		{name: "failed", script: `throw new Error(request.body);`, wantReason: ReasonMessageTransformFailed, want: messagetransform.OutcomeFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := testAccount{id: "account.primary", revision: 3, epoch: 7}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				destination:    environment.DestinationKindUpstream,
				providerOrigin: "https://provider.example/v1",
				backend:        protocolspec.DialectOpenAIChat,
				modelMode:      environment.ModelModeMap,
				mappedModel:    "opaque:upstream-model",
				accounts:       []testAccount{account},
				preferred:      account.id,
				transform:      messagetransform.Policy{RequestJavaScript: test.script},
			})
			provider := &providerDouble{results: []providerResult{{
				response: jsonResponse(http.StatusOK, completeProviderResponse("opaque:upstream-model")),
			}}}
			observer := &attemptObserverDouble{}
			pipeline := newTestPipeline(t, newAccountAuthority(t, account), provider, approvedDecisions(), observer)
			defer shutdownPipeline(t, pipeline)
			result, err := pipeline.Execute(
				context.Background(),
				mustClientRequest(t, "exchange-script-outcome-"+test.name, plan, completeClientRequest()),
				&downstreamRecorder{},
			)
			if ReasonOf(err) != test.wantReason {
				t.Fatalf("Execute() error = %v, want reason %q", err, test.wantReason)
			}
			if provider.callCount() != test.wantCalls {
				t.Fatalf("provider calls = %d, want %d", provider.callCount(), test.wantCalls)
			}
			observer.mu.Lock()
			observations := append([]AttemptObservation(nil), observer.observations...)
			observer.mu.Unlock()
			if len(observations) != 1 {
				t.Fatalf("observations = %d", len(observations))
			}
			for _, scripts := range [][]messagetransform.StageOutcome{result.Scripts, observations[0].Scripts} {
				if len(scripts) != 1 {
					t.Fatalf("script outcomes = %+v", scripts)
				}
				got := scripts[0]
				if got.Stage != messagetransform.StageRequest || got.Outcome != test.want || got.BlockReason != test.wantBlock || got.Applications != 1 {
					t.Fatalf("script outcome = %+v", got)
				}
				if got.Script.ID == "" || got.Script.Revision == 0 || got.Script.Name == "" {
					t.Fatalf("outcome does not say which script produced it: %+v", got.Script)
				}
			}
		})
	}
}
