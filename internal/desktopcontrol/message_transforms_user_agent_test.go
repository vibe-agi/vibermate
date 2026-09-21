package desktopcontrol

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

func TestMessageTransformTestEndpointValidatesUserAgent(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{transformProtocolAnthropicMessages, transformProtocolOpenAIResponses, transformProtocolOpenAIChat} {
		for _, test := range []struct {
			name, script string
			valid        bool
		}{
			{"multiple", `request.headers["user-agent"] = ["a", "b"];`, false},
			{"unicode", `request.headers["user-agent"] = "客户端";`, false},
			{"oversize", `request.headers["user-agent"] = "a".repeat(513);`, false},
			{"tab", `request.headers["user-agent"] = "a\tb";`, false},
			{"injection", `request.headers["user-agent"] = "agent\r\nX-Test: value";`, false},
			{"valid", `request.headers["user-agent"] = "test-client/1.0";`, true},
			{"limit", `request.headers["user-agent"] = "a".repeat(512);`, true},
			{"empty", `request.headers["user-agent"] = "";`, true},
			{"delete", `delete request.headers["user-agent"];`, true},
			{"unchanged", `request.body = request.body;`, true},
		} {
			t.Run(protocol+"/"+test.name, func(t *testing.T) {
				input := MessageTransformTestInput{WireProtocol: protocol, Policy: messagetransform.Policy{RequestJavaScript: test.script}}
				encoded, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest(http.MethodPost, "/api/v1/message-transforms/actions/test", bytes.NewReader(encoded))
				request.Header.Set("Content-Type", "application/json")
				writer := httptest.NewRecorder()
				(&Handler{}).testMessageTransform(writer, request)
				if test.valid {
					if writer.Code != http.StatusOK {
						t.Fatalf("valid User-Agent rejected: %d %s", writer.Code, writer.Body.String())
					}
				} else if writer.Code != http.StatusUnprocessableEntity || !strings.Contains(writer.Body.String(), "request · invalid transform output") {
					t.Fatalf("invalid User-Agent did not fail at request stage: %d %s", writer.Code, writer.Body.String())
				}
			})
		}
	}
}
