package desktopcontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

// Execute the actual raw JavaScript literals shipped by the Flutter starters,
// not a separately maintained JavaScript implementation of their behavior.
func annotationStarter(t *testing.T, name string, index int) messagetransform.Policy {
	t.Helper()
	data, err := os.ReadFile("../../ui/flutter_app/lib/features/workbench/code_library_view.dart")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	raw := regexp.MustCompile(`(?s)r'''(.*?)'''`)
	guardAt := strings.Index(source, "const _responseAnnotationRequest =")
	starterAt := strings.Index(source, "String "+name+"(")
	if guardAt < 0 || starterAt < 0 {
		t.Fatal("annotation starter was not found")
	}
	guard := raw.FindStringSubmatch(source[guardAt:])
	scripts := raw.FindAllStringSubmatch(source[starterAt:], 3)
	if len(guard) != 2 || len(scripts) != 3 {
		t.Fatal("annotation literal contract changed")
	}
	return messagetransform.Policy{RequestJavaScript: guard[1], ResponseJavaScript: "if (context.vibermateStructuredOutput === false) {\n" + scripts[index][1] + "\n}"}
}

func TestAnnotationStarterWithoutRequestGuardDoesNotDecorate(t *testing.T) {
	for _, name := range []string{"_turnTimeStarter", "_responseModelStarter"} {
		policy := annotationStarter(t, name, 0)
		policy.RequestJavaScript = ""
		result, err := runMessageTransformSample(context.Background(), MessageTransformTestInput{WireProtocol: transformProtocolOpenAIResponses, Policy: policy})
		if err != nil {
			t.Fatal(err)
		}
		if result.ResponseAfter.Body != result.ResponseBefore.Body {
			t.Fatal("annotation ran without an explicit request format decision")
		}
	}
}

func TestAnnotationStartersLeaveStructuredResultsUntouched(t *testing.T) {
	protocols := []string{transformProtocolOpenAIResponses, transformProtocolOpenAIChat, transformProtocolAnthropicMessages}
	for _, name := range []string{"_turnTimeStarter", "_responseModelStarter"} {
		for index, protocol := range protocols {
			for _, format := range []string{"absent", "text", "json_schema", "json_object", "future_format"} {
				for _, streaming := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/stream=%t", name, protocol, format, streaming), func(t *testing.T) {
						request, response, err := messageTransformSample(protocol)
						if err != nil {
							t.Fatal(err)
						}
						var payload map[string]any
						if err := json.Unmarshal(request.Body, &payload); err != nil {
							t.Fatal(err)
						}
						payload["client_metadata"] = map[string]string{"session_id": "synthetic-session"}
						if format != "absent" {
							value := map[string]string{"type": format}
							switch protocol {
							case transformProtocolOpenAIResponses:
								payload["text"] = map[string]any{"format": value}
							case transformProtocolOpenAIChat:
								payload["response_format"] = value
							default:
								payload["output_config"] = map[string]any{"format": value}
							}
						}
						request.Body, _ = json.Marshal(payload)
						if streaming {
							response.Streaming = true
							response.Headers.Set("Content-Type", "text/event-stream")
							event := `{"type":"response.output_text.delta","model":"synthetic-model","delta":"{\\\"title\\\":\\\"test\\\"}"}`
							if protocol == transformProtocolOpenAIChat {
								event = `{"model":"synthetic-model","choices":[{"delta":{"content":"title"}}]}`
							}
							if protocol == transformProtocolAnthropicMessages {
								event = `{"type":"content_block_delta","delta":{"type":"text_delta","text":"title"}}`
								response.Body = []byte("data: {\"type\":\"message_start\",\"message\":{\"model\":\"synthetic-model\"}}\n\n")
							} else {
								response.Body = nil
							}
							response.Body = append(response.Body, []byte("data: "+event+"\n\n")...)
						}
						result, err := runMessageTransformSample(context.Background(), MessageTransformTestInput{
							WireProtocol: protocol, Policy: annotationStarter(t, name, index),
							Sample: &MessageTransformTestSample{
								Request:  MessageTransformTestRequest{Method: request.Method, Path: request.Path, Headers: request.Headers, Body: string(request.Body)},
								Response: MessageTransformTestResponse{StatusCode: response.StatusCode, Headers: response.Headers, Streaming: streaming, Body: string(response.Body)},
							},
						})
						if err != nil {
							t.Fatal(err)
						}
						if result.RequestAfter.Body != string(request.Body) {
							t.Fatal("annotation changed request content or account/session metadata")
						}
						if format != "absent" && format != "text" {
							if result.ResponseAfter.Body != string(response.Body) {
								t.Fatal("annotation modified a structured result")
							}
						} else if !strings.Contains(result.ResponseAfter.Body, "vibermate:annotation:v1:") {
							t.Fatal("plain text annotation stopped working")
						}
					})
				}
			}
		}
	}
}
