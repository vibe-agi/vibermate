package exchange

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/accountselector"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestOriginalCompressedRequestPreservesWireAndRecordsContent(t *testing.T) {
	for _, encoding := range []string{"gzip", "zstd"} {
		for _, stage := range []string{"none", "response only", "request"} {
			t.Run(encoding+"/"+stage, func(t *testing.T) {
				policy := messagetransform.Policy{}
				if stage == "response only" {
					policy.ResponseJavaScript = `response.headers["x-response-script"] = "ran";`
				}
				if stage == "request" {
					policy.RequestJavaScript = `
						const payload = JSON.parse(request.body);
						if (payload.input[0].content !== "hello") throw new Error("not logical JSON");
						payload.input[0].content = "\u811a\u672c\u5df2\u66ff\u6362\u6b63\u6587";
						request.body = JSON.stringify(payload);
						request.headers["x-request-script"] = "ran";
					`
				}
				plan := mustEnvironmentRequestPlan(t, testPlanOptions{
					clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
					destination: environment.DestinationKindOriginal, providerOrigin: "https://chatgpt.com",
					backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
					transform: policy,
				})
				responseBody := chatGPTLargeStream(t)
				provider := &providerDouble{results: []providerResult{{response: &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
					Body: io.NopCloser(bytes.NewReader(responseBody)),
				}}}}
				content := &contentObserverDouble{}
				pipeline := newTestPipelineWithContentObserver(t, nil, provider, approvedDecisions(), &attemptObserverDouble{}, content)
				defer shutdownPipeline(t, pipeline)
				logical := []byte(`{"model":"codex-client-alias","stream":true,"store":false,"input":[{"type":"message","role":"user","content":"hello"}]}`)
				wire := compressedRequestFixture(t, encoding, logical)
				headers := chatGPTClientHeaderFixture()
				headers.Set("Content-Encoding", encoding)
				request := mustClientRequestWithOptions(t, "exchange-compressed-original", plan, wire, WithOriginalHeaders(headers))
				downstream := &downstreamRecorder{}
				result, err := pipeline.Execute(context.Background(), request, downstream)
				if err != nil || result.Outcome != AttemptSucceeded {
					t.Fatalf("compressed original failed: %v", err)
				}
				outbound := provider.requestsSnapshot()
				if len(outbound) != 1 || outbound[0].Headers().Get("Authorization") != "Bearer old-client-token" {
					t.Fatal("original request did not preserve client authentication")
				}
				if stage == "request" {
					var rewritten struct{ Input []struct{ Content string } }
					if outbound[0].Headers().Get("Content-Encoding") != "" || outbound[0].Headers().Get("Content-Length") != "" ||
						json.Unmarshal(outbound[0].Body(), &rewritten) != nil || len(rewritten.Input) != 1 || rewritten.Input[0].Content != "\u811a\u672c\u5df2\u66ff\u6362\u6b63\u6587" ||
						outbound[0].Headers().Get("X-Request-Script") != "ran" {
						t.Fatal("request script did not receive/emit the logical representation")
					}
				} else if outbound[0].Headers().Get("Content-Encoding") != encoding || !bytes.Equal(outbound[0].Body(), wire) {
					t.Fatal("observation or a response-only script changed the original request wire bytes")
				}
				if !bytes.Equal(request.Body(), wire) {
					t.Fatal("semantic decoding mutated the ingress evidence")
				}
				if stage == "none" && !bytes.Equal(downstream.bytesSnapshot(), responseBody) {
					t.Fatal("observation changed the provider response")
				}
				observation, ok := content.latest()
				if !ok || len(observation.Request.Messages) == 0 ||
					observation.Request.Messages[0].Blocks[0].Text != "hello" || observation.Response == nil {
					t.Fatal("compressed original request lost its semantic conversation")
				}
			})
		}
	}
}

