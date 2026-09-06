package exchange

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
)

// This exercises the real pipeline, credential finalization, HTTP serialization
// and Raw observer together. Only the upstream socket and secret reader are
// local fixtures; no real account or external inference is used.
func TestManagedChatGPTHTTPStreamWithoutContentType(t *testing.T) {
	for _, test := range []struct {
		name      string
		recording environment.ContentRecordingMode
		rawLimit  int
	}{
		{"full response retained", environment.ContentRecordingFull, 0},
		{"retention cap does not truncate delivery", environment.ContentRecordingFull, 64 << 10},
		{"recording off", environment.ContentRecordingOff, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := chatGPTLargeStream(t)
			observed := make(chan *http.Request, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Error(err)
				}
				copied := request.Clone(context.Background())
				copied.Body = io.NopCloser(bytes.NewReader(body))
				observed <- copied
				// A nil entry prevents net/http from synthesizing Content-Type.
				writer.Header()["Content-Type"] = nil
				writer.WriteHeader(http.StatusOK)
				for offset := 0; offset < len(wire); offset += 4093 {
					end := min(offset+4093, len(wire))
					if _, err := writer.Write(wire[offset:end]); err != nil {
						return
					}
					writer.(http.Flusher).Flush()
				}
			}))
			defer server.Close()

			account := testAccount{id: "account.selected", revision: 3, epoch: 7}
			policy := environment.DefaultContentRecordingPolicy()
			if test.recording == environment.ContentRecordingOff {
				policy = environment.ContentRecordingPolicy{Mode: test.recording}
			}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
				destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com",
				backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
				accounts: []testAccount{account}, preferred: account.id,
				recording: policy,
				transform: messagetransform.Policy{
					RequestJavaScript: `
						if (request.headers["session-id"][0] !== "session-fixture") throw new Error("session header lost");
						if (request.headers.authorization !== undefined || request.headers.cookie !== undefined) throw new Error("old credential exposed");
						request.headers["x-codex-beta-features"] = "script-beta";
						request.headers["x-request-transformed"] = "yes";
						context.marker = "request-ran";
					`,
					ResponseJavaScript: `response.headers["x-response-transformed"] = context.marker;`,
				},
			})
			content := &contentObserverDouble{}
			pipeline := newTestPipelineWithContentObserver(t, newAccountAuthority(t, account),
				&providerDouble{}, approvedDecisions(), &attemptObserverDouble{}, content)
			defer shutdownPipeline(t, pipeline)
			raw := &rawObserverDouble{}
			pipeline.rawEvidence = raw
			// Synthetic unsigned token, never a usable credential.
			token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(
				`{"https://api.openai.com/auth":{"chatgpt_account_id":"selected-account"}}`)) + ".fixture"
			material, err := providerauth.NewMaterial(token,
				map[string]string{"X-Codex-Beta-Features": "account-beta"}, []string{"X-OpenAI-Subagent"})
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
			upstreamURL, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			client, err := providertransport.NewClient(providertransport.ClientOptions{
				Coordinator: pipeline.actions.(offlinehold.Coordinator), Authenticator: authenticator,
				Transport: chatGPTLoopbackTransport{server.Client().Transport, upstreamURL}, RawEvidence: raw,
				RawResponseBodyBytes: test.rawLimit,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := client.Shutdown(ctx); err != nil {
					t.Error(err)
				}
			}()
			pipeline.provider = client

			body := []byte(`{"model":"codex-client-alias","stream":true,"store":false,"input":[{"type":"message","role":"user","content":"hello"}]}`)
			headers := chatGPTClientHeaderFixture()
			headers.Set("Content-Length", "999999") // must be recomputed, never copied.
			downstream := &downstreamRecorder{}
			result, err := pipeline.Execute(context.Background(), mustClientRequestWithOptions(t,
				"exchange-native-http", plan, body, WithOriginalHeaders(headers),
				WithClientUserAgent("codex-tui/0.153.4 (fixture)")), downstream)
			if err != nil {
				t.Fatalf("native stream failed: %v", err)
			}
			if result.Outcome != AttemptSucceeded || !result.Ledger.DownstreamTerminal ||
				!bytes.Equal(downstream.bytesSnapshot(), wire) || len(downstream.abortsSnapshot()) != 0 {
				t.Fatalf("stream was not delivered through its terminal: %+v, bytes=%d/%d", result,
					len(downstream.bytesSnapshot()), len(wire))
			}
			outbound := <-observed
			outboundBody, err := io.ReadAll(outbound.Body)
			if err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string]string{
				"Accept": "text/event-stream", "Content-Type": "application/json",
				"Authorization": "Bearer " + token, "Chatgpt-Account-Id": "selected-account",
				"Originator": "codex-tui", "Version": "0.153.4",
				"Session-Id": "session-fixture", "Thread-Id": "thread-fixture",
				"X-Client-Request-Id": "request-fixture", "X-Codex-Beta-Features": "account-beta",
				"X-Codex-Routing-Hint": "model=codex-client-alias", "X-Request-Transformed": "yes",
				"User-Agent": "codex-tui/0.153.4 (fixture)",
			} {
				if outbound.Header.Get(name) != want {
					t.Errorf("outgoing %s did not preserve protocol / selected account policy", name)
				}
			}
			for _, name := range []string{"Cookie", "X-Codex-Turn-State", "X-OpenAI-Subagent", "X-Private-Client"} {
				if outbound.Header.Get(name) != "" {
					t.Errorf("outgoing %s leaked client state or bypassed Delete", name)
				}
			}
			if outbound.URL.Path != "/backend-api/codex/responses" || outbound.Host != "chatgpt.com" ||
				outbound.ContentLength != int64(len(outboundBody)) {
				t.Fatal("outgoing HTTP destination or content length differs from the frozen request")
			}
			envelopes := downstream.envelopesSnapshot()
			if len(envelopes) != 1 || envelopes[0].Headers().Get("Content-Type") != "text/event-stream" ||
				envelopes[0].Headers().Get("X-Response-Transformed") != "request-ran" {
				t.Fatal("response transforms or normalized downstream stream headers were lost")
			}
			if test.recording == environment.ContentRecordingOff {
				if len(raw.snapshot()) != 0 {
					t.Fatal("recording-off stream retained raw bytes")
				}
				if _, ok := content.latest(); ok {
					t.Fatal("recording-off stream retained conversation content")
				}
				return
			}
			var responseRecorded, requestRecorded bool
			for _, observation := range raw.snapshot() {
				switch observation.Layer {
				case rawevidence.LayerProviderResponse:
					responseRecorded = true
					if observation.Headers.Get("Content-Type") != "" || !observation.FullDigestAvailable ||
						observation.TotalBodyBytes != int64(len(wire)) {
						t.Fatal("raw response did not observe EOF or invented an upstream Content-Type")
					}
					if test.rawLimit == 0 {
						if !observation.Complete || !bytes.Equal(observation.Body, wire) || observation.IncompleteReason != "" {
							t.Fatal("full retention did not preserve the complete upstream response")
						}
					} else if observation.Complete || !bytes.Equal(observation.Body, wire[:test.rawLimit]) ||
						observation.IncompleteReason != "response_payload_limit" {
						t.Fatal("retention cap did not retain an explicit prefix of the fully delivered response")
					}
				case rawevidence.LayerProviderEgress:
					requestRecorded = true
					if observation.Headers.Get("Content-Length") != strconv.Itoa(len(outboundBody)) ||
						observation.Headers.Get("Session-Id") != outbound.Header.Get("Session-Id") ||
						observation.Headers.Get("X-Codex-Beta-Features") != "account-beta" {
						t.Fatal("raw request headers do not match the serialized HTTP request")
					}
				}
			}
			if !responseRecorded || !requestRecorded {
				t.Fatal("provider request/response raw evidence is missing")
			}
		})
	}
}

