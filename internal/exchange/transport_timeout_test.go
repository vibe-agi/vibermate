package exchange

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

func TestTransportTimeoutIsNotClientCancellation(t *testing.T) {
	for _, scenario := range []string{"provider header timeout", "client deadline", "client canceled"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if scenario == "client deadline" {
				var cancelDeadline context.CancelFunc
				ctx, cancelDeadline = context.WithTimeout(ctx, 500*time.Millisecond)
				defer cancelDeadline()
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if scenario == "client canceled" {
					cancel()
				}
				<-r.Context().Done()
			}))
			defer upstream.Close()
			material, err := providerauth.NewMaterial("synthetic-timeout-token", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer material.Destroy()
			encoded, err := material.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			defer clear(encoded)
			authenticator, err := providertransport.NewStaticBearerAuthenticator(chatGPTFixtureSecrets{encoded})
			if err != nil {
				t.Fatal(err)
			}
			gate := newTestActionGate(t)
			timeouts := providertransport.DefaultTransportTimeouts()
			if scenario == "provider header timeout" {
				timeouts.ResponseHead = 100 * time.Millisecond
			}
			provider, err := providertransport.NewProductionClient(gate, authenticator, timeouts, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				shutdown, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
				defer cancelShutdown()
				if err := provider.Shutdown(shutdown); err != nil {
					t.Error(err)
				}
			}()
			account := testAccount{id: "account.timeout", revision: 1, epoch: 7}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				destination: environment.DestinationKindUpstream, providerOrigin: upstream.URL,
				backend: protocolspec.DialectOpenAIChat, modelMode: environment.ModelModePassthrough,
				accounts: []testAccount{account}, preferred: account.id,
			})
			annotations, err := clientannotation.NewSigner(bytes.Repeat([]byte{0x5a}, 32))
			if err != nil {
				t.Fatal(err)
			}
			defer annotations.Destroy()
			observer := &attemptObserverDouble{}
			pipeline, err := New(Options{
				OwnerContext: context.Background(), Actions: gate, Accounts: newAccountAuthority(t, account),
				ProtocolPaths: mustProtocolPathSelector(t), Provider: provider, ToolDecisions: approvedDecisions(),
				RetryWaiter: &retryWaiterDouble{}, Observer: observer, ObservationTimeout: time.Second,
				ContentObserver: &contentObserverDouble{},
				Hold:            HoldPolicy{MaxDuration: time.Second}, Stream: DefaultStreamBudgets(),
				ClientAnnotations: annotations, Now: time.Now,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer shutdownPipeline(t, pipeline)
			result, err := pipeline.Execute(ctx, mustClientRequest(t, "timeout-exchange", plan, completeClientRequest()), &downstreamRecorder{})
			wantOutcome, wantReason := AttemptCanceled, ReasonExchangeCanceled
			if scenario == "provider header timeout" {
				wantOutcome, wantReason = AttemptFailed, ReasonProviderTransportFailed
				if ctx.Err() != nil || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expected a real HTTP header timeout with a live caller: %v, %v", ctx.Err(), err)
				}
			}
			if result.Outcome != wantOutcome || ReasonOf(err) != wantReason {
				t.Fatalf("outcome=%s reason=%s, want %s/%s: %v", result.Outcome, ReasonOf(err), wantOutcome, wantReason, err)
			}
			observations := observer.snapshot()
			if len(observations) != 1 || observations[0].Outcome != wantOutcome || observations[0].ReasonCode != wantReason {
				t.Fatalf("terminal observation = %+v", observations)
			}
		})
	}
}
