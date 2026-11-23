package productruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

// The Claude Code to OpenAI Responses scenarios of design 15.5. Each runs the
// installed Claude Code through the real runtime against fakeResponsesUpstream
// on a published claude-openai routing set: the translated pair, the
// vibermate wire profile and the exact model mapping.

// claudeOpusUpstream is the upstream model Claude Code's default model maps to.
const claudeOpusUpstream = "gpt-6.2-astra"

func newClaudeResponsesRun(t *testing.T, ctx context.Context, binary string, toolMode environment.ToolPolicyMode, script func(fakeResponsesRequest) fakeResponsesReply) (*claudeAcceptanceRun, *fakeResponsesUpstream) {
	t.Helper()
	upstream := newFakeResponsesUpstream(t, script)
	run := newClaudeAcceptanceRun(t, ctx, binary, claudeRunOptions{
		backend: environment.ClientProtocolOpenAIResponses, serve: upstream.serve,
		wireProfile: wireprofile.UpstreamWireProfileViberMateValue, toolMode: toolMode,
	})
	return run, upstream
}

// claudeStreamEvent is one stream-json line Claude Code printed.
type claudeStreamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Model   string `json:"model"`
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
		} `json:"content"`
	} `json:"message"`
}

func claudeStreamEvents(stdout string) []claudeStreamEvent {
	var events []claudeStreamEvent
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var event claudeStreamEvent
		if json.Unmarshal([]byte(line), &event) == nil {
			events = append(events, event)
		}
	}
	return events
}

// claudeModelUsage is one modelUsage entry of Claude Code's result.
type claudeModelUsage struct {
	InputTokens              int64 `json:"inputTokens"`
	OutputTokens             int64 `json:"outputTokens"`
	CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
}

func (turn claudeAcceptanceTurn) usageOf(t *testing.T, model string) claudeModelUsage {
	t.Helper()
	var usage claudeModelUsage
	raw, ok := turn.ModelUsage[model]
	if !ok || json.Unmarshal(raw, &usage) != nil {
		t.Fatalf("Claude Code reported no usage for %s: %v", model, turn.ModelUsage)
	}
	return usage
}

// requireTurn runs one -p turn and requires it to succeed with want in its
// result.
func requireTurn(t *testing.T, ctx context.Context, run *claudeAcceptanceRun, binary string, arguments []string, want string, extra ...string) (claudeAcceptanceTurn, string) {
	t.Helper()
	stdout, stderr, err := run.claude(t, ctx, binary, arguments, extra...)
	result, parsed := claudeAcceptanceResult(stdout)
	if err != nil || !parsed || result.IsError || !strings.Contains(result.Result, want) {
		t.Fatalf("Claude Code turn %v failed: err=%v stdout=%s stderr=%s", arguments, err, stdout, stderr)
	}
	return result, stdout
}

// requireUpstreamClean checks what every scenario promises about the fake's
// traffic: nothing outside POST /v1/responses, and only mapped models.
func requireUpstreamClean(t *testing.T, upstream *fakeResponsesUpstream) []fakeResponsesRequest {
	t.Helper()
	requests, other := upstream.snapshot()
	if len(requests) == 0 {
		t.Fatal("no request reached the fake Responses upstream")
	}
	if len(other) != 0 {
		t.Errorf("Claude Code reached the provider outside /v1/responses: %v", other)
	}
	mapped := map[string]bool{}
	for _, mapping := range claudeAcceptanceModels {
		mapped[mapping.UpstreamModel] = true
	}
	for _, request := range requests {
		if !mapped[request.shape.Model] {
			t.Errorf("upstream request %d asked for %q, not a mapped model", request.ordinal, request.shape.Model)
		}
	}
	return requests
}

// A1: plain text in both output modes. Claude Code reports the Claude model
// it asked for and the upstream's usage in Anthropic's terms (design 7.6).
func testClaudeOnResponsesPlainText(t *testing.T, binary string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	run, upstream := newClaudeResponsesRun(t, ctx, binary, "", func(request fakeResponsesRequest) fakeResponsesReply {
		return fakeResponsesReply{events: responsesTurn{
			id: "resp_a1", model: request.shape.Model, text: claudeAcceptanceReply,
			reasoning: &responsesReasoning{id: "rs_a1", encrypted: "ENC-A1", parts: []string{"Answer with the fixture."}},
			usage:     responsesTurnUsage{input: 1200, cached: 1000, output: 30, reasoning: 12},
		}.events()}
	})
	for _, mode := range [][]string{{"--output-format", "json"}, {"--output-format", "stream-json", "--verbose"}} {
		result, _ := requireTurn(t, ctx, run, binary, append([]string{"-p", "Reply with the fixture text"}, mode...), claudeAcceptanceReply)
		for model := range result.ModelUsage {
			if model != "claude-opus-5-5" {
				t.Errorf("Claude Code %v reported model %q", mode, model)
			}
		}
		usage := result.usageOf(t, "claude-opus-5-5")
		if usage.InputTokens != 1200-1000 || usage.CacheReadInputTokens != 1000 || usage.OutputTokens != 30 || usage.CacheCreationInputTokens != 0 {
			t.Errorf("Claude Code %v usage = %+v, want input 200, cache read 1000, output 30", mode, usage)
		}
	}
	task7Must(t, run.fixture.pipeline.Drain(ctx))
	requests := requireUpstreamClean(t, upstream)
	if len(requests) != 2 {
		t.Errorf("want one upstream request per turn, got %d", len(requests))
	}
	for _, request := range requests {
		if request.shape.Model != claudeOpusUpstream {
			t.Errorf("upstream request %d model = %s, want %s", request.ordinal, request.shape.Model, claudeOpusUpstream)
		}
	}
}

