package desktopcontrol

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/controlprincipal"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

// These are contract tests of the complete copy/paste artifacts and the real
// JSON handler. All identities, capabilities, and wire samples are synthetic.
// httptest recorders do not start listeners or contact an installed Runtime.
const contractRoundsSecret = "SYNTHETIC-CONTRACT-SECRET-DO-NOT-ECHO"

var contractRoundsProtocols = []string{
	transformProtocolOpenAIResponses, transformProtocolAnthropicMessages, transformProtocolOpenAIChat,
}

func contractRoundsPolicy(t *testing.T) messagetransform.Policy {
	t.Helper()
	read := func(name string) string {
		body, err := os.ReadFile(filepath.Join("..", "..", "javascript", "hide-local-identity", name))
		if err != nil {
			t.Fatal(err)
		}
		if len(body) < 1000 || len(body) > messagetransform.DefaultLimits().MaximumScriptBytes {
			t.Fatalf("%s is not the bounded complete artifact", name)
		}
		return string(body)
	}
	return messagetransform.Policy{RequestJavaScript: read("request.js"), ResponseJavaScript: read("response.js")}
}

func contractRoundsJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func contractRoundsDecode(t *testing.T, body string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func contractRoundsInput(t *testing.T, protocol, user string) MessageTransformTestInput {
	t.Helper()
	path, ok := transformProtocolPath(protocol)
	if !ok {
		t.Fatal("invalid fixture protocol")
	}
	root := "/Users/" + user + "/work/project"
	text := "work=" + root + "; home=/Users/" + user + "; user=" + user
	request := map[string]any{
		"model": user + "-synthetic-model", "stream": true,
		"metadata": map[string]any{"routing": user},
	}
	if protocol == transformProtocolOpenAIResponses {
		request["input"] = text
		request["previous_response_id"] = user + "-previous-id"
	} else {
		request["messages"] = []any{map[string]any{"role": "user", "content": text}}
		if protocol == transformProtocolAnthropicMessages {
			request["max_tokens"] = 64
		}
	}
	return MessageTransformTestInput{
		WireProtocol: protocol,
		Policy:       contractRoundsPolicy(t),
		Sample: &MessageTransformTestSample{
			Request: MessageTransformTestRequest{
				Method: http.MethodPost, Path: path,
				Headers: http.Header{"content-type": {"application/json"}, "x-fixture": {"request-unchanged"}},
				Body:    contractRoundsJSON(t, request),
			},
			Response: MessageTransformTestResponse{
				StatusCode: 200, Streaming: true,
				Headers: http.Header{"content-type": {"text/event-stream"}, "x-request-id": {"synthetic-response-id"}, "cache-control": {"no-cache"}},
			},
			Runtime: &MessageTransformTestRuntime{
				UserName: user, HomeDirectory: "/Users/" + user, WorkspaceRoot: root,
				OperatingSystem: "darwin", Architecture: "arm64", TimeZone: "Etc/UTC",
				TurnStartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			},
		},
	}
}

func contractRoundsWireEvent(t *testing.T, name string, body any) string {
	t.Helper()
	var prefix string
	if name != "" {
		prefix = "event: " + name + "\n"
	}
	return prefix + "data: " + contractRoundsJSON(t, body) + "\n\n"
}

func contractRoundsTextEvent(t *testing.T, protocol, text string) string {
	t.Helper()
	switch protocol {
	case transformProtocolOpenAIResponses:
		return contractRoundsWireEvent(t, "response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "item_id": "synthetic-message-id", "delta": text,
		})
	case transformProtocolAnthropicMessages:
		return contractRoundsWireEvent(t, "content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text},
		})
	default:
		return contractRoundsWireEvent(t, "", map[string]any{
			"id": "synthetic-chat-id", "object": "chat.completion.chunk", "model": "synthetic-model",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}, "finish_reason": nil}},
		})
	}
}

func contractRoundsStopEvent(t *testing.T, protocol string) string {
	t.Helper()
	switch protocol {
	case transformProtocolOpenAIResponses:
		return contractRoundsWireEvent(t, "response.completed", map[string]any{
			"type": "response.completed", "response": map[string]any{"id": "synthetic-response-id", "status": "completed", "output": []any{}},
		})
	case transformProtocolAnthropicMessages:
		return contractRoundsWireEvent(t, "message_stop", map[string]any{"type": "message_stop"})
	default:
		return contractRoundsWireEvent(t, "", map[string]any{
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		})
	}
}

