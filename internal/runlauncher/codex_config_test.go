package runlauncher

import (
	"slices"
	"testing"
)

func TestCodexConfigReplayPreservesArgumentConsumptionAndLiterals(t *testing.T) {
	for _, original := range [][]string{
		{"-c", "model_verbosity=low", "app-server", "--stdio", "--config=model_reasoning_effort=high"},
		{"exec", "resume", "--last", "-cmodel_reasoning_effort=high", "prompt"},
		{"exec", "--image", "one.png", "-hc=model_reasoning_effort=high", "resume", "thread"},
		{"exec", "--image=--", "--config", "model_reasoning_effort=high", "--", "-cmodel_provider=literal"},
		{"exec", "-c", "model_reasoning_effort=high", "--model=-cmodel_provider=opaque", "prompt -c key=value", "--", "--config", "literal"},
	} {
		t.Run(original[0], func(t *testing.T) {
			before := slices.Clone(original)
			prepared := append([]string{"--config", "features.responses_websockets=false"}, original...)
			preparedBefore := slices.Clone(prepared)
			got, err := replayCodexConfiguration(original, prepared)
			if err != nil {
				t.Fatal(err)
			}
			// The original options must remain barriers for variadic image
			// arguments; only the actual literal suffix may move as a group.
			end := len(prepared)
			_, _, err = walkCodexInvocationWithLiteral(prepared, nil, &end)
			if err != nil || !slices.Equal(got[:end], prepared[:end]) || !slices.Equal(got[len(got)-(len(prepared)-end):], prepared[end:]) {
				t.Fatalf("original argument consumption changed: %v", got)
			}
			if !slices.Equal(original, before) || !slices.Equal(prepared, preparedBefore) {
				t.Fatal("normalization mutated input evidence")
			}
			kind, _, _ := parseCodexInvocation(original)
			if gotKind, _, err := parseCodexInvocation(got); err != nil || gotKind != kind {
				t.Fatalf("replay changed process ownership: %v", got)
			}
		})
	}
}

func TestCodexConfigReplayDoesNotCollectOpaqueConfigLookingValues(t *testing.T) {
	original := []string{"exec", "--image=-cimage", "--model=-cmodel", "prompt --config=opaque", "--", "-c", "model_provider=literal"}
	prepared := append([]string{"--config", "features.responses_websockets=false"}, original...)
	got, err := replayCodexConfiguration(original, prepared)
	if err != nil || !slices.Equal(got, prepared) {
		t.Fatalf("opaque values were treated as configuration: %v err=%v", got, err)
	}
}
