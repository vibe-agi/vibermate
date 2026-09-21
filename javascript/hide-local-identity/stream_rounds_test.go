package hideidentity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

// These tests deliberately specify aliases and expected originals independently
// of the request transform's output. All identities and provider data are synthetic.
var srProtocols = []string{"responses", "anthropic", "chat"}
var srSeeds = []int64{0, 7, 42, 20260911} // zero means every Unicode character.

func srFragments(text string, seed int64) []string {
	chars := []rune(text)
	rng := rand.New(rand.NewSource(seed))
	parts := []string{""}
	for len(chars) > 0 {
		n := 1
		if seed != 0 {
			n = 1 + rng.Intn(11)
		}
		if n > len(chars) {
			n = len(chars)
		}
		parts = append(parts, string(chars[:n]))
		chars = chars[n:]
	}
	return append(parts, "")
}

// Separate channels include content indexes, response items, Anthropic blocks,
// Chat choices, and both ordinary text and refusal text where the protocol has it.
func srTextEvent(protocol string, channel int, fragment string) map[string]any {
	switch protocol {
	case "responses":
		kind := "response.output_text.delta"
		if channel == 3 {
			kind = "response.refusal.delta"
		}
		return map[string]any{"type": kind, "output_index": channel / 2, "content_index": channel % 2, "delta": fragment}
	case "anthropic":
		return delta(protocol, fragment, false, channel)
	default:
		field := "content"
		if channel == 3 {
			field = "refusal"
		}
		return map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{
			"index": channel / 2, "delta": map[string]any{field: fragment}, "finish_reason": nil,
		}}}
	}
}

func srTextValue(value map[string]any, protocol string, channel int) string {
	if protocol != "chat" {
		return deltaText(value, protocol, false)
	}
	field := "content"
	if channel == 3 {
		field = "refusal"
	}
	return value["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)[field].(string)
}

func srTextStop(protocol string, channel int) map[string]any {
	switch protocol {
	case "responses":
		return map[string]any{"type": "response.content_part.done", "output_index": channel / 2, "content_index": channel % 2,
			"part": map[string]any{"type": "output_text", "text": ""}}
	case "anthropic":
		return map[string]any{"type": "content_block_stop", "index": channel}
	default:
		return map[string]any{"choices": []any{map[string]any{"index": channel / 2, "delta": map[string]any{}, "finish_reason": "stop"}}}
	}
}

func srAssertError(t *testing.T, turn *messagetransform.PipelineTurn, event map[string]any) {
	t.Helper()
	if _, err := applyResponse(t, turn, event, true); err == nil {
		t.Fatal("expected explicit safe refusal, not successful truncation or mutation")
	} else if strings.Contains(err.Error(), "alice") || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatalf("error disclosed synthetic message data: %v", err)
	}
}

