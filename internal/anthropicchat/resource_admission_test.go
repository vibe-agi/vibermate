package anthropicchat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func customArgumentRequest(t *testing.T, input string, count int) protocolcore.Request {
	t.Helper()
	request := protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Tools: []protocolcore.ToolDefinition{{Kind: protocolcore.ToolKindCustom, Name: "f", CustomFormat: protocolcore.CustomToolFormat{Kind: protocolcore.CustomToolFormatText}}}}
	for index := 0; index < count; index++ {
		key, err := protocolcore.NewCallKey("test", fmt.Sprintf("call-%d", index))
		if err != nil {
			t.Fatal(err)
		}
		request.Messages = append(request.Messages, protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockToolCall, ToolCall: protocolcore.ToolCall{Kind: protocolcore.ToolKindCustom, Key: key, Name: "f", Input: input}}}})
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	return request
}

func TestAnthropicResourceAdmissionCustomArgumentsBeforeExpansion(t *testing.T) {
	request := customArgumentRequest(t, strings.Repeat("&", 1<<20), 1)
	for _, control := range []struct {
		name    string
		payload uint64
		wire    int
	}{
		{"resource", 2 << 20, 16 << 20},
		// Inner {"input":"\\u0026..."} is6MiB+12; the outer JSON string
		// must also escape each generated backslash, so it cannot fit here.
		{"outer_quoting", 64 << 20, (6 << 20) + 12},
	} {
		t.Run(control.name, func(t *testing.T) {
			policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: control.payload, StructureBytes: 8 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 16 << 20, StructureBytes: 8 << 20}}
			options := DefaultOptions()
			options.Resources = &policy
			options.MaxRequestBytes = control.wire
			codec, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			if err := codec.ValidateRequest(request); err != nil {
				t.Fatalf("semantic input must fit: %v", err)
			}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, _, err = codec.EncodeProviderRequest(request)
			runtime.ReadMemStats(&after)
			allocated := after.TotalAlloc - before.TotalAlloc
			t.Logf("encoder allocation=%d error=%v", allocated, err)
			if err == nil {
				t.Fatal("generated custom arguments accepted beyond finite resource/wire policy")
			}
			if strings.Contains(err.Error(), "catalog") {
				t.Fatalf("fixture failed before the expansion seam: %v", err)
			}
			if allocated > 1<<20 {
				t.Fatalf("custom arguments materialized before refusal: %d bytes", allocated)
			}
		})
	}
}

