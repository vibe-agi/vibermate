package exchange

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

// A Stream=false client on a composed path (Claude Code's non-streaming
// fallback, or another SDK) is answered by aggregating the event stream the
// path always asks its upstream for (design 9): one Anthropic JSON body with
// nothing before it, the same answer a JSON upstream gives.

// unaryRequest is a Claude Code request body as a Stream=false request.
func unaryRequest(t *testing.T, body []byte) []byte {
	t.Helper()
	unary := bytes.Replace(body, []byte(`"stream":true`), []byte(`"stream":false`), 1)
	if bytes.Equal(unary, body) {
		t.Fatal("the request body does not stream")
	}
	return unary
}

// unaryMessage checks that the client got exactly one JSON response, with no
// keepalive and no in-band failure, and reads it as anthropic-sdk-go hands it
// to Claude Code.
func unaryMessage(t *testing.T, turn translatedTurn) anthropic.Message {
	t.Helper()
	if turn.err != nil || turn.result.Outcome != AttemptSucceeded {
		t.Fatalf("outcome=%s err=%v", turn.result.Outcome, turn.err)
	}
	envelopes := turn.downstream.envelopesSnapshot()
	if len(envelopes) != 1 || envelopes[0].Mode() != ResponseModeJSON || envelopes[0].StatusCode() != http.StatusOK ||
		envelopes[0].Headers().Get("Content-Type") != "application/json" {
		t.Fatalf("envelopes = %+v", envelopes)
	}
	if count := turn.downstream.keepaliveCount(); count != 0 {
		t.Fatalf("a Stream=false client got %d keepalives", count)
	}
	if aborts := turn.downstream.abortsSnapshot(); len(aborts) != 0 {
		t.Fatalf("a Stream=false client got in-band failures: %+v", aborts)
	}
	body := turn.downstream.bytesSnapshot()
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("event:")) || bytes.Contains(body, []byte("\n\n")) {
		t.Fatalf("the unary body is not one JSON value: %s", body)
	}
	var message anthropic.Message
	if err := json.Unmarshal(body, &message); err != nil {
		t.Fatalf("anthropic-sdk-go cannot read %s: %v", body, err)
	}
	return message
}

// checkToolTurnMessage holds the unary answer to toolTurnEvents to every
// part design 9 names: thinking with the route's seal, text, the tool call,
// the stop reason and the usage of design 7.6.
func checkToolTurnMessage(t *testing.T, message anthropic.Message, body []byte, display string) {
	t.Helper()
	if message.ID != "resp_1" || message.Type != "message" || message.Role != "assistant" ||
		message.Model != "claude-client-alias" || message.StopReason != anthropic.StopReasonToolUse {
		t.Fatalf("message = %s", body)
	}
	if len(message.Content) != 3 {
		t.Fatalf("content = %s", body)
	}
	thinking, text, call := message.Content[0], message.Content[1], message.Content[2]
	wantThinking := "Plan first."
	if display == "omitted" {
		wantThinking = ""
	}
	if thinking.Type != "thinking" || thinking.Thinking != wantThinking {
		t.Fatalf("thinking = %s", body)
	}
	seal, _, ok := protocolcore.ParseReasoningSeal(thinking.Signature)
	binding := protocolpath.NewReasoningBinding("target.primary", translatedPrimary.id)
	if !ok || seal.ItemID != "rs_1" || seal.Opaque != "ENC-1" || seal.Model != translatedModel || seal.Binding != binding {
		t.Fatalf("seal = %+v (ok=%v), want binding %s", seal, ok, binding)
	}
	if text.Type != "text" || text.Text != "Let me check." {
		t.Fatalf("text = %s", body)
	}
	if call.Type != "tool_use" || call.ID != "call_1" || call.Name != "Bash" || string(call.Input) != `{"command":"ls"}` {
		t.Fatalf("tool_use = %s", body)
	}
	// 120 input tokens of which 100 were cached: Anthropic's input_tokens
	// excludes the cached ones. Cache writes were not reported, so the field
	// is absent.
	usage := message.Usage
	if usage.InputTokens != 20 || usage.CacheReadInputTokens != 100 || usage.OutputTokens != 40 ||
		usage.JSON.CacheCreationInputTokens.Valid() {
		t.Fatalf("usage = %s", body)
	}
}

