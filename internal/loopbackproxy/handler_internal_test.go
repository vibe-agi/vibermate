package loopbackproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/responses"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

type invalidCountResponseWriter struct {
	header http.Header
}

func (writer *invalidCountResponseWriter) Header() http.Header {
	if writer.header == nil {
		writer.header = make(http.Header)
	}
	return writer.header
}

func (*invalidCountResponseWriter) WriteHeader(int) {}

func (*invalidCountResponseWriter) Write(body []byte) (int, error) {
	return len(body) + 1, nil
}

func TestHTTPDownstreamRejectsInvalidWriterByteCount(t *testing.T) {
	downstream := newHTTPDownstream(
		&invalidCountResponseWriter{},
		httpDownstreamOptions{},
	)
	downstream.begun = true
	if count, err := downstream.Write(context.Background(), []byte("body")); err == nil || count != 0 {
		t.Fatalf("count=%d error=%v", count, err)
	}
	if downstream.total != 0 || len(downstream.body) != 0 {
		t.Fatalf("invalid writer result was recorded: %+v", downstream)
	}
}

func TestNativeHTTPErrorPreservesStatusPayloadAndRetryMetadata(t *testing.T) {
	for _, throughDownstream := range []bool{false, true} {
		writer := httptest.NewRecorder()
		failure := &exchange.Failure{
			Code: exchange.ReasonProviderStatusRejected, ProviderStatus: http.StatusTooManyRequests,
			NativeError: protocolcore.NewNativeProviderError(protocolspec.DialectOpenAIResponses,
				[]byte(`{"code":"credit_balance_exhausted","message":"native fixture"}`)).WithHeaders(http.Header{
				"Retry-After": {"60"}, "X-Should-Retry": {"false"}, "X-Request-Id": {"request-fixture"},
			}),
		}
		if throughDownstream {
			if err := writeExchangeFailureDownstream(context.Background(), newHTTPDownstream(writer, httpDownstreamOptions{}), protocolspec.DialectOpenAIResponses, failure); err != nil {
				t.Fatal(err)
			}
		} else {
			writeExchangeFailure(writer, protocolspec.DialectOpenAIResponses, failure)
		}
		if writer.Code != 429 || writer.Header().Get("Retry-After") != "60" || writer.Header().Get("X-Should-Retry") != "false" || writer.Header().Get("X-Request-Id") != "request-fixture" || !strings.Contains(writer.Body.String(), "credit_balance_exhausted") {
			t.Fatalf("native rejection changed: %d %v %s", writer.Code, writer.Header(), writer.Body.String())
		}
		if headers := exchangeFailureMetadata(protocolspec.DialectAnthropicMessages, failure); len(headers) != 0 {
			t.Fatalf("native error metadata crossed dialects: %v", headers)
		}
	}
}

