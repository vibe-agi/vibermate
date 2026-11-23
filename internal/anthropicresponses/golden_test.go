package anthropicresponses

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// Golden round trips of design 15.3. Regenerate with
//
//	go test ./internal/anthropicresponses -run Golden -update
//
// and review the diff.
var updateGolden = flag.Bool("update", false, "rewrite the golden files")

const (
	goldenRoot = "testdata/claude-2.1.329"
	// goldenCorpus holds the ten Claude Code 2.1.329 requests of design
	// 15.3, made committable by tool/wire-skeleton for the S2 goldens. They
	// are read in place so the corpus has one copy.
	goldenCorpus = "../productruntime/testdata/protocolgolden/requests"
	// The beta list Claude Code 2.1.329 sends on its main requests. The path
	// needs none of them; it must not forward them either.
	goldenAnthropicBeta = "interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,prompt-caching-scope-2026-01-05,effort-2025-11-24"
	goldenEndpoint      = "target.openai.official"
	goldenAccount       = "golden-account"
	goldenSession       = "golden-session"
)

// goldenFixtures are the ten dumps design 15.3 names.
var goldenFixtures = []string{
	"claude.007",
	"claude-tools.007",
	"claude-tools.008",
	"claude-interactive.003",
	"claude-interactive.005",
	"claude-interactive-tools.004",
	"claude-interactive-tools.005",
	"claude-interactive-tools.006",
	"claude-models.003",
	"claude-models.007",
}

// goldenModels is the route's model mapping: the preset's first candidates.
var goldenModels = map[string]string{
	"claude-opus-5-5":   "gpt-6.2-astra",
	"claude-sonnet-5-5": "gpt-6.2-sol",
	"claude-haiku-5-5":  "gpt-6-luna",
}

func goldenContext(account string) protocolpath.ProviderContext {
	return protocolpath.ProviderContext{
		Target:           protocolpath.TargetOpenAIPlatform,
		ReasoningBinding: protocolpath.NewReasoningBinding(goldenEndpoint, account),
	}
}

