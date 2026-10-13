package openairesponses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestModelSSEIdleHeldArgumentsStillReportSemanticProgress(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprintf("allow=%t", allow), func(t *testing.T) {
			codec := newTestCodec(t)
			request, _, err := codec.DecodeCompatibleClientRequest([]byte(`{"model":"fixture","input":[{"role":"user","content":"Synthetic plan-only diagnostic"}],"stream":true,"store":false,"tools":[{"type":"function","name":"update_plan","parameters":{"type":"object","properties":{"plan":{"type":"array","items":{"type":"object","properties":{"step":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","completed"]}},"required":["step","status"],"additionalProperties":false}}},"required":["plan"],"additionalProperties":false}}]}`))
			if err != nil {
				t.Fatal("declared harmless request rejected", protocolcore.ReasonOf(err))
			}
			stream, err := codec.NewProviderStream(request)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			arguments := `{"plan":[{"step":"Check synthetic progress","status":"completed"}]}`
			item := func(args, status string) map[string]any {
				return map[string]any{"id": "fc_plan", "type": "function_call", "call_id": "plan_call", "name": "update_plan", "arguments": args, "status": status}
			}
			var safe, held []byte
			feed := func(name string, payload any, withheld bool) {
				t.Helper()
				wire := appendResponseEvent(t, name, payload)
				before := stream.SemanticProgress()
				out, err := stream.Feed(ctx, wire)
				if err != nil || stream.SemanticProgress() <= before {
					t.Fatalf("complete %s event did not report progress: reason=%s", name, protocolcore.ReasonOf(err))
				}
				if withheld && len(out) != 0 {
					t.Fatalf("actionable %s escaped before decision", name)
				}
				if withheld {
					held = append(held, wire...)
				}
				safe = append(safe, out...)
			}
			feed("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_idle", "created_at": 1, "model": "fixture", "status": "in_progress", "output": []any{}}}, false)
			feed("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item("", "in_progress")}, true)
			start := time.Now()
			for index := 0; index < 20; index++ {
				select {
				case <-ctx.Done():
					t.Fatal("bounded codec diagnostic expired")
				case <-time.After(20 * time.Millisecond):
				}
				delta := arguments[len(arguments)*index/20 : len(arguments)*(index+1)/20]
				feed("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_plan", "output_index": 0, "delta": delta}, true)
				t.Logf("delta=%d elapsedMS=%d semanticProgress=%d safeBytes=%d", index+1, time.Since(start).Milliseconds(), stream.SemanticProgress(), len(safe))
			}
			feed("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item(arguments, "completed")}, true)
			feed("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_idle", "created_at": 1, "model": "fixture", "status": "completed", "output": []any{item(arguments, "completed")}}}, true)
			for _, forbidden := range [][]byte{[]byte("function_call"), []byte("update_plan"), []byte("plan_call")} {
				if bytes.Contains(safe, forbidden) {
					t.Fatal("held action leaked into safe output")
				}
			}
			terminal, err := stream.FinishDecoded(ctx)
			if err != nil || len(terminal.ToolIntents()) != 1 {
				t.Fatalf("valid declared terminal failed: reason=%s", protocolcore.ReasonOf(err))
			}
			intent := terminal.ToolIntents()[0]
			var decoded, expected any
			if json.Unmarshal(intent.Call.Arguments.Bytes(), &decoded) != nil || json.Unmarshal([]byte(arguments), &expected) != nil {
				t.Fatal("tool argument validation failed")
			}
			got, _ := json.Marshal(decoded)
			want, _ := json.Marshal(expected)
			if intent.Call.Name != "update_plan" || !bytes.Equal(got, want) {
				t.Fatal("declared tool intent changed")
			}
			if allow {
				released, err := terminal.Approve()
				if err != nil || !bytes.Equal(released, held) {
					t.Fatal("approved held events changed")
				}
			} else {
				if err := terminal.Reject(); err != nil {
					t.Fatal(err)
				}
				if released, err := terminal.Approve(); err == nil || len(released) != 0 {
					t.Fatal("rejected tool became releasable")
				}
			}
			t.Logf("terminal progress=%d withheldBytes=%d safeBytes=%d allow=%t; no upstream keepalive", stream.SemanticProgress(), len(held), len(safe), allow)
		})
	}
}
