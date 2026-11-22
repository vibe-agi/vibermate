package openairesponses_test

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// The token count runs the item plan, so it counts what the body would
// carry. Each expectation is computed by hand from the tokenestimate rules:
// text ceil(ASCII bytes / 4) plus one per other rune, 4 per item, 8 per tool
// plus its name, description and ceil(schema bytes / 4).

const (
	billingLine  = `{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.329; cch=936e2;"}`
	countSystem  = `"system":[` + billingLine + `,{"type":"text","text":"You are helpful."}]`
	deferredTool = `{"name":"CronCreate","description":"` + "Schedule a prompt to run later. " + `","defer_loading":true,"input_schema":{"type":"object","properties":{}}}`
)

func countTokens(t *testing.T, providerContext protocolpath.ProviderContext, body string) int {
	t.Helper()
	side := newProviderSide(t, nil)
	count, err := side.CountInputTokens(providerContext, decodeClaude(t, []byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestCountInputTokensCountsThePlannedBody(t *testing.T) {
	t.Parallel()

	body := rowRequest(countSystem + `,"tools":[` + readTool + `],"messages":[{"role":"user","content":"hi"}]`)
	readSchema := `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`
	want := 0 +
		// developer message: "You are helpful." is 16 ASCII bytes.
		4 + 4 +
		// user "hi"
		4 + 1 +
		// Read: name 1, "Read a file" 3, schema bytes / 4.
		8 + 1 + 3 + (len(readSchema)+3)/4
	if got := countTokens(t, streamContext, body); got != want {
		t.Fatalf("count = %d, want %d", got, want)
	}
	// The billing line never reaches the body, so it costs nothing.
	without := strings.Replace(body, billingLine+",", "", 1)
	if got := countTokens(t, streamContext, without); got != want {
		t.Fatalf("count without the billing line = %d, want %d", got, want)
	}
}

func TestCountInputTokensLeavesOutWhatWouldNotBeSent(t *testing.T) {
	t.Parallel()

	base := rowRequest(`"tools":[` + readTool + `],"messages":[{"role":"user","content":"hi"}]`)
	withDeferred := rowRequest(`"tools":[` + readTool + `,` + deferredTool + `],"messages":[{"role":"user","content":"hi"}]`)
	if got, want := countTokens(t, streamContext, withDeferred), countTokens(t, streamContext, base); got != want {
		t.Fatalf("a deferred tool nothing loaded counted: %d, want %d", got, want)
	}
	// Once a tool_reference loads it, the tool is declared and counted.
	loaded := rowRequest(`"tools":[` + readTool + `,` + deferredTool + `],"messages":[{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_s","name":"Read","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_s","content":[{"type":"tool_reference","tool_name":"CronCreate"}]}]}]`)
	unloaded := strings.Replace(loaded, `[{"type":"tool_reference","tool_name":"CronCreate"}]`, `"x"`, 1)
	// The loaded tool: 8, "CronCreate" 3, its description 8, its schema
	// {"type":"object","properties":{}} 9. The output text "Loaded tool:
	// CronCreate" is 23 bytes against "x"'s 1.
	if got, want := countTokens(t, streamContext, loaded)-countTokens(t, streamContext, unloaded), 8+3+8+9+(6-1); got != want {
		t.Fatalf("a loaded deferred tool added %d tokens, want %d", got, want)
	}
}

func TestCountInputTokensCountsOnlyReasoningThatWouldBeReplayed(t *testing.T) {
	t.Parallel()

	thought := strings.Repeat("a careful thought ", 40)
	assistant := func(signature string) string {
		return rowRequest(`"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[` +
			`{"type":"thinking","thinking":"` + thought + `","signature":"` + signature + `"},{"type":"text","text":"ok"}]},` +
			`{"role":"user","content":"go"}]`)
	}
	plain := rowRequest(`"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"text","text":"ok"}]},{"role":"user","content":"go"}]`)
	base := countTokens(t, streamContext, plain)
	// A genuine Anthropic signature cannot be replayed upstream.
	if got := countTokens(t, streamContext, assistant("EuYBCkQYAiJA")); got != base {
		t.Fatalf("foreign reasoning counted: %d, want %d", got, base)
	}
	// A seal of this binding is replayed as a reasoning item: 4 for the item
	// and its visible text, never its encrypted state.
	sealed := assistant(sealFor(t, "rs_1", strings.Repeat("E", 4000), ""))
	if got, want := countTokens(t, streamContext, sealed), base+4+(len(thought)+3)/4; got != want {
		t.Fatalf("replayed reasoning = %d, want %d", got, want)
	}
	// Under another binding the same seal is dropped and not counted.
	other := protocolpath.ProviderContext{ReasoningBinding: protocolpath.NewReasoningBinding("endpoint", "account")}
	if got := countTokens(t, other, sealed); got != base {
		t.Fatalf("reasoning of another binding counted: %d, want %d", got, base)
	}
}

func TestCountInputTokensSizesAttachments(t *testing.T) {
	t.Parallel()

	text := rowRequest(`"messages":[{"role":"user","content":[{"type":"text","text":"see"}]}]`)
	withMedia := rowRequest(`"messages":[{"role":"user","content":[{"type":"text","text":"see"},` + pngPart + `,` + pdfPart + `]}]`)
	// A 1x1 PNG is one high-detail tile, 255; the PDF has no page object,
	// which counts as one page, 1500.
	if got, want := countTokens(t, streamContext, withMedia)-countTokens(t, streamContext, text), 255+1500; got != want {
		t.Fatalf("attachments added %d tokens, want %d", got, want)
	}
	low := newProviderSide(t, func(profile *openairesponses.ProviderRequestProfile) { profile.ImageDetail = "low" })
	count, err := low.CountInputTokens(streamContext, decodeClaude(t, []byte(withMedia)))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := count-countTokens(t, streamContext, text), 85+1500; got != want {
		t.Fatalf("low-detail attachments added %d tokens, want %d", got, want)
	}
}

func TestCountInputTokensFailsLikeTheEncoder(t *testing.T) {
	t.Parallel()

	side := newProviderSide(t, nil)
	request := decodeClaude(t, []byte(rowRequest(`"messages":[{"role":"user","content":[`+
		`{"type":"image","source":{"type":"file","file_id":"file_1"}}]}]`)))
	if _, err := side.CountInputTokens(streamContext, request); err == nil {
		t.Fatal("a provider file reference was counted although it cannot be sent")
	}
}