// A2: the reasoning round trip with one tool call. The thinking Claude Code
// shows is the summary's two parts joined by a blank line, and the next
// request replays the upstream's own reasoning item right before the
// commentary it produced, which precedes the call.
func testClaudeOnResponsesReasoningRoundTrip(t *testing.T, binary string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const final = "a2 done"
	run, upstream := newClaudeResponsesRun(t, ctx, binary, "", func(request fakeResponsesRequest) fakeResponsesReply {
		if len(request.shape.outputs()) != 0 {
			return textReply(request, "resp_a2_final", final)
		}
		return fakeResponsesReply{events: responsesTurn{
			id: "resp_a2", model: request.shape.Model, text: "Let me run it.",
			reasoning: &responsesReasoning{id: "rs_a2", encrypted: "ENC-1", parts: []string{"First part.", "Second part."}},
			calls:     []responsesCall{{id: "call_a2", name: "Bash", arguments: `{"command":"echo a2-marker","description":"Print a marker"}`}},
			usage:     responsesTurnUsage{input: 100, cached: 0, output: 20},
		}.events()}
	})
	_, stdout := requireTurn(t, ctx, run, binary, []string{"-p", "Run the marker command", "--output-format", "stream-json", "--verbose"}, final)
	thinking := ""
	for _, event := range claudeStreamEvents(stdout) {
		for _, block := range event.Message.Content {
			if event.Type == "assistant" && block.Type == "thinking" {
				thinking = block.Thinking
				if !strings.HasPrefix(block.Signature, "vmr1.") {
					t.Errorf("thinking signature = %q, want a ViberMate seal", block.Signature)
				}
			}
		}
	}
	if thinking != "First part.\n\nSecond part." {
		t.Errorf("Claude Code showed thinking %q", thinking)
	}
	task7Must(t, run.fixture.pipeline.Drain(ctx))
	requests := requireUpstreamClean(t, upstream)
	second := slices.IndexFunc(requests, func(request fakeResponsesRequest) bool { return len(request.shape.outputs()) != 0 })
	if second < 0 {
		t.Fatal("no upstream request carried the tool output")
	}
	input := requests[second].shape.Input
	at := slices.IndexFunc(input, func(item responsesInputItem) bool { return item.Type == "reasoning" })
	if at < 0 || at+2 >= len(input) {
		t.Fatalf("request %d replays no reasoning: %s", second, requests[second].body)
	}
	reasoning, message, call := input[at], input[at+1], input[at+2]
	if reasoning.ID != "rs_a2" || reasoning.EncryptedContent != "ENC-1" {
		t.Errorf("replayed reasoning = %+v", reasoning)
	}
	if message.Type != "message" || message.Role != "assistant" || message.Phase != "commentary" ||
		!bytes.Contains(message.Content, []byte("Let me run it.")) {
		t.Errorf("item after the reasoning = %+v", message)
	}
	if call.Type != "function_call" || call.CallID != "call_a2" || call.Name != "Bash" {
		t.Errorf("replayed call = %+v", call)
	}
	if output := requests[second].shape.outputs()["call_a2"]; !bytes.Contains(output, []byte("a2-marker")) {
		t.Errorf("Bash output = %s", output)
	}
}

// claudeFixtureFiles writes the tool fixtures Claude Code reads into its
// working directory.
func claudeFixtureFiles(t *testing.T, directory string) (png, pdf, txt string) {
	t.Helper()
	image, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	png, pdf, txt = filepath.Join(directory, "wire-fixture.png"), filepath.Join(directory, "wire-fixture.pdf"), filepath.Join(directory, "wire-fixture.txt")
	for name, content := range map[string][]byte{png: image, pdf: claudeMinimalPDF(), txt: []byte("wire fixture text line one\nwire fixture text line two\n")} {
		task7Must(t, os.WriteFile(name, content, 0o600))
	}
	return png, pdf, txt
}