func contractRoundsStream(t *testing.T, protocol string, done bool) string {
	t.Helper()
	stream := ": synthetic keepalive\n\n" +
		contractRoundsTextEvent(t, protocol, "work=/__vmi1_work") +
		contractRoundsTextEvent(t, protocol, "space__/README.md; home=/__vmi1_home__; user=⟪vmi1_user⟫") +
		contractRoundsStopEvent(t, protocol)
	if done {
		stream += "data: [DONE]\n\n"
	}
	return stream
}

func contractRoundsEvents(t *testing.T, body string) []ssewire.Event {
	t.Helper()
	decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	events, err := decoder.Feed([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := decoder.Finish(); err != nil {
		t.Fatal(err)
	}
	return events
}

func contractRoundsStreamText(t *testing.T, protocol, body string) string {
	t.Helper()
	var joined strings.Builder
	for _, event := range contractRoundsEvents(t, body) {
		if string(event.Data) == "[DONE]" {
			continue
		}
		value := contractRoundsDecode(t, string(event.Data))
		switch protocol {
		case transformProtocolOpenAIResponses:
			if value["type"] == "response.output_text.delta" {
				joined.WriteString(value["delta"].(string))
			}
		case transformProtocolAnthropicMessages:
			if value["type"] == "content_block_delta" {
				joined.WriteString(value["delta"].(map[string]any)["text"].(string))
			}
		default:
			for _, raw := range value["choices"].([]any) {
				choice := raw.(map[string]any)
				if delta, ok := choice["delta"].(map[string]any); ok {
					if text, ok := delta["content"].(string); ok {
						joined.WriteString(text)
					}
				}
			}
		}
	}
	return joined.String()
}

// Exercise exactly the production endpoint parser and problem encoder without
// depending on storage or any installed Runtime credentials.
func contractRoundsHTTP(t *testing.T, input MessageTransformTestInput) *httptest.ResponseRecorder {
	t.Helper()
	body := contractRoundsJSON(t, input)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/message-transforms/actions/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	(&Handler{}).testMessageTransform(recorder, req)
	return recorder
}

func contractRoundsResult(t *testing.T, input MessageTransformTestInput) MessageTransformTestResult {
	t.Helper()
	response := contractRoundsHTTP(t, input)
	if response.Code != http.StatusOK {
		t.Fatalf("HTTP status=%d problem=%s", response.Code, response.Body.String())
	}
	var result MessageTransformTestResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.RequestBefore, input.Sample.Request) || !reflect.DeepEqual(result.ResponseBefore, input.Sample.Response) {
		t.Fatal("before snapshots differ from the supplied JSON wire copy")
	}
	if result.WireProtocol != input.WireProtocol || result.RequestAfter.Method != input.Sample.Request.Method || result.RequestAfter.Path != input.Sample.Request.Path ||
		result.ResponseAfter.StatusCode != input.Sample.Response.StatusCode || result.ResponseAfter.Streaming != input.Sample.Response.Streaming {
		t.Fatal("unrelated protocol envelope changed")
	}
	if result.RequestAfter.Headers.Get("X-Fixture") != "request-unchanged" || result.ResponseAfter.Headers.Get("X-Request-ID") != "synthetic-response-id" {
		t.Fatal("unrelated header values changed")
	}
	request := contractRoundsDecode(t, result.RequestAfter.Body)
	user := input.Sample.Runtime.UserName
	if request["model"] != user+"-synthetic-model" || request["metadata"].(map[string]any)["routing"] != user {
		t.Fatal("model or opaque routing metadata changed")
	}
	return result
}

func TestTransformContractRoundsOneFullScriptsHTTP(t *testing.T) {
	for _, protocol := range contractRoundsProtocols {
		for _, spelling := range []string{"content-type", "Content-Type", "CONTENT-TYPE", "cOnTeNt-TyPe"} {
			for _, done := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/done_%t", protocol, spelling, done), func(t *testing.T) {
					input := contractRoundsInput(t, protocol, "alice")
					delete(input.Sample.Response.Headers, "content-type")
					input.Sample.Response.Headers[spelling] = []string{"text/event-stream"}
					input.Sample.Response.Body = contractRoundsStream(t, protocol, done)
					result := contractRoundsResult(t, input)
					if !strings.Contains(result.RequestAfter.Body, "/__vmi1_workspace__") || !strings.Contains(result.RequestAfter.Body, "⟪vmi1_user⟫") || strings.Contains(result.RequestAfter.Body, "/Users/alice") {
						t.Fatal("request identity replacement failed")
					}
					want := "work=/Users/alice/work/project/README.md; home=/Users/alice; user=alice"
					if got := contractRoundsStreamText(t, protocol, result.ResponseAfter.Body); got != want {
						t.Fatalf("reassembled client text=%q, want=%q", got, want)
					}
					before := contractRoundsEvents(t, input.Sample.Response.Body)
					after := contractRoundsEvents(t, result.ResponseAfter.Body)
					if len(before) != len(after) {
						t.Fatal("SSE event count changed")
					}
					for index := range before {
						if before[index].Name != after[index].Name {
							t.Fatal("SSE event name changed")
						}
					}
					if done && string(after[len(after)-1].Data) != "[DONE]" {
						t.Fatal("DONE did not survive JSON/HTTP round trip")
					}
				})
			}
		}
	}
}