func TestStreamRoundsTextInterleaving(t *testing.T) {
	fixtures := []struct {
		name string
		meta messagetransform.RuntimeMetadata
		from [4]string
		want [4]string
	}{
		{"posix", metadata("alice"),
			[4]string{"start " + workspaceAlias + "/a.txt " + userAlias + " end", homeAlias + " 中文🙂 /", workspaceAlias + workspaceAlias + "!", "refuse " + homeAlias + " then " + userAlias + "."},
			[4]string{"start /Users/alice/work/project/a.txt alice end", "/Users/alice 中文🙂 /", "/Users/alice/work/project/Users/alice/work/project!", "refuse /Users/alice then alice."}},
		{"windows", messagetransform.RuntimeMetadata{LocalUserName: "alice", HomeDirectory: `C:\Users\alice`, WorkspaceRoot: `C:\Users\alice\Work\project`, OperatingSystem: "windows"},
			[4]string{`C:\__vmi1_workspace__\src\a.go`, `C:/__vmi1_home_slash__/file.txt`, "hello " + userAlias + " 普通 C:", `C%3A%5C__vmi1_workspace_encoded__%5Ca.txt`},
			[4]string{`C:\Users\alice\Work\project\src\a.go`, `C:/Users/alice/file.txt`, "hello alice 普通 C:", `C%3A%5CUsers%5Calice%5CWork%5Cproject%5Ca.txt`}},
		{"uri_unicode", metadata("Alice Smith"),
			[4]string{`/__vmi1_home_uri__/f`, `%2F__vmi1_workspace_encoded__%2Ff`, "用户=" + userAlias + "; 🙂", workspaceAlias + "/中文.file"},
			[4]string{`/Users/Alice%20Smith/f`, `%2FUsers%2FAlice%20Smith%2Fwork%2Fproject%2Ff`, "用户=Alice Smith; 🙂", "/Users/Alice Smith/work/project/中文.file"}},
	}
	for _, protocol := range srProtocols {
		for _, fixture := range fixtures {
			for _, seed := range srSeeds {
				t.Run(fmt.Sprintf("%s/%s/seed_%d", protocol, fixture.name, seed), func(t *testing.T) {
					turn := ready(t, fixture.meta)
					var parts [4][]string
					var got [4]strings.Builder
					for i := range parts {
						parts[i] = srFragments(fixture.from[i], seed)
					}
					for step := 0; ; step++ {
						active := false
						for n := range parts {
							ch := (n + step) % len(parts)
							if step >= len(parts[ch]) {
								continue
							}
							active = true
							// Chat channels 0/1 use distinct choices; channel 3 uses
							// refusal of choice 1, independently of choice 1 text.
							channel := ch
							if protocol == "chat" && ch == 1 {
								channel = 4
							}
							got[ch].WriteString(srTextValue(output(t, turn, srTextEvent(protocol, channel, parts[ch][step]), true), protocol, channel))
						}
						if !active {
							break
						}
					}
					for ch := range got {
						channel := ch
						if protocol == "chat" && ch == 1 {
							channel = 4
						}
						output(t, turn, srTextStop(protocol, channel), true)
						if got[ch].String() != fixture.want[ch] {
							t.Fatalf("channel %d: got %q, want %q", ch, got[ch].String(), fixture.want[ch])
						}
					}
				})
			}
		}
	}
}

func TestStreamRoundsToolEscapesAndInterleaving(t *testing.T) {
	for _, protocol := range srProtocols {
		for _, windows := range []bool{false, true} {
			for _, seed := range srSeeds {
				t.Run(fmt.Sprintf("%s/windows_%v/seed_%d", protocol, windows, seed), func(t *testing.T) {
					meta := metadata("alice")
					alias, original := workspaceAlias, "/Users/alice/work/project"
					if windows {
						meta.HomeDirectory, meta.WorkspaceRoot = `C:\Users\alice`, `C:\Users\alice\Work\project`
						meta.OperatingSystem = "windows"
						alias, original = `C:\__vmi1_workspace__`, `C:\Users\alice\Work\project`
					}
					turn := ready(t, meta)
					var parts [3][]string
					var got [3]strings.Builder
					want := make([]map[string]any, 3)
					for tool := range parts {
						suffix := fmt.Sprintf(`/file-%d"quoted"\slash\中文🙂`, tool)
						value := map[string]any{"cwd": alias + suffix, "username": userAlias, "options": []any{7, true, nil, "line1\nline2\tquote\"slash\\"}, "tool": tool}
						body := string(marshal(t, value))
						// Exercise both outer JSON SSE encoding and escaped inner
						// JSON Unicode sequences, whose cuts can split backslashes.
						body = strings.ReplaceAll(body, "⟪", `\u27ea`)
						body = strings.ReplaceAll(body, "⟫", `\u27eb`)
						parts[tool] = srFragments(body, seed)
						want[tool] = map[string]any{"cwd": original + suffix, "username": "alice", "options": []any{float64(7), true, nil, "line1\nline2\tquote\"slash\\"}, "tool": float64(tool)}
					}
					for step := 0; ; step++ {
						active := false
						for tool := range parts {
							if step >= len(parts[tool]) {
								continue
							}
							active = true
							part := deltaText(output(t, turn, delta(protocol, parts[tool][step], true, tool), true), protocol, true)
							if part != "" && step < len(parts[tool])-2 {
								t.Fatal("incomplete tool JSON was emitted before the complete object")
							}
							got[tool].WriteString(part)
						}
						if !active {
							break
						}
					}
					output(t, turn, terminal(protocol), true)
					for tool := range got {
						if actual := decode(t, []byte(got[tool].String())); !reflect.DeepEqual(actual, want[tool]) {
							t.Fatalf("tool %d mixed channels or changed argument structure: %#v; want %#v", tool, actual, want[tool])
						}
					}
				})
			}
		}
	}
}

