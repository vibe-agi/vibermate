package exchange

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/anthropicresponses"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/ssewire"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

// These tests run the production Anthropic Messages to OpenAI Responses path
// through the real Exchange pipeline. The test catalog and selector hold the
// five production pairs (pipeline_test.go).

const (
	translatedModel  = "gpt-provider"
	translatedOrigin = "https://api.openai.com"
	translatedTools  = `"tools":[{"name":"Bash","description":"Run a command",` +
		`"input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}]`
)

var (
	translatedPrimary   = testAccount{id: "account.primary", revision: 1, epoch: 1}
	translatedSecondary = testAccount{id: "account.secondary", revision: 1, epoch: 1}
)

func translatedProtocolPath(t *testing.T) *protocolpath.Path {
	t.Helper()
	path, err := anthropicresponses.NewProtocolPath(anthropicresponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func translatedPlanOptions(account testAccount, configure ...func(*testPlanOptions)) testPlanOptions {
	options := testPlanOptions{
		destination: environment.DestinationKindUpstream, providerOrigin: translatedOrigin,
		backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModeMap, mappedModel: translatedModel,
		accounts: []testAccount{account}, preferred: account.id,
	}
	for _, change := range configure {
		change(&options)
	}
	return options
}

func translatedPlan(t *testing.T, account testAccount, configure ...func(*testPlanOptions)) environment.RequestPlan {
	t.Helper()
	return mustEnvironmentRequestPlan(t, translatedPlanOptions(account, configure...))
}

// newTranslatedPipeline builds the pipeline as production does, with body
// admission, so client admission, provider admission and re-admission all
// run.
func newTranslatedPipeline(
	t *testing.T,
	accounts AccountLeaseAuthority,
	provider Provider,
	decisions ToolDecisionGate,
	configure ...func(*Options),
) *Pipeline {
	t.Helper()
	annotations, err := clientannotation.NewSigner(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(annotations.Destroy)
	policy, err := DefaultResourcePolicy()
	if err != nil {
		t.Fatal(err)
	}
	admission, err := NewBodyAdmission(policy)
	if err != nil {
		t.Fatal(err)
	}
	options := Options{
		OwnerContext: context.Background(), Actions: newTestActionGate(t), Accounts: accounts,
		ProtocolPaths: mustProtocolPathSelector(t), Provider: provider,
		ToolDecisions: decisions, RetryWaiter: &retryWaiterDouble{}, Observer: &attemptObserverDouble{},
		ContentObserver: &contentObserverDouble{}, ObservationTimeout: time.Second,
		Hold:              HoldPolicy{MaxTransportResends: 0, RetryDelay: 0, MaxDuration: time.Second},
		Stream:            DefaultStreamBudgets(),
		ClientAnnotations: annotations, Now: time.Now, BodyAdmission: admission,
	}
	for _, change := range configure {
		change(&options)
	}
	pipeline, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownPipeline(t, pipeline) })
	return pipeline
}

type translatedTurn struct {
	result     Result
	err        error
	downstream *downstreamRecorder
}

// claudeClientHeaders are what Claude Code sends besides the body. None of
// them may reach the Responses upstream.
var claudeClientHeaders = http.Header{
	"Content-Type":                {"application/json"},
	"Anthropic-Version":           {"2023-06-01"},
	"Anthropic-Beta":              {"interleaved-thinking-2025-05-14"},
	"X-Claude-Code-Session-Id":    {"session-fixture"},
	"X-Stainless-Lang":            {"js"},
	"X-Stainless-Package-Version": {"0.70.0"},
}

func runTranslated(t *testing.T, pipeline *Pipeline, plan environment.RequestPlan, exchangeID string, body []byte) translatedTurn {
	t.Helper()
	downstream := &downstreamRecorder{}
	result, err := executeTranslated(t, pipeline, plan, exchangeID, body, downstream)
	return translatedTurn{result: result, err: err, downstream: downstream}
}

func executeTranslated(
	t *testing.T,
	pipeline *Pipeline,
	plan environment.RequestPlan,
	exchangeID string,
	body []byte,
	downstream Downstream,
) (Result, error) {
	t.Helper()
	lease, err := pipeline.bodyAdmission.AcquirePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	request := mustClientRequestWithOptions(t, exchangeID, plan, body,
		WithBodyLease(lease), WithOriginalHeaders(claudeClientHeaders),
		WithAnthropicBetaHeader("interleaved-thinking-2025-05-14"), WithClientUserAgent("claude-cli/2.1.329 (external, cli)"))
	return pipeline.Execute(t.Context(), request, downstream)
}

// claudeBody is a Claude Code streaming request with one tool.
func claudeBody(messages string) []byte {
	return []byte(`{"model":"claude-client-alias","max_tokens":1024,"stream":true,` +
		`"thinking":{"type":"adaptive","display":"summarized"},` + translatedTools + `,"messages":` + messages + `}`)
}

const claudeFirstTurn = `[{"role":"user","content":"list files"}]`

// responsesWire frames event payloads as the public Responses API does.
func responsesWire(t *testing.T, payloads ...string) []byte {
	t.Helper()
	var wire bytes.Buffer
	for _, payload := range payloads {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(payload), &header); err != nil || header.Type == "" {
			t.Fatalf("fixture event is invalid: %s", payload)
		}
		fmt.Fprintf(&wire, "event: %s\ndata: %s\n\n", header.Type, payload)
	}
	return wire.Bytes()
}

func responsesObject(status, output, usage string) string {
	return `{"id":"resp_1","object":"response","created_at":1700000000,"status":"` + status + `","model":"` + translatedModel +
		`","output":[` + output + `],"usage":` + usage + `}`
}

const responsesUsage = `{"input_tokens":120,"input_tokens_details":{"cached_tokens":100},"output_tokens":40,` +
	`"output_tokens_details":{"reasoning_tokens":12},"total_tokens":160}`

