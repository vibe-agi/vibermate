package anthropicchat

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// The public codecs must retain every notice and history entry without copying
// the entire notice prefix for every message or nested block.
func TestAnthropicRequestNoticeDecodeAllocationGrowth(t *testing.T) {
	codec := newTestCodec(t)
	for _, compatible := range []bool{false, true} {
		for _, shape := range []string{"messages", "system", "blocks"} {
			t.Run(fmt.Sprintf("compatible=%t/%s", compatible, shape), func(t *testing.T) {
				decode := codec.DecodeClientRequest
				if compatible {
					decode = codec.DecodeCompatibleClientRequest
				}
				measure := func(n int) uint64 {
					body := anthropicNoticeFixture(t, n, shape, -1)
					if _, _, err := decode(body); err != nil {
						t.Fatal(err)
					}
					var request protocolcore.Request
					var report protocolcore.TranslationReport
					var err error
					var elapsed time.Duration
					allocated := anthropicMeasuredBytes(t, func() { start := time.Now(); request, report, err = decode(body); elapsed = time.Since(start) })
					if err != nil {
						t.Fatal(err)
					}
					assertAnthropicCacheNotices(t, report, n, shape, !compatible)
					if shape == "messages" {
						if len(request.Messages) != n {
							t.Fatalf("messages=%d want=%d", len(request.Messages), n)
						}
						for i, message := range request.Messages {
							if message.Role != protocolcore.RoleUser || len(message.Blocks) != 1 || message.Blocks[0].Text != "synthetic" {
								t.Fatalf("message[%d]=%#v", i, message)
							}
						}
					} else {
						blocks := request.System
						if shape == "blocks" {
							blocks = request.Messages[0].Blocks
						}
						if len(blocks) != n {
							t.Fatalf("blocks=%d want=%d", len(blocks), n)
						}
						for i, block := range blocks {
							if block.Kind != protocolcore.BlockText || block.Text != "synthetic" {
								t.Fatalf("block[%d]=%#v", i, block)
							}
						}
					}
					t.Logf("n=%d allocated_bytes=%d elapsed=%s", n, allocated, elapsed)
					return allocated
				}
				small, large := measure(512), measure(2048)
				if large > 6*small+(8<<20) {
					t.Fatalf("request notice allocation grows faster than bounded linear work: small=%d large=%d", small, large)
				}
			})
		}
	}
}

func TestAnthropicRequestNoticeDecodeLateErrorPrefixes(t *testing.T) {
	codec := newTestCodec(t)
	for _, compatible := range []bool{false, true} {
		decode := codec.DecodeClientRequest
		if compatible {
			decode = codec.DecodeCompatibleClientRequest
		}
		for _, shape := range []string{"messages", "system", "blocks"} {
			_, report, err := decode(anthropicNoticeFixture(t, 2048, shape, 2040))
			if err == nil {
				t.Fatalf("%s late error accepted", shape)
			}
			count := 0 // The outer request does not append a failed helper report.
			if shape == "messages" {
				count = 2040
			}
			assertAnthropicCacheNotices(t, report, count, shape, !compatible)
		}
	}
}

func TestAnthropicRequestNoticeEncodeAllocationGrowth(t *testing.T) {
	codec := newTestCodec(t)
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprintf("nested=%t", nested), func(t *testing.T) {
			measure := func(n int) uint64 {
				request := chatNoticeFixture(t, n, nested)
				if _, _, err := codec.EncodeProviderRequest(request); err != nil {
					t.Fatal(err)
				}
				var body []byte
				var report protocolcore.TranslationReport
				var err error
				var elapsed time.Duration
				allocated := anthropicMeasuredBytes(t, func() {
					start := time.Now()
					body, report, err = codec.EncodeProviderRequest(request)
					elapsed = time.Since(start)
				})
				if err != nil {
					t.Fatal(err)
				}
				assertChatNoticeOutput(t, body, report, n, nested)
				t.Logf("n=%d allocated_bytes=%d elapsed=%s", n, allocated, elapsed)
				return allocated
			}
			small, large := measure(512), measure(2048)
			if large > 6*small+(8<<20) {
				t.Fatalf("request notice allocation grows faster than bounded linear work: small=%d large=%d", small, large)
			}
		})
	}
}