func TestStreamRoundsExplicitTerminationAndJSONFailures(t *testing.T) {
	for _, protocol := range srProtocols {
		cases := []struct {
			name string
			tool bool
			text string
		}{
			{"partial_workspace", false, "/__vmi1_work"}, {"partial_username", false, "⟪vmi1_use"},
			{"unfinished_string", true, `{"cwd":"/__vmi1_workspace__`},
			{"unfinished_escape", true, `{"value":"synthetic-secret\`},
			{"unfinished_nested", true, `{"value":[{"path":"/__vmi1_home__"}`},
		}
		for _, test := range cases {
			t.Run(protocol+"/"+test.name, func(t *testing.T) {
				turn := ready(t, metadata("alice"))
				output(t, turn, delta(protocol, test.text, test.tool, 0), true)
				srAssertError(t, turn, terminal(protocol))
			})
		}
		for name, argument := range map[string]string{
			"array": `[]`, "null": `null`, "primitive": `"string"`,
			"unsafe_integer": `{"value":9007199254740993}`, "rounded_decimal": `{"value":0.10000000000000001}`,
			"negative_zero": `{"value":-0}`, "reserved_key": `{"/__vmi1_home__":"keep"}`,
		} {
			t.Run(protocol+"/json_"+name, func(t *testing.T) {
				srAssertError(t, ready(t, metadata("alice")), delta(protocol, argument, true, 0))
			})
		}
		t.Run(protocol+"/second_object_refused", func(t *testing.T) {
			turn := ready(t, metadata("alice"))
			output(t, turn, delta(protocol, `{}`, true, 0), true)
			space := deltaText(output(t, turn, delta(protocol, " \n\t", true, 0), true), protocol, true)
			if space != " \n\t" {
				t.Fatal("allowed trailing whitespace changed")
			}
			srAssertError(t, turn, delta(protocol, `{}`, true, 0))
		})
		for _, ordinary := range []string{"ordinary /", "x__", "中文🙂 /", "hello ⟪another⟫"} {
			t.Run(protocol+"/ordinary_"+ordinary, func(t *testing.T) {
				turn := ready(t, metadata("alice"))
				var got strings.Builder
				for _, fragment := range srFragments(ordinary, 0) {
					got.WriteString(deltaText(output(t, turn, delta(protocol, fragment, false, 0), true), protocol, false))
				}
				output(t, turn, terminal(protocol), true)
				if got.String() != ordinary {
					t.Fatalf("ordinary stream suffix lost: %q != %q", got.String(), ordinary)
				}
			})
		}
	}
}

func TestStreamRoundsBufferAndChannelLimits(t *testing.T) {
	for _, protocol := range srProtocols {
		for _, length := range []int{16383, 16384, 16385} {
			t.Run(fmt.Sprintf("%s/json_units_%d", protocol, length), func(t *testing.T) {
				// Empty wrapper has 8 UTF-16 units. All ASCII makes the
				// exact buffer boundary independent of UTF-8 byte counts.
				argument := `{"v":"` + strings.Repeat("x", length-8) + `"}`
				if len(argument) != length {
					t.Fatal("fixture length calculation is wrong")
				}
				turn := ready(t, metadata("alice"))
				if length > 16384 {
					srAssertError(t, turn, delta(protocol, argument, true, 0))
					return
				}
				got := deltaText(output(t, turn, delta(protocol, argument, true, 0), true), protocol, true)
				if got != argument {
					t.Fatal("within-limit unmodified tool JSON changed")
				}
				output(t, turn, terminal(protocol), true)
			})
		}
		t.Run(protocol+"/aggregate_context_limit", func(t *testing.T) {
			turn := ready(t, metadata("alice"))
			part := `{"value":"` + strings.Repeat("x", 12500)
			output(t, turn, delta(protocol, part, true, 0), true)
			srAssertError(t, turn, delta(protocol, part, true, 1))
		})
		t.Run(protocol+"/32_channels_then_33_refused", func(t *testing.T) {
			turn := ready(t, metadata("alice"))
			for ch := 0; ch < 32; ch++ {
				output(t, turn, delta(protocol, `{}`, true, ch), true)
			}
			srAssertError(t, turn, delta(protocol, `{}`, true, 32))
		})
		t.Run(protocol+"/released_channels_are_reusable", func(t *testing.T) {
			turn := ready(t, metadata("alice"))
			for round := 0; round < 3; round++ {
				for ch := 0; ch < 32; ch++ {
					output(t, turn, delta(protocol, `{}`, true, ch), true)
				}
				output(t, turn, terminal(protocol), true)
			}
		})
	}
}

func TestStreamRoundsResponseSnapshotsAndScope(t *testing.T) {
	for _, seed := range srSeeds {
		t.Run(fmt.Sprintf("responses_snapshots/seed_%d", seed), func(t *testing.T) {
			turn := ready(t, metadata("alice"))
			masked, want := "file "+workspaceAlias+"/a and "+userAlias, "file /Users/alice/work/project/a and alice"
			var got strings.Builder
			for _, fragment := range srFragments(masked, seed) {
				got.WriteString(deltaText(output(t, turn, delta("responses", fragment, false, 0), true), "responses", false))
			}
			textDone := output(t, turn, map[string]any{"type": "response.output_text.done", "output_index": 0, "content_index": 0, "text": masked}, true)
			part := map[string]any{"type": "output_text", "text": masked, "annotations": []any{map[string]any{"type": "probe", "value": userAlias}}}
			partDone := output(t, turn, map[string]any{"type": "response.content_part.done", "output_index": 0, "content_index": 0, "part": part}, true)
			item := map[string]any{"type": "message", "id": "keep-item", "role": "assistant", "content": []any{part}}
			itemDone := output(t, turn, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}, true)
			completed := output(t, turn, map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{item}, "usage": map[string]any{"output_tokens": 9}}}, true)
			texts := []string{got.String(), textDone["text"].(string), partDone["part"].(map[string]any)["text"].(string),
				readText(itemDone["item"].(map[string]any)), readText(completed["response"].(map[string]any)["output"].([]any)[0].(map[string]any))}
			for i, text := range texts {
				if text != want {
					t.Fatalf("snapshot %d = %q; want independently specified %q", i, text, want)
				}
			}
			if partDone["part"].(map[string]any)["annotations"].([]any)[0].(map[string]any)["value"] != userAlias {
				t.Fatal("opaque annotation was rewritten")
			}
		})
	}
	for _, protocol := range srProtocols {
		t.Run(protocol+"/one_channel_stop_does_not_flush_other", func(t *testing.T) {
			turn := ready(t, metadata("alice"))
			left := srTextValue(output(t, turn, srTextEvent(protocol, 0, "/__vmi1_"), true), protocol, 0)
			// Channel 4 is another output/choice/block in every layout.
			output(t, turn, srTextEvent(protocol, 4, userAlias), true)
			output(t, turn, srTextStop(protocol, 4), true)
			left += srTextValue(output(t, turn, srTextEvent(protocol, 0, "home__/a"), true), protocol, 0)
			output(t, turn, srTextStop(protocol, 0), true)
			if left != "/Users/alice/a" {
				t.Fatalf("unrelated channel stop discarded pending state: %q", left)
			}
		})
	}
}

