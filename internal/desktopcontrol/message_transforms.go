package desktopcontrol

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

const (
	transformProtocolAnthropicMessages = "anthropic_messages"
	transformProtocolOpenAIResponses   = "openai_responses"
	transformProtocolOpenAIChat        = "openai_chat"
)

type MessageTransformTestInput struct {
	WireProtocol string                      `json:"wireProtocol"`
	Policy       messagetransform.Policy     `json:"policy"`
	Sample       *MessageTransformTestSample `json:"sample,omitempty"`
}

type MessageTransformTestRequest struct {
	Method  string      `json:"method"`
	Path    string      `json:"path"`
	Headers http.Header `json:"headers"`
	Body    string      `json:"body"`
}

type MessageTransformTestResponse struct {
	StatusCode int         `json:"statusCode"`
	Streaming  bool        `json:"streaming"`
	Headers    http.Header `json:"headers"`
	Body       string      `json:"body"`
}

type MessageTransformTestSample struct {
	Request  MessageTransformTestRequest  `json:"request"`
	Response MessageTransformTestResponse `json:"response"`
}

type MessageTransformTestResult struct {
	WireProtocol   string                       `json:"wireProtocol"`
	RequestBefore  MessageTransformTestRequest  `json:"requestBefore"`
	RequestAfter   MessageTransformTestRequest  `json:"requestAfter"`
	ResponseBefore MessageTransformTestResponse `json:"responseBefore"`
	ResponseAfter  MessageTransformTestResponse `json:"responseAfter"`
}

func (handler *Handler) testMessageTransform(
	writer http.ResponseWriter,
	request *http.Request,
) {
	body, err := readJSONBody(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonMessageTransformTestFailed)
		return
	}
	var input MessageTransformTestInput
	if err := decodeStrictJSON(body, &input); err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonMessageTransformTestFailed)
		return
	}
	result, err := runMessageTransformSample(request.Context(), input)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonMessageTransformTestFailed)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func runMessageTransformSample(
	ctx context.Context,
	input MessageTransformTestInput,
) (MessageTransformTestResult, error) {
	request, response, err := resolveMessageTransformSample(input)
	if err != nil {
		return MessageTransformTestResult{}, err
	}
	pipeline, err := messagetransform.CompilePipeline(
		[]messagetransform.Policy{input.Policy},
		messagetransform.DefaultLimits(),
	)
	if err != nil {
		return MessageTransformTestResult{}, err
	}
	annotations, err := clientannotation.NewSigner(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		return MessageTransformTestResult{}, err
	}
	defer annotations.Destroy()
	turn, err := pipeline.NewTurnWithOptions(messagetransform.TurnOptions{
		Metadata: messagetransform.RuntimeMetadata{
			LocalUserName: "example-user", HomeDirectory: "/Users/example-user",
			OperatingSystem: "darwin", OperatingSystemVersion: "15.0",
			Architecture: "arm64", TimeZone: "Etc/UTC",
			WorkspaceRoot: "/Users/example-user/Code/example", WorkspaceLabel: "example",
			TurnStartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		},
		Annotations: annotations,
	})
	if err != nil {
		return MessageTransformTestResult{}, err
	}
	requestOutput, err := turn.ApplyRequest(ctx, request)
	if err != nil {
		return MessageTransformTestResult{}, err
	}
	responseOutput, err := applyMessageTransformTestResponse(ctx, turn, response)
	if err != nil {
		return MessageTransformTestResult{}, err
	}
	return MessageTransformTestResult{
		WireProtocol:   input.WireProtocol,
		RequestBefore:  messageTransformTestRequest(request),
		RequestAfter:   messageTransformTestRequest(requestOutput),
		ResponseBefore: messageTransformTestResponse(response),
		ResponseAfter:  messageTransformTestResponse(responseOutput),
	}, nil
}

func resolveMessageTransformSample(
	input MessageTransformTestInput,
) (messagetransform.RequestMessage, messagetransform.ResponseMessage, error) {
	if input.Sample == nil {
		return messageTransformSample(input.WireProtocol)
	}
	expectedPath, ok := transformProtocolPath(input.WireProtocol)
	if !ok || input.Sample.Request.Method != http.MethodPost ||
		input.Sample.Request.Path != expectedPath ||
		input.Sample.Response.StatusCode < 100 || input.Sample.Response.StatusCode > 599 {
		return messagetransform.RequestMessage{}, messagetransform.ResponseMessage{},
			errors.New("wire protocol sample is invalid")
	}
	return messagetransform.RequestMessage{
			Method: input.Sample.Request.Method, Path: input.Sample.Request.Path,
			Headers: input.Sample.Request.Headers.Clone(), Body: []byte(input.Sample.Request.Body),
		}, messagetransform.ResponseMessage{
			StatusCode: input.Sample.Response.StatusCode,
			Streaming:  input.Sample.Response.Streaming,
			Headers:    input.Sample.Response.Headers.Clone(),
			Body:       []byte(input.Sample.Response.Body),
		}, nil
}

func transformProtocolPath(protocol string) (string, bool) {
	switch protocol {
	case transformProtocolAnthropicMessages:
		return "/v1/messages", true
	case transformProtocolOpenAIResponses:
		return "/v1/responses", true
	case transformProtocolOpenAIChat:
		return "/v1/chat/completions", true
	default:
		return "", false
	}
}

