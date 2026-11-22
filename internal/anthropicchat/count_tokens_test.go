package anthropicchat

import (
	"slices"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// Claude Code 2.1.329 counts tokens with countTokens({model, messages,
// tools, thinking}) and no max_tokens; for a model without adaptive thinking
// it sends thinking {type:"enabled", budget_tokens}. That exact shape must
// decode, which the Messages decoder alone refuses (an enabled budget must
// sit below max_tokens).
func TestCountTokensDecodesClaudeCodesBodyWithoutMaxTokens(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	body := []byte(`{
		"model":"claude-sonnet-5-5",
		"messages":[
			{"role":"user","content":"foo"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"t","signature":"EqQBCkgIARABGAIiQL"},
				{"type":"text","text":"ok"}
			]}
		],
		"tools":[{"name":"Read","input_schema":{"type":"object"}}],
		"thinking":{"type":"enabled","budget_tokens":1024}
	}`)
	if _, _, err := codec.DecodeClientRequest(body); err == nil {
		t.Fatal("the Messages decoder accepted an enabled budget without max_tokens")
	}
	request, _, err := codec.DecodeCountTokensRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if request.Stream || request.MaxOutputTokens != 1025 ||
		request.Reasoning.Thinking != protocolcore.ThinkingModeEnabled || request.Reasoning.BudgetTokens != 1024 {
		t.Fatalf("request = %+v", request)
	}
	// The translated profile applies whatever the codec's own profile is: the
	// replayed thinking is reasoning, not a raw Anthropic extension.
	if block := request.Messages[1].Blocks[0]; block.Kind != protocolcore.BlockReasoning {
		t.Fatalf("thinking decoded as %s", block.Kind)
	}

	// An explicit max_tokens and adaptive thinking are left as sent.
	request, _, err = codec.DecodeCountTokensRequest([]byte(`{
		"model":"claude-opus-5-5","max_tokens":2048,
		"messages":[{"role":"user","content":"foo"}],
		"thinking":{"type":"enabled","budget_tokens":1024}
	}`))
	if err != nil || request.MaxOutputTokens != 2048 {
		t.Fatalf("explicit limit = %d, %v", request.MaxOutputTokens, err)
	}
	request, _, err = codec.DecodeCountTokensRequest([]byte(`{
		"model":"claude-opus-5-5",
		"messages":[{"role":"user","content":"foo"}],
		"thinking":{"type":"adaptive"}
	}`))
	if err != nil || request.MaxOutputTokens != 0 {
		t.Fatalf("adaptive limit = %d, %v", request.MaxOutputTokens, err)
	}
	// A budget below the minimum is still refused.
	if _, _, err := codec.DecodeCountTokensRequest([]byte(`{
		"model":"claude-opus-5-5",
		"messages":[{"role":"user","content":"foo"}],
		"thinking":{"type":"enabled","budget_tokens":10}
	}`)); protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidClientRequest {
		t.Fatalf("small budget: %v", err)
	}
}

// A count sees what a translated provider would be sent: media and tool
// results decode, the billing line is gone, deferred tools are kept for the
// provider side to filter.
func TestCountTokensSeesTheTranslatedRequest(t *testing.T) {
	t.Parallel()

	request, report, err := newTestCodec(t).DecodeCountTokensRequest([]byte(`{
		"model":"claude-opus-5-5",
		"system":[{"type":"text","text":"x-anthropic-billing-header: cch=1;"},{"type":"text","text":"prompt"}],
		"messages":[{"role":"user","content":[
			{"type":"text","text":"what is this"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}
		]}],
		"tools":[
			{"name":"Read","input_schema":{"type":"object"}},
			{"name":"WebFetch","input_schema":{"type":"object"},"defer_loading":true}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(request.System) != 1 || request.System[0].Text != "prompt" ||
		request.Messages[0].Blocks[1].Kind != protocolcore.BlockMedia ||
		!request.Tools[1].DeferLoading || len(request.LoadedTools()) != 1 {
		t.Fatalf("request = %+v", request)
	}
	if !slices.Equal(noticeLines(report), []string{"client_attribution_not_forwarded $.system[0]"}) {
		t.Fatalf("notices = %q", noticeLines(report))
	}
	if _, _, err := newTestCodec(t).DecodeCountTokensRequest(nil); protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidClientRequest {
		t.Fatalf("empty body: %v", err)
	}
}