func TestManagedChatGPTStreamMediaTypeBoundary(t *testing.T) {
	for _, test := range []struct {
		name, origin  string
		header        http.Header
		body          []byte
		wantSuccess   bool
		wantTypeError bool
	}{
		{name: "native missing header", origin: "https://chatgpt.com", wantSuccess: true},
		{name: "native backend base", origin: "https://chatgpt.com/backend-api", wantSuccess: true},
		{name: "native codex base", origin: "https://chatgpt.com/backend-api/codex", wantSuccess: true},
		{name: "native explicit SSE", origin: "https://chatgpt.com", header: http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}}, wantSuccess: true},
		{name: "standard explicit SSE", origin: "https://api.openai.com", header: http.Header{"Content-Type": {"text/event-stream"}}, wantSuccess: true},
		{name: "explicit HTML never inferred", origin: "https://chatgpt.com", header: http.Header{"Content-Type": {"text/html"}}, wantTypeError: true},
		{name: "explicit JSON never inferred", origin: "https://chatgpt.com", header: http.Header{"Content-Type": {"application/json"}}, wantTypeError: true},
		{name: "empty is not missing", origin: "https://chatgpt.com", header: http.Header{"Content-Type": {""}}, wantTypeError: true},
		{name: "conflicting types", origin: "https://chatgpt.com", header: http.Header{"Content-Type": {"text/event-stream", "text/html"}}, wantTypeError: true},
		{name: "standard missing header", origin: "https://api.openai.com", wantTypeError: true},
		{name: "lookalike host", origin: "https://chatgpt.com.example", wantTypeError: true},
		{name: "other API path", origin: "https://chatgpt.com/other", wantTypeError: true},
		{name: "missing type HTML body", origin: "https://chatgpt.com", body: []byte("<html>not an event stream</html>")},
		{name: "missing type JSON body", origin: "https://chatgpt.com", body: []byte(`{"error":{"message":"not an event stream"}}`)},
		{name: "missing terminal", origin: "https://chatgpt.com", body: []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"codex-client-alias\",\"status\":\"in_progress\"}}\n\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := testAccount{id: "account.selected", revision: 3, epoch: 7}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
				destination: environment.DestinationKindUpstream, providerOrigin: test.origin,
				backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
				accounts: []testAccount{account}, preferred: account.id,
			})
			wire := test.body
			if wire == nil {
				wire = chatGPTLargeStream(t)
			}
			provider := &providerDouble{results: []providerResult{{response: &http.Response{
				StatusCode: http.StatusOK, Header: test.header,
				Body: io.NopCloser(&boundedChunkReader{reader: bytes.NewReader(wire), maximum: 97}),
			}}}}
			pipeline := newTestPipeline(t, newAccountAuthority(t, account), provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			downstream := &downstreamRecorder{}
			result, err := pipeline.Execute(context.Background(), mustClientRequest(t, "exchange-media-type", plan,
				[]byte(`{"model":"codex-client-alias","stream":true,"input":[{"type":"message","role":"user","content":"hello"}]}`)), downstream)
			if test.wantSuccess {
				if err != nil || result.Outcome != AttemptSucceeded || !bytes.Equal(downstream.bytesSnapshot(), wire) {
					t.Fatalf("expected complete SSE, got outcome=%s err=%v bytes=%d/%d", result.Outcome, err, len(downstream.bytesSnapshot()), len(wire))
				}
				return
			}
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != ReasonProviderResponseInvalid ||
				result.Outcome == AttemptSucceeded || result.Ledger.DownstreamTerminal {
				t.Fatalf("invalid stream was accepted: %+v err=%v", result, err)
			}
			if (failure.ResponseIssue == ProviderResponseIssueContentType) != test.wantTypeError {
				t.Fatalf("failure = %v issue=%s, want content-type failure=%v", err, failure.ResponseIssue, test.wantTypeError)
			}
		})
	}
}

