package desktopcontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func detailTestLimits() exchangecontent.SourceLimits {
	return exchangecontent.SourceLimits{Semantic: protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}}, Scratch: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 32 << 20}, CanonicalBytes: 256 << 20, RetainedBytes: 64 << 20, StructureBytes: 32 << 20}
}
func TestDetailWriterPreservesOrdinaryBytesAndDeepLeaf(t *testing.T) {
	detail := ExchangeDetail{ID: "detail", Status: "succeeded", Content: ExchangeContentDetail{State: ExchangeContentRecorded, Request: &exchangecontent.Request{RequestedModel: "m", EffectiveModel: "m", Messages: []exchangecontent.Message{{Role: "user", Blocks: []exchangecontent.Block{{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: "<ordinary>&"}}}}}, Response: &exchangecontent.Response{ID: "r", Blocks: []exchangecontent.Block{{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: "reply"}}}}}
	want, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := prepareExchangeDetailJSON(context.Background(), detailTestLimits(), detail)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if _, err = plan.WriteTo(&got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), append(want, '\n')) {
		t.Fatalf("ordinary bytes changed\ngot %s\nwant %s", got.Bytes(), want)
	}
	raw := `{"a":` + strings.Repeat("[", 9999) + "0" + strings.Repeat("]", 9999) + "}"
	detail.Content.Response.Blocks = []exchangecontent.Block{{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "call", ToolName: "f", Arguments: json.RawMessage(raw)}}
	plan, err = prepareExchangeDetailJSON(context.Background(), detailTestLimits(), detail)
	if err != nil {
		t.Fatal(err)
	}
	got.Reset()
	if _, err = plan.WriteTo(&got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got.Bytes(), []byte(`"arguments":`+raw)) {
		t.Fatal("complete deep leaf missing")
	}
	if _, err = json.Marshal(detail); err == nil {
		t.Fatal("fixture did not cross parent scanner boundary")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = prepareExchangeDetailJSON(ctx, detailTestLimits(), detail); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preflight: %v", err)
	}
	if _, err = plan.WriteTo(detailFailWriter{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("writer failure hidden: %v", err)
	}
}

type detailFailWriter struct{}

func (detailFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