// toolTurnEvents is one public-API turn: a reasoning item with
// encrypted_content, a commentary message and a call to Bash.
func toolTurnEvents() []string {
	reasoning := `{"id":"rs_1","type":"reasoning","encrypted_content":"ENC-1","summary":[{"type":"summary_text","text":"Plan first."}]}`
	message := `{"id":"msg_1","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"text":"Let me check."}],"role":"assistant"}`
	call := `{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"command\":\"ls\"}","call_id":"call_1","name":"Bash"}`
	part := func(sequence int, kind, members string) string {
		return `{"type":"response.` + kind + `","sequence_number":` + strconv.Itoa(sequence) +
			`,"item_id":"msg_1","output_index":1,"content_index":0,` + members + `}`
	}
	return []string{
		`{"type":"response.created","sequence_number":0,"response":` + responsesObject("in_progress", "", "null") + `}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
		`{"type":"response.reasoning_summary_part.added","sequence_number":2,"item_id":"rs_1","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`,
		`{"type":"response.reasoning_summary_text.delta","sequence_number":3,"item_id":"rs_1","output_index":0,"summary_index":0,"delta":"Plan first."}`,
		`{"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":` + reasoning + `}`,
		`{"type":"response.output_item.added","sequence_number":5,"output_index":1,"item":{"id":"msg_1","type":"message","status":"in_progress","content":[],"role":"assistant"}}`,
		part(6, "content_part.added", `"part":{"type":"output_text","annotations":[],"text":""}`),
		part(7, "output_text.delta", `"delta":"Let me check."`),
		part(8, "output_text.done", `"text":"Let me check."`),
		`{"type":"response.output_item.done","sequence_number":9,"output_index":1,"item":` + message + `}`,
		`{"type":"response.output_item.added","sequence_number":10,"output_index":2,"item":{"id":"fc_1","type":"function_call","status":"in_progress","arguments":"","call_id":"call_1","name":"Bash"}}`,
		`{"type":"response.output_item.done","sequence_number":11,"output_index":2,"item":` + call + `}`,
		`{"type":"response.completed","sequence_number":12,"response":` + responsesObject("completed", reasoning+","+message+","+call, responsesUsage) + `}`,
	}
}

// textTurnEvents is a public-API turn that only answers.
func textTurnEvents(text string) []string {
	message := `{"id":"msg_2","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"text":` +
		strconv.Quote(text) + `}],"role":"assistant"}`
	return []string{
		`{"type":"response.created","sequence_number":0,"response":` + responsesObject("in_progress", "", "null") + `}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_2","type":"message","status":"in_progress","content":[],"role":"assistant"}}`,
		`{"type":"response.content_part.added","sequence_number":2,"item_id":"msg_2","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"text":""}}`,
		`{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_2","output_index":0,"content_index":0,"delta":` + strconv.Quote(text) + `}`,
		`{"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":` + message + `}`,
		`{"type":"response.completed","sequence_number":5,"response":` + responsesObject("completed", message, responsesUsage) + `}`,
	}
}

func responsesStream(t *testing.T, events []string) *http.Response {
	t.Helper()
	return streamResponse(http.StatusOK, bytes.NewReader(responsesWire(t, events...)))
}

// accumulateClaude reads client bytes as anthropic-sdk-go hands them to
// Claude Code.
func accumulateClaude(t *testing.T, stream []byte) anthropic.Message {
	t.Helper()
	decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	events, err := decoder.Feed(stream)
	if err != nil || decoder.Finish() != nil {
		t.Fatalf("client stream is not SSE: %v", err)
	}
	var message anthropic.Message
	for _, event := range events {
		var union anthropic.MessageStreamEventUnion
		if err := union.UnmarshalJSON(event.Data); err != nil {
			t.Fatalf("anthropic-sdk-go cannot parse %s: %v", event.Data, err)
		}
		if err := message.Accumulate(union); err != nil {
			t.Fatalf("anthropic-sdk-go cannot accumulate %s: %v", event.Data, err)
		}
	}
	return message
}

// replayedTurn is the next request the way Claude Code replays an answer:
// every block of the accumulated message verbatim, then the tool result.
func replayedTurn(t *testing.T, message anthropic.Message) []byte {
	t.Helper()
	blocks := make([]string, 0, len(message.Content))
	for _, block := range message.Content {
		var encoded []byte
		var err error
		switch block.Type {
		case "thinking":
			encoded, err = json.Marshal(map[string]string{"type": "thinking", "thinking": block.Thinking, "signature": block.Signature})
		case "text":
			encoded, err = json.Marshal(map[string]string{"type": "text", "text": block.Text})
		case "tool_use":
			encoded, err = json.Marshal(map[string]any{"type": "tool_use", "id": block.ID, "name": block.Name, "input": block.Input})
		default:
			t.Fatalf("unexpected block %q", block.Type)
		}
		if err != nil {
			t.Fatal(err)
		}
		blocks = append(blocks, string(encoded))
	}
	return claudeBody(`[{"role":"user","content":"list files"},{"role":"assistant","content":[` + strings.Join(blocks, ",") +
		`]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"a.txt"}]}]`)
}

type responsesBody struct {
	Model  string                       `json:"model"`
	Stream bool                         `json:"stream"`
	Store  *bool                        `json:"store"`
	Input  []map[string]json.RawMessage `json:"input"`
	Access struct {
		Cyber string `json:"cyber"`
	} `json:"access_programs"`
}

func decodeResponsesBody(t *testing.T, request providertransport.Request) responsesBody {
	t.Helper()
	var body responsesBody
	if err := json.Unmarshal(request.Body(), &body); err != nil {
		t.Fatalf("provider body is not JSON: %v", err)
	}
	return body
}

func (body responsesBody) itemTypes() []string {
	types := make([]string, 0, len(body.Input))
	for _, item := range body.Input {
		var kind string
		_ = json.Unmarshal(item["type"], &kind)
		types = append(types, kind)
	}
	return types
}

func noticeCodes(report protocolcore.TranslationReport) []string {
	var codes []string
	for _, notice := range report.Notices() {
		codes = append(codes, string(notice.Code))
	}
	return codes
}

// firstTurn runs the tool turn on plan and returns what Claude Code
// accumulated from it.
func firstTurn(t *testing.T, plan environment.RequestPlan, account testAccount) (anthropic.Message, providertransport.Request) {
	t.Helper()
	provider := &providerDouble{results: []providerResult{{response: responsesStream(t, toolTurnEvents())}}}
	pipeline := newTranslatedPipeline(t, newAccountAuthority(t, account), provider, approvedDecisions())
	turn := runTranslated(t, pipeline, plan, "exchange-turn-1", claudeBody(claudeFirstTurn))
	if turn.err != nil || turn.result.Outcome != AttemptSucceeded {
		t.Fatalf("turn 1: outcome=%s err=%v", turn.result.Outcome, turn.err)
	}
	return accumulateClaude(t, turn.downstream.bytesSnapshot()), provider.requestsSnapshot()[0]
}

// A turn-1 reasoning seal is minted at the stream site with the route's
// binding, and the turn-2 encode site replays it under the same binding. The
// same history on a route with another account drops it (design 15.4).
func TestTranslatedReasoningIsBoundToTheRouteAndReplayedOnTheNextTurn(t *testing.T) {
	plan := translatedPlan(t, translatedPrimary)
	message, sent := firstTurn(t, plan, translatedPrimary)

	// What reached the upstream: the Responses wire, no Anthropic headers.
	body := decodeResponsesBody(t, sent)
	if sent.RelativePath() != "v1/responses" || body.Model != translatedModel || !body.Stream ||
		body.Store == nil || *body.Store {
		t.Fatalf("provider request = %s %s", sent.RelativePath(), sent.Body())
	}
	// Exactly the provider side's own header: nothing of Claude Code's
	// envelope (Anthropic-*, X-Claude-Code-*, X-Stainless-*, its Anthropic-Beta)
	// and no Codex identity header. The transport adds authentication and
	// the wire profile's User-Agent when it sends.
	if headers := sent.Headers(); len(headers) != 1 || headers.Get("Accept") != "text/event-stream" {
		t.Fatalf("provider request headers = %v", headers)
	}
	// The cyber finalizer runs after re-admission on a managed account of the
	// public API, as it does for Codex.
	if body.Access.Cyber != "standard" {
		t.Fatalf("access_programs = %q, want the account's standard program", body.Access.Cyber)
	}

	if len(message.Content) != 3 || message.Content[0].Type != "thinking" || message.Content[0].Thinking != "Plan first." ||
		message.Content[1].Text != "Let me check." || message.Content[2].Name != "Bash" {
		t.Fatalf("Claude Code accumulated %+v", message.Content)
	}
	seal, _, ok := protocolcore.ParseReasoningSeal(message.Content[0].Signature)
	binding := protocolpath.NewReasoningBinding("target.primary", translatedPrimary.id)
	if !ok || seal.ItemID != "rs_1" || seal.Opaque != "ENC-1" || seal.Model != translatedModel || seal.Binding != binding {
		t.Fatalf("seal = %+v (ok=%v), want binding %s", seal, ok, binding)
	}

	// Turn 2 on the same route replays the reasoning right before the
	// message it produced.
	next := replayedTurn(t, message)
	provider := &providerDouble{results: []providerResult{{response: responsesStream(t, textTurnEvents("a.txt"))}}}
	pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, approvedDecisions())
	turn := runTranslated(t, pipeline, plan, "exchange-turn-2", next)
	if turn.err != nil {
		t.Fatalf("turn 2: %v", turn.err)
	}
	replayed := decodeResponsesBody(t, provider.requestsSnapshot()[0])
	if !slices.Equal(replayed.itemTypes(), []string{"message", "reasoning", "message", "function_call", "function_call_output"}) {
		t.Fatalf("turn 2 items = %v", replayed.itemTypes())
	}
	if string(replayed.Input[1]["id"]) != `"rs_1"` || string(replayed.Input[1]["encrypted_content"]) != `"ENC-1"` {
		t.Fatalf("replayed reasoning = %s", provider.requestsSnapshot()[0].Body())
	}
	if slices.Contains(noticeCodes(turn.result.Translation), string(protocolcore.NoticeReasoningNotReplayed)) {
		t.Fatalf("turn 2 notices = %v", noticeCodes(turn.result.Translation))
	}

	// The same history on a route whose fixed account differs: the seal's
	// binding no longer matches, so the reasoning is dropped and declared.
	other := translatedPlan(t, translatedSecondary)
	provider = &providerDouble{results: []providerResult{{response: responsesStream(t, textTurnEvents("a.txt"))}}}
	pipeline = newTranslatedPipeline(t, newAccountAuthority(t, translatedSecondary), provider, approvedDecisions())
	turn = runTranslated(t, pipeline, other, "exchange-turn-2-other", next)
	if turn.err != nil {
		t.Fatalf("turn 2 on another account: %v", turn.err)
	}
	if bytes.Contains(provider.requestsSnapshot()[0].Body(), []byte(`"type":"reasoning"`)) ||
		!slices.Contains(noticeCodes(turn.result.Translation), string(protocolcore.NoticeReasoningNotReplayed)) {
		t.Fatalf("another account's turn replayed the seal: %s %v", provider.requestsSnapshot()[0].Body(), noticeCodes(turn.result.Translation))
	}
}