func chatGPTLargeStream(t *testing.T) []byte {
	t.Helper()
	// A single event larger than the old 64 KiB diagnostic prefix, with the
	// actual model terminal deliberately at the end. Retention cannot mask EOF.
	wire := appendSSEFixture(t, "response.created", map[string]any{
		"type": "response.created", "sequence_number": 0,
		"response": map[string]any{
			"id": "resp_original", "status": "in_progress", "model": "codex-client-alias",
			"instructions": strings.Repeat("large fixture context ", 5000),
		},
	})
	return append(wire, originalResponsesTerminalWire(t)...)
}

func TestManagedChatGPTHeaderlessStreamStillRequiresToolApproval(t *testing.T) {
	for _, approve := range []bool{false, true} {
		t.Run(strconv.FormatBool(approve), func(t *testing.T) {
			account := testAccount{id: "account.selected", revision: 3, epoch: 7}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
				destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com",
				backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
				accounts: []testAccount{account}, preferred: account.id,
			})
			item := json.RawMessage(`{"type":"function_call","id":"fc_fixture","call_id":"call_fixture","name":"shell","arguments":"{}","status":"completed"}`)
			wire := appendSSEFixture(t, "response.output_item.done", map[string]any{
				"type": "response.output_item.done", "sequence_number": 0, "output_index": 0, "item": item,
			})
			wire = append(wire, appendSSEFixture(t, "response.completed", map[string]any{
				"type": "response.completed", "sequence_number": 1,
				"response": map[string]any{
					"id": "resp_tool_fixture", "created_at": 1, "status": "completed",
					"model": "codex-client-alias", "output": []json.RawMessage{item},
					"usage": map[string]any{"input_tokens": 4, "output_tokens": 2},
				},
			})...)
			provider := &providerDouble{results: []providerResult{{response: &http.Response{
				StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(wire)),
			}}}}
			decision := ToolDecision{Outcome: ToolDecisionRejected, ReasonCode: "user_rejected"}
			if approve {
				decision = ToolDecision{Outcome: ToolDecisionApproved}
			}
			decisions := &decisionDouble{decision: decision}
			pipeline := newTestPipeline(t, newAccountAuthority(t, account), provider, decisions, &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			downstream := &downstreamRecorder{}
			result, err := pipeline.Execute(context.Background(), mustClientRequest(t, "exchange-native-tools", plan,
				[]byte(`{"model":"codex-client-alias","stream":true,
					"input":[{"type":"message","role":"user","content":"hello"}],
					"tools":[{"type":"function","name":"shell","description":"fixture","parameters":{"type":"object","properties":{}}}]
				}`)), downstream)
			if decisions.callCount() != 1 {
				t.Fatal("headerless stream bypassed the tool decision gate")
			}
			if approve {
				if err != nil || result.Outcome != AttemptSucceeded || !bytes.Equal(downstream.bytesSnapshot(), wire) {
					t.Fatalf("approved native tool stream failed: %+v err=%v", result, err)
				}
			} else if ReasonOf(err) != ReasonToolDecisionRejected || len(downstream.bytesSnapshot()) != 0 || result.Ledger.DownstreamTerminal {
				t.Fatalf("rejected tool bytes escaped: %+v err=%v", result, err)
			}
		})
	}
}