func goldenPath(t *testing.T) *protocolpath.Path {
	t.Helper()
	path, err := NewProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// goldenDecode decodes a Claude Code body as the Exchange hands it to the
// provider side: the route's model mapping applied and the session header's
// evidence merged.
func goldenDecode(t *testing.T, path *protocolpath.Path, body []byte) (protocolcore.Request, protocolcore.TranslationReport) {
	t.Helper()
	request, report, err := path.Client().DecodeRequest(body)
	if err != nil {
		t.Fatalf("translated decode: %v", err)
	}
	mapped, ok := goldenModels[request.RequestedModel]
	if !ok {
		t.Fatalf("no mapping for %s", request.RequestedModel)
	}
	request, err = request.WithEffectiveModel(mapped)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(request.ProtocolEvidence, func(value protocolcore.ProtocolEvidenceValue) bool {
		return value.Name == "claude.session_id"
	}) {
		request.ProtocolEvidence = append(request.ProtocolEvidence, protocolcore.ProtocolEvidenceValue{
			Name: "claude.session_id", Value: goldenSession,
		})
		slices.SortFunc(request.ProtocolEvidence, func(left, right protocolcore.ProtocolEvidenceValue) int {
			return strings.Compare(left.Name, right.Name)
		})
	}
	return request, report
}

func goldenEncode(t *testing.T, path *protocolpath.Path, providerContext protocolpath.ProviderContext, request protocolcore.Request, body []byte) (protocolpath.ProviderRequest, protocolcore.TranslationReport) {
	t.Helper()
	headers := http.Header{"Anthropic-Beta": {goldenAnthropicBeta}}
	provider, report, err := path.EncodeProviderRequestFor(providerContext, request, body, headers)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if provider.Method() != http.MethodPost || provider.RelativePath() != "v1/responses" {
		t.Fatalf("provider request head = %s %s", provider.Method(), provider.RelativePath())
	}
	if len(provider.Headers()) != 1 || provider.Headers().Get("Accept") != "text/event-stream" {
		t.Fatalf("provider headers = %v, want only Accept: text/event-stream", provider.Headers())
	}
	if err := path.ValidateTransformedRequest(provider.Body()); err != nil {
		t.Fatalf("the encoded body fails re-admission: %v", err)
	}
	return provider, report
}

// noticeText renders reports one notice per line under their phase.
func noticeText(phases ...any) []byte {
	var text bytes.Buffer
	for index := 0; index+1 < len(phases); index += 2 {
		text.WriteString(phases[index].(string) + "\n")
		for _, notice := range phases[index+1].(protocolcore.TranslationReport).Notices() {
			text.WriteString("  " + string(notice.Code) + " " + notice.Path + "\n")
		}
	}
	return text.Bytes()
}

func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	file := filepath.Join(goldenRoot, name)
	if *updateGolden {
		if err := os.WriteFile(file, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("golden %s: %v (regenerate with -update)", name, err)
	}
	if !bytes.Equal(got, want) {
		at := 0
		for at < len(got) && at < len(want) && got[at] == want[at] {
			at++
		}
		excerpt := func(value []byte) string {
			return string(value[max(0, at-80):min(len(value), at+80)])
		}
		t.Fatalf("%s differs at byte %d\n got: %s\nwant: %s", name, at, excerpt(got), excerpt(want))
	}
}

// Golden A: every Claude Code body decodes under the translated profile and
// encodes to the checked-in Responses body and notices; the body passes
// re-admission and the openai-go oracle.
func TestGoldenRequests(t *testing.T) {
	path := goldenPath(t)
	for _, fixture := range goldenFixtures {
		t.Run(fixture, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(goldenCorpus, fixture+".json"))
			if err != nil {
				t.Fatal(err)
			}
			request, decodeReport := goldenDecode(t, path, body)
			provider, encodeReport := goldenEncode(t, path, goldenContext(goldenAccount), request, body)
			compareGolden(t, fixture+".responses.json", provider.Body())
			compareGolden(t, fixture+".notices.txt", noticeText("decode", decodeReport, "encode", encodeReport))
			assertResponsesOracle(t, request, provider.Body())
			// What design 6 promises for every Claude Code body, whatever the
			// golden holds.
			for _, absent := range []string{"x-anthropic-billing-header", "DeferredToolPlaceholder", goldenSession, goldenAnthropicBeta} {
				if bytes.Contains(provider.Body(), []byte(absent)) {
					t.Fatalf("the provider body carries %q", absent)
				}
			}
			var envelope struct {
				Stream         bool     `json:"stream"`
				Store          *bool    `json:"store"`
				Include        []string `json:"include"`
				PromptCacheKey string   `json:"prompt_cache_key"`
			}
			if err := json.Unmarshal(provider.Body(), &envelope); err != nil {
				t.Fatal(err)
			}
			if !envelope.Stream || envelope.Store == nil || *envelope.Store ||
				!slices.Equal(envelope.Include, []string{"reasoning.encrypted_content"}) ||
				!strings.HasPrefix(envelope.PromptCacheKey, "vm1-") || len(envelope.PromptCacheKey) != len("vm1-")+32 {
				t.Fatalf("envelope = %+v", envelope)
			}
		})
	}
}

