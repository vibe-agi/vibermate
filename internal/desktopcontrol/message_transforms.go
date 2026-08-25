package desktopcontrol

import (
	"context"
	"errors"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

const (
	transformProtocolAnthropicMessages = "anthropic_messages"
	transformProtocolOpenAIResponses   = "openai_responses"
	transformProtocolOpenAIChat        = "openai_chat"
)

type MessageTransformTestInput struct {
	ClientProtocol string                  `json:"clientProtocol"`
	Policy         messagetransform.Policy `json:"policy"`
}

type MessageTransformTestRequest struct {
	Method  string      `json:"method"`
	Path    string      `json:"path"`
	Headers http.Header `json:"headers"`
	Body    string      `json:"body"`
}

type MessageTransformTestResponse struct {
	StatusCode int         `json:"statusCode"`
	Headers    http.Header `json:"headers"`
	Body       string      `json:"body"`
}

type MessageTransformTestResult struct {
	ClientProtocol string                       `json:"clientProtocol"`
	Request        MessageTransformTestRequest  `json:"request"`
	Response       MessageTransformTestResponse `json:"response"`
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
	request, response, err := messageTransformSample(input.ClientProtocol)
	if err != nil {
		return MessageTransformTestResult{}, err
	}
	program, err := messagetransform.Compile(
		input.Policy,
		messagetransform.DefaultLimits(),
	)
	if err != nil {
		return MessageTransformTestResult{}, err
	}
	turn := program.NewTurn()
	requestOutput, err := turn.ApplyRequest(ctx, request)
	if err != nil {
		return MessageTransformTestResult{}, err
	}
	responseOutput, err := turn.ApplyResponse(ctx, response)
	if err != nil {
		return MessageTransformTestResult{}, err
	}
	return MessageTransformTestResult{
		ClientProtocol: input.ClientProtocol,
		Request: MessageTransformTestRequest{
			Method:  requestOutput.Method,
			Path:    requestOutput.Path,
			Headers: requestOutput.Headers,
			Body:    string(requestOutput.Body),
		},
		Response: MessageTransformTestResponse{
			StatusCode: responseOutput.StatusCode,
			Headers:    responseOutput.Headers,
			Body:       string(responseOutput.Body),
		},
	}, nil
}

func messageTransformSample(
	clientProtocol string,
) (messagetransform.RequestMessage, messagetransform.ResponseMessage, error) {
	headers := http.Header{"Content-Type": {"application/json"}}
	switch clientProtocol {
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
				Body:       []byte(`{"id":"resp_sample","object":"response","status":"completed","model":"gpt-sample","output":[],"usage":{"input_tokens":8,"output_tokens":3,"total_tokens":11}}`),
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
			errors.New("client protocol has no transform sample")
	}
}