func TestAnthropicRequestNoticeEncodeLateErrorPrefix(t *testing.T) {
	request := chatNoticeFixture(t, 2048, false)
	refusal, err := protocolcore.NewRefusalBlock("synthetic refusal")
	if err != nil {
		t.Fatal(err)
	}
	request.Messages[2040].Blocks = append(request.Messages[2040].Blocks, refusal)
	body, report, err := newTestCodec(t).EncodeProviderRequest(request)
	if body != nil || protocolcore.ReasonOf(err) != protocolcore.ReasonUnsupportedClientInput || !strings.Contains(err.Error(), "$.messages") {
		t.Fatalf("late encode body=%d error=%v", len(body), err)
	}
	assertChatIdentityNotices(t, report, 2040, false)
}

func TestAnthropicRequestNoticeRemainingCountGuard(t *testing.T) {
	codec := newTestCodec(t)
	for _, n := range []int{4000, 4111} {
		body := anthropicNoticeFixture(t, n, "messages", -1)
		for _, compatible := range []bool{false, true} {
			decode := codec.DecodeClientRequest
			if compatible {
				decode = codec.DecodeCompatibleClientRequest
			}
			var request protocolcore.Request
			var report protocolcore.TranslationReport
			var err error
			var elapsed time.Duration
			allocated := anthropicMeasuredBytes(t, func() {
				start := time.Now()
				request, report, err = decode(body)
				elapsed = time.Since(start)
			})
			t.Logf("decode compatible=%t n=%d allocated_bytes=%d elapsed=%s error=%v", compatible, n, allocated, elapsed, err)
			assertAnthropicCacheNotices(t, report, n, "messages", !compatible)
			if err != nil || len(request.Messages) != n || request.Messages[n-1].Blocks[0].Text != "synthetic" {
				t.Fatalf("default messages=%d error=%v", len(request.Messages), err)
			}
		}
		request := chatNoticeFixture(t, n, false)
		var encoded []byte
		var report protocolcore.TranslationReport
		var err error
		var elapsed time.Duration
		allocated := anthropicMeasuredBytes(t, func() {
			start := time.Now()
			encoded, report, err = codec.EncodeProviderRequest(request)
			elapsed = time.Since(start)
		})
		t.Logf("encode n=%d allocated_bytes=%d elapsed=%s error=%v", n, allocated, elapsed, err)
		if err != nil {
			t.Fatal(err)
		}
		assertChatNoticeOutput(t, encoded, report, n, false)
	}
}