// Both call sites of one attempt build the same context: the encode site
// from the selection and credential, the stream site from the frozen
// request built from them.
func TestTranslatedProviderContextAgreesAtBothCallSites(t *testing.T) {
	pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), &providerDouble{}, approvedDecisions())
	for _, test := range []struct {
		origin string
		target protocolpath.ProviderTargetKind
	}{
		{translatedOrigin, protocolpath.TargetOpenAIPlatform},
		{"https://api.openai.com/v1", protocolpath.TargetOpenAIPlatform},
		{"https://compatible.example/v1", protocolpath.TargetGeneric},
		// Only a restored revision can hold this route; stage 2 reads the kind.
		{"https://chatgpt.com/backend-api/codex", protocolpath.TargetChatGPTCodex},
	} {
		plan := translatedPlan(t, translatedPrimary, func(options *testPlanOptions) {
			options.providerOrigin, options.restore = test.origin, true
		})
		selection, err := selectFrozenPlan(plan)
		if err != nil {
			t.Fatal(err)
		}
		encode := pipeline.providerContext(selection.targetRef, selection.target.Origin(), providerauth.CredentialManaged, translatedPrimary.id)
		if encode.Target != test.target ||
			encode.ReasoningBinding != protocolpath.NewReasoningBinding("target.primary", translatedPrimary.id) {
			t.Fatalf("%s: encode context = %+v", test.origin, encode)
		}
		if encode.RejectedSeal == nil {
			t.Fatal("the encode context has no rejected-seal memory")
		}
		passthrough := pipeline.providerContext(selection.targetRef, selection.target.Origin(), providerauth.CredentialClientPassthrough, "ignored")
		if passthrough.ReasoningBinding != protocolpath.NewReasoningBinding("target.primary", protocolpath.ClientCredentialAccount) {
			t.Fatalf("%s: a client credential binds to %s", test.origin, passthrough.ReasoningBinding)
		}
	}
	// The stream site reads the frozen request; the turn-1 seal above proves
	// it carries the same binding end to end. Here the frozen request is
	// checked directly.
	plan := translatedPlan(t, translatedPrimary)
	_, sent := firstTurn(t, plan, translatedPrimary)
	stream := pipeline.providerContextOf(sent)
	if stream.Target != protocolpath.TargetOpenAIPlatform ||
		stream.ReasoningBinding != protocolpath.NewReasoningBinding("target.primary", translatedPrimary.id) {
		t.Fatalf("stream context = %+v", stream)
	}
}