func TestStreamRoundsOpaqueAndNonSuccess(t *testing.T) {
	for name, event := range map[string]map[string]any{
		"responses_reasoning_delta": {"type": "response.reasoning_summary_text.delta", "output_index": 0, "delta": workspaceAlias},
		"responses_encrypted_item":  {"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "reasoning", "encrypted_content": "synthetic-opaque", "summary": []any{map[string]any{"type": "summary_text", "text": userAlias}}}},
		"anthropic_thinking":        {"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": homeAlias}},
		"anthropic_signature":       {"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "signature_delta", "signature": "synthetic-signature-" + userAlias}},
		"anthropic_thinking_start":  {"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking", "thinking": workspaceAlias, "signature": "synthetic-signature"}},
		"chat_reasoning":            {"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"reasoning_content": workspaceAlias}, "finish_reason": nil}}},
	} {
		t.Run(name, func(t *testing.T) {
			turn := ready(t, metadata("alice"))
			body := append([]byte(" \n"), marshal(t, event)...)
			body = append(body, '\n')
			result, err := turn.ApplyResponse(context.Background(), messagetransform.ResponseMessage{StatusCode: 200, Streaming: true, EventName: "unchanged-event", Headers: http.Header{"X-Probe": {"same"}}, Body: body})
			if err != nil || !bytes.Equal(result.Body, body) || result.EventName != "unchanged-event" || result.StatusCode != 200 || result.Headers.Get("X-Probe") != "same" {
				t.Fatalf("signed/opaque event or transport envelope changed: %v", err)
			}
		})
	}
	for _, status := range []int{199, 300, 401, 403, 429, 500, 502, 503} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("status_%d/stream_%v", status, streaming), func(t *testing.T) {
				turn := ready(t, metadata("alice"))
				body := []byte("<html>" + userAlias + ": synthetic failure\x00</html>")
				result, err := turn.ApplyResponse(context.Background(), messagetransform.ResponseMessage{StatusCode: status, Streaming: streaming, EventName: "provider_error", Headers: http.Header{"Retry-After": {"5"}}, Body: body})
				if err != nil || !bytes.Equal(result.Body, body) || result.StatusCode != status || result.Streaming != streaming || result.EventName != "provider_error" || result.Headers.Get("Retry-After") != "5" {
					t.Fatalf("provider error was obscured or changed: %v", err)
				}
			})
		}
	}
}

