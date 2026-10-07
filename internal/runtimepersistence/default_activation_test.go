package runtimepersistence

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// The same history crosses every public default adapter. The response expands
// past one physical row under canonical escaping, without changing leaf limits.
func TestCoupledDefaultsCompleteLongFragmentedRecord(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	record := contentRecordFixture(t, "default-long-fragment", now)
	request := protocolcore.Request{RequestedModel: "model", EffectiveModel: "model", Messages: make([]protocolcore.Message, 4111)}
	input := make([]map[string]string, 4111)
	record.Request.Messages = make([]exchangecontent.Message, 4111)
	for i := range request.Messages {
		text := "historical"
		if i == 4110 {
			text = "complete-tail"
		}
		request.Messages[i] = protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: text}}}
		input[i] = map[string]string{"role": "user", "content": text}
		record.Request.Messages[i] = exchangecontent.Message{Role: "user", Blocks: []exchangecontent.Block{sourceText(text)}}
	}
	text := strings.Repeat("&", 6<<20)
	record.Response.Blocks = []exchangecontent.Block{sourceText(text)}
	response := protocolcore.Response{ID: "response-default", RequestedModel: "model", EffectiveModel: "model", ReportedModel: "model", StopReason: protocolcore.StopReasonEndTurn, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: text}}}
	t.Run("semantic", func(t *testing.T) {
		if err := request.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	body, _ := json.Marshal(map[string]any{"model": "model", "input": input})
	for _, nilPolicy := range []bool{false, true} {
		t.Run(map[bool]string{false: "codec_default", true: "codec_nil"}[nilPolicy], func(t *testing.T) {
			options := openairesponses.DefaultOptions()
			if nilPolicy {
				options.Resources = nil
			}
			codec, err := openairesponses.New(options)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := codec.DecodeClientRequest(body)
			if err != nil || len(got.Messages) != 4111 {
				t.Fatalf("history=%d error=%v", len(got.Messages), err)
			}
		})
	}
	t.Run("new_record", func(t *testing.T) {
		got, err := exchangecontent.NewRecord(record.ExchangeID, record.Frozen, environment.DefaultContentRecordingPolicy(), now, request, &response)
		if err != nil || len(got.Request.Messages) != 4111 {
			t.Fatalf("history=%d error=%v", len(got.Request.Messages), err)
		}
	})
	t.Run("record", func(t *testing.T) {
		if err := record.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	short := record
	short.Request.Messages = record.Request.Messages[:1]
	t.Run("whole_record_over_32MiB", func(t *testing.T) {
		if err := short.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("byte_convenience_preflight", func(t *testing.T) {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		encoded, err := exchangecontent.CanonicalJSON(short)
		runtime.ReadMemStats(&after)
		if err == nil || encoded != nil {
			t.Fatal("32MiB byte convenience admitted fragmented record")
		}
		// Producing this forbidden canonical output alone needs >32MiB.
		// This checks admission before output allocation, not RSS or speed.
		if after.TotalAlloc-before.TotalAlloc >= exchangecontent.MaxEncodedBytes {
			t.Fatalf("byte convenience materialized forbidden canonical output: allocated=%d", after.TotalAlloc-before.TotalAlloc)
		}
	})
	t.Run("projection", func(t *testing.T) {
		p := exchangecontent.Projection{ExchangeID: record.ExchangeID, Frozen: record.Frozen, Mode: record.Mode, RecordedAt: record.RecordedAt, ExpiresAt: record.ExpiresAt, Request: record.Request, Response: record.Response, Presentation: record.Presentation, View: exchangecontent.RequestViewFull, TotalMessageCount: 4111}
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
		if _, err := exchangecontent.Project(record, exchangecontent.RequestViewFull); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("store_manager", func(t *testing.T) {
		store := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), nil)
		manager, err := exchangecontent.New(ctx, exchangecontent.Options{Repository: store.exchangeContents, Clock: exchangecontent.SystemClock{}})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Shutdown(ctx)
		if err := manager.Record(ctx, record); err != nil {
			t.Fatal(err)
		}
		got, err := manager.Get(ctx, record.ExchangeID)
		if err != nil || len(got.Request.Messages) != 4111 || got.Request.Messages[4110].Blocks[0].Text != "complete-tail" || got.Response.Blocks[0].Text != text {
			t.Fatalf("complete readback failed: %v", err)
		}
	})
	t.Run("explicit_tighter", func(t *testing.T) {
		l := protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 1, StructureBytes: 1}, Response: protocolcore.ResourceCost{PayloadBytes: 1, StructureBytes: 1}}
		options := openairesponses.DefaultOptions()
		options.Resources = &l
		codec, err := openairesponses.New(options)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := codec.DecodeClientRequest(body); err == nil {
			t.Fatal("default overwrote tighter policy")
		}
	})
	t.Run("explicit_content_tighter", func(t *testing.T) {
		limits, err := exchangecontent.DefaultSourceLimits()
		if err != nil {
			t.Fatal(err)
		}
		limits.RetainedBytes = 1
		store := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &limits)
		if err := store.exchangeContents.Put(ctx, record); err == nil {
			t.Fatal("Store default overwrote tighter content policy")
		}
		manager, err := exchangecontent.New(ctx, exchangecontent.Options{Repository: store.exchangeContents, Clock: exchangecontent.SystemClock{}, ContentLimits: &limits})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Shutdown(ctx)
		limits.RetainedBytes = 1 << 30
		if err := manager.Record(ctx, record); err == nil {
			t.Fatal("Manager lost its copied tighter policy")
		}
	})
}

func TestDefaultResponseDomainSourceStoreReadback(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	base := contentRecordFixture(t, "response-default", now)
	request := protocolcore.Request{RequestedModel: "model", EffectiveModel: "model", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "question"}}}}}
	codec, err := openairesponses.New(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, shape := range []string{"dense_json", "near_text_leaf", "tool_extension", "chat_cumulative_wire"} {
		t.Run(shape, func(t *testing.T) {
			var response protocolcore.Response
			switch shape {
			case "dense_json":
				wire := `{"id":"resp","status":"completed","model":"model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"tail"}]}],"ignored":[` + strings.Repeat("[],", 100000) + `[]]}`
				wire += strings.Repeat(" ", (16<<20)-256-len(wire))
				cost, err := protocolcore.MeasureJSON([]byte(wire))
				if err != nil || cost.StructureBytes <= 32<<20 {
					t.Fatalf("dense lexical fixture: %+v %v", cost, err)
				}
				response, _, err = codec.DecodeProviderResponse(request, []byte(wire))
				if err != nil {
					t.Fatal(err)
				}
			case "near_text_leaf":
				text := strings.Repeat("&", (8<<20)-64) + "<tail>\n"
				quoted, _ := json.Marshal(text)
				// Literal HTML characters are legal input; canonical persistence expands them.
				quoted = []byte(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(string(quoted), `\u0026`, "&"), `\u003c`, "<"), `\u003e`, ">"))
				wire := []byte(`{"id":"resp","status":"completed","model":"model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":` + string(quoted) + `}]}]}`)
				response, _, err = codec.DecodeProviderResponse(request, wire)
				if err != nil || response.Blocks[0].Text != text {
					t.Fatalf("near text response: %v", err)
				}
			case "tool_extension":
				response, _, err = codec.DecodeProviderResponse(request, []byte(`{"id":"resp","status":"completed","model":"model","output":[{"type":"function_call","id":"item","call_id":"call","name":"read","arguments":"{\"value\":\"&\"}"},{"type":"reasoning","id":"reason","summary":[{"type":"summary_text","text":"reasoning-tail"}]}]}`))
				if err != nil || len(response.ProviderExtensions) == 0 {
					t.Fatalf("tool extension response: %v", err)
				}
			case "chat_cumulative_wire":
				chat, err := anthropicchat.New(anthropicchat.DefaultOptions())
				if err != nil {
					t.Fatal(err)
				}
				streaming := request
				streaming.Stream = true
				stream, err := chat.NewProviderStream(streaming)
				if err != nil {
					t.Fatal(err)
				}
				usage := `{"id":"resp","object":"chat.completion.chunk","created":1,"model":"model","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":0,"total_tokens":2},"service_tier":"default"}` + strings.Repeat(" ", 64<<10)
				event := []byte("data: " + usage + "\n\n")
				for range 257 {
					if _, err := stream.Feed(ctx, event); err != nil {
						t.Fatal(err)
					}
				}
				if len(event)*257 <= 16<<20 {
					t.Fatal("Chat fixture did not exceed old total-wire ceiling")
				}
				end := []byte("data: " + `{"id":"resp","object":"chat.completion.chunk","created":1,"model":"model","choices":[{"index":0,"delta":{"content":"chat-tail"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n")
				if _, err := stream.Feed(ctx, end); err != nil {
					t.Fatal(err)
				}
				pending, err := stream.FinishDecoded(ctx)
				if err != nil {
					t.Fatal(err)
				}
				response = pending.DecodedResponse()
				if response.Blocks[0].Text != "chat-tail" || response.Usage.InputUncached.Tokens != 2 {
					t.Fatal("Chat retained content/equal usage lost")
				}
			}
			id := "default-" + shape
			source, err := exchangecontent.NewSource(id, base.Frozen, environment.DefaultContentRecordingPolicy(), now, request, &response)
			if err != nil {
				t.Fatal(err)
			}
			store := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), nil)
			if err := store.exchangeContents.PutSource(ctx, source); err != nil {
				t.Fatal(err)
			}
			got, err := store.exchangeContents.Get(ctx, id, now)
			if err != nil || got.Response == nil {
				t.Fatalf("default response full readback: %v", err)
			}
			record, err := exchangecontent.NewRecord(id, base.Frozen, environment.DefaultContentRecordingPolicy(), now, request, &response)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := json.Marshal(record.Response)
			actual, _ := json.Marshal(got.Response)
			if string(want) != string(actual) {
				t.Fatal("Source/Store response differs from admitted response projection")
			}
		})
	}
}
