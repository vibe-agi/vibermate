package protocolpath

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// recordingEncoder writes one readable token per call, so a test reads the
// exact client sequence: "S" start, "T0[" open text 0, "t0:x" text, "]0"
// close, "C1:name" a tool call, "R0[" "r0:x" "]R0:<signature>" reasoning,
// "F0[" "f0:x" "]F0" refusal, "E:<stop>" terminal.
type recordingEncoder struct {
	report protocolcore.TranslationReport
}

func (recordingEncoder) Start(start StreamStart) ([]byte, error) {
	return []byte("S(" + start.ResponseID + ") "), nil
}

func (recordingEncoder) StartText(index int) ([]byte, error) {
	return []byte(fmt.Sprintf("T%d[ ", index)), nil
}

func (recordingEncoder) AppendText(index int, text string) ([]byte, error) {
	return []byte(fmt.Sprintf("t%d:%s ", index, text)), nil
}

func (recordingEncoder) StopText(index int) ([]byte, error) {
	return []byte(fmt.Sprintf("]%d ", index)), nil
}

func (recordingEncoder) ToolCall(index int, call protocolcore.ToolCall) ([]byte, error) {
	return []byte(fmt.Sprintf("C%d:%s%s ", index, call.Name, call.Arguments.Bytes())), nil
}

func (recordingEncoder) Terminal(response protocolcore.Response) ([]byte, error) {
	return []byte("E:" + string(response.StopReason)), nil
}

func (encoder recordingEncoder) TranslationReport() protocolcore.TranslationReport {
	return encoder.report
}

type reasoningEncoder struct{ recordingEncoder }

func (reasoningEncoder) StartReasoning(index int) ([]byte, error) {
	return []byte(fmt.Sprintf("R%d[ ", index)), nil
}

func (reasoningEncoder) AppendReasoning(index int, text string) ([]byte, error) {
	return []byte(fmt.Sprintf("r%d:%s ", index, text)), nil
}

func (reasoningEncoder) StopReasoning(index int, value protocolcore.Reasoning) ([]byte, error) {
	return []byte(fmt.Sprintf("]R%d:%s ", index, value.Seal.Opaque)), nil
}

type refusalEncoder struct{ recordingEncoder }

func (refusalEncoder) StartRefusal(index int) ([]byte, error) {
	return []byte(fmt.Sprintf("F%d[ ", index)), nil
}

func (refusalEncoder) AppendRefusal(index int, text string) ([]byte, error) {
	return []byte(fmt.Sprintf("f%d:%s ", index, text)), nil
}

func (refusalEncoder) StopRefusal(index int) ([]byte, error) {
	return []byte(fmt.Sprintf("]F%d ", index)), nil
}