func TestAnthropicResourceAdmissionCustomArgumentsBudgetAndOwnership(t *testing.T) {
	request := customArgumentRequest(t, strings.Repeat("&", 1<<20), 1)
	legacy, _ := New(DefaultOptions())
	expected, expectedReport, err := legacy.EncodeProviderRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 64 << 20, StructureBytes: 8 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 16 << 20, StructureBytes: 8 << 20}}
	options := DefaultOptions()
	options.Resources = &policy
	codec, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	policy.Request = protocolcore.ResourceCost{PayloadBytes: 1, StructureBytes: 1}
	actual, report, err := codec.EncodeProviderRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) || !reflect.DeepEqual(report.Notices(), expectedReport.Notices()) {
		t.Fatal("budgeted custom tool encoding or notice order changed")
	}
	var wire struct {
		Messages []struct {
			ToolCalls []struct {
				Function struct {
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(actual, &wire); err != nil {
		t.Fatal(err)
	}
	var arguments struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal([]byte(wire.Messages[0].ToolCalls[0].Function.Arguments), &arguments); err != nil {
		t.Fatal(err)
	}
	if arguments.Input != request.Messages[0].Blocks[0].ToolCall.Input || len(arguments.Input) != 1<<20 {
		t.Fatal("custom input changed or was shortened")
	}
}

func TestAnthropicResourceAdmissionCustomArgumentsShareParentBudget(t *testing.T) {
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 160 << 10, StructureBytes: 8 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 16 << 20, StructureBytes: 8 << 20}}
	options := DefaultOptions()
	options.Resources = &policy
	codec, _ := New(options)
	input := strings.Repeat("x", 32<<10)
	if _, _, err := codec.EncodeProviderRequest(customArgumentRequest(t, input, 1)); err != nil {
		t.Fatalf("single complete call should fit: %v", err)
	}
	request := customArgumentRequest(t, input, 2)
	if err := codec.ValidateRequest(request); err != nil {
		t.Fatal(err)
	}
	_, report, err := codec.EncodeProviderRequest(request)
	if err == nil {
		t.Fatal("repeated arguments received a new full per-call budget")
	}
	notices := report.Notices()
	if len(notices) != 1 || notices[0].Code != protocolcore.NoticeCustomToolKindEncoded || notices[0].Path != "$.messages[0].blocks[0].kind" {
		t.Fatalf("preceding complete-call notice prefix changed: %+v", notices)
	}
}

func TestAnthropicResourceAdmissionCustomArgumentsLiteralAndFunctionControl(t *testing.T) {
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 8 << 20, StructureBytes: 8 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 16 << 20, StructureBytes: 8 << 20}}
	options := DefaultOptions()
	options.Resources = &policy
	for _, fixture := range []struct{ input, want string }{
		{"&", `{"input":"\u0026"}`},
		{"\"\\\n<&>\u2028\u4e16\u754c", "{\"input\":\"\\\"\\\\\\n\\u003c\\u0026\\u003e\\u2028\u4e16\u754c\"}"},
		{`\u0026`, `{"input":"\\u0026"}`},
		{"\x01\b\f\r\t\u2029", `{"input":"\u0001\b\f\r\t\u2029"}`},
		{"", `{"input":""}`},
	} {
		request := customArgumentRequest(t, fixture.input, 1)
		legacy, _ := New(DefaultOptions())
		expected, _, err := legacy.EncodeProviderRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		options.MaxRequestBytes = len(expected)
		codec, _ := New(options)
		actual, _, err := codec.EncodeProviderRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actual, expected) {
			t.Fatal("custom argument output changed")
		}
		var wire openAIRequestWire
		if err := json.Unmarshal(actual, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.Messages[0].ToolCalls[0].Function.Arguments != fixture.want {
			t.Fatalf("inner JSON differs from literal: %q", wire.Messages[0].ToolCalls[0].Function.Arguments)
		}
	}
	schema, err := protocolcore.NewJSONObject([]byte(`{"type":"object"}`), protocolcore.MaxToolJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := protocolcore.NewJSONObject([]byte("{\"s\":\"&\",\"q\":\"\\\"\",\"u\":\"\u4e16\u754c\"}"), protocolcore.MaxToolJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	request := customArgumentRequest(t, "", 1)
	request.Tools = []protocolcore.ToolDefinition{{Kind: protocolcore.ToolKindFunction, Name: "f", InputSchema: schema}}
	request.Messages[0].Blocks[0].ToolCall.Kind = protocolcore.ToolKindFunction
	request.Messages[0].Blocks[0].ToolCall.Arguments = arguments
	legacy, _ := New(DefaultOptions())
	expected, _, err := legacy.EncodeProviderRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	options.MaxRequestBytes = len(expected)
	codec, _ := New(options)
	actual, _, err := codec.EncodeProviderRequest(request)
	if err != nil || !bytes.Equal(actual, expected) {
		t.Fatalf("function argument control changed: %v", err)
	}
}

func TestAnthropicResourceAdmissionCustomArgumentsOuterLiteralBoundary(t *testing.T) {
	request := customArgumentRequest(t, "&", 1)
	// Inner literal {"input":"\u0026"} is18 bytes; quoting it as a
	// JSON string is25 bytes (four escaped quotes, one escaped backslash).
	// At24 it must fail before a completed-call notice; at25 the argument
	// fits and the enclosing full request then fails, retaining that notice.
	for _, control := range []struct{ wire, notices int }{{18, 0}, {24, 0}, {25, 1}} {
		policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 8 << 20, StructureBytes: 8 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 16 << 20, StructureBytes: 8 << 20}}
		options := DefaultOptions()
		options.Resources = &policy
		options.MaxRequestBytes = control.wire
		codec, _ := New(options)
		_, report, err := codec.EncodeProviderRequest(request)
		if err == nil {
			t.Fatal("enclosing request must exceed this small wire limit")
		}
		if protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidClientRequest {
			t.Fatalf("resource failure category changed: %v", err)
		}
		if len(report.Notices()) != control.notices {
			t.Fatalf("outer quoted literal boundary%d produced%d notices, want%d", control.wire, len(report.Notices()), control.notices)
		}
	}
}