func TestTransformContractRoundsTwoHeaderAndTerminationBoundaries(t *testing.T) {
	for _, protocol := range contractRoundsProtocols {
		for _, edit := range []string{"add", "remove", "replace", "framing_and_validators"} {
			t.Run(protocol+"/stable_"+edit, func(t *testing.T) {
				input := contractRoundsInput(t, protocol, "alice")
				input.Sample.Response.Headers["x-edit"] = []string{"before"}
				input.Sample.Response.Body = contractRoundsStream(t, protocol, true)
				switch edit {
				case "add":
					input.Policy.ResponseJavaScript += `response.headers["x-added"] = ["one", "two"];`
				case "remove":
					input.Policy.ResponseJavaScript += `delete response.headers["x-edit"];`
				case "replace":
					input.Policy.ResponseJavaScript += `response.headers["x-edit"] = ["after"];`
				case "framing_and_validators":
					// A body-changing data event establishes sanitized headers; DONE
					// must not resurrect the upstream framing or validators.
					input.Sample.Response.Body = contractRoundsTextEvent(t, protocol, "/__vmi1_workspace__/README.md") + "data: [DONE]\n\n"
					for _, header := range []string{"content-length", "transfer-encoding", "connection", "content-encoding", "content-md5", "digest", "etag"} {
						input.Sample.Response.Headers[header] = []string{"synthetic-stale"}
					}
				}
				result := contractRoundsResult(t, input)
				switch edit {
				case "add":
					if !reflect.DeepEqual(result.ResponseAfter.Headers.Values("X-Added"), []string{"one", "two"}) {
						t.Fatal("DONE lost stable added values or order")
					}
				case "remove":
					if _, exists := result.ResponseAfter.Headers["X-Edit"]; exists {
						t.Fatal("DONE restored deleted header")
					}
				case "replace":
					if result.ResponseAfter.Headers.Get("X-Edit") != "after" {
						t.Fatal("DONE restored replaced header")
					}
				case "framing_and_validators":
					for _, header := range []string{"Content-Length", "Transfer-Encoding", "Connection", "Content-Encoding", "Content-MD5", "Digest", "ETag"} {
						if result.ResponseAfter.Headers.Get(header) != "" {
							t.Fatalf("DONE restored core-owned header %s", header)
						}
					}
				}
			})
		}
		for _, mutation := range []string{"value", "add", "delete", "order"} {
			t.Run(protocol+"/reject_midstream_"+mutation, func(t *testing.T) {
				input := contractRoundsInput(t, protocol, "alice")
				input.Sample.Response.Headers["x-edit"] = []string{"before"}
				input.Sample.Response.Body = contractRoundsStream(t, protocol, true)
				input.Policy.ResponseJavaScript += `context.contractEvents = (context.contractEvents || 0) + 1;`
				switch mutation {
				case "value":
					input.Policy.ResponseJavaScript += `response.headers["x-edit"] = [String(context.contractEvents)];`
				case "add":
					input.Policy.ResponseJavaScript += `if (context.contractEvents > 1) response.headers["x-new"] = ["added"];`
				case "delete":
					input.Policy.ResponseJavaScript += `if (context.contractEvents > 1) delete response.headers["x-edit"];`
				case "order":
					input.Policy.ResponseJavaScript += `response.headers["x-edit"] = context.contractEvents === 1 ? ["a", "b"] : ["b", "a"];`
				}
				response := contractRoundsHTTP(t, input)
				if response.Code != 422 || !strings.Contains(response.Body.String(), "response · invalid streaming sample") {
					t.Fatalf("genuine header mutation accepted: status=%d body=%s", response.Code, response.Body.String())
				}
			})
		}
		for _, ending := range []string{"done_only", "twice_done", "explicit_stop_rejects_partial_alias", "provider_error", "truncated_without_terminal_known_limit"} {
			t.Run(protocol+"/"+ending, func(t *testing.T) {
				input := contractRoundsInput(t, protocol, "alice")
				switch ending {
				case "done_only":
					input.Sample.Response.Body = "data: [DONE]\n\n"
				case "twice_done":
					input.Sample.Response.Body = contractRoundsStream(t, protocol, true) + "data: [DONE]\n\n"
				case "explicit_stop_rejects_partial_alias":
					input.Sample.Response.Body = contractRoundsTextEvent(t, protocol, "prefix /__vmi1_work") + contractRoundsStopEvent(t, protocol)
				case "provider_error":
					input.Sample.Response.StatusCode = 502
					input.Sample.Response.Body = "event: error\ndata: {\"error\":\"synthetic-provider-unavailable\"}\n\ndata: [DONE]\n\n"
				case "truncated_without_terminal_known_limit":
					input.Sample.Response.Body = contractRoundsTextEvent(t, protocol, "prefix /__vmi1_work")
				}
				response := contractRoundsHTTP(t, input)
				if ending == "explicit_stop_rejects_partial_alias" {
					if response.Code != 422 || !strings.Contains(response.Body.String(), "response · JavaScript execution failed") {
						t.Fatalf("unfinished alias was not rejected at explicit terminal: status=%d", response.Code)
					}
					return
				}
				if response.Code != 200 {
					t.Fatalf("status=%d problem=%s", response.Code, response.Body.String())
				}
				var result MessageTransformTestResult
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if ending == "provider_error" && (result.ResponseAfter.StatusCode != 502 || result.ResponseAfter.Body != input.Sample.Response.Body) {
					t.Fatal("non-success provider response changed")
				}
				if ending == "done_only" && !reflect.DeepEqual(result.ResponseAfter, input.Sample.Response) {
					t.Fatal("terminal-only response changed despite bypassing JS")
				}
				if ending == "truncated_without_terminal_known_limit" && contractRoundsStreamText(t, protocol, result.ResponseAfter.Body) != "prefix /" {
					t.Fatal("the documented no-finalize boundary changed; reassess EOF expectations")
				}
			})
		}
	}
}