func newTestAssembler(t *testing.T, encoder ClientStreamEncoder, display protocolcore.ThinkingDisplay, options AssemblerOptions) *Assembler {
	t.Helper()
	request := protocolcore.Request{RequestedModel: "claude-test", EffectiveModel: "gpt-test", Stream: true}
	if display != "" {
		request.Reasoning = protocolcore.ReasoningIntent{Thinking: protocolcore.ThinkingModeAdaptive, Display: display}
	}
	assembler, err := NewAssembler(request, encoder, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assembler.Start(StreamStart{ResponseID: "resp_1", ReportedModel: "gpt-test"}); err != nil {
		t.Fatal(err)
	}
	return assembler
}

// eventTape accumulates the live bytes of a sequence of assembler calls.
type eventTape struct {
	t    *testing.T
	live strings.Builder
}

func (tape *eventTape) record(output []byte, err error) {
	tape.t.Helper()
	if err != nil {
		tape.t.Fatal(err)
	}
	tape.live.Write(output)
}

func testCall(t *testing.T, id, name, arguments string) protocolcore.ToolCall {
	t.Helper()
	key, err := protocolcore.NewCallKey("openai-responses-call", id)
	if err != nil {
		t.Fatal(err)
	}
	document, err := protocolcore.NewJSONObject([]byte(arguments), protocolcore.MaxToolJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	return protocolcore.ToolCall{Key: key, Name: name, Arguments: document}
}

func textBlock(text string) protocolcore.ContentBlock {
	return protocolcore.ContentBlock{Kind: protocolcore.BlockText, Text: text}
}

func terminalResponse(stop protocolcore.StopReason, blocks ...protocolcore.ContentBlock) protocolcore.Response {
	return protocolcore.Response{ID: "resp_terminal", RequestedModel: "claude-test", EffectiveModel: "gpt-test", ReportedModel: "gpt-test", StopReason: stop, Blocks: blocks}
}

func finish(t *testing.T, assembler *Assembler, response protocolcore.Response) PendingTerminal {
	t.Helper()
	if err := assembler.Terminal(response); err != nil {
		t.Fatal(err)
	}
	terminal, err := assembler.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return terminal
}

func approve(t *testing.T, terminal PendingTerminal) string {
	t.Helper()
	release, err := terminal.Approve()
	if err != nil {
		t.Fatal(err)
	}
	return string(release)
}

func TestAssemblerStartIsIdempotent(t *testing.T) {
	t.Parallel()
	assembler, err := NewAssembler(protocolcore.Request{}, recordingEncoder{}, AssemblerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assembler.Text("0", "early"); err == nil {
		t.Fatal("content before Start accepted")
	}
	assembler, _ = NewAssembler(protocolcore.Request{}, recordingEncoder{}, AssemblerOptions{})
	first, err := assembler.Start(StreamStart{ResponseID: "msg_vm_provisional"})
	if err != nil || string(first) != "S(msg_vm_provisional) " {
		t.Fatalf("Start() = %q, %v", first, err)
	}
	again, err := assembler.Start(StreamStart{ResponseID: "resp_real"})
	if err != nil || len(again) != 0 {
		t.Fatalf("second Start() = %q, %v", again, err)
	}
}

func TestAssemblerStreamsTextLiveAndHoldsTheTerminal(t *testing.T) {
	t.Parallel()
	assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
	tape := &eventTape{t: t}
	tape.record(assembler.Text("1:0", ""))
	tape.record(assembler.Text("1:0", "Hel"))
	tape.record(assembler.Text("1:0", "lo"))
	tape.record(assembler.TextDone("1:0", "Hello"))
	if tape.live.String() != "T0[ t0:Hel t0:lo ]0 " {
		t.Fatalf("live = %q", tape.live.String())
	}
	terminal := finish(t, assembler, terminalResponse(protocolcore.StopReasonEndTurn, textBlock("Hello")))
	if len(terminal.ToolIntents()) != 0 {
		t.Fatal("a text-only response has intents")
	}
	if release := approve(t, terminal); release != "E:end_turn" {
		t.Fatalf("release = %q", release)
	}
}

func TestAssemblerRepairsSuffixAndEmitsUnstreamedTextWhole(t *testing.T) {
	t.Parallel()
	assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
	tape := &eventTape{t: t}
	tape.record(assembler.Text("a", "Hel"))
	tape.record(assembler.TextDone("a", "Hello"))
	// The minimal ChatGPT shape: only output_item.done, no deltas.
	tape.record(assembler.TextDone("b", "Whole"))
	if tape.live.String() != "T0[ t0:Hel t0:lo ]0 T1[ t1:Whole ]1 " {
		t.Fatalf("live = %q", tape.live.String())
	}
	finish(t, assembler, terminalResponse(protocolcore.StopReasonEndTurn, textBlock("Hello"), textBlock("Whole")))
}

func TestAssemblerRefusesTextThatIsNotAPrefix(t *testing.T) {
	t.Parallel()
	for name, final := range map[string]string{"different": "Hallo", "shorter": "He"} {
		assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
		if _, err := assembler.Text("a", "Hel"); err != nil {
			t.Fatal(err)
		}
		_, err := assembler.TextDone("a", final)
		if protocolcore.ReasonOf(err) != protocolcore.ReasonStreamStateViolation {
			t.Fatalf("%s: TextDone() error = %v", name, err)
		}
		if _, err := assembler.Text("b", "x"); err == nil {
			t.Fatalf("%s: a failed assembler accepted more content", name)
		}
	}
}

func TestAssemblerTerminalMustMatchEveryBlock(t *testing.T) {
	t.Parallel()
	for name, response := range map[string]protocolcore.Response{
		"different text":  terminalResponse(protocolcore.StopReasonEndTurn, textBlock("Hellp")),
		"missing block":   terminalResponse(protocolcore.StopReasonEndTurn),
		"extra block":     terminalResponse(protocolcore.StopReasonEndTurn, textBlock("Hello"), textBlock("more")),
		"kind changed":    terminalResponse(protocolcore.StopReasonEndTurn, protocolcore.ContentBlock{Kind: protocolcore.BlockRefusal, Refusal: "Hello"}),
		"unstreamed tool": terminalResponse(protocolcore.StopReasonToolUse, textBlock("Hello"), protocolcore.ContentBlock{Kind: protocolcore.BlockToolCall, ToolCall: testCall(t, "call_1", "Bash", `{}`)}),
	} {
		assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
		if _, err := assembler.TextDone("a", "Hello"); err != nil {
			t.Fatal(err)
		}
		if err := assembler.Terminal(response); err == nil {
			t.Fatalf("%s: terminal accepted", name)
		}
	}
	// A terminal while a block is still open is invalid ordering.
	assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
	if _, err := assembler.Text("a", "Hel"); err != nil {
		t.Fatal(err)
	}
	if err := assembler.Terminal(terminalResponse(protocolcore.StopReasonEndTurn, textBlock("Hel"))); err == nil {
		t.Fatal("terminal accepted with an open block")
	}
}

func TestAssemblerHoldsEverythingAfterTheBarrier(t *testing.T) {
	t.Parallel()
	for _, armed := range []bool{true, false} {
		assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
		tape := &eventTape{t: t}
		tape.record(assembler.TextDone("1:0", "Before."))
		if armed {
			assembler.ArmBarrier()
		}
		bash := testCall(t, "call_1", "Bash", `{"command":"ls"}`)
		read := testCall(t, "call_2", "Read", `{"file_path":"a"}`)
		tape.record(assembler.ToolCall("2", bash))
		tape.record(assembler.Text("4:0", "After"))
		tape.record(assembler.TextDone("4:0", "After"))
		tape.record(assembler.ToolCall("3", read))
		if tape.live.String() != "T0[ t0:Before. ]0 " {
			t.Fatalf("armed=%v: live leaked held bytes: %q", armed, tape.live.String())
		}
		response := terminalResponse(protocolcore.StopReasonToolUse,
			textBlock("Before."),
			protocolcore.ContentBlock{Kind: protocolcore.BlockToolCall, ToolCall: bash},
			textBlock("After"),
			protocolcore.ContentBlock{Kind: protocolcore.BlockToolCall, ToolCall: read})
		terminal := finish(t, assembler, response)
		intents := terminal.ToolIntents()
		if len(intents) != 2 || intents[0].Call.Name != "Bash" || intents[1].Call.Name != "Read" ||
			intents[0].ResponseID != "resp_terminal" || intents[0].Ordinal != 0 || intents[1].Ordinal != 1 {
			t.Fatalf("intents = %+v", intents)
		}
		want := `C1:Bash{"command":"ls"} T2[ t2:After ]2 C3:Read{"file_path":"a"} E:tool_use`
		if release := approve(t, terminal); release != want {
			t.Fatalf("armed=%v: release = %q", armed, release)
		}
	}
}

func TestAssemblerHeldBytesAreBounded(t *testing.T) {
	t.Parallel()
	assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{MaxHeldBytes: 64})
	assembler.ArmBarrier()
	if _, err := assembler.Text("a", strings.Repeat("x", 40)); err != nil {
		t.Fatal(err)
	}
	_, err := assembler.Text("a", strings.Repeat("y", 40))
	if protocolcore.ReasonOf(err) != protocolcore.ReasonStreamLimitExceeded {
		t.Fatalf("held bound: %v", err)
	}
}

func TestAssemblerRejectDiscardsHeldBytes(t *testing.T) {
	t.Parallel()
	assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
	call := testCall(t, "call_1", "Bash", `{}`)
	if _, err := assembler.ToolCall("1", call); err != nil {
		t.Fatal(err)
	}
	terminal := finish(t, assembler, terminalResponse(protocolcore.StopReasonToolUse, protocolcore.ContentBlock{Kind: protocolcore.BlockToolCall, ToolCall: call}))
	if err := terminal.Reject(); err != nil {
		t.Fatal(err)
	}
	if release, err := terminal.Approve(); !errors.Is(err, ErrTerminalAlreadyDecided) || len(release) != 0 {
		t.Fatalf("approve after reject = %q, %v", release, err)
	}
}

func TestAssemblerBuffersInterleavedKeysAndReplaysThemInOrder(t *testing.T) {
	t.Parallel()
	assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
	tape := &eventTape{t: t}
	tape.record(assembler.Text("a", "A1"))
	tape.record(assembler.Text("b", "B1"))
	tape.record(assembler.Text("c", "C1"))
	tape.record(assembler.Text("a", "A2"))
	tape.record(assembler.Text("b", "B2"))
	if tape.live.String() != "T0[ t0:A1 t0:A2 " {
		t.Fatalf("live before close = %q", tape.live.String())
	}
	tape.record(assembler.TextDone("b", "B1B2"))
	tape.record(assembler.TextDone("a", "A1A2"))
	tape.record(assembler.TextDone("c", "C1"))
	want := "T0[ t0:A1 t0:A2 ]0 T1[ t1:B1 t1:B2 ]1 T2[ t2:C1 ]2 "
	if tape.live.String() != want {
		t.Fatalf("live = %q, want %q", tape.live.String(), want)
	}
	finish(t, assembler, terminalResponse(protocolcore.StopReasonEndTurn, textBlock("A1A2"), textBlock("B1B2"), textBlock("C1")))
}

func TestAssemblerInterleaveBufferIsBounded(t *testing.T) {
	t.Parallel()
	assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{MaxInterleaveBytes: 8})
	if _, err := assembler.Text("a", "open"); err != nil {
		t.Fatal(err)
	}
	if _, err := assembler.Text("b", "12345"); err != nil {
		t.Fatal(err)
	}
	_, err := assembler.Text("b", "6789")
	if protocolcore.ReasonOf(err) != protocolcore.ReasonStreamLimitExceeded {
		t.Fatalf("interleave bound: %v", err)
	}
}

// A function call that completes while text is still open waits for the
// text block, and arms the barrier the moment it arrives: the rest of the
// open block is held with it.
func TestAssemblerToolCallWaitsForTheOpenTextBlock(t *testing.T) {
	t.Parallel()
	assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
	tape := &eventTape{t: t}
	tape.record(assembler.Text("1:0", "Work"))
	call := testCall(t, "call_1", "Bash", `{}`)
	tape.record(assembler.ToolCall("2", call))
	tape.record(assembler.Text("1:0", "ing"))
	tape.record(assembler.TextDone("1:0", "Working"))
	if tape.live.String() != "T0[ t0:Work " {
		t.Fatalf("live = %q", tape.live.String())
	}
	terminal := finish(t, assembler, terminalResponse(protocolcore.StopReasonToolUse, textBlock("Working"), protocolcore.ContentBlock{Kind: protocolcore.BlockToolCall, ToolCall: call}))
	if release := approve(t, terminal); release != "t0:ing ]0 C1:Bash{} E:tool_use" {
		t.Fatalf("release = %q", release)
	}
}

func TestAssemblerChecksToolCallsBothWays(t *testing.T) {
	t.Parallel()
	bash := testCall(t, "call_1", "Bash", `{"command":"ls"}`)
	changed := testCall(t, "call_1", "Bash", `{"command":"rm"}`)
	other := testCall(t, "call_2", "Read", `{}`)
	for name, response := range map[string]protocolcore.Response{
		"arguments changed":  terminalResponse(protocolcore.StopReasonToolUse, protocolcore.ContentBlock{Kind: protocolcore.BlockToolCall, ToolCall: changed}),
		"call missing":       terminalResponse(protocolcore.StopReasonEndTurn),
		"call added":         terminalResponse(protocolcore.StopReasonToolUse, protocolcore.ContentBlock{Kind: protocolcore.BlockToolCall, ToolCall: bash}, protocolcore.ContentBlock{Kind: protocolcore.BlockToolCall, ToolCall: other}),
		"call replaced":      terminalResponse(protocolcore.StopReasonToolUse, protocolcore.ContentBlock{Kind: protocolcore.BlockToolCall, ToolCall: other}),
		"text instead of it": terminalResponse(protocolcore.StopReasonEndTurn, textBlock("x")),
	} {
		assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
		if _, err := assembler.ToolCall("1", bash); err != nil {
			t.Fatal(err)
		}
		err := assembler.Terminal(response)
		if protocolcore.ReasonOf(err) != protocolcore.ReasonToolCallIncomplete {
			t.Fatalf("%s: Terminal() error = %v, want %s", name, err, protocolcore.ReasonToolCallIncomplete)
		}
	}
	// A call the terminal adds after streamed text is a tool mismatch too.
	assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
	if _, err := assembler.TextDone("0", "Hello"); err != nil {
		t.Fatal(err)
	}
	err := assembler.Terminal(terminalResponse(protocolcore.StopReasonToolUse, textBlock("Hello"), protocolcore.ContentBlock{Kind: protocolcore.BlockToolCall, ToolCall: bash}))
	if protocolcore.ReasonOf(err) != protocolcore.ReasonToolCallIncomplete {
		t.Fatalf("unstreamed call: Terminal() error = %v", err)
	}
	assembler = newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
	if _, err := assembler.ToolCall("1", bash); err != nil {
		t.Fatal(err)
	}
	if _, err := assembler.ToolCall("2", bash); err == nil {
		t.Fatal("a duplicated tool call identity was accepted")
	}
}

// A tool call the provider side built wrongly is refused when it arrives,
// before it can reach the client or the approval.
func TestAssemblerRefusesAnInvalidToolCall(t *testing.T) {
	t.Parallel()
	valid := testCall(t, "call_1", "Bash", `{}`)
	unnamed := valid
	unnamed.Name = ""
	empty := valid
	empty.Arguments = protocolcore.JSONDocument{}
	for name, call := range map[string]protocolcore.ToolCall{
		"no key":       {Name: "Bash", Arguments: valid.Arguments},
		"no name":      unnamed,
		"no arguments": empty,
	} {
		assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
		output, err := assembler.ToolCall("1", call)
		if protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidProviderResponse || len(output) != 0 {
			t.Fatalf("%s: ToolCall() = %q, %v", name, output, err)
		}
		if _, err := assembler.Text("2", "x"); err == nil {
			t.Fatalf("%s: a failed assembler accepted more content", name)
		}
	}
}

// Tool calls that wait behind an open block are charged to the held bound,
// so a stream cannot buffer calls without limit while a block stays open.
func TestAssemblerBufferedToolCallsAreBounded(t *testing.T) {
	t.Parallel()
	assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{MaxHeldBytes: 200})
	if _, err := assembler.Text("text", "open"); err != nil {
		t.Fatal(err)
	}
	arguments := `{"command":"` + strings.Repeat("x", 60) + `"}`
	var err error
	accepted := 0
	for index := 0; index < 50 && err == nil; index++ {
		_, err = assembler.ToolCall(fmt.Sprintf("call-%d", index), testCall(t, fmt.Sprintf("call_%d", index), "Bash", arguments))
		if err == nil {
			accepted++
		}
	}
	if protocolcore.ReasonOf(err) != protocolcore.ReasonStreamLimitExceeded || accepted != 2 {
		t.Fatalf("accepted %d buffered calls, then %v", accepted, err)
	}
}

