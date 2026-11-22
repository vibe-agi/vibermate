package openairesponses_test

import (
	"bytes"
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// A turn that reaches max_output_tokens inside a function call's arguments
// ends with an incomplete terminal holding the call unfinished. The call
// cannot run, so it is dropped and declared, and the client sees the
// max_tokens stop it recovers from instead of an error.

const (
	cutOffMessage = `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Let me check."}]}`
	cutOffCall    = `{"id":"fc_1","type":"function_call","status":"incomplete","call_id":"call_1","name":"Bash","arguments":"{\"comm"}`
)

func cutOffTerminal(kind, status, output string) string {
	details := ""
	if status == "incomplete" {
		details = `,"incomplete_details":{"reason":"max_output_tokens"}`
	}
	return `{"type":"response.` + kind + `","response":{"id":"resp_1","created_at":1,"model":"m","status":"` + status + `"` +
		details + `,"output":[` + output + `]}}`
}

// cutOffStream is a text item, then a call that the output limit stops
// mid-arguments. done adds the call's output_item.done with its incomplete
// status before the terminal.
func cutOffStream(done bool, terminal string) []string {
	payloads := []string{
		`{"type":"response.created","response":{"id":"resp_1","created_at":1,"model":"m","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.done","output_index":0,"item":` + cutOffMessage + `}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"Bash","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"comm"}`,
	}
	if done {
		payloads = append(payloads, `{"type":"response.output_item.done","output_index":1,"item":`+cutOffCall+`}`)
	}
	return append(payloads, terminal)
}

func TestCutOffCallEndsTheTurnAtTheOutputLimit(t *testing.T) {
	t.Parallel()

	for name, payloads := range map[string][]string{
		"cut off in the terminal only":     cutOffStream(false, cutOffTerminal("incomplete", "incomplete", cutOffMessage+","+cutOffCall)),
		"completed unfinished first":       cutOffStream(true, cutOffTerminal("incomplete", "incomplete", cutOffMessage+","+cutOffCall)),
		"completed first, terminal empty":  cutOffStream(true, cutOffTerminal("incomplete", "incomplete", "")),
		"in progress in the terminal only": cutOffStream(false, cutOffTerminal("incomplete", "incomplete", cutOffMessage+`,{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"Bash","arguments":"{\"comm"}`)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			side, _ := newStreamSide(t)
			request := streamRequest(t, "summarized", true)
			wire := sseWire(t, payloads...)
			result := everySplit(t, side, request, wire)
			client := approved(t, result)
			if bytes.Contains(client, []byte(`"tool_use"`)) {
				t.Fatalf("the cut-off call reached the client:\n%s", client)
			}
			message := accumulate(t, client)
			if len(message.Content) != 1 || message.Content[0].Text != "Let me check." || message.StopReason != "max_tokens" {
				t.Fatalf("message = %+v", message)
			}
			if intents := result.terminal.ToolIntents(); len(intents) != 0 {
				t.Fatalf("intents = %+v", intents)
			}
			decoded := result.terminal.DecodedResponse()
			if decoded.StopReason != protocolcore.StopReasonMaxTokens || len(decoded.Blocks) != 1 ||
				decoded.Blocks[0].Kind != protocolcore.BlockText {
				t.Fatalf("decoded = %+v", decoded)
			}
			if notices := noticeLines(result.terminal.TranslationReport()); !slices.Contains(notices, "incomplete_tool_call_not_forwarded $.output[1]") {
				t.Fatalf("notices = %v", notices)
			}

			// A Stream=false request aggregates the same stream alike.
			aggregating, err := side.NewAggregatingStream(streamContext, streamRequest(t, "summarized", false))
			if err != nil {
				t.Fatal(err)
			}
			if out, err := aggregating.Feed(context.Background(), wire); err != nil || len(out) != 0 {
				t.Fatalf("aggregating Feed = %q, %v", out, err)
			}
			aggregated, err := aggregating.FinishDecoded(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got := aggregated.DecodedResponse(); !reflect.DeepEqual(got, decoded) || len(aggregated.ToolIntents()) != 0 {
				t.Fatalf("aggregated = %+v, want %+v", got, decoded)
			}
		})
	}
}

func TestUnaryCutOffCallEndsWithTheTerminalsStopReason(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		reason string
		want   protocolcore.StopReason
	}{
		{"max_output_tokens", protocolcore.StopReasonMaxTokens},
		{"content_filter", protocolcore.StopReasonRefusal},
	} {
		status := `"status":"incomplete","incomplete_details":{"reason":"` + test.reason + `"}`
		response, notices := mustDecodeUnary(t, cutOffMessage+","+cutOffCall, status, "")
		if response.StopReason != test.want || len(response.Blocks) != 1 || response.Blocks[0].Kind != protocolcore.BlockText ||
			!slices.Equal(notices, []string{"incomplete_tool_call_not_forwarded $.output[1]"}) {
			t.Fatalf("%s: response %+v, notices %v", test.reason, response, notices)
		}
	}
}

// Only an incomplete terminal may leave a call unfinished: in a completed
// response the same call is inconsistent and fails closed, wherever the
// stream showed it.
func TestCutOffCallInACompletedResponseFailsClosed(t *testing.T) {
	t.Parallel()

	for name, payloads := range map[string][]string{
		"in the terminal output":    cutOffStream(false, cutOffTerminal("completed", "completed", cutOffMessage+","+cutOffCall)),
		"completed first":           cutOffStream(true, cutOffTerminal("completed", "completed", cutOffMessage+","+cutOffCall)),
		"completed first only":      cutOffStream(true, cutOffTerminal("completed", "completed", cutOffMessage)),
		"completed, terminal empty": cutOffStream(true, cutOffTerminal("completed", "completed", "")),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			side, _ := newStreamSide(t)
			result, err := feed(t, side, streamRequest(t, "summarized", true), sseWire(t, payloads...))
			if protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidProviderResponse || failurePathOf(err) != "$.output[1].status" {
				t.Fatalf("err = %v, want invalid_provider_response at $.output[1].status", err)
			}
			if bytes.Contains(result.live, []byte(`"tool_use"`)) {
				t.Fatal("the call reached the client")
			}
		})
	}
	if _, _, err := decodeUnary(t, cutOffMessage+","+cutOffCall, "", ""); protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidProviderResponse ||
		failurePathOf(err) != "$.output[1].status" {
		t.Fatalf("unary err = %v", err)
	}
	queued := `{"id":"fc_1","type":"function_call","status":"queued","call_id":"call_1","name":"Bash","arguments":"{}"}`
	status := `"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}`
	if _, _, err := decodeUnary(t, queued, status, ""); protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidProviderResponse ||
		failurePathOf(err) != "$.output[0].status" {
		t.Fatalf("an unknown call status: %v", err)
	}
}
