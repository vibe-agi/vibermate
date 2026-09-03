package loopbackproxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
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
	got := clientProtocolEvidenceFromHeaders(headers)
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
	got = clientProtocolEvidenceFromHeaders(headers)
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
