package openairesponses_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// The Claude Code 2.1.329 corpus is the S2 characterization corpus, read in
// place. The provider side never parses Anthropic JSON; these tests use the
// Anthropic client side with its translated profile to build the neutral
// requests a composed path hands it, so each case reads as Claude Code sent
// it.
const claudeCorpus = "../productruntime/testdata/protocolgolden/requests"

// mappedModel stands in for the route's model mapping, which the exchange
// applies between decode and encode.
const mappedModel = "gpt-6.2-sol"

const testBinding = "0123456789abcdef0123456789abcdef"

func translatedClient(t testing.TB) *anthropicchat.Codec {
	t.Helper()
	options := anthropicchat.DefaultOptions()
	options.ClientInput = anthropicchat.TranslatedClientInputProfile()
	codec, err := anthropicchat.New(options)
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func newProviderSide(t testing.TB, profile func(*openairesponses.ProviderRequestProfile)) *openairesponses.ProviderSide {
	t.Helper()
	options := openairesponses.DefaultProviderSideOptions()
	if profile != nil {
		profile(&options.Profile)
	}
	side, err := openairesponses.NewProviderSide(options)
	if err != nil {
		t.Fatal(err)
	}
	return side
}

// decodeClaude decodes an Anthropic body under the translated profile and
// maps its model, as the exchange does before encoding.
func decodeClaude(t testing.TB, body []byte) protocolcore.Request {
	t.Helper()
	request, _, err := translatedClient(t).DecodeClientRequest(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	request, err = request.WithEffectiveModel(mappedModel)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func noticeLines(report protocolcore.TranslationReport) []string {
	lines := []string{}
	for _, notice := range report.Notices() {
		lines = append(lines, string(notice.Code)+" "+notice.Path)
	}
	return lines
}

func claudeCorpusFiles(t testing.TB) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(claudeCorpus, "claude*.json"))
	if err != nil || len(files) != 12 {
		t.Fatalf("Claude corpus = %v, %v; want 12 bodies", files, err)
	}
	return files
}

func readFile(t testing.TB, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// sealFor mints the signature this path gives Claude Code for a reasoning
// item of the mapped model under testBinding.
func sealFor(t testing.TB, itemID, opaque string, source protocolcore.ReasoningTextSource) string {
	t.Helper()
	seal, err := protocolcore.FormatReasoningSeal(protocolcore.ReasoningSeal{
		Format:  protocolcore.SealResponsesEncrypted,
		ItemID:  itemID,
		Model:   mappedModel,
		Binding: testBinding,
		Opaque:  opaque,
	}, source)
	if err != nil {
		t.Fatal(err)
	}
	return seal
}

// cacheKey is design 6.1's prompt_cache_key formula, written out.
func cacheKey(session, agent string) string {
	digest := sha256.Sum256([]byte("vibermate/prompt-cache\x00" + session + "\x00" + agent))
	return "vm1-" + hex.EncodeToString(digest[:])[:32]
}

// rewrittenCallID is design 6.3's call id rule for an id the API refuses.
func rewrittenCallID(id string) string {
	digest := sha256.Sum256([]byte("vibermate/call-id\x00" + id))
	return "call_vm_" + hex.EncodeToString(digest[:])[:24]
}

// hashedToolName is design 6.5's name rule for a name the API refuses.
func hashedToolName(name string) string {
	digest := sha256.Sum256([]byte("function\x00\x00" + name))
	return "vm_tool_" + hex.EncodeToString(digest[:])[:32]
}

// encodeRow is one row of the request mapping of design 6: a Claude Code
// request and the exact Responses body and encoder notices it gives.
type encodeRow struct {
	name     string
	request  string
	evidence []protocolcore.ProtocolEvidenceValue
	context  *protocolpath.ProviderContext
	profile  func(*openairesponses.ProviderRequestProfile)
	want     string
	notices  []string
}

const (
	// The envelope of a row request without tools or reasoning.
	rowHead  = `{"model":"gpt-6.2-sol","stream":true,"store":false,"include":["reasoning.encrypted_content"],"max_output_tokens":1024`
	rowTools = `,"tool_choice":"auto","parallel_tool_calls":true,"tools":[` + readWire + `]`
	userHi   = `{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`
	readTool = `{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}`
	readWire = `{"type":"function","name":"Read","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},"strict":false}`
	pngData  = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAAEElEQVR4nGLSyr8NCAAA//8CQwF3VJL9nQAAAABJRU5ErkJggg=="
	pdfData  = "JVBERi0xLjQKJSVFT0YK"
	pngPart  = `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + pngData + `"}}`
	pdfPart  = `{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + pdfData + `"}}`
	pngWire  = `{"type":"input_image","image_url":"data:image/png;base64,` + pngData + `"}`
	pdfWire  = `{"type":"input_file","file_data":"data:application/pdf;base64,` + pdfData + `","filename":"document.pdf"}`
	// A turn that calls Read, as Claude Code replays it.
	readCall   = `{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"a.txt"}}]}`
	readCalled = `{"type":"function_call","call_id":"toolu_1","name":"Read","arguments":"{\"path\":\"a.txt\"}"}`
)

func rowRequest(members string) string {
	return `{"model":"claude-opus-5-5","max_tokens":1024,` + members + `}`
}

func toolRow(messages string) string {
	return rowRequest(`"tools":[` + readTool + `],"messages":[{"role":"user","content":"read"},` + readCall + `,` + messages + `]`)
}

const userRead = `{"type":"message","role":"user","content":[{"type":"input_text","text":"read"}]}`

func toolWant(items string) string {
	return rowHead + rowTools + `,"input":[` + userRead + `,` + readCalled + `,` + items + `]}`
}

func encodeRows() []encodeRow {
	sealed := protocolpath.ProviderContext{ReasoningBinding: testBinding}
	return []encodeRow{
		// 6.1 Envelope.
		{
			name:    "a stream:false client still streams upstream, unstored",
			request: rowRequest(`"stream":false,"messages":[{"role":"user","content":"hi"}]`),
			want:    rowHead + `,"input":[` + userHi + `]}`,
		},
		{
			name:    "max_tokens of 32000 or more is omitted",
			request: `{"model":"claude-opus-5-5","max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`,
			want:    `{"model":"gpt-6.2-sol","stream":true,"store":false,"include":["reasoning.encrypted_content"],"input":[` + userHi + `]}`,
			notices: []string{"max_output_tokens_not_forwarded $.max_tokens"},
		},
		{
			name:    "a small max_tokens is raised to 16",
			request: `{"model":"claude-opus-5-5","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`,
			want:    `{"model":"gpt-6.2-sol","stream":true,"store":false,"include":["reasoning.encrypted_content"],"max_output_tokens":16,"input":[` + userHi + `]}`,
		},
		{
			name:    "sampling is declared under the public profile",
			request: rowRequest(`"temperature":0.5,"top_p":0.25,"messages":[{"role":"user","content":"hi"}]`),
			want:    rowHead + `,"input":[` + userHi + `]}`,
			notices: []string{"sampling_not_forwarded $.temperature", "sampling_not_forwarded $.top_p"},
		},
		{
			name:    "sampling is sent when the profile forwards it",
			request: rowRequest(`"temperature":0.5,"top_p":0.25,"messages":[{"role":"user","content":"hi"}]`),
			profile: func(profile *openairesponses.ProviderRequestProfile) {
				profile.Sampling = openairesponses.SamplingForward
			},
			want: rowHead + `,"temperature":0.5,"top_p":0.25,"input":[` + userHi + `]}`,
		},
		{
			name: "stop sequences, diagnostics and the task budget are declared",
			request: rowRequest(`"stop_sequences":["END"],"diagnostics":{"previous_message_id":"msg_1"},` +
				`"output_config":{"task_budget":{"type":"tokens","total":1000}},"messages":[{"role":"user","content":"hi"}]`),
			want: rowHead + `,"input":[` + userHi + `]}`,
			notices: []string{
				"stop_sequences_not_forwarded $.stop_sequences",
				"diagnostics_not_forwarded $.diagnostics",
				"task_budget_not_forwarded $.output_config.task_budget",
			},
		},
		{
			name: "metadata, top_k, service_tier and safeguards never reach the body",
			request: rowRequest(`"metadata":{"user_id":"device-1"},"top_k":5,"service_tier":"auto",` +
				`"safeguards":[{"type":"dangerous_tool_use","classifier_context":"secret"}],"messages":[{"role":"user","content":"hi"}]`),
			want: rowHead + `,"input":[` + userHi + `]}`,
		},
		{
			name:    "the prompt cache key comes from the hashed session and agent",
			request: rowRequest(`"system":[{"type":"text","text":"Be exact.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]`),
			evidence: []protocolcore.ProtocolEvidenceValue{
				{Name: "claude.agent_id", Value: "agent-7"},
				{Name: "claude.session_id", Value: "session-1"},
			},
			want: `{"model":"gpt-6.2-sol","stream":true,"store":false,"include":["reasoning.encrypted_content"],"prompt_cache_key":"` +
				cacheKey("session-1", "agent-7") + `","max_output_tokens":1024,"input":[` +
				`{"type":"message","role":"developer","content":[{"type":"input_text","text":"Be exact."}]},` + userHi + `]}`,
			notices: []string{"prompt_cache_key_derived $"},
		},
		{
			name:     "the main agent keys its cache as main",
			request:  rowRequest(`"messages":[{"role":"user","content":"hi"}]`),
			evidence: []protocolcore.ProtocolEvidenceValue{{Name: "claude.session_id", Value: "session-1"}},
			want: `{"model":"gpt-6.2-sol","stream":true,"store":false,"include":["reasoning.encrypted_content"],"prompt_cache_key":"` +
				cacheKey("session-1", "main") + `","max_output_tokens":1024,"input":[` + userHi + `]}`,
			notices: []string{"prompt_cache_key_derived $"},
		},
		// 6.2 System prompt and the billing line.
		{
			name: "the billing line is gone and the system blocks lead as one developer message",
			request: rowRequest(`"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.329; cch=936e2;"},` +
				`{"type":"text","text":"You are Claude Code."},{"type":"text","text":"<rules>a & b</rules>"}],"messages":[{"role":"user","content":"hi"}]`),
			want: rowHead + `,"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"You are Claude Code."},` +
				`{"type":"input_text","text":"<rules>a & b</rules>"}]},` + userHi + `]}`,
		},
		{
			name:    "the instructions placement sends the system prompt as instructions",
			request: rowRequest(`"system":[{"type":"text","text":"One. "},{"type":"text","text":"Two."}],"messages":[{"role":"user","content":"hi"}]`),
			profile: func(profile *openairesponses.ProviderRequestProfile) {
				profile.InstructionPlacement = openairesponses.InstructionPlacementInstructions
			},
			want: rowHead + `,"instructions":"One. Two.","input":[` + userHi + `]}`,
		},
		// 6.3 Messages.
		{
			name: "user text and images share one message; base64 becomes a data URL",
			request: rowRequest(`"messages":[{"role":"user","content":[{"type":"text","text":"look"},` + pngPart + `,` +
				`{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}]`),
			want: rowHead + `,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},` + pngWire + `,` +
				`{"type":"input_image","image_url":"https://example.com/a.png"}]}]}`,
		},
		{
			name:    "the profile's image detail is written on each image",
			request: rowRequest(`"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}]`),
			profile: func(profile *openairesponses.ProviderRequestProfile) { profile.ImageDetail = "high" },
			want: rowHead + `,"input":[{"type":"message","role":"user","content":[` +
				`{"type":"input_image","image_url":"https://example.com/a.png","detail":"high"}]}]}`,
		},
		{
			name: "PDF documents become input_file parts named by their title",
			request: rowRequest(`"messages":[{"role":"user","content":[` + pdfPart + `,` +
				`{"type":"document","title":"spec.pdf","source":{"type":"base64","media_type":"application/pdf","data":"` + pdfData + `"}},` +
				`{"type":"document","source":{"type":"url","url":"https://example.com/b.pdf"}}]}]`),
			want: rowHead + `,"input":[{"type":"message","role":"user","content":[` + pdfWire + `,` +
				`{"type":"input_file","file_data":"data:application/pdf;base64,` + pdfData + `","filename":"spec.pdf"},` +
				`{"type":"input_file","file_url":"https://example.com/b.pdf"}]}]}`,
		},
		{
			name: "a plain-text document becomes input_text",
			request: rowRequest(`"messages":[{"role":"user","content":[` +
				`{"type":"document","title":"a \"b\" <c>","source":{"type":"text","media_type":"text/plain","data":"line one"}}]}]`),
			want: rowHead + `,"input":[{"type":"message","role":"user","content":[` +
				`{"type":"input_text","text":"<document title=\"a &quot;b&quot; &lt;c&gt;\">line one</document>"}]}]}`,
			notices: []string{"document_converted $.messages[0].blocks[0]"},
		},
		{
			name: "tool results keep their position; strings stay strings and lists keep their parts",
			request: rowRequest(`"tools":[` + readTool + `],"messages":[{"role":"user","content":"read"},` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"a.txt"}},` +
				`{"type":"tool_use","id":"toolu_2","name":"Read","input":{"path":"a.pdf"}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"text one"},` +
				`{"type":"tool_result","tool_use_id":"toolu_2","content":[{"type":"text","text":"PDF file read"},` + pdfPart + `,` + pngPart + `]},` +
				`{"type":"text","text":"Tool loaded."}]}]`),
			want: rowHead + rowTools + `,"input":[` + userRead + `,` + readCalled + `,` +
				`{"type":"function_call","call_id":"toolu_2","name":"Read","arguments":"{\"path\":\"a.pdf\"}"},` +
				`{"type":"function_call_output","call_id":"toolu_1","output":"text one"},` +
				`{"type":"function_call_output","call_id":"toolu_2","output":[{"type":"input_text","text":"PDF file read"},` + pdfWire + `,` + pngWire + `]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"Tool loaded."}]}]}`,
		},
		{
			name: "a tool-output PDF becomes a note when the profile does not send documents",
			request: toolRow(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[` +
				`{"type":"document","title":"r.pdf","source":{"type":"base64","media_type":"application/pdf","data":"` + pdfData + `"}}]}]}`),
			profile: func(profile *openairesponses.ProviderRequestProfile) {
				profile.ToolOutputDocuments = openairesponses.ToolOutputDocumentsTextNote
			},
			want: toolWant(`{"type":"function_call_output","call_id":"toolu_1","output":[{"type":"input_text",` +
				`"text":"[The document r.pdf was returned here but could not be forwarded.]"}]}`),
			notices: []string{"media_not_forwarded $.messages[2].blocks[0].content[0]"},
		},
		{
			name: "tool_reference loads become text and make their deferred tools visible",
			request: rowRequest(`"tools":[` + readTool + `,` +
				`{"name":"ToolSearch","input_schema":{"type":"object","properties":{}}},` +
				`{"name":"CronCreate","defer_loading":true,"input_schema":{"type":"object","properties":{}}},` +
				`{"name":"DeferredToolPlaceholder","defer_loading":true,"input_schema":{"type":"object","properties":{}}},` +
				`{"name":"Monitor","defer_loading":true,"input_schema":{"type":"object","properties":{}}}],` +
				`"messages":[{"role":"user","content":"load"},` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_s","name":"ToolSearch","input":{"query":"select:CronCreate,Monitor"}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_s","content":[` +
				`{"type":"tool_reference","tool_name":"CronCreate"},{"type":"tool_reference","tool_name":"Monitor"}]},` +
				`{"type":"text","text":"Tool loaded."}]}]`),
			want: rowHead + `,"tool_choice":"auto","parallel_tool_calls":true,"tools":[` + readWire + `,` +
				`{"type":"function","name":"ToolSearch","parameters":{"type":"object","properties":{}},"strict":false},` +
				`{"type":"function","name":"CronCreate","parameters":{"type":"object","properties":{}},"strict":false},` +
				`{"type":"function","name":"Monitor","parameters":{"type":"object","properties":{}},"strict":false}],"input":[` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"load"}]},` +
				`{"type":"function_call","call_id":"toolu_s","name":"ToolSearch","arguments":"{\"query\":\"select:CronCreate,Monitor\"}"},` +
				`{"type":"function_call_output","call_id":"toolu_s","output":[{"type":"input_text","text":"Loaded tool: CronCreate\nLoaded tool: Monitor"}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"Tool loaded."}]}]}`,
			notices: []string{
				"deferred_tool_not_forwarded $.tools[3]",
				"tool_reference_encoded $.messages[2].blocks[0].content",
			},
		},
		{
			name:    "an error result is marked in its text",
			request: toolRow(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"no such file"}]}`),
			want:    toolWant(`{"type":"function_call_output","call_id":"toolu_1","output":"Tool error: no such file"}`),
			notices: []string{"tool_error_flag_encoded $.messages[2].blocks[0].is_error"},
		},
		{
			name: "an error result that already says so is unchanged",
			request: toolRow(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,` +
				`"content":"<tool_use_error>no such file</tool_use_error>"}]}`),
			want: toolWant(`{"type":"function_call_output","call_id":"toolu_1","output":"<tool_use_error>no such file</tool_use_error>"}`),
		},
		{
			name: "an error list output gains its mark before the first part",
			request: toolRow(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,` +
				`"content":[` + pngPart + `,{"type":"text","text":"bad image"}]}]}`),
			want: toolWant(`{"type":"function_call_output","call_id":"toolu_1","output":[{"type":"input_text","text":"Tool error:"},` +
				pngWire + `,{"type":"input_text","text":"bad image"}]}`),
			notices: []string{"tool_error_flag_encoded $.messages[2].blocks[0].is_error"},
		},
		{
			name: "a mid-conversation system message becomes a developer message in place",
			request: rowRequest(`"messages":[{"role":"user","content":"hi"},` +
				`{"role":"system","content":[{"type":"text","text":"# Environment"}],"output_config":{"effort":"medium"}},` +
				`{"role":"user","content":"go"}]`),
			want: rowHead + `,"input":[` + userHi + `,{"type":"message","role":"developer","content":[{"type":"input_text","text":"# Environment"}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]}]}`,
			notices: []string{"system_role_normalized $.messages[1].role"},
		},
		{
			name: "assistant text before a call is commentary, text alone is the final answer",
			request: rowRequest(`"tools":[` + readTool + `],"messages":[{"role":"user","content":"read"},` +
				`{"role":"assistant","content":[{"type":"text","text":"Let me look."},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"a.txt"}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"one"}]},` +
				`{"role":"assistant","content":[{"type":"text","text":"It says one."}]},{"role":"user","content":"thanks"}]`),
			want: rowHead + rowTools + `,"input":[` + userRead + `,` +
				`{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Let me look."}]},` + readCalled + `,` +
				`{"type":"function_call_output","call_id":"toolu_1","output":"one"},` +
				`{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"It says one."}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"thanks"}]}]}`,
			notices: []string{"message_phase_inferred $.messages[1].blocks[0]", "message_phase_inferred $.messages[3].blocks[0]"},
		},
		{
			name: "the omit switch writes no phase",
			request: rowRequest(`"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"},` +
				`{"role":"user","content":"bye"}]`),
			profile: func(profile *openairesponses.ProviderRequestProfile) {
				profile.AssistantPhase = openairesponses.AssistantPhaseOmit
			},
			want: rowHead + `,"input":[` + userHi + `,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"bye"}]}]}`,
		},
		{
			name: "a call id the API refuses is rewritten the same way for the call and its output",
			request: rowRequest(`"tools":[` + readTool + `],"messages":[{"role":"user","content":"read"},` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"toolu.with.dots","name":"Read","input":{"path":"a.txt"}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu.with.dots","content":"one"}]}]`),
			want: rowHead + rowTools + `,"input":[` + userRead + `,` +
				`{"type":"function_call","call_id":"` + rewrittenCallID("toolu.with.dots") + `","name":"Read","arguments":"{\"path\":\"a.txt\"}"},` +
				`{"type":"function_call_output","call_id":"` + rewrittenCallID("toolu.with.dots") + `","output":"one"}]}`,
			notices: []string{
				"tool_item_identity_not_forwarded $.messages[1].blocks[0].id",
				"tool_item_identity_not_forwarded $.messages[2].blocks[0].id",
			},
		},
		{
			name: "Anthropic thinking and redacted thinking are not replayed",
			request: toolRow(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"one"}]},` +
				`{"role":"assistant","content":[{"type":"thinking","thinking":"Plan.","signature":"EqQBCkYIARgCKkBgenuine"},` +
				`{"type":"redacted_thinking","data":"EmwKAhgBEgy"},{"type":"text","text":"Done."}]},{"role":"user","content":"ok"}`),
			want: toolWant(`{"type":"function_call_output","call_id":"toolu_1","output":"one"},` +
				`{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done."}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"ok"}]}`),
			notices: []string{
				"reasoning_not_replayed $.messages[3].blocks[0]",
				"reasoning_not_replayed $.messages[3].blocks[1]",
				"message_phase_inferred $.messages[3].blocks[2]",
			},
		},
		{
			name:    "a sealed reasoning block is replayed right before the item it produced",
			context: &sealed,
			request: rowRequest(`"tools":[` + readTool + `],"messages":[{"role":"user","content":"read"},` +
				`{"role":"assistant","content":[{"type":"thinking","thinking":"I should read it.","signature":"SEAL-1"},` +
				`{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"a.txt"}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"one"}]}]`),
			want: rowHead + rowTools + `,"input":[` + userRead + `,` +
				`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"I should read it."}],"encrypted_content":"ENC-1"},` + readCalled + `,` +
				`{"type":"function_call_output","call_id":"toolu_1","output":"one"}]}`,
		},
		{
			name:    "raw reasoning text is replayed as content",
			context: &sealed,
			request: rowRequest(`"messages":[{"role":"user","content":"hi"},` +
				`{"role":"assistant","content":[{"type":"thinking","thinking":"raw thought","signature":"SEAL-CONTENT"},{"type":"text","text":"hello"}]},` +
				`{"role":"user","content":"bye"}]`),
			want: rowHead + `,"input":[` + userHi + `,` +
				`{"type":"reasoning","id":"rs_2","summary":[],"content":[{"type":"reasoning_text","text":"raw thought"}],"encrypted_content":"ENC-2"},` +
				`{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"hello"}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"bye"}]}]}`,
			notices: []string{"message_phase_inferred $.messages[1].blocks[1]"},
		},
		{
			name:    "the empty-summary switch replays the seal without its text",
			context: &sealed,
			profile: func(profile *openairesponses.ProviderRequestProfile) {
				profile.ReplaySummary = openairesponses.ReplaySummaryEmpty
			},
			request: rowRequest(`"messages":[{"role":"user","content":"hi"},` +
				`{"role":"assistant","content":[{"type":"thinking","thinking":"I should read it.","signature":"SEAL-1"},{"type":"text","text":"hello"}]},` +
				`{"role":"user","content":"bye"}]`),
			want: rowHead + `,"input":[` + userHi + `,` +
				`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"ENC-1"},` +
				`{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"hello"}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"bye"}]}]}`,
			notices: []string{"message_phase_inferred $.messages[1].blocks[1]"},
		},
		// 6.4 Reasoning intent.
		{
			name:    "effort maps one to one and adaptive thinking asks for summaries",
			request: rowRequest(`"thinking":{"type":"adaptive","display":"omitted"},"output_config":{"effort":"max"},"messages":[{"role":"user","content":"hi"}]`),
			want:    rowHead + `,"reasoning":{"effort":"max","summary":"auto"},"input":[` + userHi + `]}`,
		},
		{
			name:    "a budget without an effort becomes an effort",
			request: `{"model":"claude-opus-5-5","max_tokens":40000,"thinking":{"type":"enabled","budget_tokens":8192},"messages":[{"role":"user","content":"hi"}]}`,
			want: `{"model":"gpt-6.2-sol","stream":true,"store":false,"include":["reasoning.encrypted_content"],` +
				`"reasoning":{"effort":"medium","summary":"auto"},"input":[` + userHi + `]}`,
			notices: []string{"max_output_tokens_not_forwarded $.max_tokens", "thinking_budget_not_forwarded $.thinking.budget_tokens"},
		},
		{
			name:    "disabled thinking keeps the least effort and asks for no reasoning state",
			request: rowRequest(`"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]`),
			want:    `{"model":"gpt-6.2-sol","stream":true,"store":false,"max_output_tokens":1024,"reasoning":{"effort":"low"},"input":[` + userHi + `]}`,
			notices: []string{"thinking_mode_not_forwarded $.thinking.type"},
		},
		{
			name:    "disabled thinking keeps an explicit effort",
			request: rowRequest(`"thinking":{"type":"disabled"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]`),
			want:    `{"model":"gpt-6.2-sol","stream":true,"store":false,"max_output_tokens":1024,"reasoning":{"effort":"high"},"input":[` + userHi + `]}`,
			notices: []string{"thinking_mode_not_forwarded $.thinking.type"},
		},
		{
			name:    "absent thinking sends the effort without a summary",
			request: rowRequest(`"output_config":{"effort":"low"},"messages":[{"role":"user","content":"hi"}]`),
			want:    rowHead + `,"reasoning":{"effort":"low"},"input":[` + userHi + `]}`,
		},
		// 6.5 Tools.
		{
			name: "tool schemas lose only their top-level identity; strict is always false",
			request: rowRequest(`"tools":[{"name":"Bash","description":"Run","eager_input_streaming":true,"input_schema":` +
				`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","$id":"bash","properties":{"a":{"$id":"kept","type":"string"}}}},` +
				`{"name":"Plain","input_schema":{"type":"object","properties":{}}}],"messages":[{"role":"user","content":"hi"}]`),
			want: rowHead + `,"tool_choice":"auto","parallel_tool_calls":true,"tools":[` +
				`{"type":"function","name":"Bash","description":"Run","parameters":{"type":"object","properties":{"a":{"$id":"kept","type":"string"}}},"strict":false},` +
				`{"type":"function","name":"Plain","parameters":{"type":"object","properties":{}},"strict":false}],"input":[` + userHi + `]}`,
			notices: []string{"eager_tool_input_streaming_not_forwarded $.tools[0].eager_input_streaming", "tool_schema_normalized $.tools"},
		},
		{
			name: "a tool name the API refuses is hashed the same way for its definition, calls and choice",
			request: rowRequest(`"tools":[{"name":"my.tool","input_schema":{"type":"object","properties":{}}}],"tool_choice":{"type":"tool","name":"my.tool"},` +
				`"messages":[{"role":"user","content":"go"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"my.tool","input":{}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]`),
			want: rowHead + `,"tool_choice":{"type":"function","name":"` + hashedToolName("my.tool") + `"},"parallel_tool_calls":true,"tools":[` +
				`{"type":"function","name":"` + hashedToolName("my.tool") + `","parameters":{"type":"object","properties":{}},"strict":false}],"input":[` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]},` +
				`{"type":"function_call","call_id":"toolu_1","name":"` + hashedToolName("my.tool") + `","arguments":"{}"},` +
				`{"type":"function_call_output","call_id":"toolu_1","output":"ok"}]}`,
		},
		{
			name: "a call to a tool no longer declared is still encoded",
			request: rowRequest(`"messages":[{"role":"user","content":"go"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Gone","input":{}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]`),
			want: rowHead + `,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]},` +
				`{"type":"function_call","call_id":"toolu_1","name":"Gone","arguments":"{}"},` +
				`{"type":"function_call_output","call_id":"toolu_1","output":"ok"}]}`,
		},
		// 6.6 Tool choice.
		{
			name:    "any becomes required",
			request: rowRequest(`"tools":[` + readTool + `],"tool_choice":{"type":"any"},"messages":[{"role":"user","content":"hi"}]`),
			want:    rowHead + `,"tool_choice":"required","parallel_tool_calls":true,"tools":[` + readWire + `],"input":[` + userHi + `]}`,
		},
		{
			name:    "a named choice keeps its tool",
			request: rowRequest(`"tools":[` + readTool + `],"tool_choice":{"type":"tool","name":"Read"},"messages":[{"role":"user","content":"hi"}]`),
			want:    rowHead + `,"tool_choice":{"type":"function","name":"Read"},"parallel_tool_calls":true,"tools":[` + readWire + `],"input":[` + userHi + `]}`,
		},
		{
			name:    "none stays none and parallel calls can be disabled",
			request: rowRequest(`"tools":[` + readTool + `],"tool_choice":{"type":"none","disable_parallel_tool_use":true},"messages":[{"role":"user","content":"hi"}]`),
			want:    rowHead + `,"tool_choice":"none","parallel_tool_calls":false,"tools":[` + readWire + `],"input":[` + userHi + `]}`,
		},
		{
			name:    "no tools means no tool members",
			request: rowRequest(`"tools":[],"tool_choice":{"type":"auto"},"messages":[{"role":"user","content":"hi"}]`),
			want:    rowHead + `,"input":[` + userHi + `]}`,
		},
		// 6.7 Structured output.
		{
			name: "a closed schema is sent strict under a synthesized name",
			request: rowRequest(`"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"title":{"type":"string"}},` +
				`"required":["title"],"additionalProperties":false}}},"messages":[{"role":"user","content":"hi"}]`),
			want: rowHead + `,"text":{"format":{"type":"json_schema","name":"vibermate_output_cfc7c525c81648f54a804f29",` +
				`"schema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false},"strict":true}},"input":[` + userHi + `]}`,
			notices: []string{"structured_output_name_synthesized $.output_config.format"},
		},
		{
			name: "an open schema is sent non-strict",
			request: rowRequest(`"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"title":{"type":"string"}}}}},` +
				`"messages":[{"role":"user","content":"hi"}]`),
			want: rowHead + `,"text":{"format":{"type":"json_schema","name":"` + outputName(`{"type":"object","properties":{"title":{"type":"string"}}}`) + `",` +
				`"schema":{"type":"object","properties":{"title":{"type":"string"}}},"strict":false}},"input":[` + userHi + `]}`,
			notices: []string{"structured_output_name_synthesized $.output_config.format"},
		},
	}
}