func TestAssemblerEmptyResponseGivesOneEmptyTextBlock(t *testing.T) {
	t.Parallel()
	assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
	terminal := finish(t, assembler, terminalResponse(protocolcore.StopReasonEndTurn))
	if release := approve(t, terminal); release != "T0[ ]0 E:end_turn" {
		t.Fatalf("release = %q", release)
	}
	blocks := terminal.DecodedResponse().Blocks
	if len(blocks) != 1 || blocks[0].Kind != protocolcore.BlockText || blocks[0].Text != "" {
		t.Fatalf("decoded response = %+v", blocks)
	}
}

func TestAssemblerReasoningOnlyResponseIsValid(t *testing.T) {
	t.Parallel()
	value := protocolcore.Reasoning{Text: "Thinking.", Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealResponsesEncrypted, ItemID: "rs_1", Opaque: "ENC"}}
	assembler := newTestAssembler(t, reasoningEncoder{}, protocolcore.ThinkingDisplaySummarized, AssemblerOptions{})
	tape := &eventTape{t: t}
	tape.record(assembler.ReasoningDone("0", value))
	if tape.live.String() != "R0[ r0:Thinking. ]R0:ENC " {
		t.Fatalf("live = %q", tape.live.String())
	}
	terminal := finish(t, assembler, terminalResponse(protocolcore.StopReasonEndTurn, protocolcore.ContentBlock{Kind: protocolcore.BlockReasoning, Reasoning: &value}))
	if release := approve(t, terminal); release != "E:end_turn" {
		t.Fatalf("a reasoning-only response gained a text block: %q", release)
	}
	if blocks := terminal.DecodedResponse().Blocks; len(blocks) != 1 {
		t.Fatalf("decoded response = %+v", blocks)
	}
}

