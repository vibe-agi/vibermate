package anthropicchat

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestChatStreamEventScratchDoesNotAccumulate(t *testing.T) {
	for _, batch := range []bool{false, true} {
		options := DefaultOptions()
		options.Resources = &protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 1 << 20, StructureBytes: 1 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 64 << 10, StructureBytes: 64 << 10}}
		codec, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		stream, err := codec.NewProviderStream(newStreamingRequest(t, codec))
		if err != nil {
			t.Fatal(err)
		}
		const usage = `{"id":"chatcmpl-noop","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":0,"total_tokens":2}}`
		if batch {
			if _, err = stream.Feed(context.Background(), []byte(strings.Repeat("data: "+usage+"\n\n", 256))); err != nil {
				t.Fatalf("batched discarded scratch accumulated: %v", err)
			}
		} else {
			for i := 0; i < 256; i++ {
				if _, err = stream.Feed(context.Background(), joinProviderEvents(t, usage)); err != nil {
					t.Fatalf("event %d discarded scratch accumulated: %v", i, err)
				}
			}
		}
		if stream.SemanticProgress() != 1 {
			t.Fatalf("noop progress=%d", stream.SemanticProgress())
		}
		end := `{"id":"chatcmpl-noop","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`
		if _, err = stream.Feed(context.Background(), joinProviderEvents(t, end, "[DONE]")); err != nil {
			t.Fatal(err)
		}
		pending, err := stream.FinishDecoded(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if pending.DecodedResponse().Blocks[0].Text != "done" {
			t.Fatal("terminal content changed")
		}
	}
}

func TestChatStreamRetainedGrowthConsumesCreditBeforeAppend(t *testing.T) {
	for _, kind := range []string{"text", "notices", "reasoning", "arguments"} {
		t.Run(kind, func(t *testing.T) {
			options := DefaultOptions()
			options.Resources = &protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 1 << 20, StructureBytes: 1 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 64 << 10, StructureBytes: 64 << 10}}
			codec, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			stream, err := codec.NewProviderStream(newStreamingRequest(t, codec))
			if err != nil {
				t.Fatal(err)
			}
			failed := false
			for i := 0; i < 2000; i++ {
				delta := `"content":"` + strings.Repeat("x", 1024) + `"`
				suffix := ""
				switch kind {
				case "notices":
					delta = `"content":""`
					suffix = `,"service_tier":"default"`
				case "reasoning":
					delta = `"reasoning_content":"` + strings.Repeat("x", 1024) + `"`
				case "arguments":
					delta = `"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"f","arguments":"` + strings.Repeat("x", 1024) + `"}}]`
				}
				event := fmt.Sprintf(`{"id":"r","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{%s},"finish_reason":null}]%s}`, delta, suffix)
				beforeText, beforeReasoning, beforeTools := stream.preToolText.Len(), len(stream.reasoning), 0
				if tool := stream.tools[0]; tool != nil {
					beforeTools = len(tool.arguments)
				}
				_, err := stream.Feed(context.Background(), joinProviderEvents(t, event))
				if err != nil {
					failed = true
					if kind == "text" && stream.preToolText.Len() != beforeText || kind == "reasoning" && len(stream.reasoning) != beforeReasoning || kind == "arguments" && len(stream.tools[0].arguments) != beforeTools {
						t.Fatal("growth allocated before its retained refusal")
					}
					break
				}
			}
			if !failed {
				t.Fatal("retained growth escaped finite policy")
			}
		})
	}
}