// Reasoning and text before the first call stream live; the call and the
// terminal stay held until the tool decision, and a rejection discards them.
// While a person decides, keepalives keep the client alive.
func TestTranslatedToolCallsWaitForTheDecision(t *testing.T) {
	plan := translatedPlan(t, translatedPrimary)
	t.Run("approve after keepalives", func(t *testing.T) {
		decisions := newBlockingDecisionDouble()
		provider := &providerDouble{results: []providerResult{{response: responsesStream(t, toolTurnEvents())}}}
		pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, decisions,
			func(options *Options) {
				options.Stream = StreamBudgets{KeepaliveInterval: 5 * time.Millisecond, ProviderProgressTimeout: time.Minute}
			})
		downstream := &downstreamRecorder{}
		done := make(chan error, 1)
		go func() {
			_, err := executeTranslated(t, pipeline, plan, "exchange-approve", claudeBody(claudeFirstTurn), downstream)
			done <- err
		}()
		select {
		case <-decisions.entered:
		case err := <-done:
			t.Fatalf("the turn ended before its decision: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("the tool decision never started")
		}
		deadline := time.Now().Add(10 * time.Second)
		for downstream.keepaliveCount() < 3 {
			if time.Now().After(deadline) {
				t.Fatal("no keepalives during the tool decision")
			}
			time.Sleep(time.Millisecond)
		}
		held := downstream.bytesSnapshot()
		if !bytes.Contains(held, []byte("Let me check.")) || bytes.Contains(held, []byte("tool_use")) ||
			bytes.Contains(held, []byte("message_stop")) {
			t.Fatalf("held bytes leaked before Approve: %s", held)
		}
		close(decisions.approve)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		message := accumulateClaude(t, downstream.bytesSnapshot())
		if message.StopReason != anthropic.StopReasonToolUse || message.Content[len(message.Content)-1].Name != "Bash" {
			t.Fatalf("released message = %+v", message)
		}
	})
	t.Run("reject", func(t *testing.T) {
		decisions := &decisionDouble{decision: ToolDecision{Outcome: ToolDecisionRejected, ReasonCode: "user_rejected"}}
		provider := &providerDouble{results: []providerResult{{response: responsesStream(t, toolTurnEvents())}}}
		pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, decisions)
		turn := runTranslated(t, pipeline, plan, "exchange-reject", claudeBody(claudeFirstTurn))
		if ReasonOf(turn.err) != ReasonToolDecisionRejected || turn.result.Outcome != AttemptAborted {
			t.Fatalf("outcome=%s err=%v", turn.result.Outcome, turn.err)
		}
		request := decisions.lastRequest()
		if intents := request.ToolIntents(); len(intents) != 1 || intents[0].Call.Name != "Bash" ||
			intents[0].Call.Key.Source() != "openai-responses-call" {
			t.Fatalf("decision intents = %+v", request.ToolIntents())
		}
		if written := turn.downstream.bytesSnapshot(); bytes.Contains(written, []byte("tool_use")) ||
			bytes.Contains(written, []byte("message_delta")) || bytes.Contains(written, []byte("message_stop")) {
			t.Fatalf("a rejected call reached the client: %s", written)
		}
	})
}

// A request script edits the encoded Responses body; the edited body is
// re-admitted by the provider side and sent.
func TestTranslatedRequestScriptEditsTheResponsesBody(t *testing.T) {
	edit := translatedPlan(t, translatedPrimary, func(options *testPlanOptions) {
		options.transform = messagetransform.Policy{RequestJavaScript: `const p=JSON.parse(request.body);` +
			`p.input[0].content[0].text="edited by script";request.body=JSON.stringify(p);`}
	})
	provider := &providerDouble{results: []providerResult{{response: responsesStream(t, textTurnEvents("ok"))}}}
	pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, approvedDecisions())
	if turn := runTranslated(t, pipeline, edit, "exchange-script", claudeBody(claudeFirstTurn)); turn.err != nil {
		t.Fatalf("an admissible edit failed: %v", turn.err)
	}
	if !bytes.Contains(provider.requestsSnapshot()[0].Body(), []byte(`"edited by script"`)) {
		t.Fatalf("the script's edit was not sent: %s", provider.requestsSnapshot()[0].Body())
	}

	// An edit the Responses decoder refuses never leaves the machine.
	broken := translatedPlan(t, translatedPrimary, func(options *testPlanOptions) {
		options.transform = messagetransform.Policy{RequestJavaScript: `const p=JSON.parse(request.body);` +
			`p.input[0].role="invalid-role";request.body=JSON.stringify(p);`}
	})
	provider = &providerDouble{}
	pipeline = newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, approvedDecisions())
	if turn := runTranslated(t, pipeline, broken, "exchange-script-broken", claudeBody(claudeFirstTurn)); ReasonOf(turn.err) != ReasonMessageTransformFailed ||
		provider.callCount() != 0 {
		t.Fatalf("err=%v provider calls=%d", turn.err, provider.callCount())
	}
}

func rejection(status int, body string, headers ...string) *http.Response {
	response := jsonResponse(status, []byte(body))
	for index := 0; index+1 < len(headers); index += 2 {
		response.Header.Set(headers[index], headers[index+1])
	}
	return response
}

func openAIError(code, message, param string) string {
	encodedParam := "null"
	if param != "" {
		encodedParam = strconv.Quote(param)
	}
	return `{"error":{"message":` + strconv.Quote(message) + `,"type":"invalid_request_error","param":` + encodedParam +
		`,"code":` + strconv.Quote(code) + `}}`
}