func chatGPTClientHeaderFixture() http.Header {
	return http.Header{
		"Accept": {"text/event-stream"}, "Content-Type": {"application/json"},
		"Authorization": {"Bearer old-client-token"}, "Chatgpt-Account-Id": {"old-client-account"},
		"Cookie": {"session=old-client"}, "Originator": {"codex-tui"}, "Version": {"0.153.4"},
		"Session-Id": {"session-fixture"}, "Thread-Id": {"thread-fixture"},
		"X-Client-Request-Id": {"request-fixture"}, "X-Codex-Beta-Features": {"client-beta"},
		"X-Codex-Routing-Hint": {"model=codex-client-alias"}, "X-Codex-Turn-State": {"old-account-state"},
		"X-Openai-Subagent": {"review"}, "X-Private-Client": {"never-forward"},
	}
}

type chatGPTFixtureSecrets struct{ encoded []byte }

func (reader chatGPTFixtureSecrets) Read(context.Context, secretstore.Reference) (*secretstore.Value, error) {
	return nil, errors.New("test requires an exact credential revision")
}

func (reader chatGPTFixtureSecrets) ReadAtRevision(_ context.Context, _ secretstore.Reference, revision secretstore.Revision) (*secretstore.Value, error) {
	if revision != 7 {
		return nil, errors.New("wrong fixture credential revision")
	}
	return secretstore.NewValue(reader.encoded)
}

type chatGPTLoopbackTransport struct {
	transport http.RoundTripper
	url       *url.URL
}

func (transport chatGPTLoopbackTransport) RoundTrip(request *http.Request, _ providertransport.TransportDispatch) (*http.Response, transportprofile.Evidence, error) {
	local := request.Clone(request.Context())
	local.URL.Scheme = transport.url.Scheme
	local.URL.Host = transport.url.Host
	response, err := transport.transport.RoundTrip(local)
	return response, transportprofile.Evidence{}, err
}
