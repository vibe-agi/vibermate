package anthropicchat

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestAnthropicResourceAdmissionCompleteHistories(t *testing.T) {
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{128 << 20, 128 << 20}, Response: protocolcore.ResourceCost{128 << 20, 128 << 20}}
	options := DefaultOptions()
	options.Resources = &policy
	codec, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := New(DefaultOptions())
	policy.Request = protocolcore.ResourceCost{1, 1}
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
				if _, _, err := legacy.DecodeClientRequest([]byte(body.String())); err == nil {
					t.Fatal("legacy count guard disappeared")
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
	budget, _ := protocolcore.NewResourceBudget(protocolcore.ResourceCost{1000, 1 << 20})
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
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{128 << 20, 128 << 20}, Response: protocolcore.ResourceCost{128 << 20, 20000}}
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
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{128 << 20, 128 << 20}, Response: protocolcore.ResourceCost{128 << 20, 128 << 20}}
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
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{128 << 20, 128 << 20}, Response: protocolcore.ResourceCost{128 << 20, 128 << 20}}
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
	policy := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{128 << 20, 128 << 20}, Response: protocolcore.ResourceCost{128 << 20, 128 << 20}}
	for _, fraction := range []float64{0, 0.000001, 0.0000001, 0.1234, 1} {
		request := protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Temperature: &fraction, Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "<&>\n世界\u2028"}}}}}
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