// claudeMinimalPDF is a one-page PDF with valid cross references.
func claudeMinimalPDF() []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		"<< /Length 44 >>\nstream\nBT /F1 18 Tf 20 100 Td (wire fixture) Tj ET\nendstream",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var document bytes.Buffer
	document.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for index, object := range objects {
		offsets[index] = document.Len()
		fmt.Fprintf(&document, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := document.Len()
	fmt.Fprintf(&document, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&document, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&document, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return document.Bytes()
}

// claudeDeferredTools are deferred tools Claude Code 2.1.329 names; a
// ToolSearch select of them loads whichever the running build still defers.
var claudeDeferredTools = []string{"CronCreate", "CronDelete", "CronList", "Monitor", "NotebookEdit", "WebFetch"}

// A3: media tool results and deferred tools. The second request carries the
// image as input_image, the PDF as input_text plus input_file, the text
// file and the shell output as strings, and the ToolSearch output naming
// every tool it loaded, which now appear in tools with strict false.
func testClaudeOnResponsesMediaAndDeferredTools(t *testing.T, binary string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const final = "a3 done"
	var png, pdf, txt string
	run, upstream := newClaudeResponsesRun(t, ctx, binary, "", func(request fakeResponsesRequest) fakeResponsesReply {
		if len(request.shape.outputs()) != 0 {
			return textReply(request, "resp_a3_final", final)
		}
		arguments := func(path string) string {
			encoded, _ := json.Marshal(map[string]string{"file_path": path})
			return string(encoded)
		}
		query, _ := json.Marshal(map[string]any{"query": "select:" + strings.Join(claudeDeferredTools, ","), "max_results": 20})
		return fakeResponsesReply{events: responsesTurn{
			id: "resp_a3", model: request.shape.Model, text: "Reading the fixtures.",
			calls: []responsesCall{
				{id: "call_a3_search", name: "ToolSearch", arguments: string(query)},
				{id: "call_a3_png", name: "Read", arguments: arguments(png)},
				{id: "call_a3_pdf", name: "Read", arguments: arguments(pdf)},
				{id: "call_a3_txt", name: "Read", arguments: arguments(txt)},
				{id: "call_a3_bash", name: "Bash", arguments: `{"command":"echo wire-fixture","description":"Print a marker"}`},
			},
			usage: responsesTurnUsage{input: 100, output: 20},
		}.events()}
	})
	png, pdf, txt = claudeFixtureFiles(t, run.home)
	requireTurn(t, ctx, run, binary, []string{"-p", "Read the fixtures", "--output-format", "stream-json", "--verbose"}, final)
	task7Must(t, run.fixture.pipeline.Drain(ctx))
	requests := requireUpstreamClean(t, upstream)
	first := requests[0].shape
	for _, name := range claudeDeferredTools {
		if first.tool(name) {
			t.Errorf("the first request already declares the deferred %s", name)
		}
	}
	second := slices.IndexFunc(requests, func(request fakeResponsesRequest) bool { return len(request.shape.outputs()) != 0 })
	if second < 0 {
		t.Fatal("no upstream request carried the tool outputs")
	}
	shape := requests[second].shape
	outputs := shape.outputs()
	parts := func(output json.RawMessage) []string {
		var list []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(output, &list) != nil {
			return []string{"string"}
		}
		var types []string
		for _, part := range list {
			types = append(types, part.Type)
		}
		return types
	}
	for call, want := range map[string][]string{
		"call_a3_png":  {"input_image"},
		"call_a3_pdf":  {"input_text", "input_file"},
		"call_a3_txt":  {"string"},
		"call_a3_bash": {"string"},
	} {
		if got := parts(outputs[call]); !slices.Equal(got, want) {
			t.Errorf("%s output parts = %v (%s), want %v", call, got, outputs[call], want)
		}
	}
	var search []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(outputs["call_a3_search"], &search); err != nil || len(search) == 0 {
		t.Fatalf("ToolSearch output = %s", outputs["call_a3_search"])
	}
	var loaded []string
	for _, part := range search {
		for _, line := range strings.Split(part.Text, "\n") {
			if name, ok := strings.CutPrefix(line, "Loaded tool: "); ok {
				loaded = append(loaded, name)
			}
		}
	}
	if len(loaded) == 0 {
		t.Fatalf("ToolSearch loaded nothing: %s", outputs["call_a3_search"])
	}
	for _, name := range loaded {
		found := false
		for _, tool := range shape.Tools {
			if tool.Name == name {
				found = true
				if tool.Strict == nil || *tool.Strict {
					t.Errorf("loaded tool %s strict = %v", name, tool.Strict)
				}
			}
		}
		if !found {
			t.Errorf("loaded tool %s is not declared on the next request", name)
		}
	}
	t.Logf("ToolSearch loaded %v", loaded)
}

// A4: two parallel calls under Review. The approval names both tools and
// their calls; until it is decided Claude Code receives only keepalive
// comments after the pre-tool text; then it runs both calls.
func testClaudeOnResponsesParallelCallsUnderReview(t *testing.T, binary string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const final = "a4 done"
	var txt string
	run, upstream := newClaudeResponsesRun(t, ctx, binary, environment.ToolPolicyReview, func(request fakeResponsesRequest) fakeResponsesReply {
		if len(request.shape.outputs()) != 0 {
			return textReply(request, "resp_a4_final", final)
		}
		read, _ := json.Marshal(map[string]string{"file_path": txt})
		return fakeResponsesReply{events: responsesTurn{
			id: "resp_a4", model: request.shape.Model, text: "Checking two things.",
			calls: []responsesCall{
				{id: "call_a4_read", name: "Read", arguments: string(read)},
				{id: "call_a4_bash", name: "Bash", arguments: `{"command":"echo a4-marker","description":"Print a marker"}`},
			},
			usage: responsesTurnUsage{input: 100, output: 20},
		}.events()}
	})
	_, _, txt = claudeFixtureFiles(t, run.home)
	type outcome struct {
		stdout, stderr string
		err            error
	}
	done := make(chan outcome, 1)
	go func() {
		stdout, stderr, err := run.claude(t, ctx, binary, []string{"-p", "Check two things", "--output-format", "stream-json", "--verbose"})
		done <- outcome{stdout, stderr, err}
	}()
	pending := waitForApproval(t, ctx, run, done)
	if !slices.Equal(pending.SubjectLabels, []string{"Read", "Bash"}) || len(pending.SubjectRefs) != 2 {
		t.Errorf("approval subjects = %v %v, want Read and Bash", pending.SubjectLabels, pending.SubjectRefs)
	}
	// Two keepalive intervals pass with the decision open.
	time.Sleep(31 * time.Second)
	exchanges := run.exchanges.snapshot()
	held := exchanges[len(exchanges)-1].snapshot()
	written := ""
	keepalives := 0
	for _, event := range held {
		switch event.kind {
		case "write":
			written += string(event.bytes)
		case "keepalive":
			keepalives++
		}
	}
	if !strings.Contains(written, "Checking two things.") || strings.Contains(written, "tool_use") || strings.Contains(written, "message_delta") {
		t.Errorf("before the decision Claude Code received %s", written)
	}
	if keepalives < 2 {
		t.Errorf("keepalives while the decision was open = %d", keepalives)
	}
	if last := held[len(held)-1]; last.kind != "keepalive" {
		t.Errorf("the last downstream event before the decision was %s", last.kind)
	}
	approveOnce(t, ctx, run, pending.ID, "a4-approve-parallel-calls")
	result := <-done
	turn, parsed := claudeAcceptanceResult(result.stdout)
	if result.err != nil || !parsed || turn.IsError || !strings.Contains(turn.Result, final) {
		t.Fatalf("Claude Code turn failed: err=%v stdout=%s stderr=%s", result.err, result.stdout, result.stderr)
	}
	for _, event := range claudeStreamEvents(result.stdout) {
		for _, block := range event.Message.Content {
			if block.Type == "tool_use" && block.Name == "Read" && !bytes.Contains(block.Input, []byte("wire-fixture.txt")) {
				t.Errorf("Claude Code's Read input = %s", block.Input)
			}
		}
	}
	task7Must(t, run.fixture.pipeline.Drain(ctx))
	requests := requireUpstreamClean(t, upstream)
	last := requests[len(requests)-1].shape.outputs()
	if !bytes.Contains(last["call_a4_read"], []byte("wire fixture text line one")) || !bytes.Contains(last["call_a4_bash"], []byte("a4-marker")) {
		t.Errorf("Claude Code did not run both calls: %v", last)
	}
}

// approveOnce allows one pending approval at its current revision.
func approveOnce(t *testing.T, ctx context.Context, run *claudeAcceptanceRun, id, key string) {
	t.Helper()
	current, err := run.fixture.runtime.approvals.GetApproval(ctx, id)
	task7Must(t, err)
	_, err = run.fixture.runtime.approvals.DecideApproval(ctx, toolapproval.DecisionCommand{
		ApprovalID: id, ExpectedRevision: current.Revision, IdempotencyKey: key,
		Decision: toolapproval.DecisionAllowOnce, Scope: toolapproval.ScopeRequest,
	})
	task7Must(t, err)
}

// waitForApproval returns the one pending approval, failing if the turn ends
// first.
func waitForApproval[T any](t *testing.T, ctx context.Context, run *claudeAcceptanceRun, done <-chan T) toolapproval.View {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case result := <-done:
			t.Fatalf("Claude Code finished before any approval was asked: %+v", result)
		case <-ctx.Done():
			t.Fatal("no approval was asked")
		case <-ticker.C:
		}
		page, err := run.fixture.runtime.approvals.ListApprovals(ctx, toolapproval.PageRequest{State: toolapproval.StatePending, Limit: 10})
		task7Must(t, err)
		if len(page.Items) > 0 {
			if len(page.Items) != 1 {
				t.Fatalf("want one pending approval, got %d", len(page.Items))
			}
			return page.Items[0]
		}
	}
}

