package loopbackproxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
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
