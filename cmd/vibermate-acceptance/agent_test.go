package main

import (
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

func TestAgentEvidenceTrustsOnlyAssistantOutput(t *testing.T) {
	t.Parallel()

	run := &agentRun{marker: "VIBEMATE_STREAM_OK"}
	run.observeLine(mustJSON(t, map[string]any{
		"type": "user",
		"message": map[string]any{
			"content": []any{
				map[string]any{
					"type": "text",
					"text": "VIBEMATE_STREAM_OK",
				},
			},
		},
	}))
	if run.markerSeen {
		t.Fatal("User prompt text satisfied the assistant marker")
	}

	run.observeLine(mustJSON(t, map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"type": "content_block_delta",
			"delta": map[string]any{
				"type": "text_delta",
				"text": "VIBEMATE_STREAM_",
			},
		},
	}))
	run.observeLine(mustJSON(t, map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"type": "content_block_delta",
			"delta": map[string]any{
				"type": "text_delta",
				"text": "OK",
			},
		},
	}))
	if run.deltas != 2 {
		t.Fatalf("delta count = %d", run.deltas)
	}
	if !run.markerSeen {
		t.Fatal("Split assistant marker was not observed")
	}
	run.observeLine(mustJSON(t, map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"type":  "content_block_start",
			"index": 1,
			"content_block": map[string]any{
				"type": "tool_use",
				"name": "TodoWrite",
			},
		},
	}))
	if run.toolUses != 1 {
		t.Fatalf("tool use count = %d", run.toolUses)
	}
}

func TestAcceptanceEnvironmentReplacesAmbientProviderCredentials(t *testing.T) {
	t.Parallel()

	environment := acceptanceEnvironment([]string{
		"PATH=/usr/bin",
		"ANTHROPIC_API_KEY=ambient",
		"ANTHROPIC_AUTH_TOKEN=ambient",
		"ANTHROPIC_BASE_URL=https://ambient.invalid",
		"ANTHROPIC_CUSTOM_HEADERS=Authorization: ambient",
		"CLAUDE_CODE_OAUTH_TOKEN=ambient",
		"OPENAI_ACCESS_TOKEN=ambient",
		"OPENAI_API_KEY=ambient",
	})
	expected := []string{
		"ANTHROPIC_API_KEY=vibermate-assembly-placeholder",
		"PATH=/usr/bin",
	}
	if len(environment) != len(expected) {
		t.Fatalf("environment = %v", environment)
	}
	for index := range expected {
		if environment[index] != expected[index] {
			t.Fatalf("environment = %v", environment)
		}
	}
}

func TestFixedClaudeInvocationEnablesBoundedAPIDiagnostics(t *testing.T) {
	t.Parallel()

	arguments := fixedClaudeArguments(
		"/absolute/claude",
		"",
	)
	if !slices.Contains(arguments, "--debug") ||
		!slices.Contains(arguments, "api") {
		t.Fatalf("Claude arguments = %q", arguments)
	}
	if slices.Contains(arguments, "") ||
		!slices.Contains(arguments, "--tools=") {
		t.Fatalf("Claude arguments contain an unsafe empty argv: %q", arguments)
	}
	withTool := fixedClaudeArguments("/absolute/claude", "TodoWrite")
	if !slices.Contains(withTool, "--tools=TodoWrite") {
		t.Fatalf("Claude tool arguments = %q", withTool)
	}
}