// claudeObservation is what one error scenario's turn did, logged so an
// "observe, then pin" run records 2.1.329's behaviour.
type claudeObservation struct {
	turn     claudeAcceptanceTurn
	parsed   bool
	stdout   string
	stderr   string
	err      error
	requests []fakeResponsesRequest
}

func observeTurn(t *testing.T, ctx context.Context, run *claudeAcceptanceRun, upstream *fakeResponsesUpstream, binary string, arguments []string, extra ...string) claudeObservation {
	t.Helper()
	stdout, stderr, err := run.claude(t, ctx, binary, arguments, extra...)
	turn, parsed := claudeAcceptanceResult(stdout)
	task7Must(t, run.fixture.pipeline.Drain(ctx))
	requests, other := upstream.snapshot()
	if len(other) != 0 {
		t.Errorf("Claude Code reached the provider outside /v1/responses: %v", other)
	}
	var summary []string
	for _, request := range requests {
		kinds := map[string]int{}
		for _, item := range request.shape.Input {
			kinds[item.Type]++
		}
		summary = append(summary, request.at.Format("15:04:05.000")+" "+request.shape.Model+" "+jsonText(kinds))
	}
	t.Logf("exit err=%v parsed=%v is_error=%v result=%q requests=%d\n%s", err, parsed, turn.IsError, turn.Result, len(requests), strings.Join(summary, "\n"))
	return claudeObservation{turn: turn, parsed: parsed, stdout: stdout, stderr: stderr, err: err, requests: requests}
}