func TestHTTPDownstreamFailureUsesClientDialect(t *testing.T) {
	for _, dialect := range []protocolspec.Dialect{protocolspec.DialectOpenAIResponses, protocolspec.DialectAnthropicMessages} {
		t.Run(string(dialect), func(t *testing.T) {
			writer := httptest.NewRecorder()
			downstream := newHTTPDownstream(writer, httpDownstreamOptions{ClientDialect: dialect})
			envelope, err := exchange.NewResponseEnvelope(exchange.ResponseModeEventStream, http.StatusOK, http.Header{"Content-Type": {"text/event-stream"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := downstream.Begin(context.Background(), envelope); err != nil {
				t.Fatal(err)
			}
			if err := downstream.Abort(context.Background(), exchange.FailureNotice{
				ReasonCode: exchange.ReasonProviderStreamTruncated, ProtocolReason: protocolcore.ReasonTruncatedEventStream,
			}); err != nil {
				t.Fatal(err)
			}
			decoder, _ := ssewire.NewDecoder(ssewire.DefaultOptions())
			events, err := decoder.Feed(writer.Body.Bytes())
			if err != nil || len(events) != 1 || decoder.Finish() != nil {
				t.Fatalf("invalid failure stream: %v", err)
			}
			if dialect == protocolspec.DialectOpenAIResponses {
				var event responses.ResponseStreamEventUnion
				if err := json.Unmarshal(events[0].Data, &event); err != nil {
					t.Fatal(err)
				}
				failed := event.AsResponseFailed()
				if events[0].Name != "response.failed" || event.Type != "response.failed" || failed.Response.Status != "failed" || !strings.Contains(failed.Response.Error.Message, "provider_stream_truncated") {
					t.Fatalf("failure is not a Responses terminal: %s", events[0].Data)
				}
			} else {
				var event struct {
					Type  string
					Error struct{ Type, Message string }
				}
				if err := json.Unmarshal(events[0].Data, &event); err != nil {
					t.Fatal(err)
				}
				if events[0].Name != "error" || event.Type != "error" || event.Error.Type != "api_error" || !strings.Contains(event.Error.Message, "provider_stream_truncated") {
					t.Fatalf("failure is not an Anthropic error: %s", events[0].Data)
				}
			}
		})
	}
}

// An installed Codex oracle catches failures that a permissive JSON decoder
// accepts but the real client silently ignores. No user config, tokens or
// inference endpoint is used; every request is served by the loopback fixture.
func TestInstalledCodexRecognizesStreamFailure(t *testing.T) {
	binary := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if binary == "" {
		t.Skip("requires an explicitly selected local Codex binary")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("Codex binary must be absolute")
	}

	for _, test := range []struct {
		name, code, want string
		reason           exchange.ReasonCode
	}{
		{name: "truncated", reason: exchange.ReasonProviderStreamTruncated, want: "provider_stream_truncated"},
		{name: "context limit", reason: exchange.ReasonProviderResponseFailed, code: "context_length_exceeded", want: "context window"},
		{name: "encrypted history", reason: exchange.ReasonProviderResponseFailed, code: "invalid_encrypted_content", want: "invalid_encrypted_content"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/responses") {
					http.NotFound(writer, request)
					return
				}
				downstream := newHTTPDownstream(writer, httpDownstreamOptions{ClientDialect: protocolspec.DialectOpenAIResponses})
				envelope, _ := exchange.NewResponseEnvelope(exchange.ResponseModeEventStream, http.StatusOK, http.Header{"Content-Type": {"text/event-stream"}})
				if err := downstream.Begin(request.Context(), envelope); err != nil {
					t.Error(err)
					return
				}
				if err := downstream.Abort(request.Context(), exchange.FailureNotice{ReasonCode: test.reason, ProviderErrorCode: test.code}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			directory := t.TempDir()
			requireNoNativeCodexPluginSync(t, directory)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "exec", "--skip-git-repo-check", "--ephemeral", "--model", "fixture",
				"-c", `model_provider="fixture"`,
				"-c", `model_providers.fixture.name="fixture"`,
				"-c", "model_providers.fixture.base_url="+strconv.Quote(server.URL),
				"-c", `model_providers.fixture.wire_api="responses"`,
				"-c", `model_providers.fixture.requires_openai_auth=false`,
				"-c", `model_providers.fixture.stream_max_retries=0`,
				"-c", `model_providers.fixture.request_max_retries=0`,
				// Exercise native model errors without detached plugin catalog sync.
				"-c", `features.plugins=false`,
				"-c", `analytics.enabled=false`, "-c", `feedback.enabled=false`, "hello")
			command.Dir = directory
			command.Env = []string{"PATH=/opt/homebrew/bin:/usr/bin:/bin", "CODEX_HOME=" + directory}
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(strings.ToLower(string(output)), test.want) || strings.Contains(string(output), "stream closed before response.completed") {
				t.Fatalf("native client did not recognize failure: err=%v output=%s", err, output)
			}
		})
	}
}

func TestClientProtocolEvidenceFromHeadersIsExactAndCanonical(t *testing.T) {
	t.Parallel()

	headers := http.Header{
		"X-Claude-Code-Agent-Id":        []string{"agent-1"},
		"X-Claude-Code-Parent-Agent-Id": []string{"parent-1"},
		"X-Claude-Code-Session-Id":      []string{"session-1"},
		"Authorization":                 []string{"Bearer must-not-cross"},
	}
	got := clientProtocolEvidenceFromHeaders(headers, protocolspec.DialectAnthropicMessages)
	want := []protocolcore.ProtocolEvidenceValue{
		{Name: "claude.agent_id", Value: "agent-1"},
		{Name: "claude.parent_agent_id", Value: "parent-1"},
		{Name: "claude.session_id", Value: "session-1"},
	}
	if !equalProtocolEvidence(got, want) {
		t.Fatalf("client protocol evidence = %#v", got)
	}

	headers["X-Claude-Code-Agent-Id"] = []string{"agent-1", "agent-2"}
	headers["X-Claude-Code-Session-Id"] = []string{" session-1"}
	got = clientProtocolEvidenceFromHeaders(headers, protocolspec.DialectAnthropicMessages)
	want = []protocolcore.ProtocolEvidenceValue{
		{Name: "claude.parent_agent_id", Value: "parent-1"},
	}
	if !equalProtocolEvidence(got, want) {
		t.Fatalf("malformed optional headers were retained: %#v", got)
	}
}

func equalProtocolEvidence(
	left []protocolcore.ProtocolEvidenceValue,
	right []protocolcore.ProtocolEvidenceValue,
) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestCodexHeadersKeepSessionAndReportedVersionWithoutDecodingBody(t *testing.T) {
	headers := http.Header{
		"Session-Id": {"session-1"}, "Thread-Id": {"thread-1"},
		"Version": {"0.153.4"}, "Authorization": {"Bearer private"},
	}
	want := []protocolcore.ProtocolEvidenceValue{
		{Name: "codex.cli_version", Value: "0.153.4"},
		{Name: "openai_responses.session_id", Value: "session-1"},
		{Name: "openai_responses.thread_id", Value: "thread-1"},
	}
	if got := clientProtocolEvidenceFromHeaders(headers, protocolspec.DialectOpenAIResponses); !equalProtocolEvidence(got, want) {
		t.Fatalf("Codex identity headers = %#v", got)
	}
	if got := clientProtocolEvidenceFromHeaders(headers, protocolspec.DialectAnthropicMessages); len(got) != 0 {
		t.Fatalf("generic headers invented a different client identity: %#v", got)
	}
	headers["Session-Id"] = []string{"first", "second"}
	if got := clientProtocolEvidenceFromHeaders(headers, protocolspec.DialectOpenAIResponses); len(got) != 2 {
		t.Fatalf("ambiguous session header accepted: %#v", got)
	}
}