func TestAnthropicResourceAdmissionFunctionArgumentsCopiesAndWire(t *testing.T) {
	request := customArgumentRequest(t, "", 1)
	schema, err := protocolcore.NewJSONObject([]byte(`{"type":"object"}`), protocolcore.MaxToolJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := protocolcore.NewJSONObject([]byte(`{"input":"`+strings.Repeat("&", 1<<20)+`"}`), protocolcore.MaxToolJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	request.Tools = []protocolcore.ToolDefinition{{Kind: protocolcore.ToolKindFunction, Name: "f", InputSchema: schema}}
	request.Messages[0].Blocks[0].ToolCall.Kind = protocolcore.ToolKindFunction
	request.Messages[0].Blocks[0].ToolCall.Arguments = arguments
	for _, control := range []struct {
		name          string
		payload       uint64
		wire          int
		maxAllocation uint64
	}{
		{"copies", 2 << 20, 16 << 20, 1 << 20},
		{"outer_wire", 64 << 20, 2 << 20, 1800 << 10},
	} {
		t.Run(control.name, func(t *testing.T) {
			policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: control.payload, StructureBytes: 8 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 16 << 20, StructureBytes: 8 << 20}}
			options := DefaultOptions()
			options.Resources = &policy
			options.MaxRequestBytes = control.wire
			codec, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			if err := codec.ValidateRequest(request); err != nil {
				t.Fatal(err)
			}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, _, err = codec.EncodeProviderRequest(request)
			runtime.ReadMemStats(&after)
			allocated := after.TotalAlloc - before.TotalAlloc
			t.Logf("function argument allocation=%d error=%v", allocated, err)
			if err == nil {
				t.Fatal("function copies/wire received free allowance")
			}
			if allocated > control.maxAllocation {
				t.Fatalf("unreserved function argument copy: %d bytes", allocated)
			}
		})
	}
}

func TestAnthropicResourceAdmissionCompleteHistories(t *testing.T) {
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 128 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 128 << 20}}
	options := DefaultOptions()
	options.Resources = &policy
	codec, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := New(DefaultOptions())
	policy.Request = protocolcore.ResourceCost{PayloadBytes: 1, StructureBytes: 1}
	for _, count := range []int{4095, 4096, 4097, 4102, 4111, 16384} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var body strings.Builder
			body.WriteString(`{"model":"m","max_tokens":100,"messages":[`)
			for i := 0; i < count; i++ {
				if i > 0 {
					body.WriteByte(',')
				}
				text := ""
				if count == 4111 {
					text = strings.Repeat("x", 1000)
				}
				if i == count-1 {
					text = "distinct tail"
				}
				fmt.Fprintf(&body, `{"role":"user","content":%q}`, text)
			}
			body.WriteString(`]}`)
			request, _, err := codec.DecodeClientRequest([]byte(body.String()))
			if err != nil {
				t.Fatal(err)
			}
			if len(request.Messages) != count || request.Messages[count-1].Blocks[0].Text != "distinct tail" {
				t.Fatal("history lost")
			}
			if count > 4096 {
				if got, _, err := legacy.DecodeClientRequest([]byte(body.String())); err != nil || len(got.Messages) != count {
					t.Fatalf("default complete history lost: %v", err)
				}
			}
			if _, _, err := codec.EncodeProviderRequest(request); err != nil {
				t.Fatal(err)
			}
			request.Stream = true
			if _, err := codec.NewProviderStream(request); err != nil {
				t.Fatal(err)
			}
			if _, err := codec.NewAnthropicProviderStream(request); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAnthropicResourceAdmissionGeneratedPathsBeforeAllocation(t *testing.T) {
	var body strings.Builder
	body.WriteByte('{')
	for i := 0; i < 512; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `"k%x":0`, i)
	}
	body.WriteByte('}')
	prefix := strings.Repeat("p", 32<<10)
	budget, _ := protocolcore.NewResourceBudget(protocolcore.ResourceCost{PayloadBytes: 1000, StructureBytes: 1 << 20})
	var destination struct {
		Known string `json:"known"`
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := decodeTolerantWithin([]byte(body.String()), &destination, prefix, budget)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("generated long notice paths accepted")
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4<<20 {
		t.Fatalf("notice paths allocated before refusal: %d", allocated)
	}
}