func TestTransformContractRoundsThreeHTTPAndIsolation(t *testing.T) {
	for index := 0; index < 12; index++ {
		t.Run(fmt.Sprintf("independent_parallel_user_%02d", index), func(t *testing.T) {
			t.Parallel()
			user := fmt.Sprintf("syntheticuser%02d", index)
			protocol := contractRoundsProtocols[index%len(contractRoundsProtocols)]
			input := contractRoundsInput(t, protocol, user)
			input.Sample.Response.Body = contractRoundsStream(t, protocol, true)
			result := contractRoundsResult(t, input)
			want := "work=/Users/" + user + "/work/project/README.md; home=/Users/" + user + "; user=" + user
			if got := contractRoundsStreamText(t, protocol, result.ResponseAfter.Body); got != want {
				t.Fatalf("independent turn used another identity: got=%q", got)
			}
		})
	}
	for _, failure := range []string{"missing_metadata", "reserved_alias", "invalid_request_json", "invalid_response_json", "javascript_throw", "invalid_script", "invalid_method", "invalid_path", "invalid_status"} {
		t.Run("sanitized_"+failure, func(t *testing.T) {
			input := contractRoundsInput(t, transformProtocolOpenAIResponses, "alice")
			input.Sample.Response.Body = contractRoundsStream(t, input.WireProtocol, true)
			switch failure {
			case "missing_metadata":
				input.Sample.Runtime.HomeDirectory = ""
				input.Sample.Request.Body = `{"input":"` + contractRoundsSecret + `"}`
			case "reserved_alias":
				input.Sample.Request.Body = `{"input":"/__vmi1_workspace__/` + contractRoundsSecret + `"}`
			case "invalid_request_json":
				input.Sample.Request.Body = `{"input":"` + contractRoundsSecret
			case "invalid_response_json":
				input.Sample.Response.Body = "data: {\"output\":\"" + contractRoundsSecret + "\n\n"
			case "javascript_throw":
				input.Policy.ResponseJavaScript += `throw new Error("` + contractRoundsSecret + `");`
			case "invalid_script":
				input.Policy.ResponseJavaScript += `const ` + contractRoundsSecret
			case "invalid_method":
				input.Sample.Request.Method = "GET"
			case "invalid_path":
				input.Sample.Request.Path = "/" + contractRoundsSecret
			case "invalid_status":
				input.Sample.Response.StatusCode = 600
			}
			response := contractRoundsHTTP(t, input)
			if response.Code != 422 || !strings.Contains(response.Body.String(), `"code":"message_transform_test_failed"`) || strings.Contains(response.Body.String(), contractRoundsSecret) || strings.Contains(response.Body.String(), "/Users/alice") {
				t.Fatalf("unsafe or missing error response: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	for _, test := range []struct{ name, body, contentType string }{
		{"unknown_field", `{"wireProtocol":"openai_responses","unknown":"` + contractRoundsSecret + `"}`, "application/json"},
		{"invalid_json", `{"wireProtocol":"` + contractRoundsSecret, "application/json"},
		{"null_body", `null`, "application/json"},
		{"empty_body", "", "application/json"},
		{"trailing_document", `{"wireProtocol":"openai_responses","policy":{}} {"private":"` + contractRoundsSecret + `"}`, "application/json"},
		{"wrong_headers_shape", `{"wireProtocol":"openai_responses","sample":{"request":{"headers":{"x-private":"` + contractRoundsSecret + `"}}}}`, "application/json"},
		{"oversized_body", strings.Repeat(" ", maxControlBodyBytes+1) + contractRoundsSecret, "application/json"},
		{"wrong_content_type", `{"wireProtocol":"openai_responses","policy":{}}`, "text/plain"},
	} {
		t.Run("strict_input_"+test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/message-transforms/actions/test", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			(&Handler{}).testMessageTransform(response, request)
			if response.Code != 422 || strings.Contains(response.Body.String(), contractRoundsSecret) {
				t.Fatalf("strict input was accepted or reflected: status=%d", response.Code)
			}
		})
	}
}

type contractRoundsClock struct{ now time.Time }

func (clock contractRoundsClock) Now() time.Time { return clock.now }

type contractRoundsManualRejector struct{}

func (contractRoundsManualRejector) ServeHTTP(writer http.ResponseWriter, _ *http.Request, _ controlprincipal.Principal) {
	writer.WriteHeader(http.StatusServiceUnavailable)
}

func TestTransformContractRoundsThreeRouterAuthentication(t *testing.T) {
	// Mirror router_integration_test.go's synthetic capability/transport fixture,
	// with only the actual transform route registered so no Runtime is started.
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	readToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x35}, 32))
	writeToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x36}, 32))
	auth, err := NewAuthenticator(CapabilityGrant{ReadToken: readToken, WriteToken: writeToken, ExpiresAt: now.Add(time.Hour)}, contractRoundsClock{now})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := controlprincipal.New(controlprincipal.Attributes{
		ID: "desktop-app:contract-synthetic", Kind: controlprincipal.KindDesktopApp, CredentialRevision: 1,
		AllowedGrantKinds: []controlprincipal.GrantKind{controlprincipal.GrantManualCapture},
	})
	if err != nil {
		t.Fatal(err)
	}
	application := &Handler{mux: http.NewServeMux()}
	application.mux.HandleFunc("POST /api/v1/message-transforms/actions/test", application.testMessageTransform)
	stub := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(503) })
	const authority = "127.0.0.1:43199"
	router, err := NewRouter(RouterOptions{
		Authority: authority, AllowedOrigins: []string{"vibermate://desktop"}, Authenticator: auth, Application: application,
		Bootstrap: stub, CLIControl: stub, ManualCaptures: contractRoundsManualRejector{}, DesktopPrincipal: principal,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, token, origin, remote string
		want                        int
	}{
		{"write_capability", writeToken, "vibermate://desktop", "127.0.0.1:50000", 200},
		{"read_cannot_run_test", readToken, "vibermate://desktop", "127.0.0.1:50000", 401},
		{"no_capability", "", "vibermate://desktop", "127.0.0.1:50000", 401},
		{"invalid_capability", contractRoundsSecret, "vibermate://desktop", "127.0.0.1:50000", 401},
		{"wrong_origin", writeToken, "https://synthetic-attacker.invalid", "127.0.0.1:50000", 403},
		{"non_loopback", writeToken, "vibermate://desktop", "192.0.2.1:50000", 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := contractRoundsInput(t, transformProtocolOpenAIResponses, "alice")
			input.Sample.Response.Body = contractRoundsStream(t, input.WireProtocol, true)
			request := httptest.NewRequest(http.MethodPost, "http://"+authority+"/api/v1/message-transforms/actions/test", strings.NewReader(contractRoundsJSON(t, input)))
			request.Host, request.RemoteAddr = authority, test.remote
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Sec-Fetch-Site", "cross-site")
			request.Header.Set("Sec-Fetch-Mode", "cors")
			request.Header.Set("Sec-Fetch-Dest", "empty")
			request.Header.Set("Content-Type", "application/json")
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d, want=%d", response.Code, test.want)
			}
			for _, secret := range []string{readToken, writeToken, contractRoundsSecret} {
				if strings.Contains(response.Body.String(), secret) {
					t.Fatal("authentication response reflected a synthetic credential")
				}
			}
			if test.want == 200 && request.Header.Get("Authorization") != "" {
				t.Fatal("capability was not consumed at the router boundary")
			}
		})
	}
}