func messageTransformTestRequest(input messagetransform.RequestMessage) MessageTransformTestRequest {
	return MessageTransformTestRequest{
		Method: input.Method, Path: input.Path, Headers: input.Headers, Body: string(input.Body),
	}
}

func messageTransformTestResponse(input messagetransform.ResponseMessage) MessageTransformTestResponse {
	return MessageTransformTestResponse{
		StatusCode: input.StatusCode, Streaming: input.Streaming,
		Headers: input.Headers, Body: string(input.Body),
	}
}

func applyMessageTransformTestResponse(
	ctx context.Context,
	turn *messagetransform.PipelineTurn,
	input messagetransform.ResponseMessage,
) (messagetransform.ResponseMessage, error) {
	if !input.Streaming {
		return turn.ApplyResponse(ctx, input)
	}
	decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
	if err != nil {
		return messagetransform.ResponseMessage{}, err
	}
	events, err := decoder.Feed(input.Body)
	if err != nil {
		return messagetransform.ResponseMessage{}, err
	}
	if err := decoder.Finish(); err != nil {
		return messagetransform.ResponseMessage{}, err
	}
	if len(events) == 0 {
		return messagetransform.ResponseMessage{}, errors.New("streaming sample contains no SSE event")
	}
	var body bytes.Buffer
	var headers http.Header
	for _, event := range events {
		transformed := messagetransform.ResponseMessage{
			StatusCode: input.StatusCode, Streaming: true, EventName: event.Name,
			Headers: input.Headers.Clone(), Body: bytes.Clone(event.Data),
		}
		if !bytes.Equal(bytes.TrimSpace(event.Data), []byte("[DONE]")) {
			transformed, err = turn.ApplyResponse(ctx, transformed)
			if err != nil {
				return messagetransform.ResponseMessage{}, err
			}
		}
		if headers == nil {
			headers = transformed.Headers.Clone()
		} else if !messageTransformTestHeadersEqual(headers, transformed.Headers) {
			return messagetransform.ResponseMessage{}, errors.New(
				"streaming response transform changed Headers after the first event",
			)
		}
		event.Data = transformed.Body
		encoded, encodeErr := ssewire.Encode(event)
		if encodeErr != nil {
			return messagetransform.ResponseMessage{}, encodeErr
		}
		_, _ = body.Write(encoded)
	}
	return messagetransform.ResponseMessage{
		StatusCode: input.StatusCode, Streaming: true,
		Headers: headers, Body: body.Bytes(),
	}, nil
}

func messageTransformTestHeadersEqual(left, right http.Header) bool {
	if len(left) != len(right) {
		return false
	}
	for name, values := range left {
		if !slices.Equal(values, right.Values(name)) {
			return false
		}
	}
	return true
}

func messageTransformSample(
	wireProtocol string,
) (messagetransform.RequestMessage, messagetransform.ResponseMessage, error) {
	headers := http.Header{"Content-Type": {"application/json"}}
	switch wireProtocol {
	case transformProtocolAnthropicMessages:
		requestHeaders := headers.Clone()
		requestHeaders.Set("Anthropic-Version", "2023-06-01")
		return messagetransform.RequestMessage{
				Method:  http.MethodPost,
				Path:    "/v1/messages",
				Headers: requestHeaders,
				Body:    []byte(`{"model":"claude-sample","max_tokens":64,"messages":[{"role":"user","content":"Hello from ViberMate"}]}`),
			}, messagetransform.ResponseMessage{
				StatusCode: http.StatusOK,
				Headers:    headers.Clone(),
				Body:       []byte(`{"id":"msg_sample","type":"message","role":"assistant","model":"claude-sample","content":[{"type":"text","text":"Sample response"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":8,"output_tokens":3}}`),
			}, nil
	case transformProtocolOpenAIResponses:
		return messagetransform.RequestMessage{
				Method:  http.MethodPost,
				Path:    "/v1/responses",
				Headers: headers.Clone(),
				Body:    []byte(`{"model":"gpt-sample","input":"Hello from ViberMate","stream":false}`),
			}, messagetransform.ResponseMessage{
				StatusCode: http.StatusOK,
				Headers:    headers.Clone(),
				Body:       []byte(`{"id":"resp_sample","object":"response","status":"completed","model":"gpt-sample","output":[{"id":"msg_sample","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Sample response","annotations":[]}]}],"usage":{"input_tokens":8,"output_tokens":3,"total_tokens":11}}`),
			}, nil
	case transformProtocolOpenAIChat:
		return messagetransform.RequestMessage{
				Method:  http.MethodPost,
				Path:    "/v1/chat/completions",
				Headers: headers.Clone(),
				Body:    []byte(`{"model":"gpt-sample","messages":[{"role":"user","content":"Hello from ViberMate"}],"stream":false}`),
			}, messagetransform.ResponseMessage{
				StatusCode: http.StatusOK,
				Headers:    headers.Clone(),
				Body:       []byte(`{"id":"chatcmpl_sample","object":"chat.completion","model":"gpt-sample","choices":[{"index":0,"message":{"role":"assistant","content":"Sample response"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11}}`),
			}, nil
	default:
		return messagetransform.RequestMessage{}, messagetransform.ResponseMessage{},
			errors.New("wire protocol has no transform sample")
	}
}