func TestAssemblerStreamsVisibleReasoningAndRepairsItsSuffix(t *testing.T) {
	t.Parallel()
	value := protocolcore.Reasoning{Text: "Plan.\n\nAct.", Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealResponsesEncrypted, ItemID: "rs_1", Opaque: "ENC"}}
	assembler := newTestAssembler(t, reasoningEncoder{}, protocolcore.ThinkingDisplaySummarized, AssemblerOptions{})
	tape := &eventTape{t: t}
	tape.record(assembler.Reasoning("0", ""))
	tape.record(assembler.Reasoning("0", "Plan."))
	tape.record(assembler.Reasoning("0", "\n\n"))
	tape.record(assembler.Reasoning("0", "A"))
	tape.record(assembler.ReasoningDone("0", value))
	tape.record(assembler.TextDone("1:0", "Done"))
	want := "R0[ r0:Plan. r0:\n\n r0:A r0:ct. ]R0:ENC T1[ t1:Done ]1 "
	if tape.live.String() != want {
		t.Fatalf("live = %q, want %q", tape.live.String(), want)
	}
	finish(t, assembler, terminalResponse(protocolcore.StopReasonEndTurn, protocolcore.ContentBlock{Kind: protocolcore.BlockReasoning, Reasoning: &value}, textBlock("Done")))

	diverging := newTestAssembler(t, reasoningEncoder{}, protocolcore.ThinkingDisplaySummarized, AssemblerOptions{})
	if _, err := diverging.Reasoning("0", "Other"); err != nil {
		t.Fatal(err)
	}
	if _, err := diverging.ReasoningDone("0", value); protocolcore.ReasonOf(err) != protocolcore.ReasonStreamStateViolation {
		t.Fatalf("diverging reasoning: %v", err)
	}
}