func TestTransformContractRoundsThreeJSONNonStreaming(t *testing.T) {
	for _, protocol := range contractRoundsProtocols {
		for _, unchanged := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unchanged_%t", protocol, unchanged), func(t *testing.T) {
				input := contractRoundsInput(t, protocol, "alice")
				input.Sample.Response.Streaming = false
				input.Sample.Response.Headers["content-type"] = []string{"application/json"}
				text := "/__vmi1_workspace__/README.md; home=/__vmi1_home__; user=⟪vmi1_user⟫"
				if unchanged {
					text = "ordinary text with trailing slash / and safe integer 9007199254740991"
				}
				var payload map[string]any
				switch protocol {
				case transformProtocolOpenAIResponses:
					payload = map[string]any{"id": "synthetic-response-id", "model": "alice-model-id", "status": "completed", "usage": map[string]any{"total_tokens": 11}, "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": text}}}}}
				case transformProtocolAnthropicMessages:
					payload = map[string]any{"id": "synthetic-response-id", "type": "message", "model": "alice-model-id", "stop_reason": "end_turn", "usage": map[string]any{"output_tokens": 11}, "content": []any{map[string]any{"type": "text", "text": text}}}
				default:
					payload = map[string]any{"id": "synthetic-response-id", "model": "alice-model-id", "usage": map[string]any{"total_tokens": 11}, "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}}
				}
				input.Sample.Response.Body = "\n " + contractRoundsJSON(t, payload) + "\t\n"
				result := contractRoundsResult(t, input)
				if unchanged && result.ResponseAfter.Body != input.Sample.Response.Body {
					t.Fatal("unchanged non-streaming body lost byte-for-byte formatting")
				}
				if !unchanged && (!strings.Contains(result.ResponseAfter.Body, "/Users/alice/work/project/README.md") || strings.Contains(result.ResponseAfter.Body, "/__vmi1_")) {
					t.Fatal("non-streaming response failed to restore identity")
				}
				after := contractRoundsDecode(t, result.ResponseAfter.Body)
				if after["id"] != payload["id"] || after["model"] != payload["model"] || !reflect.DeepEqual(after["usage"], contractRoundsDecode(t, contractRoundsJSON(t, payload))["usage"]) {
					t.Fatal("response IDs, model, or usage changed")
				}
			})
		}
	}
}