func toolTurnJSON() string {
	reasoning := `{"id":"rs_1","type":"reasoning","encrypted_content":"ENC-1","summary":[{"type":"summary_text","text":"Plan first."}]}`
	message := `{"id":"msg_1","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"text":"Let me check."}],"role":"assistant"}`
	call := `{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"command\":\"ls\"}","call_id":"call_1","name":"Bash"}`
	return responsesObject("completed", reasoning+","+message+","+call, responsesUsage)
}

// The upstream request streams whatever the client asked for, and an SSE
// answer becomes one Anthropic message with thinking, text, tool_use and
// usage, after the same tool decision a JSON answer takes. A JSON upstream
// gives the same body byte for byte, its seal bound to the route too.
func TestTranslatedUnaryClientIsAnsweredFromTheUpstreamStream(t *testing.T) {
	plan := translatedPlan(t, translatedPrimary)
	var bodies [][]byte
	for _, upstream := range []struct {
		name     string
		response func() *http.Response
	}{
		{"SSE upstream", func() *http.Response { return responsesStream(t, toolTurnEvents()) }},
		{"JSON upstream", func() *http.Response { return jsonResponse(http.StatusOK, []byte(toolTurnJSON())) }},
	} {
		t.Run(upstream.name, func(t *testing.T) {
			provider := &providerDouble{results: []providerResult{{response: upstream.response()}}}
			decisions := approvedDecisions()
			pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, decisions,
				func(options *Options) {
					options.Stream = StreamBudgets{KeepaliveInterval: time.Millisecond, ProviderProgressTimeout: time.Minute}
				})
			turn := runTranslated(t, pipeline, plan, "exchange-unary", unaryRequest(t, claudeBody(claudeFirstTurn)))
			message := unaryMessage(t, turn)
			body := turn.downstream.bytesSnapshot()
			checkToolTurnMessage(t, message, body, "summarized")
			bodies = append(bodies, body)

			sent := provider.requestsSnapshot()
			if len(sent) != 1 || !decodeResponsesBody(t, sent[0]).Stream || sent[0].Headers().Get("Accept") != "text/event-stream" {
				t.Fatalf("the upstream request does not stream: %s", sent[0].Body())
			}
			if decisions.callCount() != 1 {
				t.Fatalf("tool decisions = %d", decisions.callCount())
			}
			intents := decisions.lastRequest().ToolIntents()
			if len(intents) != 1 || intents[0].Call.Name != "Bash" || intents[0].Call.Key.Source() != "openai-responses-call" ||
				intents[0].ResponseID != "resp_1" {
				t.Fatalf("decision intents = %+v", intents)
			}
			if ledger := turn.result.Ledger; ledger.DownstreamSemanticWrites != 1 || ledger.DownstreamHoldEnvelope ||
				ledger.DownstreamSemanticBytes != int64(len(body)) {
				t.Fatalf("ledger = %+v", ledger)
			}
			// The unary encoder declares the reasoning tokens it has no field for.
			if !slices.Contains(noticeCodes(turn.result.Translation), string(protocolcore.NoticeReasoningUsageNotForwarded)) {
				t.Fatalf("notices = %v", noticeCodes(turn.result.Translation))
			}
		})
	}
	if len(bodies) == 2 && !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("an SSE and a JSON upstream answered differently:\n%s\n%s", bodies[0], bodies[1])
	}
}

// With display omitted (every claude -p request) the thinking block is
// empty but keeps its seal, as on a stream.
func TestTranslatedUnaryOmittedThinkingKeepsItsSeal(t *testing.T) {
	body := bytes.Replace(unaryRequest(t, claudeBody(claudeFirstTurn)),
		[]byte(`"display":"summarized"`), []byte(`"display":"omitted"`), 1)
	provider := &providerDouble{results: []providerResult{{response: responsesStream(t, toolTurnEvents())}}}
	pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, approvedDecisions())
	turn := runTranslated(t, pipeline, translatedPlan(t, translatedPrimary), "exchange-unary-omitted", body)
	checkToolTurnMessage(t, unaryMessage(t, turn), turn.downstream.bytesSnapshot(), "omitted")
}

