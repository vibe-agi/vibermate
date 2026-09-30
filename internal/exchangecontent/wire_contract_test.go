package exchangecontent

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// Flutter parses this very same fixture in content_wire_contract_test.dart.
// Producer and consumer tests must not use separate hand-written examples.
func TestContentWireContract(t *testing.T) {
	var fixture struct {
		Previews  []RequestPreview `json:"previews"`
		Responses []*Response      `json:"responses"`
	}
	for _, text := range []string{
		strings.Repeat("x", 179) + " word", strings.Repeat("\U0001f680", 181), "  one\n\ttwo\x00three\ufeff ",
	} {
		preview, ok := PreviewRequestMessage(Message{Role: "user", Blocks: []Block{{Kind: "text", Availability: AvailabilityRecorded, Text: text}}})
		if !ok || preview.Validate() != nil {
			t.Fatalf("invalid preview: %+v", preview)
		}
		fixture.Previews = append(fixture.Previews, preview)
	}
	for _, stop := range []protocolcore.StopReason{
		protocolcore.StopReasonEndTurn, protocolcore.StopReasonMaxTokens, protocolcore.StopReasonToolUse,
		protocolcore.StopReasonStopSequence, protocolcore.StopReasonRefusal, protocolcore.StopReasonPauseTurn,
		protocolcore.StopReasonContextLimit, protocolcore.StopReasonIncomplete,
	} {
		request, response := evidenceFixture(t)
		response.StopReason = stop
		if stop == protocolcore.StopReasonToolUse {
			key, err := protocolcore.NewCallKey("fixture", "call_1")
			if err != nil {
				t.Fatal(err)
			}
			args, err := protocolcore.NewJSONObject([]byte(`{}`), 16)
			if err != nil {
				t.Fatal(err)
			}
			block, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{Key: key, Name: "read", Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			response.Blocks = []protocolcore.ContentBlock{block}
		}
		for _, empty := range []bool{false, true} {
			if empty && stop == protocolcore.StopReasonToolUse {
				continue
			}
			if empty {
				response.Blocks = nil
			}
			record, err := NewRecord("exchange-wire", frozenFixture(), environment.DefaultContentRecordingPolicy(), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), request, &response)
			if err != nil {
				t.Fatal(err)
			}
			fixture.Responses = append(fixture.Responses, record.Response)
		}
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	const path = "../../api/samples/content-contract.json"
	if os.Getenv("VIBERMATE_UPDATE") == "1" {
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, encoded) {
		t.Fatal("content contract fixture changed; regenerate with VIBERMATE_UPDATE=1 and verify Flutter parses it")
	}
}