type renderedAnthropicError struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Error     struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Every row of design 8.3 that an HTTP rejection of the public API reaches,
// through the real stream path: the classified class on Failure.Provider,
// the Anthropic error Claude Code recovers from, its client status and
// headers, and no upstream text anywhere.
func TestTranslatedRejectionsBecomeTheAnthropicErrorsClaudeCodeRecoversFrom(t *testing.T) {
	const secret = "secret-upstream-text org-4242"
	for _, test := range []struct {
		name       string
		responses  []*http.Response
		hold       HoldPolicy
		class      protocolcore.ProviderErrorClass
		status     int
		errorType  string
		message    string
		retryAfter string
		noRetry    bool
	}{
		{
			name:      "rate limited with retry-after",
			responses: []*http.Response{rejection(429, openAIError("rate_limit_exceeded", secret, ""), "Retry-After", "7")},
			class:     protocolcore.ProviderErrorRateLimited, status: 429, errorType: "rate_limit_error",
			message: "The upstream service is rate limiting this account.", retryAfter: "7",
		},
		{
			name: "context window known, input clamped above it",
			responses: []*http.Response{rejection(400, openAIError("context_length_exceeded",
				"This model's maximum context length is 1000 tokens. "+secret, "input"))},
			class: protocolcore.ProviderErrorContextWindow, status: 400, errorType: "invalid_request_error",
			message: "prompt is too long: 1001 tokens > 1000 maximum", noRetry: true,
		},
		{
			name:      "context window unknown",
			responses: []*http.Response{rejection(400, openAIError("context_length_exceeded", secret, "input"))},
			class:     protocolcore.ProviderErrorContextWindow, status: 400, errorType: "invalid_request_error",
			message: "capability_rejected: prompt_too_long", noRetry: true,
		},
		{
			name:      "invalid encrypted content",
			responses: []*http.Response{rejection(400, openAIError("invalid_encrypted_content", secret, "input[1].encrypted_content"))},
			class:     protocolcore.ProviderErrorReasoningState, status: 400, errorType: "invalid_request_error",
			message: "capability_rejected: thinking_signature", noRetry: true,
		},
		{
			name:      "image",
			responses: []*http.Response{rejection(400, openAIError("invalid_image", secret, "input[0].content[1].image_url"))},
			class:     protocolcore.ProviderErrorImage, status: 400, errorType: "invalid_request_error",
			message: "capability_rejected: image_block", noRetry: true,
		},
		{
			name:      "authentication",
			responses: []*http.Response{rejection(401, `{"error":{"message":"Incorrect API key `+secret+`","type":"invalid_request_error","param":null,"code":"invalid_api_key"}}`)},
			class:     protocolcore.ProviderErrorAuthentication, status: 401, errorType: "authentication_error",
			message: "The upstream account's credential was rejected (invalid_api_key). Update the account in ViberMate \u2192 Accounts.",
			noRetry: true,
		},
		{
			name:      "model not found",
			responses: []*http.Response{rejection(404, `{"error":{"message":"The model `+secret+` does not exist","type":"invalid_request_error","param":null,"code":"model_not_found"}}`)},
			class:     protocolcore.ProviderErrorNotFound, status: 404, errorType: "not_found_error",
			message: "This route sends claude-client-alias to an upstream model the account cannot use (model_not_found). " +
				"Change the route's model mapping in ViberMate.",
			noRetry: true,
		},
		{
			name:      "quota",
			responses: []*http.Response{rejection(429, `{"error":{"message":"`+secret+`","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`)},
			class:     protocolcore.ProviderErrorQuotaExhausted, status: 429, errorType: "rate_limit_error",
			message: "The OpenAI account has no remaining credit or quota (insufficient_quota).", noRetry: true,
		},
		{
			name: "503 retried once, then 529",
			responses: []*http.Response{
				rejection(503, `{"error":{"message":"`+secret+`","type":"server_error","param":null,"code":"server_is_overloaded"}}`),
				rejection(529, `{"error":{"message":"`+secret+`","type":"server_error","param":null,"code":null}}`),
			},
			hold:  HoldPolicy{MaxTransportResends: 1, MaxDuration: time.Second, AllowResendAfterProviderResponse: true},
			class: protocolcore.ProviderErrorOverloaded, status: 529, errorType: "overloaded_error",
			message: "The upstream service is overloaded.",
		},
		// A gateway's bare rejection: no readable error object, so the class
		// comes from the status and headers alone.
		{
			name:      "rate limited with an empty body",
			responses: []*http.Response{rejection(429, "", "Retry-After", "7")},
			class:     protocolcore.ProviderErrorRateLimited, status: 429, errorType: "rate_limit_error",
			message: "The upstream service is rate limiting this account.", retryAfter: "7",
		},
		{
			name: "rate limited with an oversized body",
			responses: []*http.Response{rejection(429, `{"error":{"code":"insufficient_quota","message":"`+
				strings.Repeat("x", maxProviderErrorBytes)+`"}}`, "Retry-After", "7")},
			class: protocolcore.ProviderErrorRateLimited, status: 429, errorType: "rate_limit_error",
			message: "The upstream service is rate limiting this account.", retryAfter: "7",
		},
		{
			name: "rate limited with an unsupported encoding",
			responses: []*http.Response{rejection(429, openAIError("insufficient_quota", secret, ""),
				"Retry-After", "7", "Content-Encoding", "br")},
			class: protocolcore.ProviderErrorRateLimited, status: 429, errorType: "rate_limit_error",
			message: "The upstream service is rate limiting this account.", retryAfter: "7",
		},
		{
			name:      "policy",
			responses: []*http.Response{rejection(400, `{"error":{"message":"`+secret+`","type":"invalid_request_error","param":null,"code":"cyber_policy"}}`)},
			class:     protocolcore.ProviderErrorPolicy, status: 400, errorType: "policy_blocked",
			message: "OpenAI declined this request under its usage policies (cyber_policy).", noRetry: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			results := make([]providerResult, len(test.responses))
			for index, response := range test.responses {
				results[index] = providerResult{response: response}
			}
			provider := &providerDouble{results: results}
			pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, approvedDecisions(),
				func(options *Options) {
					if test.hold.MaxDuration != 0 {
						options.Hold = test.hold
					}
				})
			turn := runTranslated(t, pipeline, translatedPlan(t, translatedPrimary), "exchange-rejected", claudeBody(claudeFirstTurn))
			var failure *Failure
			if !errors.As(turn.err, &failure) || failure.Code != ReasonProviderStatusRejected {
				t.Fatalf("err = %v", turn.err)
			}
			if provider.callCount() != len(test.responses) {
				t.Fatalf("provider calls = %d, want %d", provider.callCount(), len(test.responses))
			}
			if failure.Provider == nil || failure.Provider.Class != test.class {
				t.Fatalf("class = %+v, want %s", failure.Provider, test.class)
			}
			native := failure.NativeError
			if native.ForDialect(protocolspec.DialectOpenAIResponses) != nil {
				t.Fatal("the provider's own error would reach the Anthropic client")
			}
			if got := native.ClientStatus(protocolspec.DialectAnthropicMessages); got != test.status {
				t.Fatalf("client status = %d, want %d", got, test.status)
			}
			var rendered renderedAnthropicError
			payload := native.ForDialect(protocolspec.DialectAnthropicMessages)
			if err := json.Unmarshal(payload, &rendered.Error); err != nil {
				t.Fatalf("rendered error = %s: %v", payload, err)
			}
			if rendered.Error.Type != test.errorType || rendered.Error.Message != test.message {
				t.Fatalf("rendered error = %s", payload)
			}
			headers := native.HeadersForDialect(protocolspec.DialectAnthropicMessages)
			if headers.Get("Retry-After") != test.retryAfter || (headers.Get("X-Should-Retry") == "false") != test.noRetry {
				t.Fatalf("rendered headers = %v", headers)
			}
			if test.class == protocolcore.ProviderErrorPolicy {
				body := native.HTTPBodyForDialect(protocolspec.DialectAnthropicMessages)
				if err := json.Unmarshal(body, &rendered); err != nil || rendered.Type != "error" ||
					rendered.RequestID != "exchange-rejected" || rendered.Error.Type != "policy_blocked" {
					t.Fatalf("policy body = %s", body)
				}
			}
			everything := string(payload) + fmt.Sprint(headers) + string(native.HTTPBodyForDialect(protocolspec.DialectAnthropicMessages)) +
				fmt.Sprintf("%+v", *failure.Provider) + turn.err.Error()
			if strings.Contains(everything, "secret") || strings.Contains(everything, "4242") {
				t.Fatalf("upstream text reached the client or the failure: %s", everything)
			}
		})
	}
}

