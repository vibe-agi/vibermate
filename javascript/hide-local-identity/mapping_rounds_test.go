package hideidentity_test

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

// These tests use the generated copy/paste scripts and the real Goja sandbox
// through identity_test.go's helpers. All identities and messages are synthetic.
// Each named round uses different data; -count=3 is an additional race/repeat run.
var mappingRoundSeeds = []int64{1109, 2309, 4709}

type mappingFixture struct {
	name string
	meta messagetransform.RuntimeMetadata
}

func mappingFixtureRounds() [][]mappingFixture {
	return [][]mappingFixture{
		{
			{"posix_spaces_unicode", messagetransform.RuntimeMetadata{LocalUserName: "alice.smith", HomeDirectory: "/Users/alice.smith", WorkspaceRoot: "/Users/alice.smith/Work Project/中文仓库"}},
			{"independent_workspace", messagetransform.RuntimeMetadata{LocalUserName: "bob-42", HomeDirectory: "/home/bob-42", WorkspaceRoot: "/srv/组 A"}},
			{"accented_account", messagetransform.RuntimeMetadata{LocalUserName: "élise", HomeDirectory: "/home/élise", WorkspaceRoot: "/home/élise/work"}},
			{"ambiguous_account_paths", messagetransform.RuntimeMetadata{LocalUserName: "root", HomeDirectory: "/root", WorkspaceRoot: "/root/project"}},
			{"equal_roots", messagetransform.RuntimeMetadata{LocalUserName: "sameperson", HomeDirectory: "/home/sameperson", WorkspaceRoot: "/home/sameperson"}},
		},
		{
			{"windows_spaces", messagetransform.RuntimeMetadata{LocalUserName: "Avery Stone", HomeDirectory: `C:\Users\Avery Stone`, WorkspaceRoot: `C:\Users\Avery Stone\Work Projects\app`, OperatingSystem: "windows"}},
			{"windows_unicode", messagetransform.RuntimeMetadata{LocalUserName: "chen_88", HomeDirectory: `D:\Profiles\chen_88`, WorkspaceRoot: `D:\Profiles\chen_88\中文 项目`, OperatingSystem: "windows"}},
			{"unc_paths", messagetransform.RuntimeMetadata{LocalUserName: "unc_user", HomeDirectory: `\\filesrv\people\unc_user`, WorkspaceRoot: `\\filesrv\people\unc_user\work`, OperatingSystem: "windows"}},
			{"windows_equal_roots", messagetransform.RuntimeMetadata{LocalUserName: "worker77", HomeDirectory: `D:\Profiles\worker77`, WorkspaceRoot: `D:\Profiles\worker77`, OperatingSystem: "windows"}},
			{"windows_slash_native", messagetransform.RuntimeMetadata{LocalUserName: "slash-user", HomeDirectory: "C:/Users/slash-user", WorkspaceRoot: "C:/Users/slash-user/work", OperatingSystem: "windows"}},
		},
		{
			{"emoji_account", messagetransform.RuntimeMetadata{LocalUserName: "dev🧪", HomeDirectory: "/home/dev🧪", WorkspaceRoot: "/home/dev🧪/检查 🧪"}},
			{"decomposed_unicode", messagetransform.RuntimeMetadata{LocalUserName: "e\u0301lodie", HomeDirectory: "/home/e\u0301lodie", WorkspaceRoot: "/home/e\u0301lodie/a b"}},
			{"regexp_metacharacters", messagetransform.RuntimeMetadata{LocalUserName: "case+(x)", HomeDirectory: "/home/case+(x)", WorkspaceRoot: "/home/case+(x)/a[1]"}},
			{"trailing_separators", messagetransform.RuntimeMetadata{LocalUserName: "trailing88", HomeDirectory: "/home/trailing88///", WorkspaceRoot: "/home/trailing88/work///"}},
			{"workspace_is_ancestor", messagetransform.RuntimeMetadata{LocalUserName: "longuser", HomeDirectory: "/mnt/team/users/longuser", WorkspaceRoot: "/mnt/team"}},
		},
	}
}

