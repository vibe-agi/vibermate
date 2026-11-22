package anthropicchat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// The Claude Code 2.1.329 corpus is the S2 characterization corpus, read in
// place so the repository holds one copy; the A->C goldens beside it record
// what the default profile decodes from each body.
const (
	clientProfileCorpus        = "../productruntime/testdata/protocolgolden/requests"
	clientProfileStrictGoldens = "../productruntime/testdata/protocolgolden/golden/anthropic-messages-to-openai-chat"
)

func newTranslatedTestCodec(t *testing.T) *Codec {
	t.Helper()
	options := DefaultOptions()
	options.ClientInput = TranslatedClientInputProfile()
	codec, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func noticeLines(report protocolcore.TranslationReport) []string {
	lines := []string{}
	for _, notice := range report.Notices() {
		lines = append(lines, string(notice.Code)+" "+notice.Path)
	}
	return lines
}

// translatedShape summarizes a request decoded with the translated profile:
// the block kinds by role (tool-result parts and reasoning seal formats
// included), and what M6 and M7 derive from it.
type translatedShape struct {
	systemBlocks int
	loadedTools  int
	totalTools   int
	safeguards   []protocolcore.SafeguardKind
	kinds        map[string]int
}

func shapeOf(request protocolcore.Request) translatedShape {
	shape := translatedShape{
		systemBlocks: len(request.System),
		loadedTools:  len(request.LoadedTools()),
		totalTools:   len(request.Tools),
		safeguards:   request.UnservedSafeguards,
		kinds:        map[string]int{},
	}
	for _, message := range request.Messages {
		for _, block := range message.Blocks {
			shape.kinds[string(message.Role)+"/"+string(block.Kind)]++
			if block.Kind == protocolcore.BlockToolResult && block.ToolResult.Parts != nil {
				for _, part := range block.ToolResult.Parts.Items {
					shape.kinds["part/"+string(part.Kind)]++
				}
			}
			if block.Kind == protocolcore.BlockReasoning {
				shape.kinds["seal/"+string(block.Reasoning.Seal.Format)]++
			}
		}
	}
	return shape
}

// TestClientProfilesOverTheClaudeCorpus is the two-profile table of design
// 15.1. The default profile is the A->C decoder and must give exactly the
// errors and notices the A->C goldens record; the translated profile must
// decode every body Claude Code 2.1.329 sent, with exactly these losses.
func TestClientProfilesOverTheClaudeCorpus(t *testing.T) {
	t.Parallel()

	// Notices every main request declares: the billing line, the cache
	// breakpoints of system[2] and system[3], the per-message output_config
	// of messages[1] and metadata.user_id.
	main := func(middle ...string) []string {
		return slices.Concat([]string{
			"client_attribution_not_forwarded $.system[0]",
			"cache_control_not_forwarded $.system[2].cache_control",
			"cache_control_not_forwarded $.system[3].cache_control",
			"unknown_request_field_not_forwarded $.messages[1].output_config",
		}, middle, []string{"metadata_not_forwarded $.metadata"})
	}
	title := []string{
		"client_attribution_not_forwarded $.system[0]",
		"metadata_not_forwarded $.metadata",
	}
	withSafeguards := func(notices []string) []string {
		return append([]string{"safeguards_not_served $.safeguards[0]"}, notices...)
	}
	// claude-tools.008: an assistant turn with thinking, redacted thinking,
	// text and five calls, then the five results (12 tool_reference loads, a
	// PNG, text plus a PDF, two strings) and "Tool loaded.".
	toolRound := map[string]int{
		"system/text": 1, "user/text": 3, "assistant/reasoning": 2, "assistant/text": 1,
		"assistant/tool_call": 5, "user/tool_result": 5,
		"seal/anthropic.signature": 1, "seal/anthropic.redacted": 1,
	}
	firstTurn := map[string]int{"system/text": 1, "user/text": 2}
	tests := map[string]struct {
		notices []string
		shape   translatedShape
	}{
		"claude.007": {main("cache_control_not_forwarded $.messages[1].content[0].cache_control"),
			translatedShape{systemBlocks: 3, loadedTools: 11, totalTools: 12, kinds: firstTurn}},
		"claude-tools.007": {main("cache_control_not_forwarded $.messages[1].content[0].cache_control"),
			translatedShape{systemBlocks: 3, loadedTools: 11, totalTools: 12, kinds: firstTurn}},
		"claude-tools.008": {main("cache_control_not_forwarded $.messages[3].content[5].cache_control"),
			translatedShape{systemBlocks: 3, loadedTools: 23, totalTools: 24, kinds: merged(toolRound, map[string]int{
				"part/tool_reference": 12, "part/media": 2, "part/text": 1,
			})}},
		"claude-tools.008-text-results": {main("cache_control_not_forwarded $.messages[3].content[5].cache_control"),
			translatedShape{systemBlocks: 3, loadedTools: 11, totalTools: 24, kinds: merged(toolRound, map[string]int{"part/text": 3})}},
		"claude-tools.008-chat-history": {main("cache_control_not_forwarded $.messages[3].content[5].cache_control"),
			translatedShape{systemBlocks: 3, loadedTools: 11, totalTools: 24, kinds: map[string]int{
				"system/text": 1, "user/text": 3, "assistant/text": 1, "assistant/tool_call": 5,
				"user/tool_result": 5, "part/text": 3,
			}}},
		"claude-interactive.003": {title, translatedShape{systemBlocks: 2, kinds: map[string]int{"user/text": 1}}},
		"claude-interactive.005": {withSafeguards(main("cache_control_not_forwarded $.messages[1].content[0].cache_control")),
			translatedShape{systemBlocks: 3, loadedTools: 13, totalTools: 14,
				safeguards: []protocolcore.SafeguardKind{protocolcore.SafeguardDangerousToolUse}, kinds: firstTurn}},
		"claude-interactive-tools.004": {title, translatedShape{systemBlocks: 2, kinds: map[string]int{"user/text": 1}}},
		"claude-interactive-tools.005": {withSafeguards(main("cache_control_not_forwarded $.messages[1].content[0].cache_control")),
			translatedShape{systemBlocks: 3, loadedTools: 13, totalTools: 14,
				safeguards: []protocolcore.SafeguardKind{protocolcore.SafeguardDangerousToolUse}, kinds: firstTurn}},
		"claude-interactive-tools.006": {withSafeguards(main("cache_control_not_forwarded $.messages[3].content[1].cache_control")),
			translatedShape{systemBlocks: 3, loadedTools: 27, totalTools: 28,
				safeguards: []protocolcore.SafeguardKind{protocolcore.SafeguardDangerousToolUse}, kinds: map[string]int{
					"system/text": 1, "user/text": 3, "assistant/reasoning": 2, "assistant/text": 1,
					"assistant/tool_call": 1, "user/tool_result": 1, "part/tool_reference": 14,
					"seal/anthropic.signature": 1, "seal/anthropic.redacted": 1,
				}}},
		"claude-models.003": {main("cache_control_not_forwarded $.messages[1].content[0].cache_control"),
			translatedShape{systemBlocks: 3, loadedTools: 11, totalTools: 12, kinds: firstTurn}},
		"claude-models.007": {main("cache_control_not_forwarded $.messages[1].content[0].cache_control"),
			translatedShape{systemBlocks: 3, loadedTools: 11, totalTools: 12, kinds: firstTurn}},
	}

	files, err := filepath.Glob(filepath.Join(clientProfileCorpus, "claude*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(tests) {
		t.Fatalf("corpus holds %d Claude bodies, the table %d", len(files), len(tests))
	}
	strict, translated := newTestCodec(t), newTranslatedTestCodec(t)
	for _, file := range files {
		fixture := strings.TrimSuffix(filepath.Base(file), ".json")
		want, ok := tests[fixture]
		if !ok {
			t.Fatalf("no expectation for %s", fixture)
		}
		t.Run(fixture, func(t *testing.T) {
			t.Parallel()
			body, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}

			goldenBody, err := os.ReadFile(filepath.Join(clientProfileStrictGoldens, fixture+".golden.json"))
			if err != nil {
				t.Fatal(err)
			}
			var golden struct {
				Decode struct {
					Error   string   `json:"error"`
					Notices []string `json:"notices"`
				} `json:"decode"`
			}
			if err := json.Unmarshal(goldenBody, &golden); err != nil {
				t.Fatal(err)
			}
			_, report, err := strict.DecodeClientRequest(body)
			gotError := ""
			if err != nil {
				gotError = err.Error()
			}
			if gotError != golden.Decode.Error || !slices.Equal(noticeLines(report), golden.Decode.Notices) {
				t.Fatalf("default profile differs from the A->C golden\n got: %q %q\nwant: %q %q",
					gotError, noticeLines(report), golden.Decode.Error, golden.Decode.Notices)
			}

			request, report, err := translated.DecodeClientRequest(body)
			if err != nil {
				t.Fatalf("translated profile refused the body: %v", err)
			}
			if got := noticeLines(report); !slices.Equal(got, want.notices) {
				t.Fatalf("translated notices\n got: %q\nwant: %q", got, want.notices)
			}
			if got := shapeOf(request); !reflect.DeepEqual(got, want.shape) {
				t.Fatalf("translated shape\n got: %+v\nwant: %+v", got, want.shape)
			}
			for _, block := range request.System {
				if strings.Contains(block.Text, clientAttributionPrefix) {
					t.Fatal("the billing line reached the neutral system prompt")
				}
			}
			// The default profile never produces the translated profile's
			// values, which is what keeps A->C unchanged downstream.
			if strictRequest, _, err := strict.DecodeClientRequest(body); err == nil {
				if shape := shapeOf(strictRequest); shape.safeguards != nil ||
					shape.kinds["assistant/reasoning"] != 0 || shape.loadedTools != shape.totalTools {
					t.Fatalf("default profile produced translated values: %+v", shape)
				}
			}
		})
	}
}

func merged(maps ...map[string]int) map[string]int {
	out := map[string]int{}
	for _, values := range maps {
		for key, value := range values {
			out[key] = value
		}
	}
	return out
}

// decodeBoth decodes one body with both profiles.
func decodeBoth(t *testing.T, body string) (strictErr error, translated protocolcore.Request, notices []string, translatedErr error) {
	t.Helper()
	_, _, strictErr = newTestCodec(t).DecodeClientRequest([]byte(body))
	request, report, err := newTranslatedTestCodec(t).DecodeClientRequest([]byte(body))
	return strictErr, request, noticeLines(report), err
}

func requestBody(messages string, extra ...string) string {
	return `{"model":"claude-opus-5-5","max_tokens":64,"messages":` + messages + strings.Join(extra, "") + `}`
}

func TestTranslatedProfileDecodesImagesAndDocuments(t *testing.T) {
	t.Parallel()

	body := requestBody(`[{"role":"user","content":[
		{"type":"text","text":"look"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="},"cache_control":{"type":"ephemeral"}},
		{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}},
		{"type":"image","source":{"type":"file","file_id":"file_011"}},
		{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="},"title":"Spec","citations":{"enabled":true}},
		{"type":"document","source":{"type":"url","url":"https://example.com/a.pdf"},"citations":{"enabled":false}},
		{"type":"document","source":{"type":"text","media_type":"text/plain","data":"plain body"},"title":null},
		{"type":"document","source":{"type":"file","file_id":"file_012"}}
	]}]`)
	strictErr, request, notices, err := decodeBoth(t, body)
	if protocolcore.ReasonOf(strictErr) != protocolcore.ReasonUnsupportedClientInput {
		t.Fatalf("default profile must keep refusing media: %v", strictErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	want := []protocolcore.Media{
		{Kind: protocolcore.MediaImage, MediaType: "image/png", Data: "iVBORw0KGgo="},
		{Kind: protocolcore.MediaImage, URL: "https://example.com/a.png"},
		{Kind: protocolcore.MediaImage, FileID: "file_011", FileSource: protocolspec.DialectAnthropicMessages},
		{Kind: protocolcore.MediaDocument, MediaType: "application/pdf", Data: "JVBERi0=", Title: "Spec"},
		{Kind: protocolcore.MediaDocument, URL: "https://example.com/a.pdf"},
		{Kind: protocolcore.MediaDocument, MediaType: "text/plain", Text: "plain body"},
		{Kind: protocolcore.MediaDocument, FileID: "file_012", FileSource: protocolspec.DialectAnthropicMessages},
	}
	blocks := request.Messages[0].Blocks
	if len(blocks) != 1+len(want) || blocks[0].Text != "look" {
		t.Fatalf("blocks = %+v", blocks)
	}
	for index, media := range want {
		if block := blocks[index+1]; block.Kind != protocolcore.BlockMedia || *block.Media != media {
			t.Fatalf("media %d = %+v, want %+v", index, block.Media, media)
		}
	}
	wantNotices := []string{
		"cache_control_not_forwarded $.messages[0].content[1].cache_control",
		"citations_not_forwarded $.messages[0].content[4].citations",
	}
	if !slices.Equal(notices, wantNotices) {
		t.Fatalf("notices = %q, want %q", notices, wantNotices)
	}

	for name, test := range map[string]struct {
		block  string
		role   string
		reason protocolcore.Reason
		path   string
	}{
		"document context is model-visible": {`{"type":"document","source":{"type":"text","media_type":"text/plain","data":"x"},"context":"about x"}`,
			"user", protocolcore.ReasonUnsupportedClientInput, "$.messages[0].content[0].context"},
		"content-block document": {`{"type":"document","source":{"type":"content","content":[{"type":"text","text":"x"}]}}`,
			"user", protocolcore.ReasonUnsupportedClientInput, "$.messages[0].content[0].source.type"},
		"image text source": {`{"type":"image","source":{"type":"text","media_type":"text/plain","data":"x"}}`,
			"user", protocolcore.ReasonUnsupportedClientInput, "$.messages[0].content[0].source.type"},
		"image outside the closed media types": {`{"type":"image","source":{"type":"base64","media_type":"image/bmp","data":"Qk0="}}`,
			"user", protocolcore.ReasonInvalidClientRequest, "$.messages[0].content[0].source"},
		"plain http URL": {`{"type":"image","source":{"type":"url","url":"http://example.com/a.png"}}`,
			"user", protocolcore.ReasonInvalidClientRequest, "$.messages[0].content[0].source"},
		"base64 without its media type": {`{"type":"image","source":{"type":"base64","data":"iVBORw0KGgo="}}`,
			"user", protocolcore.ReasonInvalidClientRequest, "$.messages[0].content[0].source"},
		"two payloads": {`{"type":"image","source":{"type":"url","url":"https://example.com/a.png","file_id":"file_1"}}`,
			"user", protocolcore.ReasonInvalidClientRequest, "$.messages[0].content[0].source"},
		"unknown image member": {`{"type":"image","source":{"type":"url","url":"https://example.com/a.png"},"title":"x"}`,
			"user", protocolcore.ReasonInvalidClientRequest, "$.messages[0].content[0]"},
		"media from the assistant": {`{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}`,
			"assistant", protocolcore.ReasonInvalidClientRequest, "$.messages[0].content[0]"},
	} {
		_, _, _, err := decodeBoth(t, requestBody(`[{"role":"`+test.role+`","content":[`+test.block+`]}]`))
		var failure *protocolcore.Failure
		if !asFailure(err, &failure) || failure.Reason != test.reason || failure.Path != test.path {
			t.Fatalf("%s: error = %v, want %s at %s", name, err, test.reason, test.path)
		}
	}
}

func asFailure(err error, target **protocolcore.Failure) bool {
	failure, ok := err.(*protocolcore.Failure)
	if ok {
		*target = failure
	}
	return ok
}

func TestTranslatedProfileKeepsToolResultParts(t *testing.T) {
	t.Parallel()

	messages := `[
		{"role":"user","content":"go"},
		{"role":"assistant","content":[
			{"type":"tool_use","id":"toolu_1","name":"ToolSearch","input":{}},
			{"type":"tool_use","id":"toolu_2","name":"Read","input":{}},
			{"type":"tool_use","id":"toolu_3","name":"Read","input":{}},
			{"type":"tool_use","id":"toolu_4","name":"Bash","input":{}}
		]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"tool_reference","tool_name":"WebFetch","cache_control":{"type":"ephemeral"}}]},
			{"type":"tool_result","tool_use_id":"toolu_2","content":[
				{"type":"text","text":"PDF file read: a.pdf","cache_control":{"type":"ephemeral"}},
				{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="}},
				{"type":"text","text":" (1 page)"},
				{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"/9j/"}}
			]},
			{"type":"tool_result","tool_use_id":"toolu_3","content":[]},
			{"type":"tool_result","tool_use_id":"toolu_4","content":"done","is_error":true},
			{"type":"text","text":"Tool loaded.","cache_control":{"type":"ephemeral"}}
		]}
	]`
	tools := `,"tools":[
		{"name":"ToolSearch","input_schema":{"type":"object"}},
		{"name":"Read","input_schema":{"type":"object"}},
		{"name":"Bash","input_schema":{"type":"object"}},
		{"name":"WebFetch","input_schema":{"type":"object"},"defer_loading":true},
		{"name":"Monitor","input_schema":{"type":"object"},"defer_loading":true}
	]`
	strictErr, request, notices, err := decodeBoth(t, requestBody(messages, tools))
	if protocolcore.ReasonOf(strictErr) != protocolcore.ReasonUnsupportedClientInput {
		t.Fatalf("default profile must keep refusing non-text tool results: %v", strictErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	results := request.Messages[2].Blocks
	if len(results) != 5 {
		t.Fatalf("blocks = %+v", results)
	}
	if parts := results[0].ToolResult.Parts; parts == nil || len(parts.Items) != 1 ||
		parts.Items[0] != (protocolcore.ToolResultPart{Kind: protocolcore.ToolResultPartToolReference, ToolName: "WebFetch"}) ||
		results[0].ToolResult.Content != "" {
		t.Fatalf("tool_reference result = %+v", results[0].ToolResult)
	}
	pdf := results[1].ToolResult
	if pdf.Content != "PDF file read: a.pdf (1 page)" || pdf.Parts == nil || len(pdf.Parts.Items) != 4 {
		t.Fatalf("PDF result = %+v", pdf)
	}
	for index, kind := range []protocolcore.ToolResultPartKind{
		protocolcore.ToolResultPartText, protocolcore.ToolResultPartMedia,
		protocolcore.ToolResultPartText, protocolcore.ToolResultPartMedia,
	} {
		if pdf.Parts.Items[index].Kind != kind {
			t.Fatalf("part %d kind = %s, want %s", index, pdf.Parts.Items[index].Kind, kind)
		}
	}
	if pdf.Parts.Items[1].Media.MediaType != "application/pdf" || pdf.Parts.Items[3].Media.MediaType != "image/jpeg" {
		t.Fatalf("PDF result media = %+v", pdf.Parts.Items)
	}
	if empty := results[2].ToolResult; empty.Parts != nil || empty.Content != "" {
		t.Fatalf("an empty list must have no parts: %+v", empty)
	}
	if plain := results[3].ToolResult; plain.Parts != nil || plain.Content != "done" || !plain.IsError {
		t.Fatalf("a string result = %+v", plain)
	}
	if results[4].Text != "Tool loaded." {
		t.Fatalf("sibling text = %+v", results[4])
	}
	// The referenced deferred tool is loaded; the other is not.
	var loaded []string
	for _, tool := range request.LoadedTools() {
		loaded = append(loaded, tool.Name)
	}
	if strings.Join(loaded, ",") != "ToolSearch,Read,Bash,WebFetch" {
		t.Fatalf("loaded tools = %q", loaded)
	}
	wantNotices := []string{
		"cache_control_not_forwarded $.messages[2].content[0].content[0].cache_control",
		"cache_control_not_forwarded $.messages[2].content[1].content[0].cache_control",
		"cache_control_not_forwarded $.messages[2].content[4].cache_control",
	}
	if !slices.Equal(notices, wantNotices) {
		t.Fatalf("notices = %q, want %q", notices, wantNotices)
	}

	// The Chat encoder refuses a non-text result even if a translated request
	// reached it.
	if _, _, err := newTranslatedTestCodec(t).EncodeProviderRequest(request); protocolcore.ReasonOf(err) != protocolcore.ReasonUnsupportedClientInput {
		t.Fatalf("Chat encoder accepted non-text tool-result parts: %v", err)
	}

	for name, part := range map[string]string{
		"search result":            `{"type":"search_result","source":"x","title":"x","content":[]}`,
		"unknown text member":      `{"type":"text","text":"x","extra":1}`,
		"invalid tool reference":   `{"type":"tool_reference","tool_name":" padded "}`,
		"unknown reference member": `{"type":"tool_reference","tool_name":"Read","title":"x"}`,
	} {
		body := requestBody(`[
			{"role":"user","content":"go"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[` + part + `]}]}
		]`)
		if _, _, _, err := decodeBoth(t, body); err == nil || !strings.Contains(err.Error(), "$.messages[2].content[0].content[0]") {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
}

func TestTranslatedProfileModelsDeferLoading(t *testing.T) {
	t.Parallel()

	body := requestBody(`[{"role":"user","content":"hi"}]`, `,"tools":[
		{"name":"Read","input_schema":{"type":"object"},"defer_loading":false},
		{"name":"DeferredToolPlaceholder","input_schema":{"type":"object"},"defer_loading":true}
	]`)
	strictRequest, strictReport, err := newTestCodec(t).DecodeClientRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if got := noticeLines(strictReport); !slices.Equal(got, []string{
		"unknown_request_field_not_forwarded $.tools[0].defer_loading",
		"unknown_request_field_not_forwarded $.tools[1].defer_loading",
	}) || strictRequest.Tools[1].DeferLoading {
		t.Fatalf("default profile must keep declaring defer_loading: %q", got)
	}
	_, request, notices, err := decodeBoth(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 0 || request.Tools[0].DeferLoading || !request.Tools[1].DeferLoading {
		t.Fatalf("translated tools = %+v, notices %q", request.Tools, notices)
	}
	if loaded := request.LoadedTools(); len(loaded) != 1 || loaded[0].Name != "Read" {
		t.Fatalf("the placeholder must not load: %+v", loaded)
	}
	if _, _, _, err := decodeBoth(t, requestBody(`[{"role":"user","content":"hi"}]`,
		`,"tools":[{"name":"Read","input_schema":{"type":"object"},"defer_loading":"yes"}]`)); err == nil {
		t.Fatal("a non-boolean defer_loading was accepted")
	}
}

func TestTranslatedProfileDropsTheBillingLine(t *testing.T) {
	t.Parallel()

	billing := "x-anthropic-billing-header: cc_version=2.1.329.580; cch=40026; cc_prompt_id=23773894-4ecd-4827-0380-50c4cdc5ade1;"
	body := requestBody(`[{"role":"user","content":"hi"}]`, `,"system":[
		{"type":"text","text":"`+billing+`"},
		{"type":"text","text":"You are a Claude agent."},
		{"type":"text","text":"`+billing+`\nand more prose"},
		{"type":"text","text":"rules","cache_control":{"type":"ephemeral"}}
	]`)
	strictRequest, _, err := newTestCodec(t).DecodeClientRequest([]byte(body))
	if err != nil || len(strictRequest.System) != 4 || strictRequest.System[0].Text != billing {
		t.Fatalf("default profile must forward the billing line as today: %v", err)
	}
	_, request, notices, err := decodeBoth(t, body)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, block := range request.System {
		texts = append(texts, block.Text)
	}
	if !slices.Equal(texts, []string{"You are a Claude agent.", billing + "\nand more prose", "rules"}) {
		t.Fatalf("system = %q", texts)
	}
	// Later notices keep their wire indexes.
	if !slices.Equal(notices, []string{
		"client_attribution_not_forwarded $.system[0]",
		"cache_control_not_forwarded $.system[3].cache_control",
	}) {
		t.Fatalf("notices = %q", notices)
	}
	// A string system prompt is the whole prompt, never only attribution.
	_, request, _, err = decodeBoth(t, requestBody(`[{"role":"user","content":"hi"}]`, `,"system":"`+billing+`"`))
	if err != nil || len(request.System) != 1 {
		t.Fatalf("a string system prompt must be kept: %v %+v", err, request.System)
	}
}

func TestTranslatedProfileReadsReasoningSeals(t *testing.T) {
	t.Parallel()

	seal := protocolcore.ReasoningSeal{
		Format:  protocolcore.SealResponsesEncrypted,
		ItemID:  "rs_a2",
		Model:   "gpt-upstream",
		Binding: "0123456789abcdef0123456789abcdef",
		Opaque:  "ENC-1.with.dots",
	}
	summary, err := protocolcore.FormatReasoningSeal(seal, "")
	if err != nil {
		t.Fatal(err)
	}
	content, err := protocolcore.FormatReasoningSeal(seal, protocolcore.ReasoningTextContent)
	if err != nil {
		t.Fatal(err)
	}
	foreignSeal, err := protocolcore.FormatReasoningSeal(protocolcore.ReasoningSeal{
		Format: protocolcore.SealAnthropicSignature, Model: "claude", Binding: "b", Opaque: "sig",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	thinking := func(text, signature string) string {
		encoded, _ := json.Marshal(map[string]string{"type": "thinking", "thinking": text, "signature": signature})
		return string(encoded)
	}
	body := requestBody(`[{"role":"user","content":"hi"},{"role":"assistant","content":[`+
		thinking("first part\n\nsecond part", summary)+`,`+
		thinking("raw", content)+`,`+
		thinking("anthropic", "EqQBCkgIARABGAIiQL")+`,`+
		thinking("stage three", foreignSeal)+`,`+
		thinking("unsigned", "")+`,`+
		`{"type":"redacted_thinking","data":"vmr1.not-ours"},`+
		`{"type":"text","text":"answer"}]}]`, `,"thinking":{"type":"adaptive"}`)
	strictRequest, _, err := newTestCodec(t).DecodeClientRequest([]byte(body))
	if err != nil || strictRequest.Messages[1].Blocks[0].Kind != protocolcore.BlockProviderExtension {
		t.Fatalf("default profile must keep thinking as an Anthropic extension: %v", err)
	}
	_, request, _, err := decodeBoth(t, body)
	if err != nil {
		t.Fatal(err)
	}
	want := []protocolcore.Reasoning{
		{Text: "first part\n\nsecond part", Seal: seal},
		{Text: "raw", TextSource: protocolcore.ReasoningTextContent, Seal: seal},
		{Text: "anthropic", Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealAnthropicSignature, Opaque: "EqQBCkgIARABGAIiQL"}},
		{Text: "stage three", Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealAnthropicSignature, Opaque: foreignSeal}},
		{Text: "unsigned"},
		{Redacted: true, Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealAnthropicRedacted, Opaque: "vmr1.not-ours"}},
	}
	blocks := request.Messages[1].Blocks
	for index, reasoning := range want {
		if blocks[index].Kind != protocolcore.BlockReasoning || *blocks[index].Reasoning != reasoning {
			t.Fatalf("block %d = %+v, want %+v", index, blocks[index].Reasoning, reasoning)
		}
	}
	if blocks[len(want)].Text != "answer" {
		t.Fatalf("text after reasoning = %+v", blocks[len(want)])
	}

	for name, message := range map[string]string{
		"thinking from the user":  `{"role":"user","content":[` + thinking("x", summary) + `]}`,
		"unknown thinking member": `{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"s","cache_control":{"type":"ephemeral"}}]}`,
	} {
		if _, _, _, err := decodeBoth(t, requestBody(`[{"role":"user","content":"hi"},`+message+`]`)); protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidClientRequest {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
}

func TestTranslatedProfileRecordsUnservedSafeguards(t *testing.T) {
	t.Parallel()

	safeguards := `,"safeguards":[
		{"type":"dangerous_tool_use","classifier_context":{"v":1,"live_cwd":"/home/someone/project"}},
		{"type":"future_safeguard"},
		{"type":"dangerous_tool_use"}
	]`
	body := requestBody(`[{"role":"user","content":"hi"}]`, safeguards)
	strictRequest, strictReport, err := newTestCodec(t).DecodeClientRequest([]byte(body))
	if err != nil || strictRequest.UnservedSafeguards != nil ||
		!slices.Equal(noticeLines(strictReport), []string{"unknown_request_field_not_forwarded $.safeguards"}) {
		t.Fatalf("default profile must keep declaring safeguards as unknown: %v %q", err, noticeLines(strictReport))
	}
	_, request, notices, err := decodeBoth(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(request.UnservedSafeguards, []protocolcore.SafeguardKind{protocolcore.SafeguardDangerousToolUse}) {
		t.Fatalf("unserved = %q", request.UnservedSafeguards)
	}
	if !slices.Equal(notices, []string{
		"safeguards_not_served $.safeguards[0]",
		"safeguards_not_served $.safeguards[1]",
		"safeguards_not_served $.safeguards[2]",
	}) {
		t.Fatalf("notices = %q", notices)
	}
	// The classifier context is never kept anywhere in the neutral request.
	if strings.Contains(fmt.Sprintf("%+v", request), "someone") {
		t.Fatal("the classifier context reached the neutral request")
	}

	for name, value := range map[string]string{
		"not an object": `,"safeguards":["dangerous_tool_use"]`,
		"no type":       `,"safeguards":[{"classifier_context":{}}]`,
		"not a list":    `,"safeguards":{"type":"dangerous_tool_use"}`,
		"too many":      `,"safeguards":[` + strings.Repeat(`{"type":"x"},`, maxSafeguardEntries) + `{"type":"x"}]`,
	} {
		if _, _, _, err := decodeBoth(t, requestBody(`[{"role":"user","content":"hi"}]`, value)); protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidClientRequest {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
}

func TestTranslatedProfileDropsServerToolHistory(t *testing.T) {
	t.Parallel()

	history := `[
		{"role":"user","content":"search"},
		{"role":"assistant","content":[
			{"type":"text","text":"Let me look."},
			{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"x"}},
			{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]},
			{"type":"text","text":"Found it."}
		]},
		{"role":"assistant","content":[
			{"type":"mcp_tool_use","id":"mcptoolu_1","name":"t","server_name":"s","input":{}},
			{"type":"mcp_tool_result","tool_use_id":"mcptoolu_1","content":[]}
		]},
		{"role":"user","content":"thanks"}
	]`
	strictErr, request, notices, err := decodeBoth(t, requestBody(history))
	if protocolcore.ReasonOf(strictErr) != protocolcore.ReasonUnsupportedClientInput {
		t.Fatalf("default profile must keep refusing server-tool history: %v", strictErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	// The emptied assistant message leaves the conversation.
	if len(request.Messages) != 3 || len(request.Messages[1].Blocks) != 2 ||
		request.Messages[1].Blocks[0].Text != "Let me look." || request.Messages[1].Blocks[1].Text != "Found it." ||
		request.Messages[2].Blocks[0].Text != "thanks" {
		t.Fatalf("messages = %+v", request.Messages)
	}
	if !slices.Equal(notices, []string{
		"server_tool_history_not_forwarded $.messages[1].content[1]",
		"server_tool_history_not_forwarded $.messages[1].content[2]",
		"server_tool_history_not_forwarded $.messages[2].content[0]",
		"server_tool_history_not_forwarded $.messages[2].content[1]",
	}) {
		t.Fatalf("notices = %q", notices)
	}

	// Server-tool history is only an assistant's; a user turn holding it is
	// refused as an unsupported block, as is a server tool definition.
	if _, _, _, err := decodeBoth(t, requestBody(`[{"role":"user","content":[{"type":"web_search_tool_result","tool_use_id":"x","content":[]}]}]`)); protocolcore.ReasonOf(err) != protocolcore.ReasonUnsupportedClientInput {
		t.Fatalf("server-tool history from the user: %v", err)
	}
	_, _, _, err = decodeBoth(t, requestBody(`[{"role":"user","content":"hi"}]`, `,"tools":[{"type":"web_search_20250305","name":"web_search"}]`))
	var failure *protocolcore.Failure
	if !asFailure(err, &failure) || failure.Reason != protocolcore.ReasonUnsupportedClientInput || failure.Path != "$.tools[0].type" {
		t.Fatalf("a server tool must stay refused at its type: %v", err)
	}
	// A conversation of server history alone has nothing left to send.
	if _, _, _, err := decodeBoth(t, requestBody(`[{"role":"assistant","content":[{"type":"server_tool_use","id":"s","name":"web_search","input":{}}]}]`)); protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidClientRequest {
		t.Fatalf("an emptied conversation: %v", err)
	}
	for kind, want := range map[string]bool{
		"server_tool_use": true, "mcp_tool_use": true, "web_search_tool_result": true,
		"code_execution_tool_result": true, "mcp_tool_result": true,
		"tool_result": false, "_tool_result": false, "tool_use": false, "text": false,
	} {
		if serverToolHistory(kind) != want {
			t.Fatalf("serverToolHistory(%q) != %v", kind, want)
		}
	}
}

// A profile wire type embeds the shared one; the embedded fields count as
// modelled, as encoding/json promotes them, and the shared type alone does not
// model what the profile adds.
func TestModelledFieldNamesFollowEmbeddedWireTypes(t *testing.T) {
	t.Parallel()

	shared := modelledFieldNames(&anthropicToolDefinitionWire{})
	profile := modelledFieldNames(&anthropicTranslatedToolDefinitionWire{})
	if _, ok := shared["defer_loading"]; ok {
		t.Fatal("the shared tool wire models defer_loading")
	}
	if _, ok := profile["defer_loading"]; !ok || len(profile) != len(shared)+1 {
		t.Fatalf("profile fields = %v", profile)
	}
	for name := range shared {
		if _, ok := profile[name]; !ok {
			t.Fatalf("profile lost %q", name)
		}
	}
	root := modelledFieldNames(&anthropicTranslatedRequestWire{})
	if _, ok := root["safeguards"]; !ok || len(root) != len(modelledFieldNames(&anthropicRequestWire{}))+1 {
		t.Fatalf("root fields = %v", root)
	}
}