// A rejected call never reaches the client: no envelope and no body.
func TestTranslatedUnaryRejectedCallReachesNothing(t *testing.T) {
	decisions := &decisionDouble{decision: ToolDecision{Outcome: ToolDecisionRejected, ReasonCode: "user_rejected"}}
	provider := &providerDouble{results: []providerResult{{response: responsesStream(t, toolTurnEvents())}}}
	pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, decisions)
	turn := runTranslated(t, pipeline, translatedPlan(t, translatedPrimary), "exchange-unary-reject",
		unaryRequest(t, claudeBody(claudeFirstTurn)))
	if ReasonOf(turn.err) != ReasonToolDecisionRejected || turn.result.Outcome != AttemptAborted {
		t.Fatalf("outcome=%s err=%v", turn.result.Outcome, turn.err)
	}
	if len(turn.downstream.envelopesSnapshot()) != 0 || len(turn.downstream.bytesSnapshot()) != 0 ||
		len(turn.downstream.abortsSnapshot()) != 0 {
		t.Fatalf("a rejected call reached the client: %s", turn.downstream.bytesSnapshot())
	}
	if intents := decisions.lastRequest().ToolIntents(); len(intents) != 1 || intents[0].Call.Name != "Bash" {
		t.Fatalf("decision intents = %+v", intents)
	}
}

// A response script sees the provider's events, as on a stream, and its
// header change lands on the JSON envelope; its edit of the events reaches
// the unary body.
func TestTranslatedUnaryResponseScriptSeesTheUpstreamEvents(t *testing.T) {
	plan := translatedPlan(t, translatedPrimary, func(options *testPlanOptions) {
		options.transform = messagetransform.Policy{ResponseJavaScript: `
			if (response.streaming && response.eventName !== "") {
				response.headers["x-script-saw"] = "events";
				response.body = response.body.split("Let me check.").join("Script edit.");
			}
		`}
	})
	provider := &providerDouble{results: []providerResult{{response: responsesStream(t, toolTurnEvents())}}}
	pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, approvedDecisions())
	turn := runTranslated(t, pipeline, plan, "exchange-unary-script", unaryRequest(t, claudeBody(claudeFirstTurn)))
	message := unaryMessage(t, turn)
	if envelope := turn.downstream.envelopesSnapshot()[0]; envelope.Headers().Get("X-Script-Saw") != "events" {
		t.Fatalf("envelope headers = %v", envelope.Headers())
	}
	if len(message.Content) != 3 || message.Content[1].Text != "Script edit." {
		t.Fatalf("the script's edit is missing: %s", turn.downstream.bytesSnapshot())
	}
}