func TestTransformContractRoundsThreeDirectContextReplay(t *testing.T) {
	// Each call compiles the real complete files and starts a fresh turn. An
	// unrelated prior run must not determine the next user's response mappings.
	for _, user := range []string{"alice", "bob", "alice"} {
		t.Run(user, func(t *testing.T) {
			input := contractRoundsInput(t, transformProtocolOpenAIResponses, user)
			input.Sample.Request.Body = contractRoundsJSON(t, map[string]any{"input": "continue", "stream": true})
			input.Sample.Response.Body = contractRoundsStream(t, input.WireProtocol, true)
			result, err := runMessageTransformSample(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if got := contractRoundsStreamText(t, input.WireProtocol, result.ResponseAfter.Body); got != "work=/Users/"+user+"/work/project/README.md; home=/Users/"+user+"; user="+user {
				t.Fatal("mapping requires a request hit or leaked from previous test run")
			}
		})
	}
}

func TestTransformContractRoundsThreeToolArgumentsJSON(t *testing.T) {
	for _, protocol := range contractRoundsProtocols {
		t.Run(protocol, func(t *testing.T) {
			input := contractRoundsInput(t, protocol, "alice")
			input.Sample.Response.Streaming = false
			input.Sample.Response.Headers["content-type"] = []string{"application/json"}
			original := map[string]any{
				"command": "cd /Users/alice/work/project && echo alice", "cwd": "/Users/alice",
				"options": []any{true, nil, 7, map[string]any{"path": "/Users/alice/work/project/README.md", "ordinary": "malice"}},
			}
			masked := map[string]any{
				"command": "cd /__vmi1_workspace__ && echo ⟪vmi1_user⟫", "cwd": "/__vmi1_home__",
				"options": []any{true, nil, 7, map[string]any{"path": "/__vmi1_workspace__/README.md", "ordinary": "malice"}},
			}
			request := contractRoundsDecode(t, input.Sample.Request.Body)
			var response map[string]any
			switch protocol {
			case transformProtocolOpenAIResponses:
				request["input"] = []any{map[string]any{"type": "function_call", "name": "alice_tool", "call_id": "alice-call-id", "arguments": contractRoundsJSON(t, original)}}
				response = map[string]any{"output": []any{map[string]any{"type": "function_call", "name": "alice_tool", "call_id": "alice-call-id", "arguments": contractRoundsJSON(t, masked)}}}
			case transformProtocolAnthropicMessages:
				request["messages"] = []any{map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "name": "alice_tool", "id": "alice-call-id", "input": original}}}}
				response = map[string]any{"type": "message", "content": []any{map[string]any{"type": "tool_use", "name": "alice_tool", "id": "alice-call-id", "input": masked}}}
			default:
				request["messages"] = []any{map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"type": "function", "id": "alice-call-id", "function": map[string]any{"name": "alice_tool", "arguments": contractRoundsJSON(t, original)}}}}}
				response = map[string]any{"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"type": "function", "id": "alice-call-id", "function": map[string]any{"name": "alice_tool", "arguments": contractRoundsJSON(t, masked)}}}}, "finish_reason": "tool_calls"}}}
			}
			input.Sample.Request.Body = contractRoundsJSON(t, request)
			input.Sample.Response.Body = contractRoundsJSON(t, response)
			result := contractRoundsResult(t, input)
			if strings.Contains(result.RequestAfter.Body, "/Users/alice") || !strings.Contains(result.RequestAfter.Body, "/__vmi1_workspace__") {
				t.Fatal("tool argument values were not masked at the HTTP test boundary")
			}
			getArgs := func(body string, isResponse bool) map[string]any {
				value := contractRoundsDecode(t, body)
				switch protocol {
				case transformProtocolOpenAIResponses:
					field := "input"
					if isResponse {
						field = "output"
					}
					call := value[field].([]any)[0].(map[string]any)
					if call["name"] != "alice_tool" || call["call_id"] != "alice-call-id" {
						t.Fatal("Responses tool name or call ID changed")
					}
					return contractRoundsDecode(t, call["arguments"].(string))
				case transformProtocolAnthropicMessages:
					if !isResponse {
						value = value["messages"].([]any)[0].(map[string]any)
					}
					call := value["content"].([]any)[0].(map[string]any)
					if call["name"] != "alice_tool" || call["id"] != "alice-call-id" {
						t.Fatal("Anthropic tool name or call ID changed")
					}
					return call["input"].(map[string]any)
				default:
					if isResponse {
						value = value["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
					} else {
						value = value["messages"].([]any)[0].(map[string]any)
					}
					call := value["tool_calls"].([]any)[0].(map[string]any)
					function := call["function"].(map[string]any)
					if function["name"] != "alice_tool" || call["id"] != "alice-call-id" {
						t.Fatal("Chat tool name or call ID changed")
					}
					return contractRoundsDecode(t, function["arguments"].(string))
				}
			}
			if !reflect.DeepEqual(getArgs(result.RequestAfter.Body, false), contractRoundsDecode(t, contractRoundsJSON(t, masked))) ||
				!reflect.DeepEqual(getArgs(result.ResponseAfter.Body, true), contractRoundsDecode(t, contractRoundsJSON(t, original))) {
				t.Fatal("tool JSON string values did not round trip, or non-string data/keys changed")
			}
		})
	}
}