func TestStreamRoundsRawWireFragmentation(t *testing.T) {
	for _, protocol := range srProtocols {
		for _, seed := range srSeeds {
			t.Run(fmt.Sprintf("%s/seed_%d", protocol, seed), func(t *testing.T) {
				turn := ready(t, metadata("alice"))
				var wire []byte
				events := []map[string]any{delta(protocol, "🙂 /__vmi1_", false, 0), delta(protocol, "home__/a "+userAlias, false, 0), terminal(protocol)}
				for i, event := range events {
					name, _ := event["type"].(string)
					if name == "" {
						name = "message"
					}
					body, err := json.MarshalIndent(event, "", " ")
					if err != nil {
						t.Fatal(err)
					}
					retry := 500 + i
					frame, err := ssewire.Encode(ssewire.Event{Name: name, ID: fmt.Sprintf("synthetic-%d", i), Retry: &retry, Data: body})
					if err != nil {
						t.Fatal(err)
					}
					wire = append(wire, []byte(": heartbeat\r\n\r\n")...)
					wire = append(wire, bytes.ReplaceAll(frame, []byte("\n"), []byte("\r\n"))...)
				}
				wire = append(wire, []byte("data: [DONE]\r\n\r\n")...)
				decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
				if err != nil {
					t.Fatal(err)
				}
				rng := rand.New(rand.NewSource(seed))
				var collected strings.Builder
				count, bypassed := 0, 0
				for len(wire) > 0 {
					n := 1
					if seed != 0 {
						n = 1 + rng.Intn(31)
					}
					if n > len(wire) {
						n = len(wire)
					}
					frames, err := decoder.Feed(wire[:n])
					wire = wire[n:]
					if err != nil {
						t.Fatal(err)
					}
					for _, frame := range frames {
						if string(frame.Data) == "[DONE]" {
							bypassed++ // Same documented host behavior: no JS finish callback.
							continue
						}
						if frame.ID != fmt.Sprintf("synthetic-%d", count) || frame.Retry == nil || *frame.Retry != 500+count {
							t.Fatal("SSE IDs/retry/order changed during fragmentation")
						}
						result, err := turn.ApplyResponse(context.Background(), messagetransform.ResponseMessage{StatusCode: 200, Streaming: true, EventName: frame.Name, Headers: http.Header{"Content-Type": {"text/event-stream"}}, Body: frame.Data})
						if err != nil || result.EventName != frame.Name || result.StatusCode != 200 || !result.Streaming || result.Headers.Get("Content-Type") != "text/event-stream" {
							t.Fatalf("raw SSE transformation envelope failed: %v", err)
						}
						if count < 2 {
							collected.WriteString(deltaText(decode(t, result.Body), protocol, false))
						}
						frame.Data = result.Body
						if _, err := ssewire.Encode(frame); err != nil {
							t.Fatalf("transformed event could not be framed: %v", err)
						}
						count++
					}
				}
				if err := decoder.Finish(); err != nil {
					t.Fatal(err)
				}
				if count != 3 || bypassed != 1 || collected.String() != "🙂 /Users/alice/a alice" {
					t.Fatalf("wire semantic mismatch: events=%d bypassed=%d text=%q", count, bypassed, collected.String())
				}
			})
		}
	}
}

