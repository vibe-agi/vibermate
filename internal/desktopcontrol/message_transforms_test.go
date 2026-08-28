package desktopcontrol

import (
	"context"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

func TestMessageTransformSampleReturnsAllFourWireSnapshots(t *testing.T) {
	t.Parallel()

	result, err := runMessageTransformSample(context.Background(), MessageTransformTestInput{
		WireProtocol: transformProtocolAnthropicMessages,
		Policy: messagetransform.Policy{
			RequestJavaScript:  `request.headers["x-edited"] = ["request"]; request.body = request.body.replace("Hello", "Changed");`,
			ResponseJavaScript: `response.headers["x-edited"] = ["response"]; response.body = response.body.replace("Sample", "Changed");`,
		},
	})
	if err != nil {
		t.Fatalf("runMessageTransformSample() error = %v", err)
	}
	if result.RequestBefore.Headers.Get("X-Edited") != "" ||
		!strings.Contains(result.RequestBefore.Body, "Hello") ||
		result.RequestAfter.Headers.Get("X-Edited") != "request" ||
		!strings.Contains(result.RequestAfter.Body, "Changed") {
		t.Fatalf("request snapshots = before=%+v after=%+v", result.RequestBefore, result.RequestAfter)
	}
	if result.ResponseBefore.Headers.Get("X-Edited") != "" ||
		!strings.Contains(result.ResponseBefore.Body, "Sample") ||
		result.ResponseAfter.Headers.Get("X-Edited") != "response" ||
		!strings.Contains(result.ResponseAfter.Body, "Changed") {
		t.Fatalf("response snapshots = before=%+v after=%+v", result.ResponseBefore, result.ResponseAfter)
	}
}

func TestMessageTransformSampleSupportsStructuredClientAnnotations(t *testing.T) {
	t.Parallel()

	result, err := runMessageTransformSample(context.Background(), MessageTransformTestInput{
		WireProtocol: transformProtocolAnthropicMessages,
		Policy: messagetransform.Policy{ResponseJavaScript: `
			const payload = JSON.parse(response.body);
			payload.content[0].text = runtime.annotations.create(
				"sample", runtime.turn.startedAt
			);
			response.body = JSON.stringify(payload);
		`},
	})
	if err != nil {
		t.Fatalf("runMessageTransformSample() error = %v", err)
	}
	if !strings.Contains(result.ResponseAfter.Body, "vibermate:annotation:v1:sample:") ||
		!strings.Contains(result.ResponseAfter.Body, "2026-01-02T03:04:05Z") {
		t.Fatalf("response after = %s", result.ResponseAfter.Body)
	}
}

func TestMessageTransformSampleUsesProvidedWireCopy(t *testing.T) {
	t.Parallel()

	result, err := runMessageTransformSample(context.Background(), MessageTransformTestInput{
		WireProtocol: transformProtocolOpenAIResponses,
		Sample: &MessageTransformTestSample{
			Request: MessageTransformTestRequest{
				Method: "POST", Path: "/v1/responses",
				Headers: map[string][]string{"Content-Type": {"application/json"}},
				Body:    `{"model":"captured-model","input":"private sample"}`,
			},
			Response: MessageTransformTestResponse{
				StatusCode: 201,
				Headers:    map[string][]string{"Content-Type": {"application/json"}},
				Body:       `{"id":"captured-response","status":"completed"}`,
			},
		},
		Policy: messagetransform.Policy{
			RequestJavaScript:  `context.requestSeen = "yes"; request.body = request.body.replace("private", "sanitized");`,
			ResponseJavaScript: `response.headers["x-tested"] = [context.requestSeen];`,
		},
	})
	if err != nil {
		t.Fatalf("runMessageTransformSample() error = %v", err)
	}
	if result.WireProtocol != transformProtocolOpenAIResponses ||
		!strings.Contains(result.RequestBefore.Body, "private sample") ||
		!strings.Contains(result.RequestAfter.Body, "sanitized sample") ||
		result.ResponseBefore.StatusCode != 201 {
		t.Fatalf("captured sample result = %+v", result)
	}
}

func TestMessageTransformSampleRunsCapturedSSEEventByEvent(t *testing.T) {
	t.Parallel()

	result, err := runMessageTransformSample(context.Background(), MessageTransformTestInput{
		WireProtocol: transformProtocolOpenAIResponses,
		Sample: &MessageTransformTestSample{
			Request: MessageTransformTestRequest{
				Method: "POST", Path: "/v1/responses",
				Headers: map[string][]string{"Content-Type": {"application/json"}},
				Body:    `{"model":"captured-model","input":"sample","stream":true}`,
			},
			Response: MessageTransformTestResponse{
				StatusCode: 200,
				Streaming:  true,
				Headers:    map[string][]string{"Content-Type": {"text/event-stream"}},
				Body: "event: response.output_text.delta\n" +
					"data: {\"text\":\"private one\"}\n\n" +
					"event: response.completed\n" +
					"data: {\"text\":\"private two\"}\n\n",
			},
		},
		Policy: messagetransform.Policy{ResponseJavaScript: `
			if (!response.streaming) throw new Error("stream flag missing");
			context.events = (context.events ?? 0) + 1;
			const payload = JSON.parse(response.body);
			payload.text = payload.text.replace("private", "sanitized");
			payload.sequence = context.events;
			response.headers["x-stream-tested"] = ["yes"];
			response.body = JSON.stringify(payload);
		`},
	})
	if err != nil {
		t.Fatalf("runMessageTransformSample() error = %v", err)
	}
	if !result.ResponseBefore.Streaming || !result.ResponseAfter.Streaming ||
		result.ResponseAfter.Headers.Get("X-Stream-Tested") != "yes" ||
		!strings.Contains(result.ResponseAfter.Body, `"text":"sanitized one","sequence":1`) ||
		!strings.Contains(result.ResponseAfter.Body, `"text":"sanitized two","sequence":2`) {
		t.Fatalf("streaming response snapshots = before=%+v after=%+v", result.ResponseBefore, result.ResponseAfter)
	}
}