// A non-composed cross-dialect path (Anthropic to Chat) keeps the generic
// envelope: no native error, no class.
func TestChatRejectionKeepsTheGenericEnvelope(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		destination: environment.DestinationKindUpstream, providerOrigin: "https://provider.example/v1",
		backend: protocolspec.DialectOpenAIChat, modelMode: environment.ModelModeMap, mappedModel: "gpt-provider",
		accounts: []testAccount{translatedPrimary}, preferred: translatedPrimary.id,
	})
	provider := &providerDouble{results: []providerResult{{response: rejection(400,
		openAIError("context_length_exceeded", "This model's maximum context length is 1000 tokens.", "messages"))}}}
	pipeline := newTestPipeline(t, newAccountAuthority(t, translatedPrimary), provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	_, err := pipeline.Execute(t.Context(), mustClientRequest(t, "exchange-chat", plan, claudeBody(claudeFirstTurn)), &downstreamRecorder{})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != ReasonProviderStatusRejected || failure.Provider != nil ||
		failure.NativeError.ForDialect(protocolspec.DialectAnthropicMessages) != nil ||
		failure.NativeError.ClientStatus(protocolspec.DialectAnthropicMessages) != 0 ||
		failure.ProviderErrorCode != "context_length_exceeded" {
		t.Fatalf("err = %v (%+v)", err, failure)
	}
}

func streamFailedEvent(code string) string {
	return `{"type":"response.failed","sequence_number":6,"response":{"id":"resp_1","object":"response","created_at":1700000000,` +
		`"status":"failed","model":"` + translatedModel + `","output":[],"error":{"code":"` + code +
		`","message":"secret upstream text for rs_1"}}}`
}

// A response.failed after the stream began reaches Claude Code as an in-band
// Anthropic error event. When it is invalid reasoning state, the failed
// request's seals enter the rejected-seal memory, and the next turn drops
// them instead of failing again (design 8.6).
func TestTranslatedInBandReasoningFailureDropsTheSealsOnTheNextTurn(t *testing.T) {
	plan := translatedPlan(t, translatedPrimary)
	message, _ := firstTurn(t, plan, translatedPrimary)
	next := replayedTurn(t, message)
	textBeforeFailure := textTurnEvents("partial")[:4]

	for _, form := range []struct {
		name     string
		response func() *http.Response
	}{
		{"in band", func() *http.Response {
			return responsesStream(t, append(slices.Clone(textBeforeFailure), streamFailedEvent("invalid_encrypted_content")))
		}},
		{"over HTTP", func() *http.Response {
			return rejection(400, openAIError("invalid_encrypted_content", "secret upstream text", "input[1].encrypted_content"))
		}},
	} {
		t.Run(form.name, func(t *testing.T) {
			provider := &providerDouble{results: []providerResult{
				{response: form.response()},
				{response: responsesStream(t, textTurnEvents("a.txt"))},
			}}
			pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, approvedDecisions())
			failed := runTranslated(t, pipeline, plan, "exchange-failed", next)
			var failure *Failure
			if !errors.As(failed.err, &failure) || failure.Provider == nil ||
				failure.Provider.Class != protocolcore.ProviderErrorReasoningState {
				t.Fatalf("err = %v", failed.err)
			}
			if form.name == "in band" {
				aborts := failed.downstream.abortsSnapshot()
				if len(aborts) != 1 {
					t.Fatalf("aborts = %d", len(aborts))
				}
				name, data := aborts[0].NativeError.StreamEvent(protocolspec.DialectAnthropicMessages)
				if name != "error" || !bytes.Contains(data, []byte(`"capability_rejected: thinking_signature"`)) ||
					bytes.Contains(data, []byte("secret")) {
					t.Fatalf("in-band error = %s %s", name, data)
				}
				if !bytes.Contains(failed.downstream.bytesSnapshot(), []byte("partial")) {
					t.Fatal("text streamed before the failure was lost")
				}
			}
			if !bytes.Contains(provider.requestsSnapshot()[0].Body(), []byte(`"encrypted_content":"ENC-1"`)) {
				t.Fatal("the failed request did not replay the seal")
			}
			if pipeline.reasoningRejections.size() != 1 {
				t.Fatalf("rejected seals = %d, want 1", pipeline.reasoningRejections.size())
			}

			healed := runTranslated(t, pipeline, plan, "exchange-healed", next)
			if healed.err != nil {
				t.Fatalf("the next turn failed again: %v", healed.err)
			}
			if bytes.Contains(provider.requestsSnapshot()[1].Body(), []byte(`"type":"reasoning"`)) ||
				!slices.Contains(noticeCodes(healed.result.Translation), string(protocolcore.NoticeReasoningNotReplayed)) {
				t.Fatalf("the rejected seal was replayed: %s", provider.requestsSnapshot()[1].Body())
			}
		})
	}
}

// Another in-band class renders its own Anthropic error, and records nothing.
func TestTranslatedInBandOverloadIsAnAnthropicErrorEvent(t *testing.T) {
	plan := translatedPlan(t, translatedPrimary)
	message, _ := firstTurn(t, plan, translatedPrimary)
	provider := &providerDouble{results: []providerResult{{
		response: responsesStream(t, append(slices.Clone(textTurnEvents("partial")[:4]), streamFailedEvent("server_is_overloaded"))),
	}}}
	pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, approvedDecisions())
	turn := runTranslated(t, pipeline, plan, "exchange-overloaded", replayedTurn(t, message))
	var failure *Failure
	if !errors.As(turn.err, &failure) || failure.Provider == nil || failure.Provider.Class != protocolcore.ProviderErrorOverloaded {
		t.Fatalf("err = %v", turn.err)
	}
	aborts := turn.downstream.abortsSnapshot()
	if len(aborts) != 1 {
		t.Fatalf("aborts = %d", len(aborts))
	}
	if name, data := aborts[0].NativeError.StreamEvent(protocolspec.DialectAnthropicMessages); name != "error" ||
		!bytes.Contains(data, []byte(`"type":"overloaded_error"`)) {
		t.Fatalf("in-band error = %s %s", name, data)
	}
	if pipeline.reasoningRejections.size() != 0 {
		t.Fatal("an overload was remembered as rejected reasoning")
	}
}

