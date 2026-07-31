package main

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
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

func TestConfiguredToolsTrustOnlyClaudeSystemInit(t *testing.T) {
	t.Parallel()

	tools, initialized := trustedConfiguredTools(map[string]any{
		"type":    "assistant",
		"subtype": "init",
		"tools":   []any{"Write"},
	})
	if initialized || len(tools) != 0 {
		t.Fatalf("assistant envelope tools = %v initialized=%t", tools, initialized)
	}

	tools, initialized = trustedConfiguredTools(map[string]any{
		"type":    "system",
		"subtype": "init",
		"tools":   []any{"Write", "Write", "Read"},
	})
	if !initialized || !slices.Equal(tools, []string{"Read", "Write"}) {
		t.Fatalf("system init tools = %v initialized=%t", tools, initialized)
	}

	tools, initialized = trustedConfiguredTools(map[string]any{
		"type":    "system",
		"subtype": "init",
		"tools":   []any{"Write", 7},
	})
	if !initialized || len(tools) != 0 {
		t.Fatalf("malformed init tools = %v initialized=%t", tools, initialized)
	}
}

func TestWaitForConfiguredToolRequiresExactInitTool(t *testing.T) {
	t.Parallel()

	run := &agentRun{
		configurationSeen: true,
		configuredTools:   []string{"Read", "Write"},
	}
	if err := run.waitForConfiguredTool(
		context.Background(),
		"Write",
	); err != nil {
		t.Fatal(err)
	}
	if err := run.waitForConfiguredTool(
		context.Background(),
		"WriteFile",
	); err == nil {
		t.Fatal("unconfigured tool was accepted")
	}
}

