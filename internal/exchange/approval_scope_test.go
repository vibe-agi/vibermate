package exchange

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/capturecredential"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type approvalScopeRuns struct{ scope recording.Scope }

func (a approvalScopeRuns) AuthorizeProxy(context.Context, capturerun.ProxyCapability) (capturerun.Evidence, error) {
	return capturerun.Evidence{RunID: "run-original", ExecutableLabel: "agent", RecordingScope: a.scope}, nil
}

type approvalScopeManuals struct{}

func (approvalScopeManuals) AuthorizeProxy(context.Context, manualcapture.ProxyCredential) (manualcapture.Evidence, error) {
	return manualcapture.Evidence{}, errors.New("unexpected manual authority")
}

func TestPipelineApprovalUsesOriginalValueWithoutRecordingTokenLiveness(t *testing.T) {
	for _, captured := range []bool{true, false} {
		t.Run(map[bool]string{true: "sealed-admission-no-token", false: "original-global-stale-token"}[captured], func(t *testing.T) {
			expected := recording.Scope{Fence: recording.Fence{ArchiveEpoch: 7}}
			var options []ClientRequestOption
			if captured {
				expected.Capture = captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "run-original"}
				expected.Fence.CaptureGeneration = 9
				authorizer, err := captureadmission.NewAuthorizer(approvalScopeRuns{expected}, approvalScopeManuals{})
				if err != nil {
					t.Fatal(err)
				}
				credential, err := capturecredential.New(capturecredential.KindManagedRun, bytes.Repeat([]byte{1}, 32))
				if err != nil {
					t.Fatal(err)
				}
				proxy, err := captureadmission.NewProxyCredential(credential.Value())
				if err != nil {
					t.Fatal(err)
				}
				admission, err := authorizer.Authorize(t.Context(), proxy)
				if err != nil {
					t.Fatal(err)
				}
				options = append(options, WithIngressCorrelation(admission, "connection-original"))
			} else {
				epoch, _ := recording.NewEpoch(7)
				stamp, _ := recording.NewStamp(epoch, captureidentity.Reference{}, expected.Fence)
				epoch.Advance(8)
				if stamp.Current() {
					t.Fatal("fixture optional token stayed live")
				}
				options = append(options, WithObservationStamp(stamp))
			}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{destination: environment.DestinationKindUpstream, providerOrigin: "https://provider.example/v1", backend: protocolspec.DialectOpenAIChat, modelMode: environment.ModelModeMap, mappedModel: "gpt-provider", accounts: []testAccount{{id: "account.primary", revision: 1, epoch: 1}}, preferred: "account.primary"})
			provider := &providerDouble{results: []providerResult{{response: jsonResponse(http.StatusOK, completeToolProviderResponse("gpt-provider"))}}}
			decisions := &decisionDouble{decision: ToolDecision{Outcome: ToolDecisionRejected, ReasonCode: "user_rejected"}}
			pipeline := newTestPipeline(t, newAccountAuthority(t, testAccount{id: "account.primary", revision: 1, epoch: 1}), provider, decisions, &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			downstream := &downstreamRecorder{}
			_, err := pipeline.Execute(t.Context(), mustClientRequestWithOptions(t, "exchange-owned", plan, toolClientRequest(), options...), downstream)
			if ReasonOf(err) != ReasonToolDecisionRejected || decisions.callCount() != 1 {
				t.Fatalf("required decision failed before ownership: %v", err)
			}
			if got := decisions.lastRequest().OriginalScope(); got != expected {
				t.Fatalf("original value changed or depended on token: %+v want %+v", got, expected)
			}
			if len(downstream.bytesSnapshot()) != 0 {
				t.Fatal("unapproved tool bytes reached client")
			}
		})
	}
}
