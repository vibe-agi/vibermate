//go:build darwin

package main

import (
	"encoding/json"
	"testing"
)

func TestModelPlanObservationRequiresExactThreadTurnAndPlan(t *testing.T) {
	for _, scenario := range []string{"exact", "wrong_thread", "wrong_turn", "wrong_step", "wrong_status", "extra_step", "wrong_terminal_thread", "wrong_terminal_turn"} {
		t.Run(scenario, func(t *testing.T) {
			observer := &modelIdleClientObservation{}
			frames := []map[string]any{{"id": 2, "result": map[string]any{"thread": map[string]any{"id": "thread_exact"}}}, {"id": 3, "result": map[string]any{"turn": map[string]any{"id": "turn_exact"}}}}
			thread, turn, step, status := "thread_exact", "turn_exact", "Check synthetic progress", "completed"
			switch scenario {
			case "wrong_thread":
				thread = "thread_wrong"
			case "wrong_turn":
				turn = "turn_wrong"
			case "wrong_step":
				step = "not the planned step"
			case "wrong_status":
				status = "in_progress"
			}
			plan := []any{map[string]any{"step": step, "status": status}}
			if scenario == "extra_step" {
				plan = append(plan, map[string]any{"step": step, "status": status})
			}
			frames = append(frames, map[string]any{"method": "turn/plan/updated", "params": map[string]any{"threadId": thread, "turnId": turn, "plan": plan}})
			thread, turn = "thread_exact", "turn_exact"
			if scenario == "wrong_terminal_thread" {
				thread = "wrong"
			}
			if scenario == "wrong_terminal_turn" {
				turn = "wrong"
			}
			frames = append(frames, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "completed"}}})
			var observedErr error
			for _, frame := range frames {
				body, _ := json.Marshal(frame)
				body = append(body, '\n')
				if _, err := observer.Write(body); err != nil {
					observedErr = err
					break
				}
			}
			if (observedErr == nil) != (scenario == "exact") {
				t.Fatal("unbound/mismatched plan notification accepted")
			}
		})
	}
}