func TestAgentPromptUsesStandardInputInsteadOfProcessArguments(t *testing.T) {
	t.Parallel()

	const prompt = "prompt-that-must-not-enter-argv"
	command := newAgentCommand(config{
		launcherPath: "/absolute/launcher",
		claudePath:   "/absolute/claude",
	}, "/trusted/workspace", prompt, "")
	if slices.Contains(command.Args, prompt) {
		t.Fatalf("Claude arguments exposed the prompt: %q", command.Args)
	}

	payload, err := io.ReadAll(command.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != prompt {
		t.Fatalf("Claude stdin = %q", payload)
	}
	if command.Dir != "/trusted/workspace" {
		t.Fatalf("Claude working directory = %q", command.Dir)
	}
}

func TestAgentFailureEvidenceExposesOnlyStableClassification(t *testing.T) {
	t.Parallel()

	run := &agentRun{
		stderr: newBoundedBuffer(256),
	}
	run.observeLine(mustJSON(t, map[string]any{
		"type":    "result",
		"subtype": "error_during_execution",
		"result": "provider response included secret-value and " +
			`{"reasonCode":"provider_status_rejected","providerStatus":404,` +
			`"providerField":"max_completion_tokens"}`,
	}))
	_, _ = run.stderr.Write([]byte(
		"upstream text contains another-secret-value",
	))
	evidence := run.safeFailureEvidence()
	for _, required := range []string{
		"stdoutLines=0",
		"deltas=0",
		"lastEnvelope=result",
		"reasonCode=provider_status_rejected",
		"providerStatus=404",
		"providerField=max_completion_tokens",
		"resultSubtype=error_during_execution",
	} {
		if !strings.Contains(evidence, required) {
			t.Fatalf("failure evidence = %q", evidence)
		}
	}
	if strings.Contains(evidence, "keywords=") {
		t.Fatalf("failure evidence = %q", evidence)
	}
	for _, forbidden := range []string{
		"secret-value",
		"upstream text",
	} {
		if strings.Contains(evidence, forbidden) {
			t.Fatalf("failure evidence leaked %q: %q", forbidden, evidence)
		}
	}
}

func TestAgentFailureEvidenceReadsBoundedStderrWithoutReturningIt(t *testing.T) {
	t.Parallel()

	run := &agentRun{stderr: newBoundedBuffer(96)}
	_, _ = run.stderr.Write([]byte(
		`private body {"reasonCode":"provider_response_invalid",` +
			`"providerStatus":502} trailing-private-body-that-overflows`,
	))
	evidence := run.safeFailureEvidence()
	if !strings.Contains(
		evidence,
		"reasonCode="+string(exchange.ReasonProviderResponseInvalid),
	) ||
		!strings.Contains(evidence, "providerStatus=502") ||
		!strings.Contains(evidence, "shape=") ||
		!strings.Contains(evidence, "stderrTruncated=true") ||
		strings.Contains(evidence, "private body") {
		t.Fatalf("failure evidence = %q", evidence)
	}
}

func TestAgentFailureEvidenceClassifiesClientSummaryWithoutReturningIt(
	t *testing.T,
) {
	t.Parallel()

	run := &agentRun{stderr: newBoundedBuffer(256)}
	_, _ = run.stderr.Write([]byte(
		"API Error: 502 model relay-model was not found; private-detail",
	))
	evidence := run.safeFailureEvidence()
	if !strings.Contains(evidence, "agentStatus=502") ||
		!strings.Contains(evidence, "category=model_unavailable") ||
		!strings.Contains(evidence, "keywords=api,error,found,model,not") ||
		strings.Contains(evidence, "relay-model") ||
		strings.Contains(evidence, "private-detail") {
		t.Fatalf("failure evidence = %q", evidence)
	}
}

func TestAgentProcessFailureDoesNotWrapNil(t *testing.T) {
	t.Parallel()

	run := &agentRun{stderr: newBoundedBuffer(1)}
	err := agentProcessFailure("normal", 1, nil, run)
	if err == nil ||
		err.Error() != "normal Claude exit=1 stdoutLines=0 deltas=0 stderrBytes=0" ||
		strings.Contains(err.Error(), "%!") {
		t.Fatalf("process failure = %v", err)
	}
}

func TestOfflineAcceptanceRequiresReleaseThenOnlineSettlement(t *testing.T) {
	t.Parallel()

	if err := requireReleasedOfflineRequest(offlinehold.Snapshot{
		State:          offlinehold.StateReleasing,
		ActiveEgress:   1,
		QueuedRequests: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := requireReleasedOfflineRequest(offlinehold.Snapshot{
		State:          offlinehold.StateOnline,
		ActiveEgress:   0,
		QueuedRequests: 0,
	}); err == nil {
		t.Fatal("Immediate online state was accepted as queued-request release")
	}
	if err := requireSettledOfflineState(offlinehold.Snapshot{
		State:          offlinehold.StateOnline,
		ActiveEgress:   0,
		QueuedRequests: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := requireSettledOfflineState(offlinehold.Snapshot{
		State:          offlinehold.StateReleasing,
		ActiveEgress:   1,
		QueuedRequests: 0,
	}); err == nil {
		t.Fatal("Active egress was accepted as settled")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