func jsonText(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// A6: the upstream reports a context overflow on the request after a tool
// round. Observed with 2.1.329 and pinned: Claude Code reads the rendered
// "prompt is too long" error, compacts the conversation with a text-only
// summary request and finishes the turn on the compacted conversation. A
// single-exchange -p turn has nothing to compact (2.1.329 then stops with
// "Prompt is too long"), which is why the overflow follows a tool round.
func testClaudeOnResponsesContextOverflow(t *testing.T, binary string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const final = "a6 after compaction"
	var overflowed atomic.Int64
	overflowed.Store(-1)
	run, upstream := newClaudeResponsesRun(t, ctx, binary, "", func(request fakeResponsesRequest) fakeResponsesReply {
		switch {
		case request.ordinal == 0:
			return claudeToolTurn(request, "a6", responsesCall{id: "call_a6", name: "Bash", arguments: `{"command":"echo a6-marker","description":"Print a marker"}`})
		case len(request.shape.outputs()) != 0 && overflowed.CompareAndSwap(-1, int64(request.ordinal)):
			return openAIErrorReply(400, "invalid_request_error", "context_length_exceeded",
				"Your input exceeds the context window of this model. This model's maximum context length is 400000 tokens.", nil)
		}
		return textReply(request, "resp_a6", final)
	})
	observation := observeTurn(t, ctx, run, upstream, binary, []string{"-p", "Run the marker command", "--output-format", "stream-json", "--verbose"})
	if observation.err != nil || !observation.parsed || observation.turn.IsError || !strings.Contains(observation.turn.Result, final) {
		t.Fatalf("Claude Code did not finish after the overflow: %+v stderr=%s", observation.turn, observation.stderr)
	}
	requests := observation.requests
	at := int(overflowed.Load())
	if at != 1 || len(requests) < 4 {
		t.Fatalf("overflow at request %d of %d", at, len(requests))
	}
	compactions := 0
	for _, request := range requests[at+1 : len(requests)-1] {
		if strings.Contains(lastUserText(request.shape), "Respond with TEXT ONLY") {
			compactions++
		}
	}
	if compactions == 0 {
		t.Errorf("no compaction request followed the overflow")
	}
	last := requests[len(requests)-1].shape
	if last.has("function_call") || len(last.Input) >= len(requests[at].shape.Input) {
		t.Errorf("the last request is not the compacted conversation: %d items", len(last.Input))
	}
}

// A7: a rate limit with retry-after: 2. Pinned as design 8.3 predicts: Claude
// Code waits at least the two seconds, retries once, and the turn succeeds.
func testClaudeOnResponsesRateLimit(t *testing.T, binary string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const final = "a7 after the limit"
	run, upstream := newClaudeResponsesRun(t, ctx, binary, "", func(request fakeResponsesRequest) fakeResponsesReply {
		if request.ordinal == 0 {
			return openAIErrorReply(429, "requests", "rate_limit_exceeded", "Rate limit reached for requests.", map[string]string{"Retry-After": "2"})
		}
		return textReply(request, "resp_a7", final)
	})
	observation := observeTurn(t, ctx, run, upstream, binary, []string{"-p", "Reply with the fixture text", "--output-format", "json"})
	if observation.err != nil || observation.turn.IsError || !strings.Contains(observation.turn.Result, final) {
		t.Fatalf("Claude Code did not finish after the rate limit: %+v stderr=%s", observation.turn, observation.stderr)
	}
	if len(observation.requests) != 2 {
		t.Fatalf("want two upstream requests, got %d", len(observation.requests))
	}
	if gap := observation.requests[1].at.Sub(observation.requests[0].at); gap < 2*time.Second {
		t.Errorf("Claude Code retried after %s, before retry-after", gap)
	}
}

// A8: no remaining quota. Claude Code stops after one request and shows the
// credit problem, never the upstream's own words.
func testClaudeOnResponsesInsufficientQuota(t *testing.T, binary string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	run, upstream := newClaudeResponsesRun(t, ctx, binary, "", func(request fakeResponsesRequest) fakeResponsesReply {
		return openAIErrorReply(429, "insufficient_quota", "insufficient_quota", "You exceeded your current quota.", nil)
	})
	observation := observeTurn(t, ctx, run, upstream, binary, []string{"-p", "Reply with the fixture text", "--output-format", "json"})
	if observation.err == nil || !observation.parsed || !observation.turn.IsError ||
		!strings.Contains(observation.turn.Result, "no remaining credit or quota (insufficient_quota)") {
		t.Fatalf("Claude Code result = %+v err=%v", observation.turn, observation.err)
	}
	if strings.Contains(observation.turn.Result, "You exceeded your current quota") {
		t.Error("the upstream's own message reached Claude Code")
	}
	if len(observation.requests) != 1 {
		t.Errorf("want exactly one upstream request, got %d", len(observation.requests))
	}
}

// A9: text, then response.failed with server_is_overloaded. Observed with
// 2.1.329 and pinned: although a text block had completed, Claude Code
// retries the turn once, carrying the partial text as an assistant turn, and
// finishes with the second attempt.
func testClaudeOnResponsesFailureAfterText(t *testing.T, binary string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const final = "a9 second attempt"
	run, upstream := newClaudeResponsesRun(t, ctx, binary, "", func(request fakeResponsesRequest) fakeResponsesReply {
		if request.ordinal == 0 {
			return fakeResponsesReply{events: responsesTurn{
				id: "resp_a9_failed", model: request.shape.Model, text: "a9 partial", failAfterText: "server_is_overloaded",
			}.events()}
		}
		return textReply(request, "resp_a9", final)
	})
	observation := observeTurn(t, ctx, run, upstream, binary, []string{"-p", "Reply with the fixture text", "--output-format", "json"})
	if observation.err != nil || observation.turn.IsError || !strings.Contains(observation.turn.Result, final) {
		t.Fatalf("Claude Code did not finish with the second attempt: %+v stderr=%s", observation.turn, observation.stderr)
	}
	if len(observation.requests) != 2 {
		t.Fatalf("want two upstream requests, got %d", len(observation.requests))
	}
	retried := observation.requests[1].shape
	if !slices.ContainsFunc(retried.Input, func(item responsesInputItem) bool {
		return item.Type == "message" && item.Role == "assistant" && bytes.Contains(item.Content, []byte("a9 partial"))
	}) {
		t.Errorf("the retry does not carry the partial text: %s", observation.requests[1].body)
	}
	records := run.exchanges.snapshot()
	if len(records) == 0 {
		t.Fatal("no exchange was recorded")
	}
	aborted := false
	for _, event := range records[0].snapshot() {
		aborted = aborted || event.kind == "abort"
	}
	if !aborted {
		t.Error("the in-band failure did not abort the first stream")
	}
}

// claudeToolTurn is a first answer that reasons and calls one tool.
func claudeToolTurn(request fakeResponsesRequest, id string, call responsesCall) fakeResponsesReply {
	return fakeResponsesReply{events: responsesTurn{
		id: id, model: request.shape.Model, text: "Running it.",
		reasoning: &responsesReasoning{id: "rs_" + id, encrypted: "ENC-" + id, parts: []string{"Plan."}},
		calls:     []responsesCall{call},
		usage:     responsesTurnUsage{input: 100, output: 20},
	}.events()}
}

func (shape responsesRequestShape) has(kind string) bool {
	return slices.ContainsFunc(shape.Input, func(item responsesInputItem) bool { return item.Type == kind })
}

// A16: the replayed reasoning is rejected as invalid_encrypted_content.
// Pinned as design 8.6 predicts: the 400 capability_rejected:
// thinking_signature makes Claude Code strip its thinking and retry, and the
// retried request replays no reasoning item.
func testClaudeOnResponsesInvalidEncryptedContent(t *testing.T, binary string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const final = "a16 healed"
	run, upstream := newClaudeResponsesRun(t, ctx, binary, "", func(request fakeResponsesRequest) fakeResponsesReply {
		switch {
		case len(request.shape.outputs()) == 0:
			return claudeToolTurn(request, "a16", responsesCall{id: "call_a16", name: "Bash", arguments: `{"command":"echo a16-marker","description":"Print a marker"}`})
		case request.shape.has("reasoning"):
			return openAIErrorReply(400, "invalid_request_error", "invalid_encrypted_content", "The encrypted content could not be verified.", nil)
		}
		return textReply(request, "resp_a16", final)
	})
	observation := observeTurn(t, ctx, run, upstream, binary, []string{"-p", "Run the marker command", "--output-format", "stream-json", "--verbose"})
	if observation.err != nil || observation.turn.IsError || !strings.Contains(observation.turn.Result, final) {
		t.Fatalf("Claude Code did not heal: %+v stderr=%s", observation.turn, observation.stderr)
	}
	requests := observation.requests
	if len(requests) != 3 || !requests[1].shape.has("reasoning") || requests[2].shape.has("reasoning") {
		t.Fatalf("want a rejected replay then one retry without reasoning, got %d requests", len(requests))
	}
	retried := requests[2].shape
	if !retried.has("function_call") || !bytes.Contains(retried.outputs()["call_a16"], []byte("a16-marker")) {
		t.Errorf("the retry lost the tool round: %s", requests[2].body)
	}
}

// A17: the image in a tool result is rejected as invalid_image. Pinned as
// design 8.3 predicts: the 400 capability_rejected: image_block makes Claude
// Code strip the image and retry, and the retried request sends none.
func testClaudeOnResponsesInvalidImage(t *testing.T, binary string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const final = "a17 healed"
	var png string
	run, upstream := newClaudeResponsesRun(t, ctx, binary, "", func(request fakeResponsesRequest) fakeResponsesReply {
		if len(request.shape.outputs()) == 0 {
			read, _ := json.Marshal(map[string]string{"file_path": png})
			return claudeToolTurn(request, "a17", responsesCall{id: "call_a17", name: "Read", arguments: string(read)})
		}
		if bytes.Contains(request.body, []byte(`"input_image"`)) {
			return openAIErrorReply(400, "invalid_request_error", "invalid_image", "The image data you provided does not represent a valid image.", nil)
		}
		return textReply(request, "resp_a17", final)
	})
	png, _, _ = claudeFixtureFiles(t, run.home)
	observation := observeTurn(t, ctx, run, upstream, binary, []string{"-p", "Read the image", "--output-format", "stream-json", "--verbose"})
	if observation.err != nil || observation.turn.IsError || !strings.Contains(observation.turn.Result, final) {
		t.Fatalf("Claude Code did not heal: %+v stderr=%s", observation.turn, observation.stderr)
	}
	requests := observation.requests
	if len(requests) != 3 || !bytes.Contains(requests[1].body, []byte(`"input_image"`)) || bytes.Contains(requests[2].body, []byte(`"input_image"`)) {
		t.Fatalf("want a rejected image then one retry without it, got %d requests", len(requests))
	}
	// 2.1.329 replaces the image with a note quoting the token.
	if output := requests[2].shape.outputs()["call_a17"]; !bytes.Contains(output, []byte("Image removed")) ||
		!bytes.Contains(output, []byte("capability_rejected: image_block")) {
		t.Errorf("the retried tool output = %s", output)
	}
}

// A15: the model calls WebSearch (loaded through ToolSearch, since 2.1.329
// defers it). Claude Code's WebSearch tool sends its own sub-request naming
// the web_search_20250305 server tool; ViberMate refuses it with 422 before
// any upstream request, and Claude Code shows the tool error and finishes.
// WebSearch needs an explicit grant in -p mode, or the call never runs.
func testClaudeOnResponsesWebSearchRefused(t *testing.T, binary string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const final = "a15 done"
	run, upstream := newClaudeResponsesRun(t, ctx, binary, "", func(request fakeResponsesRequest) fakeResponsesReply {
		outputs := request.shape.outputs()
		switch {
		case !request.shape.tool("WebSearch") && len(outputs) == 0:
			return claudeToolTurn(request, "a15_load", responsesCall{id: "call_a15_load", name: "ToolSearch", arguments: `{"query":"select:WebSearch","max_results":5}`})
		case request.shape.tool("WebSearch") && outputs["call_a15_search"] == nil:
			return fakeResponsesReply{events: responsesTurn{
				id: "resp_a15_search", model: request.shape.Model, text: "Searching.",
				calls: []responsesCall{{id: "call_a15_search", name: "WebSearch", arguments: `{"query":"vibermate fixture"}`}},
				usage: responsesTurnUsage{input: 100, output: 20},
			}.events()}
		}
		return textReply(request, "resp_a15", final)
	})
	observation := observeTurn(t, ctx, run, upstream, binary, []string{"-p", "Search the web", "--output-format", "stream-json", "--verbose", "--allowedTools", "WebSearch"})
	if observation.err != nil || observation.turn.IsError || !strings.Contains(observation.turn.Result, final) {
		t.Fatalf("Claude Code did not finish after the refused search: %+v stderr=%s", observation.turn, observation.stderr)
	}
	for _, request := range observation.requests {
		for _, tool := range request.shape.Tools {
			if tool.Type != "function" {
				t.Errorf("upstream request %d declares a %s tool", request.ordinal, tool.Type)
			}
		}
		if bytes.Contains(request.body, []byte("web_search_20250305")) {
			t.Errorf("upstream request %d carries the server tool", request.ordinal)
		}
	}
	refused := 0
	for _, record := range run.exchanges.snapshot() {
		record.mu.Lock()
		if record.err != nil && strings.Contains(record.err.Error(), "$.tools[0].type") {
			refused++
		}
		record.mu.Unlock()
	}
	if refused != 1 {
		t.Errorf("want one sub-request refused at $.tools[0].type, got %d", refused)
	}
	last := observation.requests[len(observation.requests)-1].shape.outputs()["call_a15_search"]
	if !bytes.Contains(last, []byte("Tool error: API Error: 422")) {
		t.Errorf("WebSearch output = %s", last)
	}
}

// lastUserText is the text of the request's last user message.
func lastUserText(shape responsesRequestShape) string {
	for index := len(shape.Input) - 1; index >= 0; index-- {
		if item := shape.Input[index]; item.Type == "message" && item.Role == "user" {
			return string(item.Content)
		}
	}
	return ""
}

// A14: auto mode asks for Anthropic's server-side classifier through a
// root safeguards entry. The response reports it honestly as unsupported
// (design 7.7). Observed with 2.1.329 under -p --permission-mode auto and
// pinned: Claude Code classifies locally, runs the harmless call, and sends
// no safeguards on the session's next request.
func testClaudeOnResponsesAutoModeSafeguards(t *testing.T, binary string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const final = "a14 done"
	run, upstream := newClaudeResponsesRun(t, ctx, binary, "", func(request fakeResponsesRequest) fakeResponsesReply {
		if len(request.shape.outputs()) == 0 {
			return claudeToolTurn(request, "a14", responsesCall{id: "call_a14", name: "Bash", arguments: `{"command":"echo a14-marker","description":"Print a marker"}`})
		}
		return textReply(request, "resp_a14", final)
	})
	observation := observeTurn(t, ctx, run, upstream, binary, []string{"-p", "Run the marker command", "--output-format", "stream-json", "--verbose", "--permission-mode", "auto"})
	if observation.err != nil || observation.turn.IsError || !strings.Contains(observation.turn.Result, final) {
		t.Fatalf("Claude Code did not finish in auto mode: %+v stderr=%s", observation.turn, observation.stderr)
	}
	records := run.exchanges.snapshot()
	if len(records) != 2 || len(observation.requests) != 2 {
		t.Fatalf("want two exchanges and two upstream requests, got %d and %d", len(records), len(observation.requests))
	}
	const unsupported = `"safeguard_results":[{"type":"dangerous_tool_use","status":{"type":"unsupported"}}]`
	for index, record := range records {
		written := ""
		for _, event := range record.snapshot() {
			if event.kind == "write" {
				written += string(event.bytes)
			}
		}
		asked := slices.Contains(record.notices(), "safeguards_not_served")
		if asked != (index == 0) || strings.Contains(written, unsupported) != asked {
			t.Errorf("exchange %d: safeguards asked=%v, unsupported reported=%v", index, asked, strings.Contains(written, unsupported))
		}
		if strings.Contains(written, `"status":{"type":"evaluated"`) || strings.Contains(written, "not_flagged") {
			t.Errorf("exchange %d claims a classifier verdict", index)
		}
	}
	if output := observation.requests[1].shape.outputs()["call_a14"]; !bytes.Contains(output, []byte("a14-marker")) {
		t.Errorf("Claude Code did not run the call: %s", output)
	}
}

// claudeWatchdog is the byte-level idle limit A10 and A12 give Claude Code,
// well under the 150-second silences they script.
const claudeWatchdog = "CLAUDE_BYTE_STREAM_IDLE_TIMEOUT_MS=60000"

// longestGap is the longest stretch between two downstream events from
// since on, and how many keepalives were written in it.
func longestGap(events []downstreamEvent, since, until time.Time) (time.Duration, int) {
	var gap time.Duration
	keepalives := 0
	previous := since
	for _, event := range events {
		if event.at.Before(since) || event.at.After(until) {
			continue
		}
		gap = max(gap, event.at.Sub(previous))
		previous = event.at
		if event.kind == "keepalive" {
			keepalives++
		}
	}
	return max(gap, until.Sub(previous)), keepalives
}

// A10: the upstream sends response.created and is then silent for 150 s
// before it answers. The 15-second keepalive comments keep Claude Code's
// 60-second byte watchdog from firing. Negative control: with keepalives
// dropped by the test's exchange wrapper, the same turn is abandoned by
// Claude Code while the upstream is still silent.
func testClaudeOnResponsesSilentUpstream(t *testing.T, binary string) {
	const silence = 150 * time.Second
	script := func(request fakeResponsesRequest) fakeResponsesReply {
		reply := textReply(request, "resp_a10", "a10 after the silence")
		reply.pause, reply.pauseAfter = silence, 0
		return reply
	}
	t.Run("keepalives", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		run, upstream := newClaudeResponsesRun(t, ctx, binary, "", script)
		started := time.Now()
		observation := observeTurn(t, ctx, run, upstream, binary, []string{"-p", "Reply with the fixture text", "--output-format", "json"}, claudeWatchdog)
		if observation.err != nil || observation.turn.IsError || !strings.Contains(observation.turn.Result, "a10 after the silence") {
			t.Fatalf("Claude Code did not outlast the silence: %+v stderr=%s", observation.turn, observation.stderr)
		}
		if elapsed := time.Since(started); elapsed < silence {
			t.Fatalf("the turn took %s, less than the scripted silence", elapsed)
		}
		if len(observation.requests) != 1 || upstream.abandoned.Load() != 0 {
			t.Fatalf("requests=%d abandoned=%d, want one uninterrupted request", len(observation.requests), upstream.abandoned.Load())
		}
		records := run.exchanges.snapshot()
		events := records[0].snapshot()
		begun := slices.IndexFunc(events, func(event downstreamEvent) bool { return event.kind == "write" })
		if begun < 0 {
			t.Fatal("nothing was written downstream")
		}
		from := events[begun].at
		gap, keepalives := longestGap(events, from, from.Add(silence-time.Second))
		if keepalives < 8 || gap > 25*time.Second {
			t.Errorf("during the silence: %d keepalives, longest gap %s", keepalives, gap)
		}
	})
	t.Run("negative_control_without_keepalives", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		run, upstream := newClaudeResponsesRun(t, ctx, binary, "", script)
		run.exchanges.dropKeepalives.Store(true)
		started := time.Now()
		stdout, stderr, err := run.claude(t, ctx, binary, []string{"-p", "Reply with the fixture text", "--output-format", "json"}, claudeWatchdog)
		elapsed := time.Since(started)
		turn, parsed := claudeAcceptanceResult(stdout)
		t.Logf("without keepalives Claude Code ended after %s: err=%v result=%q stderr=%s", elapsed.Round(time.Second), err, turn.Result, stderr)
		if err == nil || (parsed && !turn.IsError) || elapsed >= silence {
			t.Fatalf("the turn survived the silence without keepalives: err=%v parsed=%v elapsed=%s", err, parsed, elapsed)
		}
		// Observed with 2.1.329: its byte watchdog ends the turn at the
		// 60-second limit, without a retry.
		if elapsed < 55*time.Second || !strings.Contains(turn.Result, "Stream idle timeout") {
			t.Errorf("the turn ended after %s with %q, not at the byte watchdog", elapsed, turn.Result)
		}
		// Claude Code went away, so the Exchange abandons the silent upstream
		// stream.
		deadline := time.Now().Add(30 * time.Second)
		for upstream.abandoned.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if upstream.abandoned.Load() == 0 {
			t.Error("the silent upstream stream was not abandoned")
		}
		requests, _ := upstream.snapshot()
		if len(requests) != 1 {
			t.Errorf("want one upstream request, got %d", len(requests))
		}
		task7Must(t, run.fixture.pipeline.Drain(ctx))
	})
}