func outputName(schema string) string {
	digest := sha256.Sum256([]byte(schema))
	return "vibermate_output_" + hex.EncodeToString(digest[:12])
}

// rowBody resolves a row's seal placeholders.
func rowBody(t testing.TB, request string) []byte {
	t.Helper()
	request = strings.ReplaceAll(request, `"SEAL-1"`, `"`+sealFor(t, "rs_1", "ENC-1", "")+`"`)
	request = strings.ReplaceAll(request, `"SEAL-CONTENT"`, `"`+sealFor(t, "rs_2", "ENC-2", protocolcore.ReasoningTextContent)+`"`)
	return []byte(request)
}

// rowNeutralRequest is the neutral request a row's Claude body decodes to.
func rowNeutralRequest(t testing.TB, row encodeRow) protocolcore.Request {
	t.Helper()
	request := decodeClaude(t, rowBody(t, row.request))
	request.ProtocolEvidence = row.evidence
	return request
}

func encodeRowBody(t testing.TB, row encodeRow) ([]byte, protocolcore.TranslationReport) {
	t.Helper()
	request := rowNeutralRequest(t, row)
	context := protocolpath.ProviderContext{}
	if row.context != nil {
		context = *row.context
	}
	side := newProviderSide(t, row.profile)
	encoded, report, err := side.EncodeRequestFor(context, request)
	if err != nil {
		t.Fatalf("encode: %v (notices %v)", err, noticeLines(report))
	}
	if encoded.Method() != "POST" || encoded.RelativePath() != "v1/responses" ||
		encoded.Headers().Get("Accept") != "text/event-stream" {
		t.Fatalf("provider request = %s %s %v", encoded.Method(), encoded.RelativePath(), encoded.Headers())
	}
	body := encoded.Body()
	if err := side.ValidateTransformedRequest(body); err != nil {
		t.Fatalf("re-admission refused the encoder's own body: %v\n%s", err, body)
	}
	return body, report
}