// An attachment the provider side cannot carry (a provider file reference)
// is refused right after decode with the capability token Claude Code strips
// attachments on, and nothing is sent. Without body admission the refusal
// still runs where the request is decoded.
func TestTranslatedProviderAdmissionRefusesBeforeSending(t *testing.T) {
	body := claudeBody(`[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"file_123"}},` +
		`{"type":"text","text":"what is this"}]}]`)
	plan := translatedPlan(t, translatedPrimary)
	check := func(t *testing.T, err error, provider *providerDouble) {
		t.Helper()
		var failure *Failure
		if !errors.As(err, &failure) || failure.Code != ReasonUnsupportedClientInput || provider.callCount() != 0 {
			t.Fatalf("err=%v provider calls=%d", err, provider.callCount())
		}
		if failure.Provider == nil || failure.Provider.Class != protocolcore.ProviderErrorImage {
			t.Fatalf("class = %+v", failure.Provider)
		}
		payload := failure.NativeError.ForDialect(protocolspec.DialectAnthropicMessages)
		if !bytes.Contains(payload, []byte(`"message":"capability_rejected: image_block"`)) ||
			failure.NativeError.ClientStatus(protocolspec.DialectAnthropicMessages) != http.StatusBadRequest {
			t.Fatalf("rendered refusal = %s", payload)
		}
	}
	t.Run("with body admission", func(t *testing.T) {
		provider := &providerDouble{}
		authority := newAccountAuthority(t, translatedPrimary)
		pipeline := newTranslatedPipeline(t, authority, provider, approvedDecisions())
		turn := runTranslated(t, pipeline, plan, "exchange-admission", body)
		check(t, turn.err, provider)
		if len(authority.snapshot()) != 0 {
			t.Fatal("a refused request leased an account")
		}
	})
	t.Run("without body admission", func(t *testing.T) {
		provider := &providerDouble{}
		pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, approvedDecisions(),
			func(options *Options) { options.BodyAdmission = nil })
		_, err := pipeline.Execute(t.Context(), mustClientRequest(t, "exchange-admission-plain", plan, body), &downstreamRecorder{})
		check(t, err, provider)
	})
}

// The stage-1 compliance boundary: the compiler refuses a translated route to
// a ChatGPT plan, and the exchange refuses one a restored revision still
// holds, before any account lease or send (design 13.1).
func TestTranslatedChatGPTRouteIsRefusedAtCompileAndInTheExchange(t *testing.T) {
	for _, test := range []struct {
		name, origin, realm string
	}{
		{"Codex backend", "https://chatgpt.com/backend-api/codex", ""},
		{"chatgpt.com root", "https://chatgpt.com", ""},
		{"other base path", "https://chatgpt.com/other", ""},
		{"subdomain", "https://api.chatgpt.com/v1", ""},
		{"ChatGPT realm on another host", "https://relay.example/v1", "openai.chatgpt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configure := func(options *testPlanOptions) {
				options.providerOrigin, options.realm = test.origin, test.realm
			}
			// A candidate is refused at compile; a published revision restores.
			options := translatedPlanOptions(translatedPrimary, configure)
			aggregate, catalog, _, _ := testEnvironment(t, options)
			compiler := mustEnvironmentCompiler(t, catalog)
			if _, err := compiler.Compile(aggregate); !errors.Is(err, environment.ErrTranslatedChatGPTRoute) ||
				!errors.Is(err, environment.ErrInvalidEnvironment) {
				t.Fatalf("Compile() error = %v", err)
			}
			restored := translatedPlan(t, translatedPrimary, configure, func(options *testPlanOptions) { options.restore = true })
			authority := newAccountAuthority(t, translatedPrimary)
			provider := &providerDouble{}
			pipeline := newTranslatedPipeline(t, authority, provider, approvedDecisions())
			turn := runTranslated(t, pipeline, restored, "exchange-chatgpt", claudeBody(claudeFirstTurn))
			if ReasonOf(turn.err) != ReasonEnvironmentPlanInvalid || !errors.Is(turn.err, errTranslatedChatGPTTarget) {
				t.Fatalf("err = %v", turn.err)
			}
			if provider.callCount() != 0 || len(authority.snapshot()) != 0 {
				t.Fatalf("provider calls=%d leases=%d", provider.callCount(), len(authority.snapshot()))
			}
			// Without body admission the check after Select still refuses.
			plain := newTranslatedPipeline(t, authority, provider, approvedDecisions(),
				func(options *Options) { options.BodyAdmission = nil })
			if _, err := plain.Execute(t.Context(), mustClientRequest(t, "exchange-chatgpt-plain", restored, claudeBody(claudeFirstTurn)), &downstreamRecorder{}); !errors.Is(err, errTranslatedChatGPTTarget) || provider.callCount() != 0 {
				t.Fatalf("err = %v", err)
			}
			// The dry run follows the same decision path.
			request := mustClientRequest(t, "exchange-chatgpt-dry", restored, claudeBody(claudeFirstTurn))
			if _, err := plain.DryRun(t.Context(), request); !errors.Is(err, errTranslatedChatGPTTarget) {
				t.Fatalf("dry run err = %v", err)
			}
		})
	}
}

// A path built with New is never refused by the exchange check, so a
// restored Anthropic to Chat route on chatgpt.com keeps serving.
func TestTranslatedTargetCheckSparesPathsBuiltWithNew(t *testing.T) {
	chat, err := anthropicchat.NewProtocolPath(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	selection := frozenSelection{chatGPTPlan: true}
	if err := checkTranslatedTarget(selection, chat); err != nil {
		t.Fatalf("a Chat path was refused: %v", err)
	}
	if err := checkTranslatedTarget(selection, translatedProtocolPath(t)); !errors.Is(err, errTranslatedChatGPTTarget) {
		t.Fatalf("a composed path was not refused: %v", err)
	}
	if err := checkTranslatedTarget(frozenSelection{}, translatedProtocolPath(t)); err != nil {
		t.Fatalf("a composed path to the API was refused: %v", err)
	}
}

// A dry run encodes with the same context a run uses. On a compatible
// service (no cyber finalizer), the dry run's body is byte for byte the
// run's, the replayed reasoning included, which only the route's binding
// allows.
func TestTranslatedDryRunEncodesWithTheRunsContext(t *testing.T) {
	plan := translatedPlan(t, translatedPrimary, func(options *testPlanOptions) {
		options.providerOrigin = "https://compatible.example/v1"
	})
	message, _ := firstTurn(t, plan, translatedPrimary)
	next := replayedTurn(t, message)
	provider := &providerDouble{results: []providerResult{{response: responsesStream(t, textTurnEvents("a.txt"))}}}
	pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, approvedDecisions())
	if turn := runTranslated(t, pipeline, plan, "exchange-run", next); turn.err != nil {
		t.Fatal(turn.err)
	}
	sent := provider.requestsSnapshot()[0]
	if !bytes.Contains(sent.Body(), []byte(`"encrypted_content":"ENC-1"`)) {
		t.Fatal("the run did not replay the seal")
	}
	lease, err := pipeline.bodyAdmission.AcquirePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	result, err := pipeline.DryRun(t.Context(), mustClientRequestWithOptions(t, "exchange-dry", plan, next,
		WithBodyLease(lease), WithOriginalHeaders(claudeClientHeaders)))
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountID != translatedPrimary.id || result.bodyDigest != sha256.Sum256(sent.Body()) {
		t.Fatalf("the dry run encoded another body than the run: %+v", result)
	}
}