func TestTransformContractRoundsMetadataFirstValidatorAudit(t *testing.T) {
	for _, protocol := range contractRoundsProtocols {
		for _, header := range []string{"etag", "content-encoding", "content-md5", "digest", "content-length", "transfer-encoding"} {
			alternating := []byte(header)
			for index, value := range alternating {
				if index%2 == 0 && value >= 'a' && value <= 'z' {
					alternating[index] = value - 'a' + 'A'
				}
			}
			for _, spelling := range []struct{ name, value string }{
				{"lower", header}, {"canonical", http.CanonicalHeaderKey(header)},
				{"upper", strings.ToUpper(header)}, {"alternating", string(alternating)},
			} {
				for _, changed := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/body_changed_%t", protocol, header, spelling.name, changed), func(t *testing.T) {
						input := contractRoundsInput(t, protocol, "alice")
						input.Sample.Response.Headers[spelling.value] = []string{"synthetic-upstream-representation"}
						var first string
						switch protocol {
						case transformProtocolOpenAIResponses:
							first = contractRoundsWireEvent(t, "response.created", map[string]any{
								"type": "response.created", "response": map[string]any{"status": "in_progress", "output": []any{}},
							})
						case transformProtocolAnthropicMessages:
							first = contractRoundsWireEvent(t, "message_start", map[string]any{
								"type": "message_start", "message": map[string]any{"type": "message", "content": []any{}},
							})
						default:
							first = contractRoundsWireEvent(t, "", map[string]any{
								"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}},
							})
						}
						if changed {
							input.Sample.Response.Body = first + contractRoundsStream(t, protocol, true)
						} else {
							input.Sample.Response.Body = first + contractRoundsTextEvent(t, protocol, "ordinary sample") + contractRoundsStopEvent(t, protocol) + "data: [DONE]\n\n"
						}
						result := contractRoundsResult(t, input)
						for field := range result.ResponseAfter.Headers {
							if strings.EqualFold(field, header) {
								t.Fatalf("core-owned representation/framing header survived: %s", field)
							}
						}
						want := "ordinary sample"
						if changed {
							want = "work=/Users/alice/work/project/README.md; home=/Users/alice; user=alice"
						}
						if got := contractRoundsStreamText(t, protocol, result.ResponseAfter.Body); got != want {
							t.Fatalf("response changed unexpectedly: got=%q, want=%q", got, want)
						}
					})
				}
			}
		}
	}
}