func TestAcceptanceEnvironmentIsolatesEachFixedClient(t *testing.T) {
	t.Parallel()

	base := []string{
		"PATH=/usr/bin",
		"ANTHROPIC_API_KEY=ambient",
		"ANTHROPIC_AUTH_TOKEN=ambient",
		"ANTHROPIC_BASE_URL=https://ambient.invalid",
		"ANTHROPIC_CUSTOM_HEADERS=Authorization: ambient",
		"CLAUDE_CODE_OAUTH_TOKEN=ambient",
		"OPENAI_ACCESS_TOKEN=ambient",
		"OPENAI_API_KEY=ambient",
		"OPENAI_BASE_URL=https://ambient.invalid/v1",
		"CODEX_API_KEY=ambient",
		"CODEX_BASE_URL=https://ambient.invalid/v1",
		"CODEX_HOME=/ambient/codex",
		"SSL_CERT_FILE=/ambient/root.pem",
	}
	for _, test := range []struct {
		name     string
		client   acceptanceClientID
		stateDir string
		expected []string
	}{
		{
			name:   "Claude",
			client: acceptanceClientClaudeCode,
			expected: []string{
				"ANTHROPIC_API_KEY=vibermate-assembly-placeholder",
				"PATH=/usr/bin",
			},
		},
		{
			name:     "Codex",
			client:   acceptanceClientCodexCLI,
			stateDir: "/private/codex-home",
			expected: []string{
				"CODEX_HOME=/private/codex-home",
				"PATH=/usr/bin",
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			environment, err := clientAcceptanceEnvironment(
				test.client,
				base,
				test.stateDir,
			)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(environment, test.expected) {
				t.Fatalf("environment = %v", environment)
			}
		})
	}
	if _, err := clientAcceptanceEnvironment(
		acceptanceClientID("unknown"),
		base,
		"",
	); err == nil {
		t.Fatal("unknown client environment was accepted")
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
	withTool := fixedClaudeArguments("/absolute/claude", "Write")
	if !slices.Contains(withTool, "--tools=Write") {
		t.Fatalf("Claude tool arguments = %q", withTool)
	}
}

func TestAgentPromptUsesStandardInputInsteadOfProcessArguments(t *testing.T) {
	t.Parallel()

	const prompt = "prompt-that-must-not-enter-argv"
	command, err := newAgentCommand(config{
		clientID:     acceptanceClientClaudeCode,
		launcherPath: "/absolute/launcher",
		claudePath:   "/absolute/claude",
	}, "/trusted/workspace", prompt, "")
	if err != nil {
		t.Fatal(err)
	}
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

func TestFixedCodexInvocationUsesIsolatedStateAndStandardInput(
	t *testing.T,
) {
	t.Parallel()

	const prompt = "codex-prompt-that-must-not-enter-argv"
	workingDirectory := "/trusted/workspace"
	command, err := newAgentCommand(config{
		clientID:     acceptanceClientCodexCLI,
		launcherPath: "/absolute/launcher",
		codexPath:    "/absolute/codex",
	}, workingDirectory, prompt, "")
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"/absolute/launcher",
		"run",
		"--",
		"/absolute/codex",
		"-a",
		"never",
		"-s",
		"workspace-write",
		"exec",
		"--json",
		"--skip-git-repo-check",
		"--ignore-user-config",
		"--ignore-rules",
		"--color",
		"never",
		"--model",
		"gpt-5.6-sol",
		"-",
	}
	if !slices.Equal(command.Args, expected) {
		t.Fatalf("Codex arguments = %q", command.Args)
	}
	if slices.Contains(command.Args, prompt) {
		t.Fatalf("Codex arguments exposed the prompt: %q", command.Args)
	}
	payload, err := io.ReadAll(command.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != prompt {
		t.Fatalf("Codex stdin = %q", payload)
	}
	expectedHome := filepath.Join(workingDirectory, codexStateDirectoryName)
	if !slices.Contains(command.Env, "CODEX_HOME="+expectedHome) {
		t.Fatalf("Codex environment = %v", command.Env)
	}
}

func TestFixedCodexResumeUsesExactTrustedThreadIdentity(t *testing.T) {
	t.Parallel()

	threadID, err := parseAgentThreadID(
		"019c12b2-94d7-7d40-b3e8-ea4f1733dcba",
	)
	if err != nil {
		t.Fatal(err)
	}
	command, err := newResumeAgentCommand(
		config{
			clientID:     acceptanceClientCodexCLI,
			launcherPath: "/absolute/launcher",
			codexPath:    "/absolute/codex",
		},
		"/trusted/workspace",
		"continue-through-stdin",
		threadID,
	)
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"/absolute/launcher",
		"run",
		"--",
		"/absolute/codex",
		"-a",
		"never",
		"-s",
		"workspace-write",
		"exec",
		"resume",
		"--json",
		"--skip-git-repo-check",
		"--ignore-user-config",
		"--ignore-rules",
		"--model",
		"gpt-5.6-sol",
		threadID.String(),
		"-",
	}
	if !slices.Equal(command.Args, expected) {
		t.Fatalf("Codex resume arguments = %q", command.Args)
	}
	if _, err := newResumeAgentCommand(
		config{clientID: acceptanceClientClaudeCode},
		"/trusted/workspace",
		"prompt",
		threadID,
	); err == nil {
		t.Fatal("Claude was accepted by the Codex resume contract")
	}
}

func TestCodexJSONLEvidenceTrustsOnlyTypedClientEvents(t *testing.T) {
	t.Parallel()

	run := &agentRun{
		clientID: acceptanceClientCodexCLI,
		marker:   "VIBEMATE_CODEX_OK",
	}
	run.observeLine(mustJSON(t, map[string]any{
		"type": "user_message",
		"text": "VIBEMATE_CODEX_OK " + codexHTTPFallbackMessage,
	}))
	if run.httpFallbackSeen {
		t.Fatal("Assistant text forged HTTP fallback evidence")
	}

	run.observeLine(mustJSON(t, map[string]any{
		"type":      "thread.started",
		"thread_id": "019c12b2-94d7-7d40-b3e8-ea4f1733dcba",
	}))
	run.observeLine(mustJSON(t, map[string]any{
		"type": "turn.started",
	}))
	run.observeLine(mustJSON(t, map[string]any{
		"type": "item.completed",
		"item": map[string]any{
			"type": "agent_message",
			"text": codexHTTPFallbackMessage,
		},
	}))
	if run.httpFallbackSeen {
		t.Fatal("Assistant text forged HTTP fallback evidence")
	}
	run.observeLine(mustJSON(t, map[string]any{
		"type": "item.completed",
		"item": map[string]any{
			"type":    "error",
			"message": codexHTTPFallbackMessage,
		},
	}))
	run.observeLine(mustJSON(t, map[string]any{
		"type": "item.completed",
		"item": map[string]any{
			"type": "agent_message",
			"text": "VIBEMATE_CODEX_OK",
		},
	}))
	run.observeLine(mustJSON(t, map[string]any{
		"type": "item.completed",
		"item": map[string]any{
			"type": "command_execution",
		},
	}))
	run.observeLine(mustJSON(t, map[string]any{
		"type": "turn.completed",
	}))

	if run.readErr != nil {
		t.Fatal(run.readErr)
	}
	if run.threadID.String() !=
		"019c12b2-94d7-7d40-b3e8-ea4f1733dcba" ||
		!run.httpFallbackSeen ||
		!run.turnCompleted ||
		run.agentMessages != 2 ||
		run.toolUses != 1 ||
		!run.markerSeen {
		t.Fatalf("Codex evidence = %+v", run)
	}
}

func TestCodexJSONLRejectsConflictingThreadIdentity(t *testing.T) {
	t.Parallel()

	run := &agentRun{clientID: acceptanceClientCodexCLI}
	run.observeLine(mustJSON(t, map[string]any{
		"type":      "thread.started",
		"thread_id": "019c12b2-94d7-7d40-b3e8-ea4f1733dcba",
	}))
	run.observeLine(mustJSON(t, map[string]any{
		"type":      "thread.started",
		"thread_id": "019c12b2-94d7-7d40-b3e8-ea4f1733dcbb",
	}))
	if run.readErr == nil {
		t.Fatal("Conflicting Codex thread identity was accepted")
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

	run := &agentRun{
		stderr:   newBoundedBuffer(1),
		clientID: acceptanceClientClaudeCode,
	}
	err := agentProcessFailure("normal", 1, nil, run)
	if err == nil ||
		err.Error() != "normal Claude exit=1 stdoutLines=0 deltas=0 stderrBytes=0" ||
		strings.Contains(err.Error(), "%!") {
		t.Fatalf("process failure = %v", err)
	}
}

func TestAgentExitedBeforeApprovalReturnsBoundedFailureEvidence(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	close(done)
	outputDone := make(chan struct{})
	close(outputDone)
	run := &agentRun{
		done:       done,
		outputDone: outputDone,
		stderr:     newBoundedBuffer(256),
		clientID:   acceptanceClientClaudeCode,
	}
	run.observeLine(mustJSON(t, map[string]any{
		"type":    "result",
		"subtype": "error_during_execution",
		"result": `{"reasonCode":"provider_response_invalid",` +
			`"providerStatus":200,` +
			`"protocolReason":"invalid_provider_response",` +
			`"providerResponseIssue":"content_type"} secret-value`,
	}))
	err := agentExitedBeforeApproval(context.Background(), run)
	for _, required := range []string{
		"Claude exited before a tool approval became pending",
		"tool pre-approval Claude exit=0",
		"reasonCode=provider_response_invalid",
		"providerStatus=200",
		"protocolReason=invalid_provider_response",
		"providerResponseIssue=content_type",
		"resultSubtype=error_during_execution",
	} {
		if !strings.Contains(err.Error(), required) {
			t.Fatalf("agentExitedBeforeApproval() error = %v", err)
		}
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("agentExitedBeforeApproval() leaked result: %v", err)
	}
}

func TestAgentExitedBeforeHeldEgressReturnsBoundedFailureEvidence(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	close(done)
	outputDone := make(chan struct{})
	close(outputDone)
	run := &agentRun{
		done:       done,
		outputDone: outputDone,
		stderr:     newBoundedBuffer(256),
		clientID:   acceptanceClientCodexCLI,
	}
	_, _ = run.stderr.Write([]byte(
		"request configuration is invalid; private-detail",
	))
	err := agentExitedBeforeHeldEgress(
		context.Background(),
		run,
		offlinehold.EgressProvider,
	)
	for _, required := range []string{
		"Codex exited before provider egress queued in offline hold",
		"held-ingress Codex exit=0",
		"keywords=invalid,request",
	} {
		if !strings.Contains(err.Error(), required) {
			t.Fatalf("agentExitedBeforeHeldEgress() error = %v", err)
		}
	}
	if strings.Contains(err.Error(), "private-detail") {
		t.Fatalf("agentExitedBeforeHeldEgress() leaked stderr: %v", err)
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