// An in-band response.failed during aggregation is an ordinary HTTP error
// for a Stream=false client: nothing was begun, so nothing is aborted, and
// the rendered error carries the status Claude Code's recovery needs. Its
// reasoning state still feeds the rejected-seal memory, and the next turn
// heals.
func TestTranslatedUnaryInBandFailureIsAnHTTPError(t *testing.T) {
	plan := translatedPlan(t, translatedPrimary)
	message, _ := firstTurn(t, plan, translatedPrimary)
	next := unaryRequest(t, replayedTurn(t, message))
	provider := &providerDouble{results: []providerResult{
		{response: responsesStream(t, append(slices.Clone(textTurnEvents("partial")[:4]), streamFailedEvent("invalid_encrypted_content")))},
		{response: responsesStream(t, textTurnEvents("a.txt"))},
	}}
	pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, approvedDecisions())
	failed := runTranslated(t, pipeline, plan, "exchange-unary-failed", next)
	var failure *Failure
	if !errors.As(failed.err, &failure) || failure.Code != ReasonProviderResponseFailed || failure.Provider == nil ||
		failure.Provider.Class != protocolcore.ProviderErrorReasoningState {
		t.Fatalf("err = %v", failed.err)
	}
	native := failure.NativeError
	if native.ClientStatus(protocolspec.DialectAnthropicMessages) != http.StatusBadRequest ||
		!bytes.Contains(native.ForDialect(protocolspec.DialectAnthropicMessages), []byte(`"capability_rejected: thinking_signature"`)) ||
		bytes.Contains(native.ForDialect(protocolspec.DialectAnthropicMessages), []byte("secret")) {
		t.Fatalf("rendered error = %s (status %d)", native.ForDialect(protocolspec.DialectAnthropicMessages),
			native.ClientStatus(protocolspec.DialectAnthropicMessages))
	}
	if len(failed.downstream.envelopesSnapshot()) != 0 || len(failed.downstream.abortsSnapshot()) != 0 ||
		len(failed.downstream.bytesSnapshot()) != 0 {
		t.Fatalf("the failure began a response: %s", failed.downstream.bytesSnapshot())
	}
	if pipeline.reasoningRejections.size() != 1 {
		t.Fatalf("rejected seals = %d, want 1", pipeline.reasoningRejections.size())
	}
	healed := runTranslated(t, pipeline, plan, "exchange-unary-healed", next)
	if got := unaryMessage(t, healed); len(got.Content) != 1 || got.Content[0].Text != "a.txt" {
		t.Fatalf("healed answer = %s", healed.downstream.bytesSnapshot())
	}
	if bytes.Contains(provider.requestsSnapshot()[1].Body(), []byte(`"type":"reasoning"`)) ||
		!slices.Contains(noticeCodes(healed.result.Translation), string(protocolcore.NoticeReasoningNotReplayed)) {
		t.Fatalf("the rejected seal was replayed: %s", provider.requestsSnapshot()[1].Body())
	}
}

// pipedStream is an upstream event stream the test writes as it goes.
func pipedStream() (*http.Response, *io.PipeWriter, *idleBudgetBody) {
	reader, writer := io.Pipe()
	body := &idleBudgetBody{PipeReader: reader, closed: make(chan struct{})}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, writer, body
}

// Aggregation runs under the stream's semantic-idle timer: an upstream that
// goes silent after response.created fails the turn as provider idle, with
// nothing sent to the client and the read pump stopped.
func TestTranslatedUnaryAggregationTimesOutOnUpstreamSilence(t *testing.T) {
	response, writer, body := pipedStream()
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		defer writer.Close()
		_, _ = writer.Write(responsesWire(t, textTurnEvents("never")[0]))
		// Bounded, so a pipeline that reads to EOF fails instead of hanging.
		select {
		case <-body.closed:
		case <-time.After(5 * time.Second):
		}
	}()
	defer body.Close()
	provider := &providerDouble{results: []providerResult{{response: response}}}
	pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, approvedDecisions(),
		func(options *Options) {
			options.Stream = StreamBudgets{KeepaliveInterval: 5 * time.Millisecond, ProviderProgressTimeout: 100 * time.Millisecond}
		})
	started := time.Now()
	turn := runTranslated(t, pipeline, translatedPlan(t, translatedPrimary), "exchange-unary-idle",
		unaryRequest(t, claudeBody(claudeFirstTurn)))
	if !errors.Is(turn.err, ErrProviderSemanticIdle) || ReasonOf(turn.err) != ReasonProviderResponseIdle ||
		turn.result.Outcome == AttemptSucceeded {
		t.Fatalf("outcome=%s err=%v", turn.result.Outcome, turn.err)
	}
	if elapsed := time.Since(started); elapsed < 80*time.Millisecond {
		t.Fatalf("the turn ended after %v, before the idle budget", elapsed)
	}
	if len(turn.downstream.envelopesSnapshot()) != 0 || turn.downstream.keepaliveCount() != 0 ||
		len(turn.downstream.bytesSnapshot()) != 0 || len(turn.downstream.abortsSnapshot()) != 0 {
		t.Fatalf("an idle aggregation reached the client: %s", turn.downstream.bytesSnapshot())
	}
	select {
	case <-producerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the upstream body was not closed")
	}
	if body.active.Load() != 0 {
		t.Fatal("the provider read pump survived Execute")
	}
}