// assertResponsesOracle unmarshals a body into openai-go's request type and
// checks that the SDK files every value under the variant its type names,
// against the neutral request rather than against another parse of the same
// bytes.
func assertResponsesOracle(t *testing.T, request protocolcore.Request, body []byte) {
	t.Helper()
	var params responses.ResponseNewParams
	if err := json.Unmarshal(body, &params); err != nil {
		t.Fatalf("openai-go rejected the body: %v", err)
	}
	if params.Model != request.EffectiveModel || !params.Store.Valid() || params.Store.Value {
		t.Fatalf("SDK model/store = %q %+v", params.Model, params.Store)
	}
	loaded := request.LoadedTools()
	if len(params.Tools) != len(loaded) {
		t.Fatalf("SDK tools = %d, want the %d loaded tools", len(params.Tools), len(loaded))
	}
	for index, tool := range params.Tools {
		function := tool.OfFunction
		if function == nil || function.Name != loaded[index].Name || !function.Strict.Valid() || function.Strict.Value ||
			function.Parameters == nil || function.Parameters["$schema"] != nil {
			t.Fatalf("SDK tool %d = %+v, want %s", index, tool, loaded[index].Name)
		}
	}
	if len(loaded) != 0 && (!params.ToolChoice.OfToolChoiceMode.Valid() || !params.ParallelToolCalls.Valid()) {
		t.Fatalf("SDK tool choice = %+v, parallel = %+v", params.ToolChoice, params.ParallelToolCalls)
	}
	if request.Output.Kind != "" {
		format := params.Text.Format.OfJSONSchema
		if format == nil || !strings.HasPrefix(format.Name, "vibermate_output_") || format.Schema == nil {
			t.Fatalf("SDK text format = %+v", params.Text.Format)
		}
	}
	var calls, outputs []string
	for _, message := range request.Messages {
		for _, block := range message.Blocks {
			switch block.Kind {
			case protocolcore.BlockToolCall:
				calls = append(calls, block.ToolCall.Key.WireID()+" "+block.ToolCall.Name+" "+string(block.ToolCall.Arguments.Bytes()))
			case protocolcore.BlockToolResult:
				outputs = append(outputs, block.ToolResult.Key.WireID())
			}
		}
	}
	var sdkCalls, sdkOutputs []string
	messages := 0
	for index, item := range params.Input.OfInputItemList {
		kind := item.GetType()
		if kind == nil {
			t.Fatalf("SDK item %d has no type", index)
		}
		switch *kind {
		case "message":
			role := item.GetRole()
			if role == nil || !slices.Contains([]string{"developer", "user", "assistant"}, *role) {
				t.Fatalf("SDK item %d role = %v", index, role)
			}
			if *role != "assistant" && (item.OfMessage == nil || len(item.OfMessage.Content.OfInputItemContentList) == 0) {
				t.Fatalf("SDK item %d is not an input message with parts: %+v", index, item)
			}
			messages++
		case "function_call":
			call := item.OfFunctionCall
			if call == nil {
				t.Fatalf("SDK item %d is not a function call", index)
			}
			sdkCalls = append(sdkCalls, call.CallID+" "+call.Name+" "+call.Arguments)
		case "function_call_output":
			output := item.OfFunctionCallOutput
			if output == nil || (!output.Output.OfString.Valid() && len(output.Output.OfResponseFunctionCallOutputItemArray) == 0) {
				t.Fatalf("SDK item %d is not a function call output: %+v", index, item)
			}
			sdkOutputs = append(sdkOutputs, output.CallID)
		case "reasoning":
			reasoning := item.OfReasoning
			if reasoning == nil || reasoning.ID == "" || !reasoning.EncryptedContent.Valid() {
				t.Fatalf("SDK item %d is not a replayable reasoning item: %+v", index, item)
			}
		default:
			t.Fatalf("SDK item %d has an unexpected type %s", index, *kind)
		}
	}
	if messages == 0 || !slices.Equal(sdkCalls, calls) || !slices.Equal(sdkOutputs, outputs) {
		t.Fatalf("SDK messages=%d calls=%v outputs=%v; the request holds calls=%v outputs=%v", messages, sdkCalls, sdkOutputs, calls, outputs)
	}
}

// The golden tree holds exactly the inputs and outputs these tests use, so a
// fixture removed from a test cannot leave a stale golden behind.
func TestGoldenTreeHasNoStrayFiles(t *testing.T) {
	if *updateGolden {
		t.Skip("goldens are being rewritten")
	}
	expected := map[string]bool{"README.md": true}
	for _, fixture := range goldenFixtures {
		expected[fixture+".responses.json"] = true
		expected[fixture+".notices.txt"] = true
	}
	for _, stream := range goldenStreams {
		for _, suffix := range []string{".responses.sse", ".anthropic.sse", ".stream-notices.txt", ".round-trip.responses.json", ".round-trip.notices.txt"} {
			expected[stream.name+suffix] = true
		}
	}
	entries, err := os.ReadDir(goldenRoot)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !expected[entry.Name()] {
			t.Errorf("stray golden entry %s", entry.Name())
		}
		found[entry.Name()] = true
	}
	for name := range expected {
		if !found[name] {
			t.Errorf("golden %s is missing", name)
		}
	}
}