func TestAnthropicResourceAdmissionStreamAggregate(t *testing.T) {
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 128 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 20000}}
	options := DefaultOptions()
	options.Resources = &policy
	codec, _ := New(options)
	request := protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Stream: true, Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText}}}}}
	stream, err := codec.NewAnthropicProviderStream(request)
	if err != nil {
		t.Fatal(err)
	}
	start := []byte("event: message_start\ndata: " + `{"type":"message_start","message":{"id":"r","type":"message","role":"assistant","model":"m","usage":{"input_tokens":1}}}` + "\n\n")
	if _, err := stream.Feed(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		fragment := []byte(fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":%d,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n", i))
		if _, err := stream.Feed(context.Background(), fragment); err != nil {
			if !strings.Contains(err.Error(), "resource budget") {
				t.Fatal(err)
			}
			return
		}
		stop := []byte(fmt.Sprintf("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", i))
		if _, err := stream.Feed(context.Background(), stop); err != nil {
			if !strings.Contains(err.Error(), "resource budget") {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("stream accumulated materialized cells beyond finite response policy")
}

func TestAnthropicResourceAdmissionConstructorRejectsZeroPolicy(t *testing.T) {
	policy := protocolcore.ResourceLimits{}
	options := DefaultOptions()
	options.Resources = &policy
	if _, err := New(options); err == nil {
		t.Fatal("zero finite policy accepted")
	}
}

func TestAnthropicResourceAdmissionEscapingBeforeMarshal(t *testing.T) {
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 128 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 128 << 20}}
	options := DefaultOptions()
	options.Resources = &policy
	codec, _ := New(options)
	request := protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: strings.Repeat("&", 6<<20)}}}}}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, _, err := codec.EncodeProviderRequest(request)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("escaped provider request exceeds inherited wire bound")
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("oversized escaped output materialized before refusal: %d", allocated)
	}
}

func TestAnthropicResourceAdmissionDenseTextGrowth(t *testing.T) {
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 128 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 128 << 20}}
	options := DefaultOptions()
	options.Resources = &policy
	codec, _ := New(options)
	blocks := make([]protocolcore.ContentBlock, 4096)
	for i := range blocks {
		blocks[i] = protocolcore.ContentBlock{Kind: protocolcore.BlockText, Text: strings.Repeat("x", 256)}
	}
	request := protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: blocks}}}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	encoded, _, err := codec.EncodeProviderRequest(request)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) < 1<<20 {
		t.Fatal("dense text truncated")
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 32<<20 {
		t.Fatalf("dense text growth allocated %d bytes", allocated)
	}
}

func TestAnthropicResourceAdmissionWireBoundaryMatchesEncoding(t *testing.T) {
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 128 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 128 << 20}}
	for _, fraction := range []float64{0, 0.000001, 0.0000001, 0.1234, 1} {
		request := protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Temperature: &fraction, Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "<&>\n\u4e16\u754c\u2028"}}}}}
		legacy, _ := New(DefaultOptions())
		expected, _, err := legacy.EncodeProviderRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		options := DefaultOptions()
		options.Resources = &policy
		options.MaxRequestBytes = len(expected)
		codec, _ := New(options)
		actual, _, err := codec.EncodeProviderRequest(request)
		if err != nil || string(actual) != string(expected) {
			t.Fatalf("wire boundary changed for %g: %v", fraction, err)
		}
		options.MaxRequestBytes--
		codec, _ = New(options)
		if _, _, err := codec.EncodeProviderRequest(request); err == nil {
			t.Fatal("wire boundary overrun accepted")
		}
	}
}
