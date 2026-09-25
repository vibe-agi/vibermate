package acpobservation_test

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
)

func TestObserverExportBudgetIncludesJSONEscapingAndMetadata(t *testing.T) {
	observer := acpobservation.NewObserver(true)
	feed := func(client bool, value any) {
		data, _ := json.Marshal(value)
		observer.Feed(client, append(data, '\n'))
	}
	for i := 0; i < acpobservation.MaxSessions; i++ {
		id := fmt.Sprint(i) + strings.Repeat("\x00", 500)
		feed(true, map[string]any{"id": i, "method": "session/new", "params": map[string]any{"cwd": strings.Repeat("\x00", 4096)}})
		feed(false, map[string]any{"id": i, "result": map[string]any{"sessionId": id}})
		for j := 0; j < 4; j++ {
			feed(true, map[string]any{"id": i, "method": "session/prompt", "params": map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": strings.Repeat("\x00", 512)}}}})
			feed(false, map[string]any{"id": i, "result": map[string]any{"stopReason": "end_turn"}})
		}
	}
	observer.Finish(0, false)
	snapshot := observer.Snapshot()
	data, _ := json.Marshal(snapshot)
	if len(data) >= acpobservation.MaxSnapshotBytes || !snapshot.Incomplete || snapshot.Validate(true) != nil {
		t.Fatalf("export cannot be saved: bytes=%d incomplete=%v", len(data), snapshot.Incomplete)
	}
	snapshot.Revision = math.MaxUint64
	if snapshot.Validate(true) == nil {
		t.Fatal("revision overflows durable storage")
	}
}

func TestObserverProjectsAuthenticationRequiredWithoutRetainingError(t *testing.T) {
	observer := acpobservation.NewObserver(true)
	observer.Feed(true, []byte("{\"id\":1,\"method\":\"session/new\",\"params\":{\"cwd\":\"/workspace\"}}\n"))
	observer.Feed(false, []byte("{\"id\":1,\"error\":{\"code\":-32000,\"message\":\"PRIVATE AUTHENTICATION DETAILS\"}}\n"))
	encoded, _ := json.Marshal(observer.Snapshot())
	if !strings.Contains(string(encoded), `"needsAuthentication":true`) || strings.Contains(string(encoded), "PRIVATE") {
		t.Fatal("auth-required boundary is missing or leaks error details")
	}
	observer.Feed(true, []byte("{\"id\":2,\"method\":\"authenticate\",\"params\":{\"methodId\":\"PRIVATE\"}}\n"))
	observer.Feed(false, []byte("{\"id\":2,\"result\":null}\n"))
	encoded, _ = json.Marshal(observer.Snapshot())
	if strings.Contains(string(encoded), `"needsAuthentication":true`) || strings.Contains(string(encoded), "PRIVATE") {
		t.Fatal("successful authentication did not clear the prompt")
	}
}

func TestObserverCorrelatesSessionsWithoutRetainingMetadataOnlyContent(t *testing.T) {
	observer := acpobservation.NewObserver(false)
	feed := func(client bool, message string) { observer.Feed(client, []byte(message+"\n")) }
	feed(true, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)
	feed(false, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentInfo":{"name":"test-agent","version":"1.2.3"}}}`)
	feed(true, `{"id":"s","method":"session/new","params":{"cwd":"/workspace/a","mcpServers":[]}}`)
	feed(false, `{"id":"s","result":{"sessionId":"native-a"}}`)
	feed(true, `{"id":"p","method":"session/prompt","params":{"sessionId":"native-a","prompt":[{"type":"text","text":"PRIVATE PROMPT"}]}}`)
	// Opposite-direction IDs are not responses to the editor's prompt.
	feed(false, `{"id":"p","method":"session/request_permission","params":{"secret":"PRIVATE PERMISSION"}}`)
	feed(true, `{"id":"p","result":{"outcome":{"outcome":"selected","optionId":"allow"}}}`)
	feed(false, `{"method":"session/update","params":{"sessionId":"native-a","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"PRIVATE ANSWER"}}}}`)
	feed(false, `{"id":"p","result":{"stopReason":"end_turn"}}`)
	feed(true, `{"id":8,"method":"authenticate","params":{"token":"PRIVATE TOKEN"}}`)
	feed(false, `{"id":8,"result":null}`)
	observer.Finish(0, false)
	got := observer.Snapshot()
	if !got.Final || got.Incomplete || got.ProtocolVersion != 1 || got.Agent.Version != "1.2.3" || len(got.Sessions) != 1 || len(got.Prompts) != 1 || got.Prompts[0].State != "completed" {
		t.Fatalf("unexpected observation: %+v", got)
	}
	if got.Sessions[0].ID != "native-a" || got.Sessions[0].CWD != "/workspace/a" {
		t.Fatalf("session: %+v", got.Sessions)
	}
	encoded, err := json.Marshal(got)
	if err != nil || strings.Contains(string(encoded), "PRIVATE") {
		t.Fatalf("private input retained: %v", err)
	}
}

func TestObserverSkipsReplayAndBoundsContentWithoutAffectingOtherSessions(t *testing.T) {
	observer := acpobservation.NewObserver(true)
	feed := func(client bool, message string) {
		// Exercise fragmented UTF-8 and CRLF framing, not just whole JSON lines.
		for _, value := range []byte(message + "\r\n") {
			observer.Feed(client, []byte{value})
		}
	}
	feed(true, `{"id":1,"method":"session/load","params":{"sessionId":"old","cwd":"/other","mcpServers":[]}}`)
	feed(false, `{"method":"session/update","params":{"sessionId":"old","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"HISTORICAL"}}}}`)
	feed(false, `{"id":1,"result":{}}`)
	feed(true, `{"id":2,"method":"session/new","params":{"cwd":"/new","mcpServers":[]}}`)
	feed(false, `{"id":2,"result":{"sessionId":"new"}}`)
	feed(true, `{"id":3,"method":"session/prompt","params":{"sessionId":"old","prompt":[{"type":"text","text":"\u4f60\u597d"}]}}`)
	feed(true, `{"id":4,"method":"session/prompt","params":{"sessionId":"new","prompt":[]}}`)
	feed(false, `{"id":4,"error":{"code":-1,"message":"PRIVATE ERROR","data":{"secret":"TOKEN"}}}`)
	feed(false, `{"method":"session/update","params":{"sessionId":"old","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"\u4e16\u754c"}}}}`)
	feed(false, `{"id":3,"result":{"stopReason":"cancelled"}}`)
	got := observer.Snapshot()
	if len(got.Prompts) != 2 || got.Prompts[0].UserText != "\u4f60\u597d" || got.Prompts[0].AgentText != "\u4e16\u754c" || got.Prompts[0].State != "cancelled" || got.Prompts[1].State != "failed" || got.Sessions[0].Operation != "load" {
		t.Fatalf("projection: %+v", got)
	}
	// An oversized unknown frame is discarded only by observation. Parsing
	// resumes at its next newline and later real prompts remain visible.
	observer.Feed(false, []byte(strings.Repeat("x", acpobservation.MaxFrameBytes+1)+"\n"))
	feed(true, `{"id":5,"method":"session/prompt","params":{"sessionId":"new","prompt":[]}}`)
	frame := `{"method":"session/update","params":{"sessionId":"new","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"` + strings.Repeat("\u754c", acpobservation.MaxTextBytes) + `"}}}}`
	observer.Feed(false, []byte(frame+"\n"))
	observer.Finish(9, false)
	got = observer.Snapshot()
	if err := got.Validate(true); err != nil || !got.Incomplete || got.Prompts[2].State != "interrupted" {
		t.Fatalf("bounded snapshot invalid: %v %+v", err, got)
	}
	encoded, _ := json.Marshal(got)
	for _, forbidden := range []string{"HISTORICAL", "PRIVATE ERROR", "TOKEN"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatal("non-conversation data retained")
		}
	}
	got.Prompts[0].AgentText = "mutated"
	if observer.Snapshot().Prompts[0].AgentText != "\u4e16\u754c" {
		t.Fatal("snapshot shares mutable state")
	}
}
