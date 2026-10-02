package exchange

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestUnansweredAttemptDoesNotRecordTheRequestTwice(t *testing.T) {
	for _, failure := range []error{context.Canceled, errors.New("synthetic provider failure")} {
		t.Run(failure.Error(), func(t *testing.T) {
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				destination: environment.DestinationKindUpstream, providerOrigin: "https://provider.example/v1",
				backend: protocolspec.DialectOpenAIChat, modelMode: environment.ModelModeMap,
				mappedModel: "gpt-provider", accounts: []testAccount{{id: "account.primary", revision: 3, epoch: 7}},
				preferred: "account.primary",
			})
			content := &contentObserverDouble{}
			pipeline := newTestPipelineWithContentObserver(t,
				newAccountAuthority(t, testAccount{id: "account.primary", revision: 3, epoch: 7}),
				&providerDouble{results: []providerResult{{err: failure}}},
				approvedDecisions(), &attemptObserverDouble{}, content)
			defer shutdownPipeline(t, pipeline)
			_, err := pipeline.Execute(context.Background(),
				mustClientRequest(t, "exchange-unanswered", plan, completeClientRequest()), &downstreamRecorder{})
			if err == nil {
				t.Fatal("provider failure was not reported")
			}
			if len(content.observations) != 1 || content.observations[0].Response != nil {
				t.Fatalf("an unanswered request was recorded %d times", len(content.observations))
			}
		})
	}
}