// A12: under Review the decision takes 150 s, longer than Claude Code's
// 60-second byte watchdog. The keepalives the Exchange writes while it waits
// for the decision keep Claude Code connected, and the turn completes.
func testClaudeOnResponsesLongApproval(t *testing.T, binary string) {
	const decision = 150 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	const final = "a12 done"
	run, upstream := newClaudeResponsesRun(t, ctx, binary, environment.ToolPolicyReview, func(request fakeResponsesRequest) fakeResponsesReply {
		if len(request.shape.outputs()) == 0 {
			return claudeToolTurn(request, "a12", responsesCall{id: "call_a12", name: "Bash", arguments: `{"command":"echo a12-marker","description":"Print a marker"}`})
		}
		return textReply(request, "resp_a12", final)
	})
	type outcome struct {
		stdout, stderr string
		err            error
	}
	done := make(chan outcome, 1)
	go func() {
		stdout, stderr, err := run.claude(t, ctx, binary, []string{"-p", "Run the marker command", "--output-format", "json"}, claudeWatchdog)
		done <- outcome{stdout, stderr, err}
	}()
	pending := waitForApproval(t, ctx, run, done)
	asked := time.Now()
	if until := time.Until(pending.ExpiresAt); until < decision+30*time.Second {
		t.Fatalf("the approval expires in %s, before the scripted decision", until)
	}
	select {
	case result := <-done:
		t.Fatalf("Claude Code gave up while the decision was open: %+v", result)
	case <-time.After(decision):
	}
	approveOnce(t, ctx, run, pending.ID, "a12-approve-after-the-watchdog")
	result := <-done
	turn, parsed := claudeAcceptanceResult(result.stdout)
	if result.err != nil || !parsed || turn.IsError || !strings.Contains(turn.Result, final) {
		t.Fatalf("Claude Code turn failed: err=%v stdout=%s stderr=%s", result.err, result.stdout, result.stderr)
	}
	task7Must(t, run.fixture.pipeline.Drain(ctx))
	requests := requireUpstreamClean(t, upstream)
	if len(requests) != 2 || !bytes.Contains(requests[1].shape.outputs()["call_a12"], []byte("a12-marker")) {
		t.Fatalf("Claude Code did not run the approved call: %d requests", len(requests))
	}
	events := run.exchanges.snapshot()[0].snapshot()
	gap, keepalives := longestGap(events, asked, asked.Add(decision-time.Second))
	if keepalives < 8 || gap > 25*time.Second {
		t.Errorf("during the decision: %d keepalives, longest gap %s", keepalives, gap)
	}
}