func TestStreamRoundsDocumentedEOFBoundary(t *testing.T) {
	for _, protocol := range srProtocols {
		t.Run(protocol+"/missing_event_boundary_is_transport_truncation", func(t *testing.T) {
			decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decoder.Feed([]byte("data: " + string(marshal(t, delta(protocol, "/__vmi1_", false, 0))) + "\n")); err != nil {
				t.Fatal(err)
			}
			if err := decoder.Finish(); !errors.Is(err, ssewire.ErrTruncated) {
				t.Fatalf("expected framing truncation, got %v", err)
			}
		})
		t.Run(protocol+"/complete_frame_without_terminal_has_no_JS_flush", func(t *testing.T) {
			turn := ready(t, metadata("alice"))
			got := deltaText(output(t, turn, delta(protocol, "/__vmi1_", false, 0), true), protocol, false)
			if got != "/" {
				t.Fatalf("only the safe common prefix may already be emitted, got %q", got)
			}
			// Intentionally no invented EOF/[DONE] ApplyResponse call: the JS
			// API cannot observe that event. An explicit protocol terminal on
			// this same state demonstrates the pending prefix is still checked.
			srAssertError(t, turn, terminal(protocol))
		})
	}
}

func TestStreamRoundsUTF16AndInvalidChannels(t *testing.T) {
	for _, protocol := range srProtocols {
		for _, extra := range []string{"", "x"} {
			t.Run(fmt.Sprintf("%s/utf16_units_%d", protocol, 16384+len(extra)), func(t *testing.T) {
				argument := `{"v":"` + strings.Repeat("🙂", 8188) + extra + `"}`
				turn := ready(t, metadata("alice"))
				if extra != "" {
					srAssertError(t, turn, delta(protocol, argument, true, 0))
					return
				}
				if got := deltaText(output(t, turn, delta(protocol, argument, true, 0), true), protocol, true); got != argument {
					t.Fatal("UTF-16 boundary was confused with UTF-8 byte length")
				}
				output(t, turn, terminal(protocol), true)
			})
		}
		for _, invalid := range []any{-1, 1.5, float64(9007199254740992)} {
			t.Run(fmt.Sprintf("%s/index_%v", protocol, invalid), func(t *testing.T) {
				event := delta(protocol, userAlias, false, 0)
				switch protocol {
				case "responses":
					event["output_index"] = invalid
				case "anthropic":
					event["index"] = invalid
				default:
					event["choices"].([]any)[0].(map[string]any)["index"] = invalid
				}
				srAssertError(t, ready(t, metadata("alice")), event)
			})
		}
	}
	for _, itemID := range []any{nil, "", strings.Repeat("x", 129)} {
		t.Run(fmt.Sprintf("responses/item_id_%v", itemID), func(t *testing.T) {
			event := map[string]any{"type": "response.output_text.delta", "content_index": 0, "item_id": itemID, "delta": userAlias}
			srAssertError(t, ready(t, metadata("alice")), event)
		})
	}
}

