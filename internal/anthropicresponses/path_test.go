package anthropicresponses

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// claudeTurn is a small Claude Code request: the billing line, a deferred
// tool nothing has loaded, thinking with display updates and the generic
// max_tokens cap.
const claudeTurn = `{"model":"claude-opus-5-5","max_tokens":128000,"stream":true,` +
	`"thinking":{"type":"adaptive","display":"updates"},"output_config":{"effort":"medium"},` +
	`"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.329; cch=00000;"},` +
	`{"type":"text","text":"You are a coding agent."}],` +
	`"tools":[{"name":"Bash","description":"Run a command","input_schema":{"$schema":"https://json-schema.org/draft/2020-12/schema",` +
	`"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},` +
	`{"name":"CronCreate","description":"Schedule","input_schema":{"type":"object"},"defer_loading":true}],` +
	`"messages":[{"role":"user","content":[{"type":"text","text":"list files"},` +
	`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}`

func TestNewProtocolPathComposesTheDesignedPair(t *testing.T) {
	t.Parallel()
	path, err := NewProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !path.IsComposed() {
		t.Fatal("the pair is not a composed path")
	}
	if path.Client().Dialect() != protocolspec.DialectAnthropicMessages ||
		path.Backend().Dialect() != protocolspec.DialectOpenAIResponses {
		t.Fatalf("dialects = %s -> %s", path.Client().Dialect(), path.Backend().Dialect())
	}
	if !path.AggregatesUpstreamStream() {
		t.Fatal("the pair does not aggregate a Stream=false client from the upstream stream")
	}
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	want := operations.SemanticOperationIDs(protocolspec.DialectAnthropicMessages)
	// The catalog definition production registers must select this path.
	id, err := protocolspec.NewCodecPairID(CodecPairID)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := protocolspec.NewCatalog(operations.Definitions(), []protocolspec.CodecPairDefinition{{
		ID: id, Revision: CodecRevision,
		ClientDialect: protocolspec.DialectAnthropicMessages, ProviderDialect: protocolspec.DialectOpenAIResponses,
		ClientOperationIDs:   want,
		RequiredCapabilities: []protocolspec.ProviderCapability{protocolspec.ProviderCapabilityMessages},
	}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := catalog.Resolve(protocolspec.DialectAnthropicMessages, protocolspec.DialectOpenAIResponses)
	if err != nil {
		t.Fatal(err)
	}
	if err := path.ValidatePlan(plan); err != nil {
		t.Fatalf("the path does not serve %s revision %d: %v", CodecPairID, CodecRevision, err)
	}
	slices.SortFunc(want, func(left, right protocolspec.ClientOperationID) int {
		return strings.Compare(left.String(), right.String())
	})
	if got := path.ClientOperationIDs(); !slices.Equal(got, want) || len(got) == 0 {
		t.Fatalf("operations = %v, want %v", got, want)
	}
	// count_tokens is auxiliary: it is never a semantic operation the pair
	// forwards (design 10 answers it locally).
	for _, operation := range path.ClientOperationIDs() {
		if operation.String() == operationcatalog.AnthropicMessagesCountTokensID {
			t.Fatal("the pair claims count_tokens")
		}
	}
}

// DefaultOptions is the pair's policy: the translated input profile on the
// client side and the public Responses profile on the provider side.
func TestDefaultOptionsUseTheTranslatedProfileAndThePublicAPI(t *testing.T) {
	t.Parallel()
	path, err := NewProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, report, err := path.Client().DecodeRequest([]byte(claudeTurn))
	if err != nil {
		t.Fatalf("translated decode refused a Claude Code request: %v", err)
	}
	if !reportHas(report, protocolcore.NoticeClientAttributionNotForwarded) {
		t.Fatalf("the billing line was not dropped: %+v", report.Notices())
	}
	if len(request.Messages) != 1 || len(request.Messages[0].Blocks) != 2 ||
		request.Messages[0].Blocks[1].Kind != protocolcore.BlockMedia {
		t.Fatalf("the image did not decode as media: %+v", request.Messages)
	}
	request, err = request.WithEffectiveModel("gpt-upstream")
	if err != nil {
		t.Fatal(err)
	}
	attempt := protocolpath.ProviderContext{
		Target:           protocolpath.TargetOpenAIPlatform,
		ReasoningBinding: protocolpath.NewReasoningBinding("target.openai.official", "account.primary"),
	}
	provider, encodeReport, err := path.EncodeProviderRequestFor(attempt, request, []byte(claudeTurn), http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Method() != http.MethodPost || provider.RelativePath() != "v1/responses" ||
		provider.Headers().Get("Accept") != "text/event-stream" {
		t.Fatalf("provider request head = %s %s %v", provider.Method(), provider.RelativePath(), provider.Headers())
	}
	var body struct {
		Model           string   `json:"model"`
		Stream          bool     `json:"stream"`
		Store           *bool    `json:"store"`
		Include         []string `json:"include"`
		MaxOutputTokens *int     `json:"max_output_tokens"`
		Input           []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
		Tools []struct {
			Name       string          `json:"name"`
			Strict     *bool           `json:"strict"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(provider.Body(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "gpt-upstream" || !body.Stream || body.Store == nil || *body.Store ||
		!slices.Equal(body.Include, []string{"reasoning.encrypted_content"}) || body.MaxOutputTokens != nil {
		t.Fatalf("envelope = %s", provider.Body())
	}
	if len(body.Input) != 2 || body.Input[0].Role != "developer" ||
		len(body.Input[0].Content) != 1 || body.Input[0].Content[0].Text != "You are a coding agent." {
		t.Fatalf("the system prompt is not one developer message without the billing line: %s", provider.Body())
	}
	if len(body.Tools) != 1 || body.Tools[0].Name != "Bash" || body.Tools[0].Strict == nil || *body.Tools[0].Strict ||
		strings.Contains(string(body.Tools[0].Parameters), "$schema") {
		t.Fatalf("tools = %s", provider.Body())
	}
	for _, code := range []protocolcore.NoticeCode{
		protocolcore.NoticeMaxOutputTokensNotForwarded,
		protocolcore.NoticeDeferredToolNotForwarded,
		protocolcore.NoticeToolSchemaNormalized,
	} {
		if !reportHas(encodeReport, code) {
			t.Fatalf("encode report lacks %s: %+v", code, encodeReport.Notices())
		}
	}
	if err := path.ValidateTransformedRequest(provider.Body()); err != nil {
		t.Fatalf("the encoded body fails re-admission: %v", err)
	}
}

// The pair's own Resources replace both sides' nested limits, and the path
// keeps its own copy.
func TestResourcesOverrideBothSides(t *testing.T) {
	t.Parallel()
	options := DefaultOptions()
	tight := protocolcore.ResourceLimits{
		Request:  protocolcore.ResourceCost{PayloadBytes: 64, StructureBytes: 1 << 20},
		Response: protocolcore.ResourceCost{PayloadBytes: 1 << 20, StructureBytes: 1 << 20},
	}
	options.Resources = &tight
	path, err := NewProtocolPath(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := path.Client().DecodeRequest([]byte(claudeTurn)); err == nil {
		t.Fatal("the client side kept its own request limit")
	}
	request := protocolcore.Request{
		RequestedModel: "claude-opus-5-5", EffectiveModel: "gpt-upstream", MaxOutputTokens: 1024,
		Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{
			Kind: protocolcore.BlockText, Text: strings.Repeat("x", 128),
		}}}},
	}
	if _, _, err := path.EncodeProviderRequestFor(protocolpath.ProviderContext{}, request, nil, nil); err == nil {
		t.Fatal("the provider side kept its own request limit")
	}
	defaults, err := NewProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := defaults.EncodeProviderRequestFor(protocolpath.ProviderContext{}, request, nil, nil); err != nil {
		t.Fatalf("the same request under default limits: %v", err)
	}
	invalid := protocolcore.ResourceLimits{}
	options.Resources = &invalid
	if _, err := NewProtocolPath(options); err == nil {
		t.Fatal("an invalid resource policy was accepted")
	}
}

func reportHas(report protocolcore.TranslationReport, code protocolcore.NoticeCode) bool {
	for _, notice := range report.Notices() {
		if notice.Code == code {
			return true
		}
	}
	return false
}