// Match the standardized ECMAScript URI escape sets, including uppercase hex.
// This is only a test-data encoder; no JavaScript implementation is mocked.
func mappingURIEncode(value string, component bool) string {
	var result strings.Builder
	for _, octet := range []byte(value) {
		allowed := (octet >= 'A' && octet <= 'Z') || (octet >= 'a' && octet <= 'z') ||
			(octet >= '0' && octet <= '9') || strings.ContainsRune("-_.!~*'()", rune(octet))
		if !component && strings.ContainsRune(";,/?:@&=+$#", rune(octet)) {
			allowed = true
		}
		if allowed {
			result.WriteByte(octet)
		} else {
			fmt.Fprintf(&result, "%%%02X", octet)
		}
	}
	return result.String()
}

func mappingNativeAlias(path, kind string) string {
	root := "/"
	if len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/') {
		root = path[:3]
	} else if strings.HasPrefix(path, `\\`) {
		root = `\\`
	}
	return root + "__vmi1_" + kind + "__"
}

func mappingCanonicalPath(path string) string {
	return strings.TrimRight(path, `/\`)
}

func mappingAmbiguousName(name string) bool {
	return len(utf16.Encode([]rune(name))) < 3 ||
		strings.Contains("|root|admin|administrator|user|test|guest|system|", "|"+strings.ToLower(name)+"|")
}

func TestMappingRoundsPathRepresentations(t *testing.T) {
	for round, fixtures := range mappingFixtureRounds() {
		t.Run(fmt.Sprintf("round_%d", round+1), func(t *testing.T) {
			for _, fixture := range fixtures {
				t.Run(fixture.name, func(t *testing.T) {
					for _, target := range []struct{ name, path string }{
						{"workspace", fixture.meta.WorkspaceRoot}, {"home", fixture.meta.HomeDirectory},
					} {
						sep := "/"
						if strings.Contains(target.path, `\`) {
							sep = `\`
						}
						slashPath := strings.ReplaceAll(target.path, `\`, "/")
						fileURI := "file://" + slashPath
						if len(slashPath) >= 3 && slashPath[1] == ':' && slashPath[2] == '/' {
							fileURI = "file:///" + slashPath
						}
						for _, representation := range []struct{ name, path string }{
							{"native", target.path},
							{"native_file", target.path + sep + "src" + sep + "文件 'quoted'.txt"},
							{"forward_slashes", strings.ReplaceAll(target.path, `\`, "/")},
							{"encode_uri", mappingURIEncode(target.path, false)},
							{"encode_component", mappingURIEncode(target.path, true)},
							{"file_uri", fileURI},
						} {
							t.Run(target.name+"_"+representation.name, func(t *testing.T) {
								turn := newTurn(t, fixture.meta)
								original := "路径=" + representation.path + "；终止"
								masked := input(t, turn, map[string]any{"input": original})["input"].(string)
								if masked == original || !strings.Contains(masked, "__vmi1_") {
									t.Fatalf("path representation was not mapped: %q -> %q", original, masked)
								}
								if restored := readText(output(t, turn, responseText(masked), false)); restored != original {
									t.Fatalf("representation did not round-trip byte-for-byte: %q != %q", restored, original)
								}
								if representation.name == "native" {
									kind := target.name
									if mappingCanonicalPath(fixture.meta.HomeDirectory) == mappingCanonicalPath(fixture.meta.WorkspaceRoot) {
										kind = "workspace"
									}
									alias := mappingNativeAlias(target.path, kind)
									if !strings.Contains(masked, alias) {
										t.Fatalf("wrong canonical/longest root alias: %q does not contain %q", masked, alias)
									}
								}
							})
						}
					}
				})
			}
		})
	}
}

func TestWindowsFileURIKnownPathsRoundTrip(t *testing.T) {
	for _, name := range []string{"alice", "li", "root", "Avery Stone", "用户"} {
		for _, root := range []string{`C:\Users\`, "D:/Profiles/"} {
			t.Run(name+"/"+root, func(t *testing.T) {
				home := root + name
				sep := "/"
				if strings.Contains(root, `\`) {
					sep = `\`
				}
				workspace := home + sep + "Work" + sep + "secret project"
				meta := messagetransform.RuntimeMetadata{
					LocalUserName: name, HomeDirectory: home, WorkspaceRoot: workspace, OperatingSystem: "windows",
				}
				for _, encode := range []bool{false, true} {
					path := strings.ReplaceAll(workspace, `\`, "/")
					if encode {
						path = mappingURIEncode(path, false)
					}
					original := "open file:///" + path + "/report.txt"
					turn := newTurn(t, meta)
					masked := input(t, turn, map[string]any{"input": original})["input"].(string)
					if !strings.Contains(masked, "__vmi1_workspace") || strings.Contains(masked, "secret") {
						t.Fatalf("Windows file URI path was not masked: %q", masked)
					}
					if restored := readText(output(t, turn, responseText(masked), false)); restored != original {
						t.Fatalf("file URI changed after round-trip: %q, want %q", restored, original)
					}
				}
				// The exception must not match a drive path nested in an unrelated path.
				turn := newTurn(t, meta)
				masked := input(t, turn, map[string]any{"input": "file:///backup/" + strings.ReplaceAll(workspace, `\`, "/")})["input"].(string)
				if strings.Contains(masked, "__vmi1_workspace") || strings.Contains(masked, "__vmi1_home") {
					t.Fatal("file URI exception bypassed path start boundary")
				}
			})
		}
	}
}

func TestMappingRoundsUsernameBoundaries(t *testing.T) {
	for round, names := range [][]string{
		{"carol", "delta_77", "amy-long", "Élodie"},
		{"a", "xy", "root", "ADMIN", "guest"},
		{"李雷", "周小明", "👩‍💻dev", "a+b.c", "user"},
	} {
		t.Run(fmt.Sprintf("round_%d", round+1), func(t *testing.T) {
			for _, name := range names {
				t.Run(name, func(t *testing.T) {
					bare := userAlias
					if mappingAmbiguousName(name) {
						bare = name
					}
					for _, test := range []struct{ name, source, want string }{
						{"bare", "hello " + name + "!", "hello " + bare + "!"},
						{"terminal_period", name + ".", bare + "."},
						{"username_label", "username: " + name, "username: " + userAlias},
						{"login_label", "login='" + name + "'", "login='" + userAlias + "'"},
						{"chinese_label", "用户名：" + name, "用户名：" + name},
						{"prefix_and_suffix", "prefix" + name + " " + name + "2 " + name + "-extra", "prefix" + name + " " + name + "2 " + name + "-extra"},
						{"email", name + "@example.test", name + "@example.test"},
					} {
						// The current label parser accepts ASCII ':', not full-width
						// '：'. Nonambiguous names still match on punctuation boundaries.
						if test.name == "chinese_label" && !mappingAmbiguousName(name) {
							test.want = "用户名：" + userAlias
						}
						t.Run(test.name, func(t *testing.T) {
							turn := newTurn(t, metadata(name))
							masked := input(t, turn, map[string]any{"input": test.source})["input"].(string)
							if masked != test.want {
								t.Fatalf("username boundary/context mismatch: got %q want %q", masked, test.want)
							}
							if got := readText(output(t, turn, responseText(masked), false)); got != test.source {
								t.Fatalf("username round-trip changed text: %q", got)
							}
						})
					}
					for _, key := range []string{"username", "user", "local_user", "login", "author"} {
						t.Run("tool_hint_"+key, func(t *testing.T) {
							turn := newTurn(t, metadata(name))
							args := string(marshal(t, map[string]any{key: name}))
							masked := input(t, turn, map[string]any{"input": []any{map[string]any{"type": "function_call", "name": "check_identity", "arguments": args}}})
							maskedArgs := masked["input"].([]any)[0].(map[string]any)["arguments"].(string)
							want := userAlias
							if key == "author" && mappingAmbiguousName(name) {
								want = name
							}
							if decode(t, []byte(maskedArgs))[key] != want {
								t.Fatalf("tool value/hint mismatch for %q: %s", key, maskedArgs)
							}
							result := output(t, turn, map[string]any{"output": masked["input"]}, false)
							got := result["output"].([]any)[0].(map[string]any)["arguments"].(string)
							if !reflect.DeepEqual(decode(t, []byte(got)), map[string]any{key: name}) {
								t.Fatalf("tool hint did not round-trip: %s", got)
							}
						})
					}
				})
			}
		})
	}
}

func TestMappingRoundsMixedMessageSeeds(t *testing.T) {
	for round, seed := range mappingRoundSeeds {
		t.Run(fmt.Sprintf("round_%d_seed_%d", round+1, seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			meta := metadata(fmt.Sprintf("synthetic_%d", seed))
			for sample := 0; sample < 32; sample++ {
				var before, after strings.Builder
				for token := 0; token < 20; token++ {
					var original, alias string
					switch rng.Intn(8) {
					case 0:
						original, alias = meta.LocalUserName, userAlias
					case 1:
						original, alias = meta.HomeDirectory, homeAlias
					case 2:
						original, alias = meta.WorkspaceRoot+"/file.txt", workspaceAlias+"/file.txt"
					case 3:
						original, alias = meta.WorkspaceRoot+"-other", homeAlias+"/work/project-other"
					case 4:
						original = meta.LocalUserName + "@example.test"
						alias = original
					case 5:
						original = "pre" + meta.LocalUserName + "suf"
						alias = original
					case 6:
						original, alias = "/workspace/project /Users/guest vibermate-user", "/workspace/project /Users/guest vibermate-user"
					default:
						original, alias = "unknown-user /srv/unrelated 192.0.2.50", "unknown-user /srv/unrelated 192.0.2.50"
					}
					wrappers := [][2]string{{"[", "] "}, {"\"", "\"\n"}, {"（", "）；"}, {"key=", "! "}}
					wrap := wrappers[rng.Intn(len(wrappers))]
					before.WriteString(wrap[0] + original + wrap[1])
					after.WriteString(wrap[0] + alias + wrap[1])
				}
				t.Run(fmt.Sprintf("sample_%02d", sample), func(t *testing.T) {
					turn := newTurn(t, meta)
					masked := input(t, turn, map[string]any{"input": before.String()})["input"].(string)
					if masked != after.String() {
						t.Fatalf("deterministic mixed masking mismatch: got %q want %q", masked, after.String())
					}
					if got := readText(output(t, turn, responseText(masked), false)); got != before.String() {
						t.Fatalf("deterministic mixed response mismatch: %q", got)
					}
				})
			}
		})
	}
}

func mappingToolArguments(meta messagetransform.RuntimeMetadata) map[string]any {
	return map[string]any{
		"cwd": meta.WorkspaceRoot, "username": meta.LocalUserName,
		"home": meta.HomeDirectory, "integer": 9007199254740991,
		"decimal": 0.125, "zero": 0, "negative": -37, "bool": true, "null": nil,
		"nested":        []any{map[string]any{"path": meta.WorkspaceRoot + "/src/a.go"}, false, nil, 7, "ordinary"},
		"escapes":       "quote=\" tab=\t newline=\n slash=\\ literal=\\u0061",
		"number_string": "9007199254740993",
		"__proto__":     map[string]any{"constructor": "ordinary"},
	}
}

func TestMappingRoundsStructuresTypesAndSignatures(t *testing.T) {
	for round, fixture := range []mappingFixture{
		{"responses_posix", metadata("riya37")},
		{"messages_windows", messagetransform.RuntimeMetadata{LocalUserName: "morgan81", HomeDirectory: `C:\Users\morgan81`, WorkspaceRoot: `C:\Users\morgan81\app`, OperatingSystem: "windows"}},
		{"chat_unc", messagetransform.RuntimeMetadata{LocalUserName: "yan_61", HomeDirectory: `\\storage\homes\yan_61`, WorkspaceRoot: `\\storage\homes\yan_61\repo`, OperatingSystem: "windows"}},
	} {
		t.Run(fmt.Sprintf("round_%d", round+1), func(t *testing.T) {
			for _, protocol := range []string{"responses", "anthropic", "chat"} {
				t.Run(protocol, func(t *testing.T) {
					meta := fixture.meta
					turn := newTurn(t, meta)
					originalText := "username: " + meta.LocalUserName + "; home=" + meta.HomeDirectory + "; cwd=" + meta.WorkspaceRoot
					wantText := "username: " + userAlias + "; home=" + mappingNativeAlias(meta.HomeDirectory, "home") + "; cwd=" + mappingNativeAlias(meta.WorkspaceRoot, "workspace")
					args := mappingToolArguments(meta)
					opaque := map[string]any{"owner": meta.LocalUserName, "home": meta.HomeDirectory, "workspace": meta.WorkspaceRoot}
					request := map[string]any{
						"model": meta.LocalUserName, "stream": false, "user": meta.LocalUserName,
						"metadata": opaque, "runtime": map[string]any{"user": map[string]any{"name": "spoofed_user"}},
						"previous_response_id": meta.WorkspaceRoot, "max_output_tokens": 123,
					}
					toolName := meta.LocalUserName
					callID := meta.HomeDirectory
					schema := map[string]any{"type": "object", "properties": map[string]any{"cwd": map[string]any{"type": "string"}}, "required": []any{"cwd"}, "additionalProperties": true}
					thought := map[string]any{"type": "thinking", "thinking": "opaque " + mappingNativeAlias(meta.WorkspaceRoot, "workspace"), "signature": "signature:" + meta.LocalUserName}
					image := map[string]any{"type": "image", "source": map[string]any{"type": "base64", "data": "opaque:" + meta.HomeDirectory}}
					reasoning := map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "opaque " + userAlias}}, "encrypted_content": "opaque:" + meta.WorkspaceRoot}
					switch protocol {
					case "responses":
						request["input"] = []any{
							map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": originalText, "annotations": opaque}}},
							map[string]any{"type": "function_call", "name": toolName, "call_id": callID, "arguments": string(marshal(t, args))}, reasoning,
						}
						request["tools"] = []any{map[string]any{"type": "function", "name": toolName, "description": originalText, "parameters": schema}}
					case "anthropic":
						request["messages"] = []any{map[string]any{"role": "assistant", "content": []any{
							map[string]any{"type": "text", "text": originalText},
							map[string]any{"type": "tool_use", "name": toolName, "id": callID, "input": args}, thought, image,
						}}}
						request["tools"] = []any{map[string]any{"name": toolName, "description": originalText, "input_schema": schema}}
					case "chat":
						request["messages"] = []any{map[string]any{"role": "assistant", "content": originalText, "name": toolName,
							"tool_calls":        []any{map[string]any{"type": "function", "id": callID, "function": map[string]any{"name": toolName, "arguments": string(marshal(t, args))}}},
							"reasoning_content": "opaque " + userAlias,
						}}
						request["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": toolName, "description": originalText, "parameters": schema}}}
					}
					before := decode(t, marshal(t, request))
					masked := input(t, turn, request)
					for _, key := range []string{"model", "stream", "user", "metadata", "runtime", "previous_response_id", "max_output_tokens"} {
						if !reflect.DeepEqual(masked[key], before[key]) {
							t.Fatalf("opaque/control field %s changed", key)
						}
					}
					var body map[string]any
					var maskedArguments map[string]any
					switch protocol {
					case "responses":
						items := masked["input"].([]any)
						if readText(items[0].(map[string]any)) != wantText {
							t.Fatalf("request content was not fully masked: %#v", items[0])
						}
						if !reflect.DeepEqual(items[2], decode(t, marshal(t, reasoning))) || !reflect.DeepEqual(items[0].(map[string]any)["content"].([]any)[0].(map[string]any)["annotations"], decode(t, marshal(t, opaque))) {
							t.Fatal("reasoning or annotations changed")
						}
						maskedArguments = decode(t, []byte(items[1].(map[string]any)["arguments"].(string)))
						body = map[string]any{"output": items}
					case "anthropic":
						blocks := masked["messages"].([]any)[0].(map[string]any)["content"].([]any)
						if blocks[0].(map[string]any)["text"] != wantText || !reflect.DeepEqual(blocks[2], decode(t, marshal(t, thought))) || !reflect.DeepEqual(blocks[3], decode(t, marshal(t, image))) {
							t.Fatal("Anthropic content/signatures/binary invariants changed")
						}
						maskedArguments = blocks[1].(map[string]any)["input"].(map[string]any)
						body = map[string]any{"content": blocks}
					case "chat":
						message := masked["messages"].([]any)[0].(map[string]any)
						if message["content"] != wantText || message["reasoning_content"] != "opaque "+userAlias {
							t.Fatal("Chat content/reasoning invariant changed")
						}
						maskedArguments = decode(t, []byte(message["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)))
						body = map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": message}}}
					}
					wantArguments := decode(t, marshal(t, args))
					wantArguments["cwd"] = mappingNativeAlias(meta.WorkspaceRoot, "workspace")
					wantArguments["home"] = mappingNativeAlias(meta.HomeDirectory, "home")
					wantArguments["username"] = userAlias
					wantArguments["nested"].([]any)[0].(map[string]any)["path"] = mappingNativeAlias(meta.WorkspaceRoot, "workspace") + "/src/a.go"
					if !reflect.DeepEqual(maskedArguments, wantArguments) {
						t.Fatalf("request tool strings were not fully mapped or other types/keys changed: got %#v want %#v", maskedArguments, wantArguments)
					}
					body["usage"] = map[string]any{"output_tokens": 17, "cached_tokens": 5}
					body["model"] = meta.LocalUserName
					body["id"] = callID
					body["metadata"] = opaque
					out := output(t, turn, body, false)
					for _, key := range []string{"usage", "model", "id", "metadata"} {
						if !reflect.DeepEqual(out[key], decode(t, marshal(t, body))[key]) {
							t.Fatalf("response non-content field %s changed", key)
						}
					}
					var restoredText, gotName, gotID string
					var gotArgs map[string]any
					switch protocol {
					case "responses":
						items := out["output"].([]any)
						restoredText = readText(items[0].(map[string]any))
						call := items[1].(map[string]any)
						gotArgs = decode(t, []byte(call["arguments"].(string)))
						gotName, gotID = call["name"].(string), call["call_id"].(string)
						if !reflect.DeepEqual(items[2], decode(t, marshal(t, reasoning))) {
							t.Fatal("response reasoning was modified")
						}
					case "anthropic":
						blocks := out["content"].([]any)
						restoredText = blocks[0].(map[string]any)["text"].(string)
						call := blocks[1].(map[string]any)
						gotArgs = call["input"].(map[string]any)
						gotName, gotID = call["name"].(string), call["id"].(string)
						if !reflect.DeepEqual(blocks[2], decode(t, marshal(t, thought))) || !reflect.DeepEqual(blocks[3], decode(t, marshal(t, image))) {
							t.Fatal("response signature/binary fields were modified")
						}
					case "chat":
						message := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
						restoredText = message["content"].(string)
						call := message["tool_calls"].([]any)[0].(map[string]any)
						function := call["function"].(map[string]any)
						gotArgs = decode(t, []byte(function["arguments"].(string)))
						gotName, gotID = function["name"].(string), call["id"].(string)
						if message["reasoning_content"] != "opaque "+userAlias {
							t.Fatal("response reasoning content was modified")
						}
					}
					if restoredText != originalText || gotName != toolName || gotID != callID || !reflect.DeepEqual(gotArgs, decode(t, marshal(t, args))) {
						t.Fatalf("tool types/text/identity failed round-trip: text=%q name=%q id=%q args=%#v", restoredText, gotName, gotID, gotArgs)
					}
					// Tool schemas/descriptions are independent of returned content.
					tool := masked["tools"].([]any)[0].(map[string]any)
					if protocol == "chat" {
						tool = tool["function"].(map[string]any)
					}
					key := "parameters"
					if protocol == "anthropic" {
						key = "input_schema"
					}
					if tool["description"] != wantText || tool["name"] != toolName || !reflect.DeepEqual(tool[key], decode(t, marshal(t, schema))) {
						t.Fatal("tool schema/name changed or description was not masked")
					}
				})
			}
		})
	}
}

func TestMappingRoundsGuardedRejections(t *testing.T) {
	for round, seed := range mappingRoundSeeds {
		t.Run(fmt.Sprintf("round_%d", round+1), func(t *testing.T) {
			meta := metadata(fmt.Sprintf("guard_%d", seed))
			for _, test := range []struct {
				name  string
				value map[string]any
			}{
				{"reserved_ascii", map[string]any{"input": "literal __vmi1_other__"}},
				{"reserved_unicode", map[string]any{"input": "literal ⟪vmi1_other⟫"}},
				{"reserved_tool_key", map[string]any{"input": []any{map[string]any{"type": "function_call", "arguments": `{"__vmi1_key__":"ordinary"}`}}}},
				{"private_tool_key", map[string]any{"input": []any{map[string]any{"type": "function_call", "arguments": string(marshal(t, map[string]any{meta.WorkspaceRoot: "ordinary"}))}}}},
				{"private_schema_enum", map[string]any{"input": "safe", "tools": []any{map[string]any{"parameters": map[string]any{"enum": []any{meta.HomeDirectory}}}}}},
				{"private_schema_property", map[string]any{"input": "safe", "tools": []any{map[string]any{"input_schema": map[string]any{"properties": map[string]any{meta.LocalUserName: map[string]any{"type": "string"}}}}}}},
				{"private_unknown_block", map[string]any{"input": []any{map[string]any{"type": "new_future_type", "text": meta.HomeDirectory}}}},
				{"signed_private_thinking", map[string]any{"messages": []any{map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": meta.HomeDirectory, "signature": "must-not-alter"}}}}}},
				{"signed_private_reasoning", map[string]any{"input": []any{map[string]any{"type": "reasoning", "summary": []any{map[string]any{"text": meta.WorkspaceRoot}}}}}},
				{"tool_arguments_array", map[string]any{"input": []any{map[string]any{"type": "function_call", "arguments": `[]`}}}},
				{"tool_arguments_null", map[string]any{"input": []any{map[string]any{"type": "function_call", "arguments": `null`}}}},
				{"unsupported_input_type", map[string]any{"input": 23}},
			} {
				t.Run(test.name, func(t *testing.T) {
					turn := newTurn(t, meta)
					_, err := turn.ApplyRequest(context.Background(), messagetransform.RequestMessage{Method: "POST", Path: "/v1/responses", Headers: http.Header{}, Body: marshal(t, test.value)})
					if err == nil {
						t.Fatal("unsafe/unsupported input was silently accepted")
					}
					for _, secret := range []string{meta.LocalUserName, meta.HomeDirectory, meta.WorkspaceRoot, "must-not-alter"} {
						if strings.Contains(err.Error(), secret) {
							t.Fatalf("error disclosed protected content: %q", secret)
						}
					}
				})
			}
			for _, bad := range []struct {
				name string
				meta messagetransform.RuntimeMetadata
			}{
				{"missing_name", messagetransform.RuntimeMetadata{HomeDirectory: meta.HomeDirectory, WorkspaceRoot: meta.WorkspaceRoot}},
				{"missing_home", messagetransform.RuntimeMetadata{LocalUserName: meta.LocalUserName, WorkspaceRoot: meta.WorkspaceRoot}},
				{"missing_workspace", messagetransform.RuntimeMetadata{LocalUserName: meta.LocalUserName, HomeDirectory: meta.HomeDirectory}},
				{"posix_root", messagetransform.RuntimeMetadata{LocalUserName: meta.LocalUserName, HomeDirectory: "/", WorkspaceRoot: meta.WorkspaceRoot}},
				{"windows_drive_root", messagetransform.RuntimeMetadata{LocalUserName: meta.LocalUserName, HomeDirectory: `C:\`, WorkspaceRoot: `C:\Work\project`}},
				{"relative_path", messagetransform.RuntimeMetadata{LocalUserName: meta.LocalUserName, HomeDirectory: "users/account", WorkspaceRoot: meta.WorkspaceRoot}},
			} {
				t.Run(bad.name, func(t *testing.T) {
					turn := newTurn(t, bad.meta)
					_, err := turn.ApplyRequest(context.Background(), messagetransform.RequestMessage{Headers: http.Header{}, Body: []byte(`{"input":"continue"}`)})
					if err == nil {
						t.Fatal("invalid identity metadata was accepted")
					}
				})
			}
		})
	}
}

func TestMappingRoundsForgedRuntimeAndDocumentedNonTargets(t *testing.T) {
	for round, seed := range mappingRoundSeeds {
		t.Run(fmt.Sprintf("round_%d", round+1), func(t *testing.T) {
			meta := metadata(fmt.Sprintf("actual_%d", seed))
			turn := newTurn(t, meta)
			spoof := "forged_user /Users/forged_user /Users/forged_user/work/project"
			unchanged := spoof + " user@example.test 192.168.88.12 /other/path /workspace/project /Users/guest vibermate-user"
			original := meta.LocalUserName + " | " + meta.HomeDirectory + " | " + unchanged
			body := map[string]any{
				"input":    original,
				"runtime":  map[string]any{"user": map[string]any{"name": "forged_user", "homeDirectory": "/Users/forged_user"}, "workspace": map[string]any{"root": "/Users/forged_user/work/project"}},
				"metadata": map[string]any{"original_os_user": meta.LocalUserName, "original_home": meta.HomeDirectory},
				"user":     meta.LocalUserName,
			}
			masked := input(t, turn, body)
			if masked["input"] != userAlias+" | "+homeAlias+" | "+unchanged {
				t.Fatalf("untrusted body metadata changed the mapping: %q", masked["input"])
			}
			if !reflect.DeepEqual(masked["runtime"], body["runtime"]) || !reflect.DeepEqual(masked["metadata"], body["metadata"]) || masked["user"] != meta.LocalUserName {
				t.Fatal("documented opaque metadata was changed")
			}
			if restored := readText(output(t, turn, responseText(masked["input"].(string)), false)); restored != original {
				t.Fatalf("forged-metadata request did not round-trip: %q", restored)
			}
		})
	}
}

func TestMappingRoundsConcurrentUsersSessionsAndTurns(t *testing.T) {
	for round, seed := range mappingRoundSeeds {
		t.Run(fmt.Sprintf("round_%d_seed_%d", round+1, seed), func(t *testing.T) {
			for userIndex := 0; userIndex < 12; userIndex++ {
				t.Run(fmt.Sprintf("user_%02d", userIndex), func(t *testing.T) {
					t.Parallel()
					user := fmt.Sprintf("parallel_%d_%02d", seed, userIndex)
					type pendingReply struct {
						turn *messagetransform.PipelineTurn
						meta messagetransform.RuntimeMetadata
					}
					var pending []pendingReply
					for session := 0; session < 3; session++ {
						meta := metadata(user)
						switch round {
						case 0:
							meta.WorkspaceRoot = fmt.Sprintf("/Users/%s/session_%d/repo", user, session)
						case 1:
							meta.HomeDirectory = `C:\Users\` + user
							meta.WorkspaceRoot = fmt.Sprintf(`D:\projects\%s\session_%d`, user, session)
							meta.OperatingSystem = "windows"
						case 2:
							meta.HomeDirectory = `\\server\people\` + user
							meta.WorkspaceRoot = fmt.Sprintf(`\\server\teams\%s\会话_%d`, user, session)
							meta.OperatingSystem = "windows"
						}
						for sequence := 0; sequence < 3; sequence++ {
							turn := newTurn(t, meta)
							text := "continue"
							if sequence == 0 {
								text = "username: " + user + " " + meta.HomeDirectory + " " + meta.WorkspaceRoot
							}
							request := map[string]any{"input": text, "metadata": map[string]any{"session": session, "sequence": sequence}}
							first := input(t, turn, request)
							if sequence == 2 {
								if repeat := input(t, turn, request); !reflect.DeepEqual(first, repeat) {
									t.Fatal("same-request retry was not stable")
								}
							}
							pending = append(pending, pendingReply{turn, meta})
						}
					}
					// All nine contexts exist before any response. Complete them in
					// different deterministic orders to detect shared mutable maps.
					rng := rand.New(rand.NewSource(seed + int64(userIndex)*97))
					rng.Shuffle(len(pending), func(i, j int) { pending[i], pending[j] = pending[j], pending[i] })
					for _, reply := range pending {
						masked := userAlias + " | " + mappingNativeAlias(reply.meta.HomeDirectory, "home") + " | " + mappingNativeAlias(reply.meta.WorkspaceRoot, "workspace")
						got := readText(output(t, reply.turn, responseText(masked), false))
						want := user + " | " + reply.meta.HomeDirectory + " | " + reply.meta.WorkspaceRoot
						if got != want {
							t.Fatalf("cross-user/session/turn identity leak: got %q want %q", got, want)
						}
					}
				})
			}
		})
	}
}