// Upstream progress events keep an aggregation alive past the idle budget,
// as they keep a stream alive: the timer measures upstream silence, not how
// long the answer takes.
func TestTranslatedUnaryAggregationOutlivesTheIdleBudgetWhileTheUpstreamProgresses(t *testing.T) {
	response, writer, body := pipedStream()
	events := textTurnEvents("slow answer")
	go func() {
		defer writer.Close()
		_, _ = writer.Write(responsesWire(t, events[0]))
		for sequence := range 8 {
			time.Sleep(40 * time.Millisecond)
			progress := `{"type":"response.in_progress","sequence_number":` + strconv.Itoa(100+sequence) +
				`,"response":` + responsesObject("in_progress", "", "null") + `}`
			if _, err := writer.Write(responsesWire(t, progress)); err != nil {
				return
			}
		}
		_, _ = writer.Write(responsesWire(t, events[1:]...))
	}()
	defer body.Close()
	provider := &providerDouble{results: []providerResult{{response: response}}}
	pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, approvedDecisions(),
		func(options *Options) {
			options.Stream = StreamBudgets{KeepaliveInterval: 5 * time.Millisecond, ProviderProgressTimeout: 150 * time.Millisecond}
		})
	started := time.Now()
	turn := runTranslated(t, pipeline, translatedPlan(t, translatedPrimary), "exchange-unary-progress",
		unaryRequest(t, claudeBody(claudeFirstTurn)))
	message := unaryMessage(t, turn)
	if elapsed := time.Since(started); elapsed < 150*time.Millisecond {
		t.Fatalf("the answer took %v, shorter than the idle budget it should outlive", elapsed)
	}
	if len(message.Content) != 1 || message.Content[0].Text != "slow answer" || message.StopReason != anthropic.StopReasonEndTurn {
		t.Fatalf("message = %s", turn.downstream.bytesSnapshot())
	}
}

// A composed path accepts an event stream or JSON; any other media type
// fails like any unary content-type mismatch, naming the type it asked for.
func TestTranslatedUnaryRefusesAnotherMediaType(t *testing.T) {
	response := streamResponse(http.StatusOK, bytes.NewReader(responsesWire(t, textTurnEvents("x")...)))
	response.Header.Set("Content-Type", "text/plain")
	provider := &providerDouble{results: []providerResult{{response: response}}}
	pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), provider, approvedDecisions())
	turn := runTranslated(t, pipeline, translatedPlan(t, translatedPrimary), "exchange-unary-media",
		unaryRequest(t, claudeBody(claudeFirstTurn)))
	var failure *Failure
	if !errors.As(turn.err, &failure) || failure.Code != ReasonProviderResponseInvalid ||
		failure.ResponseIssue != ProviderResponseIssueContentType ||
		!strings.Contains(failure.cause.Error(), "is not text/event-stream") {
		t.Fatalf("err = %v", turn.err)
	}
	if len(turn.downstream.envelopesSnapshot()) != 0 {
		t.Fatal("a refused response began")
	}
}