func TestStreamRoundsFallbackIDsAndToolSnapshots(t *testing.T) {
	for _, seed := range srSeeds {
		for _, kind := range []string{"function_call_arguments", "custom_tool_call_input", "refusal"} {
			t.Run(fmt.Sprintf("%s/seed_%d", kind, seed), func(t *testing.T) {
				turn := ready(t, metadata("alice"))
				masked, expected := workspaceAlias+"/file "+userAlias, "/Users/alice/work/project/file alice"
				if kind == "function_call_arguments" {
					masked, expected = `{"cwd":"/__vmi1_workspace__/file","username":"⟪vmi1_user⟫","n":7}`, `{"cwd":"/Users/alice/work/project/file","username":"alice","n":7}`
				}
				id := `synthetic:id"quoted`
				var got strings.Builder
				for _, fragment := range srFragments(masked, seed) {
					event := map[string]any{"type": "response." + kind + ".delta", "item_id": id, "content_index": 0, "delta": fragment}
					got.WriteString(output(t, turn, event, true)["delta"].(string))
				}
				field := "refusal"
				if kind == "function_call_arguments" {
					field = "arguments"
				} else if kind == "custom_tool_call_input" {
					field = "input"
				}
				done := output(t, turn, map[string]any{"type": "response." + kind + ".done", "item_id": id, "content_index": 0, field: masked}, true)
				for _, actual := range []string{got.String(), done[field].(string)} {
					if kind == "function_call_arguments" {
						if !reflect.DeepEqual(decode(t, []byte(actual)), decode(t, []byte(expected))) {
							t.Fatalf("tool snapshot lost original structure: %s", actual)
						}
					} else if actual != expected {
						t.Fatalf("delta/done mismatch: got %q, independently expected %q", actual, expected)
					}
				}
				output(t, turn, terminal("responses"), true)
			})
		}
	}
}

func TestStreamRoundsAbnormalTerminalEvents(t *testing.T) {
	for _, protocol := range srProtocols {
		var endings []map[string]any
		switch protocol {
		case "responses":
			endings = []map[string]any{
				{"type": "response.incomplete", "response": map[string]any{"output": []any{}}},
				{"type": "response.failed", "response": map[string]any{"output": []any{}}},
			}
		case "anthropic":
			endings = []map[string]any{
				{"type": "error", "error": map[string]any{"type": "synthetic_error"}},
				{"type": "content_block_stop", "index": 0},
			}
		default:
			endings = []map[string]any{
				{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "length"}}},
				{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "content_filter"}}},
			}
		}
		for n, ending := range endings {
			for _, pending := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/ending_%d/pending_%v", protocol, n, pending), func(t *testing.T) {
					turn := ready(t, metadata("alice"))
					text := "ordinary text"
					if pending {
						text = "/__vmi1_hom"
					}
					output(t, turn, delta(protocol, text, false, 0), true)
					if pending {
						srAssertError(t, turn, ending)
					} else if got := output(t, turn, ending, true); !reflect.DeepEqual(got, decode(t, marshal(t, ending))) {
						t.Fatal("provider failure status/end reason was rewritten")
					}
				})
			}
		}
	}
}
