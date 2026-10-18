package loopbackproxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestUnavailableAccountCyberModeIsActionableAndNotRetried(t *testing.T) {
	writer := httptest.NewRecorder()
	failure := &exchange.Failure{Code: exchange.ReasonCode("cyber_access_program_unavailable")}
	writeExchangeFailure(writer, protocolspec.DialectOpenAIResponses, failure)
	if writer.Code != http.StatusUnprocessableEntity || writer.Header().Get("X-Should-Retry") != "false" || !strings.Contains(writer.Body.String(), "selected cyber access mode") {
		t.Fatalf("not actionable: %d %v %s", writer.Code, writer.Header(), writer.Body.String())
	}
}