func TestCompressedRequestSelectsAccountMapsModelAndRunsScript(t *testing.T) {
	for _, encoding := range []string{"gzip", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			accounts := []testAccount{{id: "account.selected", revision: 3, epoch: 7}}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
				destination: environment.DestinationKindUpstream, providerOrigin: "https://provider.example/v1",
				backend: protocolspec.DialectOpenAIChat, modelMode: environment.ModelModeMap, mappedModel: "mapped-model",
				accounts: accounts,
				selector: &codelibrary.AccountSelectorRevision{
					ID: "compressed-selector", Revision: 1, CollectionID: "tests", DisplayName: "Compressed selector",
					PublishedAt: time.Date(2026, time.September, 7, 0, 0, 0, 0, time.UTC),
					Policy: accountselector.Policy{JavaScript: `
						if (request.requestedModel !== "codex-client-alias" || JSON.parse(request.body).input[0].content !== "hello") throw new Error("not logical JSON");
						selection.accountId = "account.selected";
					`},
				},
				transform: messagetransform.Policy{RequestJavaScript: `
					const payload = JSON.parse(request.body);
					if (payload.model !== "mapped-model") throw new Error("mapping missing");
					payload.messages[0].content = "\u811a\u672c\u5df2\u66ff\u6362\u6b63\u6587";
					request.body = JSON.stringify(payload);
					request.headers["x-script-ran"] = "yes";
				`},
			})
			provider := &providerDouble{results: []providerResult{{response: jsonResponse(http.StatusOK, completeProviderResponse("mapped-model"))}}}
			content := &contentObserverDouble{}
			pipeline := newTestPipelineWithContentObserver(t, newAccountAuthority(t, accounts...), provider, approvedDecisions(), &attemptObserverDouble{}, content)
			defer shutdownPipeline(t, pipeline)
			logical := []byte(`{"model":"codex-client-alias","stream":false,"input":[{"type":"message","role":"user","content":"hello"}]}`)
			wire := compressedRequestFixture(t, encoding, logical)
			headers := chatGPTClientHeaderFixture()
			headers.Set("Content-Encoding", encoding)
			result, err := pipeline.Execute(context.Background(), mustClientRequestWithOptions(t,
				"exchange-compressed-selector", plan, wire, WithOriginalHeaders(headers)), &downstreamRecorder{})
			if err != nil || result.Outcome != AttemptSucceeded || result.AccountID != "account.selected" {
				t.Fatalf("compressed selector/mapping failed: %v", err)
			}
			outbound := provider.requestsSnapshot()
			if len(outbound) != 1 || outbound[0].Headers().Get("Content-Encoding") != "" ||
				outbound[0].Headers().Get("X-Script-Ran") != "yes" {
				t.Fatal("logical request did not reach the provider script")
			}
			var body struct {
				Model    string
				Messages []struct{ Content string }
			}
			if json.Unmarshal(outbound[0].Body(), &body) != nil || body.Model != "mapped-model" ||
				len(body.Messages) != 1 || body.Messages[0].Content != "\u811a\u672c\u5df2\u66ff\u6362\u6b63\u6587" {
				t.Fatal("compressed request lost its model mapping or rewritten body")
			}
			if !bytes.Equal(wire, compressedRequestFixture(t, encoding, logical)) {
				t.Fatal("request script changed compressed ingress evidence")
			}
			observation, ok := content.latest()
			if !ok || observation.Response == nil || observation.Request.RequestedModel != "codex-client-alias" {
				t.Fatal("compressed request lost the original requested model or content")
			}
		})
	}
}

func TestInvalidCompressedRequestStopsBeforeAccountLeaseAndProvider(t *testing.T) {
	for _, test := range []struct {
		name     string
		encoding []string
		body     func(*testing.T) []byte
	}{
		{"invalid zstd", []string{"zstd"}, func(*testing.T) []byte { return []byte("private-invalid-body") }},
		{"invalid gzip", []string{"gzip"}, func(*testing.T) []byte { return []byte("private-invalid-body") }},
		{"unsupported", []string{"br"}, func(*testing.T) []byte { return []byte("private-invalid-body") }},
		{"ambiguous", []string{"gzip", "zstd"}, func(*testing.T) []byte { return []byte("private-invalid-body") }},
		{"zstd expansion limit", []string{"zstd"}, func(t *testing.T) []byte {
			return compressedRequestFixture(t, "zstd", bytes.Repeat([]byte("x"), (16<<20)+1))
		}},
		{"gzip expansion limit", []string{"gzip"}, func(t *testing.T) []byte {
			return compressedRequestFixture(t, "gzip", bytes.Repeat([]byte("x"), (16<<20)+1))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := testAccount{id: "account.selected", revision: 3, epoch: 7}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
				destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com",
				backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
				accounts: []testAccount{account}, preferred: account.id,
			})
			authority := newAccountAuthority(t, account)
			provider := &providerDouble{}
			content := &contentObserverDouble{}
			pipeline := newTestPipelineWithContentObserver(t, authority, provider, approvedDecisions(), &attemptObserverDouble{}, content)
			defer shutdownPipeline(t, pipeline)
			headers := chatGPTClientHeaderFixture()
			headers["Content-Encoding"] = test.encoding
			_, err := pipeline.Execute(context.Background(), mustClientRequestWithOptions(t,
				"exchange-invalid-compression", plan, test.body(t), WithOriginalHeaders(headers)), &downstreamRecorder{})
			if ReasonOf(err) != ReasonInvalidExchangeRequest || ClientPathOf(err) != "$.headers.content_encoding" ||
				strings.Contains(err.Error(), "private-invalid-body") {
				t.Fatalf("compression failure was not safe and actionable: %v", err)
			}
			if len(authority.snapshot()) != 0 || len(provider.requestsSnapshot()) != 0 {
				t.Fatal("invalid compression acquired credentials or reached the provider")
			}
		})
	}
}

func compressedRequestFixture(t *testing.T, encoding string, body []byte) []byte {
	t.Helper()
	if encoding == "zstd" {
		return zstdRequestFixture(t, body)
	}
	return gzipFixture(t, body)
}