// The rejected-seal memory is bounded, evicts the least recently used
// digest first, counts a lookup as a use and is safe for concurrent use.
func TestReasoningRejectionsAreABoundedLRU(t *testing.T) {
	digest := func(value int) [32]byte {
		var result [32]byte
		copy(result[:], strconv.Itoa(value))
		return result
	}
	memory := newReasoningRejections(3)
	for value := range 3 {
		memory.record(digest(value))
	}
	if !memory.contains(digest(0)) { // 0 becomes the most recent
		t.Fatal("a recorded digest is missing")
	}
	memory.record(digest(3)) // evicts 1, the least recently used
	if memory.contains(digest(1)) || !memory.contains(digest(0)) || !memory.contains(digest(2)) ||
		!memory.contains(digest(3)) || memory.size() != 3 {
		t.Fatalf("eviction order is wrong: size %d", memory.size())
	}
	memory.record(digest(3))
	if memory.size() != 3 {
		t.Fatal("a repeated digest took a second slot")
	}

	var nilMemory *reasoningRejections
	if nilMemory.contains(digest(0)) {
		t.Fatal("a nil memory holds a digest")
	}
	nilMemory.record(digest(0))

	shared := newReasoningRejections(maxReasoningRejections)
	var group sync.WaitGroup
	for worker := range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			for value := range 1000 {
				shared.record(digest(worker*1000 + value))
				_ = shared.contains(digest(value))
			}
		}()
	}
	group.Wait()
	if shared.size() != maxReasoningRejections {
		t.Fatalf("size = %d, want the bound %d", shared.size(), maxReasoningRejections)
	}
}

// Only OpenAI Responses seals are remembered: an Anthropic signature can
// never be replayed to a Responses upstream, so it cannot have caused the
// rejection.
func TestReasoningRejectionsRecordOnlyResponsesSeals(t *testing.T) {
	responses := protocolcore.ReasoningSeal{Format: protocolcore.SealResponsesEncrypted, ItemID: "rs_1", Model: "m", Binding: "b", Opaque: "ENC"}
	anthropicSeal := protocolcore.ReasoningSeal{Format: protocolcore.SealAnthropicSignature, Opaque: "sig"}
	request := protocolcore.Request{Messages: []protocolcore.Message{{
		Role: protocolcore.RoleAssistant,
		Blocks: []protocolcore.ContentBlock{
			{Kind: protocolcore.BlockReasoning, Reasoning: &protocolcore.Reasoning{Seal: responses}},
			{Kind: protocolcore.BlockReasoning, Reasoning: &protocolcore.Reasoning{Seal: anthropicSeal}},
			{Kind: protocolcore.BlockText, Text: "after"},
		},
	}}}
	memory := newReasoningRejections(maxReasoningRejections)
	memory.recordRequest(request)
	if memory.size() != 1 || !memory.contains(protocolcore.SealDigest(responses)) ||
		memory.contains(protocolcore.SealDigest(anthropicSeal)) {
		t.Fatalf("recorded %d seals", memory.size())
	}
	context := protocolpath.ProviderContext{RejectedSeal: memory.contains}
	if !context.SealRejected(responses) {
		t.Fatal("the provider context does not read the memory")
	}
}

// The tool gate sees a translated turn as it sees a native one (design 15.4):
// the intents come from the Responses function_call under the client's tool
// name, the request's own definitions travel with them, and the capture's
// local workspace evidence gives the exact structured-tools basis. The gate
// itself is exercised on these intents in internal/toolpolicy, which this
// package cannot import.
func TestTranslatedToolDecisionCarriesWorkspaceEvidence(t *testing.T) {
	root := t.TempDir()
	adapter, ok := clientadapter.BuiltInCatalog().ExpectedEvidence("claude-code", "2.1.220")
	if !ok {
		t.Fatal("built-in Claude evidence is unavailable")
	}
	scope, err := workspaceidentity.NewScope(
		workspaceidentity.MachineID(base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("m", 32)))),
		workspaceidentity.WorkspaceID(base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("w", 32)))),
		"project", workspaceidentity.EvidenceLocalLauncher, 1, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := captureadmission.NewManagedRun(captureadmission.ManagedRunEvidence{
		CaptureRunID: "run-translated-workspace", SourceLabel: "claude",
		WorkspaceRoot: root, Workspace: scope, Adapter: &adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	arguments := `{"file_path":` + strconv.Quote(root+"/notes.txt") + `}`
	call := `{"id":"fc_read","type":"function_call","status":"completed","arguments":` + strconv.Quote(arguments) + `,"call_id":"call_read","name":"Read"}`
	events := []string{
		`{"type":"response.created","sequence_number":0,"response":` + responsesObject("in_progress", "", "null") + `}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_read","type":"function_call","status":"in_progress","arguments":"","call_id":"call_read","name":"Read"}}`,
		`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":` + call + `}`,
		`{"type":"response.completed","sequence_number":3,"response":` + responsesObject("completed", call, responsesUsage) + `}`,
	}
	body := []byte(`{"model":"claude-client-alias","max_tokens":1024,"stream":true,` +
		`"tools":[{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}},` +
		`{"name":"Bash","description":"Run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],` +
		`"messages":[{"role":"user","content":"read notes"}]}`)
	plan := translatedPlan(t, translatedPrimary)
	decisions := &decisionDouble{decision: ToolDecision{Outcome: ToolDecisionRejected, ReasonCode: "user_rejected"}}
	provider := &providerDouble{results: []providerResult{{response: responsesStream(t, events)}}}
	pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, decisions)
	lease, err := pipeline.bodyAdmission.AcquirePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	downstream := &downstreamRecorder{}
	_, err = pipeline.Execute(t.Context(), mustClientRequestWithOptions(t, "exchange-translated-workspace", plan, body,
		WithBodyLease(lease), WithIngressCorrelation(admission, "connection-translated-workspace")), downstream)
	if ReasonOf(err) != ReasonToolDecisionRejected || decisions.callCount() != 1 {
		t.Fatalf("err=%v decisions=%d", err, decisions.callCount())
	}
	decision := decisions.lastRequest()
	if got, ok := decision.Context().WorkspaceRoot(); !ok || got != root {
		t.Fatalf("workspace root = %q, %v", got, ok)
	}
	if decision.Context().StructuredWorkspaceToolsBasis() != clientadapter.ProtectionExactRelease {
		t.Fatalf("structured basis = %s", decision.Context().StructuredWorkspaceToolsBasis())
	}
	tools := decision.Context().Tools()
	if len(tools) != 2 || tools[0].Name != "Read" || tools[1].Name != "Bash" {
		t.Fatalf("decision tools = %+v", tools)
	}
	intents := decision.ToolIntents()
	if len(intents) != 1 || intents[0].Call.Name != "Read" || intents[0].Call.Key.Source() != "openai-responses-call" ||
		intents[0].Call.Key.WireID() != "call_read" || string(intents[0].Call.Arguments.Bytes()) != arguments {
		t.Fatalf("decision intents = %+v", intents)
	}
	if written := downstream.bytesSnapshot(); bytes.Contains(written, []byte("tool_use")) {
		t.Fatalf("a rejected call reached the client: %s", written)
	}
}