// With display omitted the client sees only empty heartbeats and the seal;
// the terminal is compared with the ReasoningDone value, not the stream.
func TestAssemblerOmittedDisplayNeverShowsReasoningText(t *testing.T) {
	t.Parallel()
	value := protocolcore.Reasoning{Text: "secret plan", Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealResponsesEncrypted, ItemID: "rs_1", Opaque: "ENC"}}
	assembler := newTestAssembler(t, reasoningEncoder{}, protocolcore.ThinkingDisplayOmitted, AssemblerOptions{})
	tape := &eventTape{t: t}
	tape.record(assembler.Reasoning("0", ""))
	tape.record(assembler.Reasoning("0", "secret "))
	tape.record(assembler.Reasoning("0", ""))
	tape.record(assembler.Reasoning("0", "plan"))
	tape.record(assembler.ReasoningDone("0", value))
	if strings.Contains(tape.live.String(), "secret") || tape.live.String() != "R0[ r0: ]R0:ENC " {
		t.Fatalf("live = %q", tape.live.String())
	}
	terminal := finish(t, assembler, terminalResponse(protocolcore.StopReasonEndTurn, protocolcore.ContentBlock{Kind: protocolcore.BlockReasoning, Reasoning: &value}))
	if blocks := terminal.DecodedResponse().Blocks; blocks[0].Reasoning.Text != "secret plan" {
		t.Fatal("the neutral response lost the reasoning text")
	}
	changed := value
	changed.Seal.Opaque = "OTHER"
	other := newTestAssembler(t, reasoningEncoder{}, protocolcore.ThinkingDisplayOmitted, AssemblerOptions{})
	if _, err := other.ReasoningDone("0", value); err != nil {
		t.Fatal(err)
	}
	if err := other.Terminal(terminalResponse(protocolcore.StopReasonEndTurn, protocolcore.ContentBlock{Kind: protocolcore.BlockReasoning, Reasoning: &changed})); err == nil {
		t.Fatal("a terminal with another seal was accepted")
	}
}