func anthropicNoticeFixture(t *testing.T, n int, shape string, invalid int) []byte {
	t.Helper()
	blocks := make([]any, n)
	messages := make([]any, n)
	for i := range blocks {
		kind, role := "text", "user"
		if i == invalid {
			kind, role = "", "invalid"
		}
		blocks[i] = map[string]any{"type": kind, "text": "synthetic", "cache_control": map[string]string{"type": "ephemeral"}}
		messages[i] = map[string]any{"role": role, "content": []any{blocks[i]}}
	}
	root := map[string]any{"model": "fixture", "max_tokens": 64, "private_fixture": true}
	switch shape {
	case "messages":
		root["messages"] = messages
	case "system":
		root["system"] = blocks
		root["messages"] = []any{map[string]string{"role": "user", "content": "tail sentinel"}}
	case "blocks":
		root["messages"] = []any{map[string]any{"role": "user", "content": blocks}}
	}
	body, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertAnthropicCacheNotices(t *testing.T, report protocolcore.TranslationReport, count int, shape string, unknown bool) {
	t.Helper()
	notices := report.Notices()
	offset := 0
	if unknown {
		offset = 1
	}
	if len(notices) != count+offset {
		t.Fatalf("%s notices=%d want=%d", shape, len(notices), count+offset)
	}
	if unknown && notices[0] != (protocolcore.TranslationNotice{Code: protocolcore.NoticeUnknownRequestFieldNotForwarded, Path: "$.private_fixture"}) {
		t.Fatalf("initial unknown notice=%#v", notices[0])
	}
	for i := 0; i < count; i++ {
		path := fmt.Sprintf("$.messages[%d].content[0].cache_control", i)
		if shape == "system" {
			path = fmt.Sprintf("$.system[%d].cache_control", i)
		}
		if shape == "blocks" {
			path = fmt.Sprintf("$.messages[0].content[%d].cache_control", i)
		}
		want := protocolcore.TranslationNotice{Code: protocolcore.NoticeCacheControlNotForwarded, Path: path}
		if notices[offset+i] != want {
			t.Fatalf("notice[%d]=%#v want=%#v", offset+i, notices[offset+i], want)
		}
	}
}

func chatNoticeFixture(t *testing.T, n int, nested bool) protocolcore.Request {
	t.Helper()
	request := protocolcore.Request{RequestedModel: "fixture", EffectiveModel: "fixture"}
	schema, err := protocolcore.NewJSONObject([]byte(`{"type":"object"}`), 1024)
	if err != nil {
		t.Fatal(err)
	}
	request.Tools = []protocolcore.ToolDefinition{{Name: "history_tool", InputSchema: schema}}
	for i := 0; i < n; i++ {
		key, err := protocolcore.NewCallKey("fixture", fmt.Sprintf("call-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		item, err := protocolcore.NewCallKey("fixture", fmt.Sprintf("item-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		arguments, err := protocolcore.NewJSONObject([]byte(`{"value":"synthetic"}`), 1024)
		if err != nil {
			t.Fatal(err)
		}
		block, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{Key: key, ItemKey: item, Name: "history_tool", Arguments: arguments})
		if err != nil {
			t.Fatal(err)
		}
		if nested && i > 0 {
			request.Messages[0].Blocks = append(request.Messages[0].Blocks, block)
		} else {
			request.Messages = append(request.Messages, protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{block}})
		}
	}
	return request
}

func assertChatIdentityNotices(t *testing.T, report protocolcore.TranslationReport, count int, nested bool) {
	t.Helper()
	notices := report.Notices()
	if len(notices) != count {
		t.Fatalf("identity notices=%d want=%d", len(notices), count)
	}
	for i, notice := range notices {
		message, block := i, 0
		if nested {
			message, block = 0, i
		}
		want := protocolcore.TranslationNotice{Code: protocolcore.NoticeToolItemIdentityNotForwarded, Path: fmt.Sprintf("$.messages[%d].blocks[%d].item_id", message, block)}
		if notice != want {
			t.Fatalf("notice[%d]=%#v want=%#v", i, notice, want)
		}
	}
}

func assertChatNoticeOutput(t *testing.T, body []byte, report protocolcore.TranslationReport, count int, nested bool) {
	t.Helper()
	assertChatIdentityNotices(t, report, count, nested)
	var wire struct {
		Model string `json:"model"`
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name       string          `json:"name"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	wantMessages := count
	if nested {
		wantMessages = 1
	}
	if wire.Model != "fixture" || len(wire.Messages) != wantMessages {
		t.Fatalf("wire model=%q messages=%d want=%d", wire.Model, len(wire.Messages), wantMessages)
	}
	if len(wire.Tools) != 1 || wire.Tools[0].Type != "function" || wire.Tools[0].Function.Name != "history_tool" || string(wire.Tools[0].Function.Parameters) != `{"type":"object"}` {
		t.Fatalf("wire tool definitions=%#v", wire.Tools)
	}
	index := 0
	for i, message := range wire.Messages {
		wantCalls := 1
		if nested {
			wantCalls = count
		}
		if message.Role != "assistant" || len(message.ToolCalls) != wantCalls {
			t.Fatalf("wire message[%d] role=%q calls=%d", i, message.Role, len(message.ToolCalls))
		}
		for _, call := range message.ToolCalls {
			if call.ID != fmt.Sprintf("call-%d", index) || call.Type != "function" || call.Function.Name != "history_tool" || call.Function.Arguments != `{"value":"synthetic"}` {
				t.Fatalf("wire tool[%d]=%#v", index, call)
			}
			index++
		}
	}
}

func anthropicMeasuredBytes(t *testing.T, run func()) uint64 {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	run()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}