// Paths built with New do not aggregate: a Stream=false client on
// Anthropic to Chat or on Responses to Responses still needs a JSON upstream,
// and an event stream is refused on its content type exactly as before.
func TestUnaryClientsOfPathsBuiltWithNewStillNeedJSON(t *testing.T) {
	account := testAccount{id: "account.primary", revision: 1, epoch: 1}
	for _, test := range []struct {
		name   string
		plan   testPlanOptions
		body   []byte
		stream func(t *testing.T) []byte
		json   []byte
		want   string
	}{
		{
			name: "Anthropic to Chat",
			plan: testPlanOptions{
				destination: environment.DestinationKindUpstream, providerOrigin: "https://provider.example/v1",
				backend: protocolspec.DialectOpenAIChat, modelMode: environment.ModelModeMap, mappedModel: "gpt-provider",
				accounts: []testAccount{account}, preferred: account.id,
			},
			body: completeClientRequest(),
			stream: func(t *testing.T) []byte {
				wire, _ := io.ReadAll(normalProviderStream(t, "gpt-provider"))
				return wire
			},
			json: completeProviderResponse("gpt-provider"),
			want: `"text":"Done."`,
		},
		{
			name: "Responses to Responses",
			plan: testPlanOptions{
				clientProtocol: environment.ClientProtocolOpenAIResponses,
				destination:    environment.DestinationKindUpstream, providerOrigin: "https://provider.example",
				backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
				accounts: []testAccount{account}, preferred: account.id,
			},
			body:   completeResponsesClientRequest(),
			stream: originalResponsesTerminalWire,
			json:   completeResponsesProviderResponse("codex-client-alias"),
			want:   `"text":"Done."`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := mustEnvironmentRequestPlan(t, test.plan)
			provider := &providerDouble{results: []providerResult{
				{response: streamResponse(http.StatusOK, bytes.NewReader(test.stream(t)))},
				{response: jsonResponse(http.StatusOK, test.json)},
			}}
			pipeline := newTestPipeline(t, newAccountAuthority(t, account), provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)

			downstream := &downstreamRecorder{}
			_, err := pipeline.Execute(context.Background(), mustClientRequest(t, "exchange-new-sse", plan, test.body), downstream)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != ReasonProviderResponseInvalid ||
				failure.ResponseIssue != ProviderResponseIssueContentType ||
				!strings.Contains(failure.cause.Error(), "is not application/json") {
				t.Fatalf("an event stream answered a unary client: %v", err)
			}
			if len(downstream.envelopesSnapshot()) != 0 || len(downstream.bytesSnapshot()) != 0 {
				t.Fatalf("a refused event stream reached the client: %s", downstream.bytesSnapshot())
			}

			downstream = &downstreamRecorder{}
			if _, err := pipeline.Execute(context.Background(), mustClientRequest(t, "exchange-new-json", plan, test.body), downstream); err != nil {
				t.Fatalf("a JSON upstream failed: %v", err)
			}
			envelopes := downstream.envelopesSnapshot()
			if len(envelopes) != 1 || envelopes[0].Mode() != ResponseModeJSON ||
				!bytes.Contains(downstream.bytesSnapshot(), []byte(test.want)) {
				t.Fatalf("JSON answer = %s", downstream.bytesSnapshot())
			}
		})
	}
}

// releasingStream is a decoder that releases client bytes for every
// fragment, which a read without a relay must refuse.
type releasingStream struct{}

func (releasingStream) Feed(context.Context, []byte) ([]byte, error) {
	return []byte("event: leaked\ndata: {}\n\n"), nil
}
func (releasingStream) SemanticProgress() uint64 { return 1 }
func (releasingStream) TerminalReceived() bool   { return false }
func (releasingStream) FinishDecoded(context.Context) (protocolpath.PendingTerminal, error) {
	return nil, errors.New("not reached")
}

// An aggregating read never writes downstream: a decoder that releases
// bytes anyway fails the read as a stream state violation, since a unary
// client has no envelope to carry them.
func TestAggregatingReadRefusesReleasedBytes(t *testing.T) {
	pipeline := newTranslatedPipeline(t, newAccountAuthority(t, translatedPrimary), &providerDouble{}, approvedDecisions())
	downstream := &downstreamRecorder{}
	ledger := &CommitLedger{}
	err := pipeline.readProviderStream(t.Context(), ClientRequest{}, releasingStream{},
		io.NopCloser(strings.NewReader("event: x\ndata: {}\n\n")), downstream, ledger, nil, providertransport.Request{}, nil)
	if protocolcore.ReasonOf(err) != protocolcore.ReasonStreamStateViolation {
		t.Fatalf("err = %v", err)
	}
	if len(downstream.bytesSnapshot()) != 0 || len(downstream.envelopesSnapshot()) != 0 || ledger.Snapshot().DownstreamSemanticWrites != 0 {
		t.Fatalf("an aggregating read wrote %q", downstream.bytesSnapshot())
	}
}