// The terminal is compared with the whole ReasoningDone value: text, text
// source, redaction and seal. A summary and raw reasoning content with the
// same text are different values.
func TestAssemblerTerminalReasoningMustMatchItsValue(t *testing.T) {
	t.Parallel()
	value := protocolcore.Reasoning{Text: "Plan.", TextSource: protocolcore.ReasoningTextContent, Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealResponsesEncrypted, ItemID: "rs_1", Opaque: "ENC"}}
	summary := value
	summary.TextSource = protocolcore.ReasoningTextSummary
	defaulted := value
	defaulted.TextSource = ""
	text := value
	text.Text = "Plan!"
	for _, encoder := range []ClientStreamEncoder{reasoningEncoder{}, recordingEncoder{}} {
		for name, test := range map[string]struct {
			streamed, terminal protocolcore.Reasoning
			accepted           bool
		}{
			"same value":           {value, value, true},
			"summary is a default": {summary, defaulted, true},
			"other text source":    {value, summary, false},
			"other text":           {value, text, false},
		} {
			assembler := newTestAssembler(t, encoder, protocolcore.ThinkingDisplaySummarized, AssemblerOptions{})
			if _, err := assembler.ReasoningDone("0", test.streamed); err != nil {
				t.Fatal(err)
			}
			terminal := test.terminal
			err := assembler.Terminal(terminalResponse(protocolcore.StopReasonEndTurn, protocolcore.ContentBlock{Kind: protocolcore.BlockReasoning, Reasoning: &terminal}))
			if (err == nil) != test.accepted {
				t.Fatalf("%T %s: Terminal() error = %v", encoder, name, err)
			}
			if err != nil && protocolcore.ReasonOf(err) != protocolcore.ReasonStreamStateViolation {
				t.Fatalf("%T %s: reason = %s", encoder, name, protocolcore.ReasonOf(err))
			}
		}
	}
}

