package exchange

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestManagedResponsesErrorNotificationWaitsForTerminal(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprintf("completed=%t", completed), func(t *testing.T) {
			account := testAccount{id: "account.notification", revision: 1, epoch: 1}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				clientProtocol: environment.ClientProtocolOpenAIResponses,
				destination:    environment.DestinationKindUpstream, providerOrigin: "https://api.openai.com",
				backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
				accounts: []testAccount{account}, preferred: account.id,
			})
			reader, writer := io.Pipe()
			defer writer.Close()
			response := streamResponse(http.StatusOK, reader)
			response.Body = reader
			provider := &providerDouble{results: []providerResult{{response: response}}}
			pipeline := newTestPipeline(t, newAccountAuthority(t, account), provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			notification := []byte("event: error\ndata: {\"type\":\"error\",\"code\":\"notification_fixture\",\"message\":\"private synthetic notification\"}\n\n")
			terminal := originalResponsesTerminalWire(t)
			go func() {
				if _, err := writer.Write(notification); err != nil {
					return
				}
				if completed {
					_, _ = writer.Write(terminal) // No EOF: the semantic terminal must finish.
				} else {
					_ = writer.Close()
				}
			}()
			downstream := &downstreamRecorder{}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err := pipeline.Execute(ctx, mustClientRequest(t, "notification", plan, streamingResponsesClientRequest()), downstream)
			if provider.callCount() != 1 {
				t.Fatal("notification triggered a provider resend")
			}
			if completed {
				if err != nil || len(downstream.abortsSnapshot()) != 0 || !bytes.Equal(downstream.bytesSnapshot(), append(notification, terminal...)) {
					t.Fatalf("notification truncated a completed stream: %v", err)
				}
			} else {
				notices := downstream.abortsSnapshot()
				if err == nil || len(notices) != 1 || notices[0].ReasonCode != ReasonProviderResponseFailed || !bytes.Equal(downstream.bytesSnapshot(), notification) {
					t.Fatalf("notification without terminal was not a failure: %v", err)
				}
				if name, _ := notices[0].NativeError.StreamEvent(protocolspec.DialectOpenAIResponses); name != "" {
					t.Fatal("already delivered notification would be repeated instead of a failed terminal")
				}
				if notices[0].ProviderErrorCode != "" || !bytes.Contains(notices[0].NativeError.ForDialect(protocolspec.DialectOpenAIResponses), []byte("notification_fixture")) {
					t.Fatal("native failure lost or arbitrary code entered diagnostics")
				}
			}
		})
	}
}
