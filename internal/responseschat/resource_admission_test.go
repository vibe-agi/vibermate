package responseschat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestPathResourceAdmissionLongNativeAndModelOwnership(t *testing.T) {
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 128 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 128 << 20}}
	options := openairesponses.DefaultOptions()
	options.Resources = &policy
	path, err := NewResponsesPassthroughProtocolPath(options)
	if err != nil {
		t.Fatal(err)
	}
	var wire strings.Builder
	wire.WriteString(`{"model":"m","input":[`)
	for i := 0; i < 4111; i++ {
		if i > 0 {
			wire.WriteByte(',')
		}
		wire.WriteString(`{"role":"user","content":"history"}`)
	}
	wire.WriteString(`,{"type":"compaction","id":"c","encrypted_content":"opaque-native-tail"}]}`)
	source := []byte(wire.String())
	original := bytes.Clone(source)
	request, report, err := path.Client().DecodeRequest(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 4111 || !report.Empty() {
		t.Fatal("native history projection lost")
	}
	request, err = protocolcore.WithEffectiveModelWithin(request, "mapped", policy)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _, err := path.EncodeProviderRequest(request, source, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	if err := json.Unmarshal(source, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded.Body(), &after); err != nil {
		t.Fatal(err)
	}
	before["model"] = "mapped"
	if !reflect.DeepEqual(before, after) || !bytes.Equal(source, original) {
		t.Fatal("native original ownership or model-only rewrite changed")
	}
	response := protocolcore.Response{ID: "r", RequestedModel: "m", EffectiveModel: "mapped", ReportedModel: "mapped", StopReason: protocolcore.StopReasonEndTurn, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "complete"}}}
	if _, _, err := path.EncodeClientResponse(request, response, []byte(`{"id":"r","model":"mapped","output":[],"native":"retained"}`)); err != nil {
		t.Fatal(err)
	}
	request.Stream = true
	if _, err := path.Streaming().NewStream(request); err != nil {
		t.Fatal(err)
	}
}

func TestPathResourceAdmissionSourceJSONBeforeRootMap(t *testing.T) {
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 20000}, Response: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 20000}}
	options := openairesponses.DefaultOptions()
	options.Resources = &policy
	path, _ := NewResponsesPassthroughProtocolPath(options)
	request := protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText}}}}}
	var source strings.Builder
	source.WriteString(`{"model":"m","input":[],"unknown":{`)
	for i := 0; i < 10000; i++ {
		if i > 0 {
			source.WriteByte(',')
		}
		fmt.Fprintf(&source, `"k%d":0`, i)
	}
	source.WriteString(`}}`)
	if _, _, err := path.EncodeProviderRequest(request, []byte(source.String()), nil); err == nil {
		t.Fatal("source JSON map allocated beyond finite policy")
	}
}

func TestPathResourceAdmissionCrossDialectLongHistory(t *testing.T) {
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 128 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 128 << 20}}
	options := DefaultOptions()
	options.Resources = &policy
	conflict := protocolcore.ResourceLimits{}
	options.Responses.Resources = &conflict
	options.Chat.Resources = &conflict
	path, err := NewProtocolPath(options)
	if err != nil {
		t.Fatal(err)
	}
	policy.Request = protocolcore.ResourceCost{PayloadBytes: 1, StructureBytes: 1}
	request := protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Messages: make([]protocolcore.Message, 4111)}
	for i := range request.Messages {
		request.Messages[i] = protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "tail"}}}
	}
	encoded, _, err := path.EncodeProviderRequest(request, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(encoded.Body(), &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Messages) != 4111 {
		t.Fatal("cross-dialect history lost")
	}
	request.Stream = true
	if _, err := path.Streaming().NewStream(request); err != nil {
		t.Fatal(err)
	}
}