func TestAssemblerWithoutReasoningCapabilityKeepsReasoningInTheTerminal(t *testing.T) {
	t.Parallel()
	value := protocolcore.Reasoning{Text: "Thinking.", Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealResponsesEncrypted, ItemID: "rs_1", Opaque: "ENC"}}
	assembler := newTestAssembler(t, recordingEncoder{}, protocolcore.ThinkingDisplaySummarized, AssemblerOptions{})
	tape := &eventTape{t: t}
	tape.record(assembler.Text("1:0", "Ans"))
	// Reasoning never occupies a client block, so it never waits.
	tape.record(assembler.Reasoning("0", "Thinking."))
	tape.record(assembler.ReasoningDone("0", value))
	tape.record(assembler.TextDone("1:0", "Answer"))
	if tape.live.String() != "T0[ t0:Ans t0:wer ]0 " {
		t.Fatalf("live = %q", tape.live.String())
	}
	terminal := finish(t, assembler, terminalResponse(protocolcore.StopReasonEndTurn, textBlock("Answer"), protocolcore.ContentBlock{Kind: protocolcore.BlockReasoning, Reasoning: &value}))
	notices := terminal.TranslationReport().Notices()
	if len(notices) != 1 || notices[0].Code != protocolcore.NoticeReasoningContentNotForwarded {
		t.Fatalf("notices = %+v", notices)
	}
	// Reasoning alone, unforwarded, still gives the client an empty text block.
	alone := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
	if _, err := alone.ReasoningDone("0", value); err != nil {
		t.Fatal(err)
	}
	terminal = finish(t, alone, terminalResponse(protocolcore.StopReasonEndTurn, protocolcore.ContentBlock{Kind: protocolcore.BlockReasoning, Reasoning: &value}))
	if release := approve(t, terminal); release != "T0[ ]0 E:end_turn" {
		t.Fatalf("release = %q", release)
	}
	// The client's empty block is not added to the decoded response, which
	// stays reasoning-only.
	if blocks := terminal.DecodedResponse().Blocks; len(blocks) != 1 || blocks[0].Kind != protocolcore.BlockReasoning {
		t.Fatalf("decoded response = %+v", blocks)
	}
}

func TestAssemblerRefusals(t *testing.T) {
	t.Parallel()
	refusal := protocolcore.ContentBlock{Kind: protocolcore.BlockRefusal, Refusal: "I can't help with that."}
	fallback := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
	tape := &eventTape{t: t}
	tape.record(fallback.Refusal("1:0", "I can't"))
	tape.record(fallback.TextDone("1:0", "I can't help with that."))
	if tape.live.String() != "T0[ t0:I can't t0: help with that. ]0 " {
		t.Fatalf("fallback live = %q", tape.live.String())
	}
	terminal := finish(t, fallback, terminalResponse(protocolcore.StopReasonEndTurn, refusal))
	notices := terminal.TranslationReport().Notices()
	if len(notices) != 1 || notices[0].Code != protocolcore.NoticeRefusalRenderedAsText {
		t.Fatalf("fallback notices = %+v", notices)
	}
	native := newTestAssembler(t, refusalEncoder{}, "", AssemblerOptions{})
	nativeTape := &eventTape{t: t}
	nativeTape.record(native.Refusal("1:0", "I can't help with that."))
	nativeTape.record(native.TextDone("1:0", "I can't help with that."))
	if nativeTape.live.String() != "F0[ f0:I can't help with that. ]F0 " {
		t.Fatalf("native live = %q", nativeTape.live.String())
	}
	terminal = finish(t, native, terminalResponse(protocolcore.StopReasonEndTurn, refusal))
	if len(terminal.TranslationReport().Notices()) != 0 {
		t.Fatal("a native refusal was declared as text")
	}
}

// A refusal delivered only whole (the minimal ChatGPT shape sends just
// item.done) stays a refusal on both kinds of client encoder.
func TestAssemblerRefusalDeliveredWhole(t *testing.T) {
	t.Parallel()
	refusal := protocolcore.ContentBlock{Kind: protocolcore.BlockRefusal, Refusal: "I can't help with that."}
	for name, test := range map[string]struct {
		encoder ClientStreamEncoder
		live    string
		notices []protocolcore.NoticeCode
	}{
		"text fallback":  {recordingEncoder{}, "T0[ t0:I can't help with that. ]0 ", []protocolcore.NoticeCode{protocolcore.NoticeRefusalRenderedAsText}},
		"native refusal": {refusalEncoder{}, "F0[ f0:I can't help with that. ]F0 ", nil},
	} {
		assembler := newTestAssembler(t, test.encoder, "", AssemblerOptions{})
		tape := &eventTape{t: t}
		tape.record(assembler.RefusalDone("1:0", refusal.Refusal))
		if tape.live.String() != test.live {
			t.Fatalf("%s: live = %q", name, tape.live.String())
		}
		terminal := finish(t, assembler, terminalResponse(protocolcore.StopReasonEndTurn, refusal))
		var notices []protocolcore.NoticeCode
		for _, notice := range terminal.TranslationReport().Notices() {
			notices = append(notices, notice.Code)
		}
		if !slices.Equal(notices, test.notices) {
			t.Fatalf("%s: notices = %v", name, notices)
		}
		if release := approve(t, terminal); release != "E:end_turn" {
			t.Fatalf("%s: release = %q", name, release)
		}
	}
	// A refusal that streamed first closes the same way.
	streamed := newTestAssembler(t, refusalEncoder{}, "", AssemblerOptions{})
	tape := &eventTape{t: t}
	tape.record(streamed.Refusal("1:0", "I can't"))
	tape.record(streamed.RefusalDone("1:0", refusal.Refusal))
	if tape.live.String() != "F0[ f0:I can't f0: help with that. ]F0 " {
		t.Fatalf("streamed live = %q", tape.live.String())
	}
	finish(t, streamed, terminalResponse(protocolcore.StopReasonEndTurn, refusal))
}