// TestProviderEncodeRows pins one case per row of the design 6 tables: the
// exact body, that re-admission accepts it, and the encoder's notices.
func TestProviderEncodeRows(t *testing.T) {
	t.Parallel()
	for _, row := range encodeRows() {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			body, report := encodeRowBody(t, row)
			var want bytes.Buffer
			if err := json.Compact(&want, []byte(row.want)); err != nil {
				t.Fatalf("expected body is not JSON: %v\n%s", err, row.want)
			}
			if !bytes.Equal(body, want.Bytes()) {
				t.Fatalf("body =\n%s\nwant\n%s", body, want.Bytes())
			}
			if got := noticeLines(report); !slices.Equal(got, append([]string{}, row.notices...)) {
				t.Fatalf("notices = %q, want %q", got, row.notices)
			}
		})
	}
}

func TestProviderEncodeRefusesWhatCannotTravel(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		request string
		reason  protocolcore.Reason
		path    string
		notices []string
	}{
		"a Files API image": {
			request: rowRequest(`"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"file_1"}}]}]`),
			reason:  protocolcore.ReasonUnsupportedClientInput,
			path:    "$.messages[0].blocks[0].source",
			notices: []string{"media_not_forwarded $.messages[0].blocks[0]"},
		},
		"a Files API document a tool returned": {
			request: toolRow(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"document","source":{"type":"file","file_id":"file_1"}}]}]}`),
			reason:  protocolcore.ReasonUnsupportedClientInput,
			path:    "$.messages[2].blocks[0].content[0].source",
			notices: []string{"media_not_forwarded $.messages[2].blocks[0].content[0]"},
		},
		"a required choice whose tools are all deferred": {
			request: rowRequest(`"tools":[{"name":"Cron","defer_loading":true,"input_schema":{"type":"object"}}],"tool_choice":{"type":"any"},"messages":[{"role":"user","content":"hi"}]`),
			reason:  protocolcore.ReasonUnsupportedClientInput,
			path:    "$.tool_choice",
			notices: []string{"deferred_tool_not_forwarded $.tools[0]"},
		},
		"two tools that share a provider name": {
			request: rowRequest(`"tools":[{"name":"my.tool","input_schema":{"type":"object"}},{"name":"` + hashedToolName("my.tool") + `","input_schema":{"type":"object"}}],` +
				`"messages":[{"role":"user","content":"hi"}]`),
			reason:  protocolcore.ReasonUnsupportedClientInput,
			path:    "$.tools[1].name",
			notices: []string{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			request := decodeClaude(t, []byte(test.request))
			_, report, err := newProviderSide(t, nil).EncodeRequestFor(protocolpath.ProviderContext{}, request)
			var failure *protocolcore.Failure
			if err == nil || !errors.As(err, &failure) || failure.Reason != test.reason || failure.Path != test.path {
				t.Fatalf("encode error = %v (%#v), want %s at %s", err, failure, test.reason, test.path)
			}
			if got := noticeLines(report); !slices.Equal(got, test.notices) {
				t.Fatalf("notices = %q, want %q", got, test.notices)
			}
		})
	}
}

// TestProviderEncodeClaudeCorpus runs every Claude body of the S2 corpus
// through translated decode, encode and re-admission, and checks what holds
// for every body: the body streams unstored, names the mapped model, carries
// no billing line, declares exactly the loaded tools without $schema, answers
// every call, never replays a foreign seal, and is the same bytes twice.
func TestProviderEncodeClaudeCorpus(t *testing.T) {
	t.Parallel()
	side := newProviderSide(t, nil)
	for _, file := range claudeCorpusFiles(t) {
		t.Run(filepath.Base(file), func(t *testing.T) {
			t.Parallel()
			request := decodeClaude(t, readFile(t, file))
			encoded, _, err := side.EncodeRequestFor(protocolpath.ProviderContext{ReasoningBinding: testBinding}, request)
			if err != nil {
				t.Fatal(err)
			}
			body := encoded.Body()
			if err := side.ValidateTransformedRequest(body); err != nil {
				t.Fatalf("re-admission: %v", err)
			}
			again, _, err := side.EncodeRequestFor(protocolpath.ProviderContext{ReasoningBinding: testBinding}, request)
			if err != nil || !bytes.Equal(again.Body(), body) {
				t.Fatal("encoding is not deterministic")
			}
			if bytes.Contains(body, []byte("x-anthropic-billing-header")) {
				t.Fatal("the billing line reached the provider body")
			}
			var wire struct {
				Model  string `json:"model"`
				Stream bool   `json:"stream"`
				Store  *bool  `json:"store"`
				Tools  []struct {
					Name       string                     `json:"name"`
					Strict     *bool                      `json:"strict"`
					Parameters map[string]json.RawMessage `json:"parameters"`
				} `json:"tools"`
				Input []map[string]json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.Model != mappedModel || !wire.Stream || wire.Store == nil || *wire.Store {
				t.Fatalf("envelope = %s %v %v", wire.Model, wire.Stream, wire.Store)
			}
			loaded := request.LoadedTools()
			if len(wire.Tools) != len(loaded) {
				t.Fatalf("tools = %d, want the %d loaded ones", len(wire.Tools), len(loaded))
			}
			for index, tool := range wire.Tools {
				if tool.Name != loaded[index].Name || tool.Strict == nil || *tool.Strict ||
					tool.Parameters["$schema"] != nil || tool.Name == "DeferredToolPlaceholder" {
					t.Fatalf("tool %d = %+v", index, tool)
				}
			}
			calls, outputs := map[string]bool{}, map[string]bool{}
			for _, item := range wire.Input {
				var kind, callID string
				_ = json.Unmarshal(item["type"], &kind)
				_ = json.Unmarshal(item["call_id"], &callID)
				switch kind {
				case "function_call":
					calls[callID] = true
				case "function_call_output":
					outputs[callID] = true
				case "reasoning":
					t.Fatal("the corpus holds no ViberMate seal, yet reasoning was replayed")
				}
			}
			for callID := range calls {
				if !outputs[callID] {
					t.Fatalf("call %s has no output", callID)
				}
			}
		})
	}
}

// TestProviderEncodeWorkedExample pins design 6.10 on claude-tools.008: the
// item order, the loaded tools, the tool outputs and the notices.
func TestProviderEncodeWorkedExample(t *testing.T) {
	t.Parallel()
	request := decodeClaude(t, readFile(t, filepath.Join(claudeCorpus, "claude-tools.008.json")))
	encoded, report, err := newProviderSide(t, nil).EncodeRequestFor(protocolpath.ProviderContext{ReasoningBinding: testBinding}, request)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		MaxOutputTokens   *int              `json:"max_output_tokens"`
		Include           []string          `json:"include"`
		Reasoning         map[string]string `json:"reasoning"`
		ToolChoice        string            `json:"tool_choice"`
		ParallelToolCalls bool              `json:"parallel_tool_calls"`
		Tools             []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Input []struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Phase   string          `json:"phase"`
			CallID  string          `json:"call_id"`
			Name    string          `json:"name"`
			Output  json.RawMessage `json:"output"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(encoded.Body(), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.MaxOutputTokens != nil || !slices.Equal(wire.Include, []string{"reasoning.encrypted_content"}) ||
		wire.Reasoning["effort"] != "medium" || wire.Reasoning["summary"] != "auto" ||
		wire.ToolChoice != "auto" || !wire.ParallelToolCalls {
		t.Fatalf("envelope = %+v", wire)
	}
	names := []string{}
	for _, tool := range wire.Tools {
		names = append(names, tool.Name)
	}
	if len(names) != 23 || slices.Contains(names, "DeferredToolPlaceholder") ||
		!slices.Contains(names, "CronCreate") || !slices.Contains(names, "WebSearch") {
		t.Fatalf("tools = %v, want the 23 loaded ones", names)
	}
	shape := []string{}
	for _, item := range wire.Input {
		label := item.Type + ":" + item.Role + item.Phase + item.CallID
		shape = append(shape, label)
	}
	wantShape := []string{
		"message:developer", "message:user", "message:developer", "message:assistantcommentary",
		"function_call:toolu_wire_search", "function_call:toolu_wire_png", "function_call:toolu_wire_txt",
		"function_call:toolu_wire_pdf", "function_call:toolu_wire_bash",
		"function_call_output:toolu_wire_search", "function_call_output:toolu_wire_txt",
		"function_call_output:toolu_wire_png", "function_call_output:toolu_wire_pdf",
		"function_call_output:toolu_wire_bash", "message:user",
	}
	if !slices.Equal(shape, wantShape) {
		t.Fatalf("items =\n%q\nwant\n%q", shape, wantShape)
	}
	outputs := map[string]string{}
	for _, item := range wire.Input {
		if item.Type == "function_call_output" {
			outputs[item.CallID] = string(item.Output)
		}
	}
	if !strings.HasPrefix(outputs["toolu_wire_search"], `[{"type":"input_text","text":"Loaded tool: CronCreate\nLoaded tool: CronDelete\n`) ||
		!strings.HasPrefix(outputs["toolu_wire_txt"], `"1\twire fixture text line one`) ||
		!strings.HasPrefix(outputs["toolu_wire_png"], `[{"type":"input_image","image_url":"data:image/png;base64,`) ||
		!strings.Contains(outputs["toolu_wire_pdf"], `{"type":"input_file","file_data":"data:application/pdf;base64,`) ||
		!strings.HasSuffix(outputs["toolu_wire_pdf"], `"filename":"document.pdf"}]`) ||
		outputs["toolu_wire_bash"] != `"wire-fixture"` {
		t.Fatalf("outputs = %v", outputs)
	}
	if last := wire.Input[len(wire.Input)-1]; len(last.Content) != 1 || last.Content[0].Text != "Tool loaded." {
		t.Fatalf("last item = %+v", last)
	}
	wantNotices := []string{
		"max_output_tokens_not_forwarded $.max_tokens",
		"diagnostics_not_forwarded $.diagnostics",
		"deferred_tool_not_forwarded $.tools[22]",
		"tool_schema_normalized $.tools",
		"system_role_normalized $.messages[1].role",
		"reasoning_not_replayed $.messages[2].blocks[0]",
		"reasoning_not_replayed $.messages[2].blocks[1]",
		"message_phase_inferred $.messages[2].blocks[2]",
		"tool_reference_encoded $.messages[3].blocks[0].content",
	}
	if got := noticeLines(report); !slices.Equal(got, wantNotices) {
		t.Fatalf("notices =\n%q\nwant\n%q", got, wantNotices)
	}
}
