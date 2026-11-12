package messagetransform

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func outcomePipeline(t *testing.T, policies ...Policy) *PipelineTurn {
	t.Helper()
	pipeline, err := CompilePipeline(policies, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return pipeline.NewTurn()
}

func outcomeRequest(body string) RequestMessage {
	return RequestMessage{Method: "POST", Path: "/v1/messages", Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(body)}
}

func TestPipelineReportsWhatEachScriptDid(t *testing.T) {
	turn := outcomePipeline(t,
		Policy{RequestJavaScript: `const unused = request.body.length;`},
		Policy{RequestJavaScript: `request.body = request.body.replace("secret", "[masked]");`, ResponseJavaScript: `if (response.body.includes("b")) { response.body = response.body.toUpperCase(); }`},
		Policy{RequestJavaScript: `request.headers["x-audit"] = ["on"];`},
	)
	if got := turn.Outcomes(); len(got) != 0 {
		t.Fatalf("outcomes before any stage ran: %+v", got)
	}
	output, err := turn.ApplyRequest(context.Background(), outcomeRequest(`{"text":"secret"}`))
	if err != nil || string(output.Body) != `{"text":"[masked]"}` {
		t.Fatalf("request: %q %v", output.Body, err)
	}
	for _, event := range []string{"a", "b", "c"} {
		if _, err := turn.ApplyResponse(context.Background(), ResponseMessage{StatusCode: 200, Streaming: true, Headers: http.Header{}, Body: []byte(event)}); err != nil {
			t.Fatal(err)
		}
	}
	got := turn.Outcomes()
	want := []StageOutcome{
		{Transform: 0, Stage: StageRequest, Outcome: OutcomeUnchanged, Applications: 1, BytesBefore: 17, BytesAfter: 17},
		{Transform: 1, Stage: StageRequest, Outcome: OutcomeChanged, Applications: 1, Changed: 1, BodyChanged: true, BytesBefore: 17, BytesAfter: 19},
		{Transform: 1, Stage: StageResponse, Outcome: OutcomeChanged, Applications: 3, Changed: 1, BodyChanged: true, BytesBefore: 3, BytesAfter: 3},
		{Transform: 2, Stage: StageRequest, Outcome: OutcomeChanged, Applications: 1, Changed: 1, HeadersChanged: true, BytesBefore: 19, BytesAfter: 19},
	}
	if len(got) != len(want) {
		t.Fatalf("outcomes: %+v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("outcome %d:\n got %+v\nwant %+v", index, got[index], want[index])
		}
	}
	// A replayed request is the same application, not a second one.
	if _, err := turn.ApplyRequest(context.Background(), outcomeRequest(`{"text":"secret"}`)); err != nil {
		t.Fatal(err)
	}
	if again := turn.Outcomes(); again[1].Applications != 1 {
		t.Fatalf("replay counted twice: %+v", again[1])
	}
}

func TestScriptBlocksWithADeclaredReason(t *testing.T) {
	turn := outcomePipeline(t,
		Policy{RequestJavaScript: `if (request.body.includes("rm -rf")) { runtime.block("dangerous-command"); }`},
		Policy{RequestJavaScript: `request.body = "never reached";`},
	)
	_, err := turn.ApplyRequest(context.Background(), outcomeRequest(`{"text":"rm -rf /"}`))
	if !errors.Is(err, ErrBlocked) || !errors.Is(err, ErrExecutionFailed) {
		t.Fatalf("block did not refuse under both contracts: %v", err)
	}
	if strings.Contains(err.Error(), "dangerous-command") || strings.Contains(err.Error(), "rm -rf") {
		t.Fatalf("refusal error carries script-supplied text: %v", err)
	}
	got := turn.Outcomes()
	if len(got) != 1 || got[0].Outcome != OutcomeBlocked || got[0].BlockReason != "dangerous-command" || got[0].Transform != 0 {
		t.Fatalf("outcomes: %+v", got)
	}
}

func TestBlockIsFinalAndItsReasonIsBounded(t *testing.T) {
	// Catching the refusal does not undo it.
	turn := outcomePipeline(t, Policy{RequestJavaScript: `try { runtime.block("policy"); } catch (ignored) {} request.body = "{}";`})
	if _, err := turn.ApplyRequest(context.Background(), outcomeRequest(`{"a":1}`)); !errors.Is(err, ErrBlocked) {
		t.Fatalf("caught block let the message through: %v", err)
	}
	if got := turn.Outcomes(); got[0].Outcome != OutcomeBlocked || got[0].BlockReason != "policy" {
		t.Fatalf("outcomes: %+v", got)
	}
	// A reason outside the label alphabet could carry message content, so it
	// refuses the message without being recorded.
	for _, source := range []string{
		`runtime.block(request.body);`,
		`runtime.block("Has Spaces");`,
		`runtime.block("-leading");`,
		`runtime.block("` + strings.Repeat("a", 65) + `");`,
		`runtime.block();`,
		`runtime.block(42);`,
		// Swallowing the complaint about the reason must not let the message through.
		`try { runtime.block(request.body); } catch (ignored) {} request.body = "{}";`,
	} {
		turn := outcomePipeline(t, Policy{RequestJavaScript: source})
		_, err := turn.ApplyRequest(context.Background(), outcomeRequest(`{"text":"private words"}`))
		if !errors.Is(err, ErrExecutionFailed) || errors.Is(err, ErrBlocked) {
			t.Fatalf("%s: %v", source, err)
		}
		got := turn.Outcomes()
		if len(got) != 1 || got[0].Outcome != OutcomeFailed || got[0].BlockReason != "" {
			t.Fatalf("%s: %+v", source, got)
		}
	}
}

func TestThrowingScriptStillRefusesAndIsRecordedAsFailed(t *testing.T) {
	turn := outcomePipeline(t, Policy{RequestJavaScript: `throw new Error(request.body);`})
	_, err := turn.ApplyRequest(context.Background(), outcomeRequest(`{"text":"private words"}`))
	if !errors.Is(err, ErrExecutionFailed) || errors.Is(err, ErrBlocked) || strings.Contains(err.Error(), "private") {
		t.Fatalf("throw: %v", err)
	}
	if got := turn.Outcomes(); len(got) != 1 || got[0].Outcome != OutcomeFailed {
		t.Fatalf("outcomes: %+v", got)
	}
}