func TestAssemblerRejectsInvalidOrdering(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Assembler) error{
		"text after its close": func(a *Assembler) error {
			if _, err := a.TextDone("a", "x"); err != nil {
				return nil
			}
			_, err := a.Text("a", "y")
			return err
		},
		"reasoning on a text key": func(a *Assembler) error {
			if _, err := a.Text("a", "x"); err != nil {
				return nil
			}
			_, err := a.Reasoning("a", "y")
			return err
		},
		"refusal on a text key": func(a *Assembler) error {
			if _, err := a.Text("a", "x"); err != nil {
				return nil
			}
			_, err := a.Refusal("a", "y")
			return err
		},
		"refusal final on a text key": func(a *Assembler) error {
			if _, err := a.Text("a", "x"); err != nil {
				return nil
			}
			_, err := a.RefusalDone("a", "x")
			return err
		},
		"terminal twice": func(a *Assembler) error {
			if err := a.Terminal(terminalResponse(protocolcore.StopReasonEndTurn)); err != nil {
				return nil
			}
			return a.Terminal(terminalResponse(protocolcore.StopReasonEndTurn))
		},
		"content after the terminal": func(a *Assembler) error {
			if err := a.Terminal(terminalResponse(protocolcore.StopReasonEndTurn)); err != nil {
				return nil
			}
			_, err := a.Text("a", "late")
			return err
		},
		"finish before the terminal": func(a *Assembler) error {
			_, err := a.Finish()
			return err
		},
		"invalid reasoning value": func(a *Assembler) error {
			_, err := a.ReasoningDone("r", protocolcore.Reasoning{TextSource: "other"})
			return err
		},
	}
	for name, run := range cases {
		assembler := newTestAssembler(t, reasoningEncoder{}, "", AssemblerOptions{})
		if err := run(assembler); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
	finish(t, assembler, terminalResponse(protocolcore.StopReasonEndTurn))
	if _, err := assembler.Finish(); err == nil {
		t.Fatal("Finish twice accepted")
	}
	if _, err := NewAssembler(protocolcore.Request{}, nil, AssemblerOptions{}); err == nil {
		t.Fatal("an assembler without an encoder was built")
	}
	if _, err := NewAssembler(protocolcore.Request{}, recordingEncoder{}, AssemblerOptions{MaxHeldBytes: -1}); err == nil {
		t.Fatal("negative limits accepted")
	}
}

func TestAssemblerProgressCountsEveryProviderEvent(t *testing.T) {
	t.Parallel()
	assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
	before := assembler.SemanticProgress()
	assembler.Progress() // a keepalive
	assembler.ArmBarrier()
	if _, err := assembler.Text("a", ""); err != nil {
		t.Fatal(err)
	}
	if got := assembler.SemanticProgress() - before; got != 3 {
		t.Fatalf("progress advanced by %d, want 3", got)
	}
}

func TestAssemblerIsSafeForConcurrentProgressReads(t *testing.T) {
	t.Parallel()
	assembler := newTestAssembler(t, recordingEncoder{}, "", AssemblerOptions{})
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		for index := 0; index < 1000; index++ {
			_ = assembler.SemanticProgress()
		}
	}()
	for index := 0; index < 200; index++ {
		if _, err := assembler.Text("a", "x"); err != nil {
			t.Error(err)
			break
		}
		assembler.Progress()
	}
	group.Wait()
}

func TestAssemblerReportsEncoderNotices(t *testing.T) {
	t.Parallel()
	encoder := recordingEncoder{report: protocolcore.NewTranslationReport(protocolcore.TranslationNotice{Code: protocolcore.NoticeStopReasonApproximated, Path: "$.stop_reason"})}
	assembler := newTestAssembler(t, encoder, "", AssemblerOptions{})
	assembler.Notice(protocolcore.TranslationNotice{Code: protocolcore.NoticeHostedOutputNotForwarded, Path: "$.output[2]"})
	assembler.Notice(protocolcore.TranslationNotice{Code: protocolcore.NoticeHostedOutputNotForwarded, Path: "$.output[2]"})
	terminal := finish(t, assembler, terminalResponse(protocolcore.StopReasonEndTurn))
	notices := terminal.TranslationReport().Notices()
	if len(notices) != 2 || notices[0].Code != protocolcore.NoticeHostedOutputNotForwarded || notices[1].Code != protocolcore.NoticeStopReasonApproximated {
		t.Fatalf("notices = %+v", notices)
	}
}
