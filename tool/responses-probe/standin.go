package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// standInTurn describes a scripted Responses turn in the public API's event
// sequence (internal/anthropicresponses/testdata/claude-2.1.329/
// public.responses.sse): one reasoning item with a summary part and its
// encrypted content, then one assistant message.
type standInTurn struct {
	responseID string
	model      string
	reasoning  string
	encrypted  string
	text       string
	usage      standInUsage
}

type standInUsage struct {
	input, cached, output, reasoning int
}

// sseEvent renders one event.
func sseEvent(name string, payload map[string]any) []byte {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return []byte("event: " + name + "\ndata: " + string(data) + "\n\n")
}

// events returns the turn's events, each rendered on its own.
func (turn standInTurn) events() [][]byte {
	sequence := 0
	event := func(name string, payload map[string]any) []byte {
		payload["type"] = name
		payload["sequence_number"] = sequence
		sequence++
		return sseEvent(name, payload)
	}
	response := func(status string, output []any, usage map[string]any) map[string]any {
		value := map[string]any{
			"id": turn.responseID, "object": "response", "created_at": 1763700000,
			"status": status, "model": turn.model, "output": output,
		}
		if usage != nil {
			value["usage"] = usage
		}
		return value
	}
	reasoningID, messageID := "rs_"+turn.responseID, "msg_"+turn.responseID
	summary := []any{map[string]any{"type": "summary_text", "text": turn.reasoning}}
	reasoningItem := map[string]any{"id": reasoningID, "type": "reasoning", "encrypted_content": turn.encrypted, "summary": summary}
	part := map[string]any{"type": "output_text", "annotations": []any{}, "text": turn.text}
	messageItem := map[string]any{"id": messageID, "type": "message", "status": "completed", "role": "assistant", "content": []any{part}}
	usage := map[string]any{
		"input_tokens":          turn.usage.input,
		"input_tokens_details":  map[string]any{"cached_tokens": turn.usage.cached},
		"output_tokens":         turn.usage.output,
		"output_tokens_details": map[string]any{"reasoning_tokens": turn.usage.reasoning},
		"total_tokens":          turn.usage.input + turn.usage.output,
	}
	return [][]byte{
		event("response.created", map[string]any{"response": response("in_progress", []any{}, nil)}),
		event("response.in_progress", map[string]any{"response": response("in_progress", []any{}, nil)}),
		event("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": reasoningID, "type": "reasoning", "summary": []any{}}}),
		event("response.reasoning_summary_part.added", map[string]any{"item_id": reasoningID, "output_index": 0, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}),
		event("response.reasoning_summary_text.delta", map[string]any{"item_id": reasoningID, "output_index": 0, "summary_index": 0, "delta": turn.reasoning}),
		event("response.reasoning_summary_text.done", map[string]any{"item_id": reasoningID, "output_index": 0, "summary_index": 0, "text": turn.reasoning}),
		event("response.reasoning_summary_part.done", map[string]any{"item_id": reasoningID, "output_index": 0, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": turn.reasoning}}),
		event("response.output_item.done", map[string]any{"output_index": 0, "item": reasoningItem}),
		event("response.output_item.added", map[string]any{"output_index": 1, "item": map[string]any{"id": messageID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}),
		event("response.content_part.added", map[string]any{"item_id": messageID, "output_index": 1, "content_index": 0, "part": map[string]any{"type": "output_text", "annotations": []any{}, "text": ""}}),
		event("response.output_text.delta", map[string]any{"item_id": messageID, "output_index": 1, "content_index": 0, "delta": turn.text}),
		event("response.output_text.done", map[string]any{"item_id": messageID, "output_index": 1, "content_index": 0, "text": turn.text}),
		event("response.content_part.done", map[string]any{"item_id": messageID, "output_index": 1, "content_index": 0, "part": part}),
		event("response.output_item.done", map[string]any{"output_index": 1, "item": messageItem}),
		event("response.completed", map[string]any{"response": response("completed", []any{reasoningItem, messageItem}, usage)}),
	}
}

func (turn standInTurn) stream() []byte {
	var stream []byte
	for _, event := range turn.events() {
		stream = append(stream, event...)
	}
	return stream
}

// planSender answers the offline plan pass without any network: every
// request succeeds with a scripted turn, so the bodies that depend on an
// earlier reply (Q4 and Q9 turns, the Q10 tampered seal) can be built and
// reviewed. Nothing it returns is reported as a finding.
type planSender struct {
	models []string
	count  int
}

func (plan *planSender) send(_ context.Context, request outbound) *exchangeRecord {
	plan.count++
	record := &exchangeRecord{status: http.StatusOK, header: http.Header{}}
	if request.method == http.MethodGet {
		var data []any
		for _, model := range plan.models {
			data = append(data, map[string]any{"id": model, "object": "model", "created": 0, "owned_by": "system"})
		}
		record.body, _ = json.Marshal(map[string]any{"object": "list", "data": data})
		record.contentType = "application/json"
		return record
	}
	var envelope struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(request.body, &envelope)
	turn := standInTurn{
		responseID: fmt.Sprintf("resp_plan_%03d", plan.count),
		model:      envelope.Model,
		reasoning:  "Plan stand-in reasoning summary.",
		encrypted:  fmt.Sprintf("gAAAAABplan-stand-in-%03d-%s", plan.count, strings.Repeat("x", 32)),
		text:       "ok",
		usage:      standInUsage{input: 1, output: 1},
	}
	record.contentType = "text/event-stream"
	record.stream = turn.stream()
	for _, event := range turn.events() {
		name := strings.TrimPrefix(strings.SplitN(string(event), "\n", 2)[0], "event: ")
		timed := timedEvent{name: name}
		if keepEventData(name) {
			data := strings.SplitN(string(event), "\ndata: ", 2)[1]
			timed.data = []byte(strings.TrimSuffix(data, "\n\n"))
		}
		record.events = append(record.events, timed)
	}
	return record
}
