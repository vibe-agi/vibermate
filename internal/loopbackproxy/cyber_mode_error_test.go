package loopbackproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestCyberDiscoveryFailureIsRetryableInBothClientDialects(t *testing.T) {
	for _, dialect := range []protocolspec.Dialect{protocolspec.DialectOpenAIResponses, protocolspec.DialectAnthropicMessages} {
		t.Run(string(dialect), func(t *testing.T) {
			writer := httptest.NewRecorder()
			failure := &exchange.Failure{Code: exchange.ReasonCode("cyber_access_discovery_unavailable")}
			writeExchangeFailure(writer, dialect, failure)
			if writer.Code != http.StatusServiceUnavailable || writer.Header().Get("X-Should-Retry") == "false" || writer.Header().Get(ReasonHeader) != "cyber_access_discovery_unavailable" {
				t.Fatalf("discovery is not a retryable service failure: %d %v %s", writer.Code, writer.Header(), writer.Body.String())
			}
			var envelope struct {
				Error struct{ Type, Message string } `json:"error"`
			}
			if err := json.Unmarshal(writer.Body.Bytes(), &envelope); err != nil || envelope.Error.Type != "api_error" || !strings.Contains(envelope.Error.Message, "model capabilities") || !strings.Contains(envelope.Error.Message, "network exit") || !strings.Contains(envelope.Error.Message, "retry") {
				t.Fatalf("discovery response lost its actionable safe message: %s, %v", writer.Body.String(), err)
			}
		})
	}
}

func TestUnavailableAccountCyberModeIsActionableAndNotRetried(t *testing.T) {
	for _, dialect := range []protocolspec.Dialect{protocolspec.DialectOpenAIResponses, protocolspec.DialectAnthropicMessages} {
		t.Run(string(dialect), func(t *testing.T) {
			writer := httptest.NewRecorder()
			failure := &exchange.Failure{Code: exchange.ReasonCode("cyber_access_program_unavailable")}
			writeExchangeFailure(writer, dialect, failure)
			if writer.Code != http.StatusUnprocessableEntity || writer.Header().Get("X-Should-Retry") != "false" || !strings.Contains(writer.Body.String(), "selected cyber access mode") {
				t.Fatalf("not actionable: %d %v %s", writer.Code, writer.Header(), writer.Body.String())
			}
		})
	}
}
