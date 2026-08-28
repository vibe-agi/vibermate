package desktophost_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/desktophost"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

const deepLiveAgentEnvironment = "VIBERMATE_LIVE_AGENT_DEEP"

type deepLiveScenario struct {
	name                           string
	executable                     string
	environmentID                  string
	origin                         string
	protocol                       environment.ClientProtocol
	baseline                       bool
	mcp                            bool
	topology                       bool
	terminalMarker                 string
	mcpNonce                       string
	subagentMarker                 string
	topologyMarkers                []string
	minimumClaudeSubagentExchanges int
	minimumSubtaskCalls            int
	minimumParallelSubtaskCalls    int
	minimumAgentConversations      int
	minimumAgentActors             int
	minimumDirectedEdges           int
	minimumRootChildren            int
	requireNestedActor             bool
	minimumGroupedActorTurns       int
}

// TestARealCurrentLoginAgentProducesDeepEvidence is deliberately opt-in. Each
// invocation performs a credentialed request using the installed client's own
// login, and may therefore consume a small amount of the operator's allowance.
// Ordinary test runs only compile this test and skip before starting a Host.
//
// Supported values:
//
//	VIBERMATE_LIVE_AGENT_DEEP=claude-baseline
//	VIBERMATE_LIVE_AGENT_DEEP=claude-mcp-subagent
//	VIBERMATE_LIVE_AGENT_DEEP=claude-parallel-subagents
//	VIBERMATE_LIVE_AGENT_DEEP=codex-baseline
//	VIBERMATE_LIVE_AGENT_DEEP=codex-mcp-subagent
//	VIBERMATE_LIVE_AGENT_DEEP=codex-parallel-nested
func TestARealCurrentLoginAgentProducesDeepEvidence(t *testing.T) {
	scenario := deepScenario(t, os.Getenv(deepLiveAgentEnvironment))
	executable, err := exec.LookPath(scenario.executable)
	if err != nil {
		t.Skipf("%s is unavailable: %v", scenario.executable, err)
	}
	assertClientLogin(t, executable, scenario.executable)

	workspace := t.TempDir()
	previousDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	options := hostOptions(t, paths, filepath.Join(root, "data"))
	secretFactory, err := hostsecret.NewDevelopmentFileFactory(
		filepath.Join(root, "private", "deep-agent-secrets.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := secretFactory.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	options.Runtime.Secrets = secrets
	options.CLIControlDiscoveryTTL = 10 * time.Minute
	options.CaptureRunLifetime = 10 * time.Minute
	options.ShutdownTimeout = 30 * time.Second
	host := startHost(t, options)
	defer shutdownHost(t, host)

	environmentID := publishOriginalEnvironment(t, host, scenario)
	allowExactOrigin(t, host, scenario.origin)
	command, mcpLog := deepClientCommand(t, executable, workspace, scenario)

	discovery, err := localdiscovery.NewFile(
		paths.DiscoveryPath(),
		productruntime.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	output := &boundedOutput{maximum: 8 << 20}
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery:          discovery,
		BaseEnvironment:    deepClientEnvironment(),
		Stdin:              strings.NewReader(""),
		Stdout:             output,
		Stderr:             output,
		HeartbeatInterval:  100 * time.Millisecond,
		ControlTimeout:     10 * time.Second,
		CreateTimeout:      15 * time.Second,
		TerminationTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	runContext, cancelRun := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelRun()
	approvalContext, cancelApproval := context.WithCancel(runContext)
	approvalResult := approveDeepClientRoot(
		approvalContext,
		host.Runtime().ToolApprovals(),
	)
	exitCode, runErr := launcher.Run(runContext, runlauncher.LaunchRequest{
		EnvironmentID: environmentID,
		Command:       command,
	})
	cancelApproval()
	approval := <-approvalResult
	if approval.err != nil {
		t.Fatalf("answer client Root approval: %v", approval.err)
	}
	if runErr != nil || exitCode != 0 {
		t.Fatalf(
			"%s failed: exit=%d err=%v output_tail=%q",
			scenario.name,
			exitCode,
			runErr,
			output.tail(6000),
		)
	}
	if !strings.Contains(output.String(), scenario.terminalMarker) {
		t.Fatalf(
			"%s omitted terminal proof %q; output_tail=%q",
			scenario.name,
			scenario.terminalMarker,
			output.tail(6000),
		)
	}

	run := onlyDeepCapture(t, host)
	records := waitForDeepExchanges(t, host, run.ID, scenario)
	assertDeepEvidence(t, host, scenario, run, records, output.String())
	if scenario.mcp {
		assertMCPTranscript(t, mcpLog, scenario.mcpNonce)
	}

	t.Logf(
		"deep_result client=%s scenario=%s root_approval=%t capture=%s exchanges=%d",
		scenario.executable,
		scenario.name,
		approval.allowed,
		run.ID,
		len(records),
	)
}

func deepScenario(t *testing.T, value string) deepLiveScenario {
	t.Helper()
	switch value {
	case "claude-baseline":
		return deepLiveScenario{
			name: "claude-baseline", executable: "claude",
			environmentID: "deep-claude-current-login",
			origin:        "https://api.anthropic.com",
			protocol:      environment.ClientProtocolAnthropicMessages,
			baseline:      true, terminalMarker: "VIBERMATE_CLAUDE_BASELINE_OK_82f3",
		}
	case "claude-mcp-subagent":
		return deepLiveScenario{
			name: "claude-mcp-subagent", executable: "claude",
			environmentID:  "deep-claude-current-login",
			origin:         "https://api.anthropic.com",
			protocol:       environment.ClientProtocolAnthropicMessages,
			mcp:            true,
			terminalMarker: "VIBERMATE_CLAUDE_DEEP_OK_19c7",
			mcpNonce:       "claude-mcp-4a62", subagentMarker: "CLAUDE_SUBAGENT_91d4",
			minimumClaudeSubagentExchanges: 1,
			minimumSubtaskCalls:            1,
			minimumGroupedActorTurns:       2,
		}
	case "claude-parallel-subagents":
		return deepLiveScenario{
			name: "claude-parallel-subagents", executable: "claude",
			environmentID:  "deep-claude-current-login",
			origin:         "https://api.anthropic.com",
			protocol:       environment.ClientProtocolAnthropicMessages,
			topology:       true,
			terminalMarker: "VIBERMATE_CLAUDE_PARALLEL_OK_c281",
			topologyMarkers: []string{
				"CLAUDE_PARALLEL_ALPHA_7f13",
				"CLAUDE_PARALLEL_BETA_944a",
			},
			minimumClaudeSubagentExchanges: 2,
			minimumSubtaskCalls:            2,
			minimumParallelSubtaskCalls:    2,
		}
	case "codex-baseline":
		return deepLiveScenario{
			name: "codex-baseline", executable: "codex",
			environmentID: "deep-codex-current-login",
			origin:        "https://chatgpt.com",
			protocol:      environment.ClientProtocolOpenAIResponses,
			baseline:      true, terminalMarker: "VIBERMATE_CODEX_BASELINE_OK_6e21",
		}
	case "codex-mcp-subagent":
		return deepLiveScenario{
			name: "codex-mcp-subagent", executable: "codex",
			environmentID:  "deep-codex-current-login",
			origin:         "https://chatgpt.com",
			protocol:       environment.ClientProtocolOpenAIResponses,
			mcp:            true,
			terminalMarker: "VIBERMATE_CODEX_DEEP_OK_73b8",
			mcpNonce:       "codex-mcp-b507", subagentMarker: "CODEX_SUBAGENT_2cf1",
			minimumAgentConversations: 1,
			minimumAgentActors:        2,
			minimumDirectedEdges:      1,
		}
	case "codex-parallel-nested":
		return deepLiveScenario{
			name: "codex-parallel-nested", executable: "codex",
			environmentID:  "deep-codex-current-login",
			origin:         "https://chatgpt.com",
			protocol:       environment.ClientProtocolOpenAIResponses,
			topology:       true,
			terminalMarker: "VIBERMATE_CODEX_TOPOLOGY_OK_d62b",
			topologyMarkers: []string{
				"CODEX_BRANCH_RESULT_72aa",
				"CODEX_SIBLING_RESULT_b181",
				"CODEX_NESTED_RESULT_0c93",
			},
			minimumAgentConversations: 3,
			minimumAgentActors:        4,
			minimumDirectedEdges:      3,
			minimumRootChildren:       2,
			requireNestedActor:        true,
		}
	case "":
		t.Skipf("deep current-login run needs %s", deepLiveAgentEnvironment)
	default:
		t.Fatalf("unsupported %s=%q", deepLiveAgentEnvironment, value)
	}
	return deepLiveScenario{}
}

func assertClientLogin(t *testing.T, executable, client string) {
	t.Helper()
	var command *exec.Cmd
	switch client {
	case "claude":
		command = exec.Command(executable, "auth", "status")
	case "codex":
		command = exec.Command(executable, "login", "status")
	default:
		t.Fatalf("unsupported client %q", client)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Skipf("%s current login is unavailable: %v", client, err)
	}
	if client == "claude" {
		var status struct {
			LoggedIn bool `json:"loggedIn"`
		}
		if json.Unmarshal(output, &status) != nil || !status.LoggedIn {
			t.Skipf("Claude current login is unavailable")
		}
	}
}

func publishOriginalEnvironment(
	t *testing.T,
	host *desktophost.Host,
	scenario deepLiveScenario,
) environment.EnvironmentID {
	t.Helper()
	id, err := environment.NewEnvironmentID(scenario.environmentID)
	if err != nil {
		t.Fatal(err)
	}
	clientOrigin, err := originidentity.ParseClientOrigin(scenario.origin)
	if err != nil {
		t.Fatal(err)
	}
	endpointID, err := environment.NewClientEndpointID("endpoint." + scenario.executable)
	if err != nil {
		t.Fatal(err)
	}
	planID, err := environment.NewClientProtocolPlanID("plan." + scenario.executable)
	if err != nil {
		t.Fatal(err)
	}
	aggregate := environment.Environment{
		ID: id, Name: "Deep " + scenario.executable + " current login",
		State: environment.StateActive, Revision: 1,
		ContentRecording: environment.DefaultContentRecordingPolicy(),
		ClientEndpoints: []environment.ClientEndpoint{{
			ID: endpointID, Revision: 1, ClientOrigin: clientOrigin,
			ProtocolPlans: []environment.ClientProtocolPlan{{
				ID: planID, Revision: 1, ClientProtocol: scenario.protocol,
				ClientAdapterPolicy: environment.ClientAdapterPolicy{
					ID: "adapter." + scenario.executable, Revision: 1,
				},
				Destination:   environment.DestinationPlan{Kind: environment.DestinationKindOriginal},
				EgressProfile: egressprofile.Direct(),
			}},
		}},
	}
	draft, err := host.Runtime().Environments().SaveDraft(
		context.Background(),
		environment.DraftCommand{Candidate: aggregate},
	)
	if err != nil {
		t.Fatalf("save original Environment: %v", err)
	}
	preview, err := host.Runtime().Environments().Preview(
		context.Background(), id, draft.Revision,
	)
	if err != nil {
		t.Fatalf("preview original Environment: %v", err)
	}
	result, err := host.Runtime().Environments().Publish(context.Background(), preview)
	if err != nil || result.Outcome != environment.CommitOutcomeCommitted {
		t.Fatalf("publish original Environment = %+v, %v", result, err)
	}
	return id
}

func allowExactOrigin(t *testing.T, host *desktophost.Host, rawOrigin string) {
	t.Helper()
	origin, err := originidentity.ParseClientOrigin(rawOrigin)
	if err != nil {
		t.Fatal(err)
	}
	rules := host.Runtime().ConnectionRules()
	_, err = rules.Replace(
		context.Background(),
		rules.Current().Revision,
		[]connectionpolicy.Rule{{
			ID:       "deep.allow." + strings.ReplaceAll(origin.Host(), ".", "-"),
			Priority: 100,
			Decision: connectionpolicy.DecisionAllow,
			Match:    connectionpolicy.MatchExactHostPort(origin.Host(), 443),
		}},
		rules.Current().Mode,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func deepClientEnvironment() []string {
	blocked := []string{
		"ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=",
		"OPENAI_API_KEY=", "CODEX_API_KEY=", deepLiveAgentEnvironment + "=",
	}
	result := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if slices.ContainsFunc(blocked, func(prefix string) bool {
			return strings.HasPrefix(entry, prefix)
		}) {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func deepClientCommand(
	t *testing.T,
	executable, workspace string,
	scenario deepLiveScenario,
) ([]string, string) {
	t.Helper()
	if scenario.baseline {
		prompt := "Reply with exactly this marker and nothing else: " + scenario.terminalMarker
		if scenario.executable == "claude" {
			return []string{
				executable, "--print", "--output-format", "json",
				"--no-chrome", prompt,
			}, ""
		}
		return []string{
			executable, "exec", "--skip-git-repo-check",
			"--ignore-user-config", "--color", "never", "--json",
			"--sandbox", "read-only", "--cd", workspace, prompt,
		}, ""
	}
	if scenario.topology {
		return deepTopologyClientCommand(t, executable, workspace, scenario), ""
	}
	if !scenario.mcp {
		t.Fatalf("deep scenario %q has no command mode", scenario.name)
	}

	markerPath := filepath.Join(workspace, "SUBAGENT_MARKER.txt")
	if err := os.WriteFile(markerPath, []byte(scenario.subagentMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mcpScript, mcpLog := writeMCPProbeServer(t, workspace)
	prompt := fmt.Sprintf(
		"This is a deterministic acceptance test. First call the MCP tool "+
			"vibermate_probe exactly once with nonce %q. Then launch exactly one "+
			"subagent using the client's built-in subagent/multi-agent tool; the "+
			"subagent must read %q and return its exact contents. Do not edit files, "+
			"do not use the web, and do not substitute a shell command for either "+
			"required tool. After both results are available, output one final line "+
			"containing %s, the MCP result, and %s.",
		scenario.mcpNonce,
		markerPath,
		scenario.terminalMarker,
		scenario.subagentMarker,
	)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 is required for the local MCP probe: %v", err)
	}
	if scenario.executable == "claude" {
		configPath := filepath.Join(workspace, "mcp.json")
		config := map[string]any{"mcpServers": map[string]any{
			"vibermate_probe": map[string]any{
				"type": "stdio", "command": python,
				"args": []string{mcpScript},
				"env":  map[string]string{"VIBERMATE_MCP_LOG": mcpLog},
			},
		}}
		encoded, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		agents := fmt.Sprintf(
			`{"vibermate-reader":{"description":"Read the one requested marker file and return it exactly","prompt":"Read only the exact file requested by the parent. Return its contents exactly.","tools":["Read"]}}`,
		)
		return []string{
			executable, "--print", "--output-format", "stream-json", "--verbose",
			"--forward-subagent-text", "--no-chrome",
			"--dangerously-skip-permissions", "--strict-mcp-config",
			"--mcp-config", configPath, "--agents", agents, prompt,
		}, mcpLog
	}
	quoted := func(value string) string {
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	return []string{
		executable, "exec", "--skip-git-repo-check",
		"--ignore-user-config", "--color", "never", "--json",
		"--sandbox", "read-only", "--cd", workspace, "--enable", "multi_agent",
		"--config", `approval_policy="never"`,
		"--config", `features.responses_websockets=false`,
		"--config", "mcp_servers.vibermate_probe.command=" + quoted(python),
		"--config", "mcp_servers.vibermate_probe.args=[" + quoted(mcpScript) + "]",
		"--config", "mcp_servers.vibermate_probe.env={VIBERMATE_MCP_LOG=" + quoted(mcpLog) + "}",
		prompt,
	}, mcpLog
}

func deepTopologyClientCommand(
	t *testing.T,
	executable, workspace string,
	scenario deepLiveScenario,
) []string {
	t.Helper()
	if len(scenario.topologyMarkers) == 0 {
		t.Fatalf("topology scenario %q has no proof markers", scenario.name)
	}
	if scenario.executable == "claude" {
		if len(scenario.topologyMarkers) != 2 {
			t.Fatalf("Claude topology scenario needs two markers")
		}
		agents := fmt.Sprintf(
			`{"vibermate-alpha":{"description":"Return the deterministic alpha acceptance marker","prompt":"Return exactly %s and nothing else.","tools":[]},"vibermate-beta":{"description":"Return the deterministic beta acceptance marker","prompt":"Return exactly %s and nothing else.","tools":[]}}`,
			scenario.topologyMarkers[0],
			scenario.topologyMarkers[1],
		)
		prompt := fmt.Sprintf(
			"This is a deterministic acceptance test. In one response, launch the "+
				"vibermate-alpha and vibermate-beta subagents together as two parallel "+
				"Agent tool calls. Do not use any other tool. Wait for both results. "+
				"Then output one final line containing %s, %s, and %s.",
			scenario.terminalMarker,
			scenario.topologyMarkers[0],
			scenario.topologyMarkers[1],
		)
		return []string{
			executable, "--print", "--output-format", "stream-json", "--verbose",
			"--forward-subagent-text", "--no-chrome",
			"--dangerously-skip-permissions", "--agents", agents, prompt,
		}
	}
	if scenario.executable != "codex" || len(scenario.topologyMarkers) != 3 {
		t.Fatalf("unsupported topology client %q", scenario.executable)
	}
	prompt := fmt.Sprintf(
		"This is a deterministic acceptance test. Use the built-in multi-agent "+
			"tools, not shell commands and not a simulated answer. Spawn two first-level "+
			"agents concurrently with the exact task names branch and sibling. The sibling "+
			"must return exactly %s. The branch must spawn one child with the exact task "+
			"name leaf; leaf must return exactly %s, then branch must return exactly %s. "+
			"Wait for both first-level agents. Finally output one line containing %s and "+
			"all three result markers.",
		scenario.topologyMarkers[1],
		scenario.topologyMarkers[2],
		scenario.topologyMarkers[0],
		scenario.terminalMarker,
	)
	return []string{
		executable, "exec", "--skip-git-repo-check",
		"--ignore-user-config", "--color", "never", "--json",
		"--sandbox", "read-only", "--cd", workspace, "--enable", "multi_agent",
		"--config", `approval_policy="never"`,
		"--config", `features.responses_websockets=false`,
		"--config", `agents.max_concurrent_threads_per_session=4`,
		"--config", `agents.max_depth=2`,
		prompt,
	}
}

func writeMCPProbeServer(t *testing.T, workspace string) (string, string) {
	t.Helper()
	scriptPath := filepath.Join(workspace, "mcp_probe.py")
	logPath := filepath.Join(workspace, "mcp_probe.jsonl")
	script := `import json
import os
import sys

log_path = os.environ["VIBERMATE_MCP_LOG"]

def log(value):
    with open(log_path, "a", encoding="utf-8") as handle:
        handle.write(json.dumps(value, separators=(",", ":")) + "\n")

def answer(request, result=None, error=None):
    if "id" not in request:
        return
    response = {"jsonrpc": "2.0", "id": request["id"]}
    if error is not None:
        response["error"] = error
    else:
        response["result"] = result
    sys.stdout.write(json.dumps(response, separators=(",", ":")) + "\n")
    sys.stdout.flush()

for raw in sys.stdin:
    request = json.loads(raw)
    method = request.get("method", "")
    log({"method": method, "params": request.get("params", {})})
    if method == "initialize":
        version = request.get("params", {}).get("protocolVersion", "2025-06-18")
        answer(request, {
            "protocolVersion": version,
            "capabilities": {"tools": {}},
            "serverInfo": {"name": "vibermate-deep-probe", "version": "1.0.0"}
        })
    elif method == "tools/list":
        answer(request, {"tools": [{
            "name": "vibermate_probe",
            "description": "Returns a deterministic acceptance nonce.",
			"annotations": {
				"title": "ViberMate deterministic probe",
				"readOnlyHint": True,
				"destructiveHint": False,
				"idempotentHint": True,
				"openWorldHint": False
			},
            "inputSchema": {
                "type": "object",
                "properties": {"nonce": {"type": "string"}},
                "required": ["nonce"],
                "additionalProperties": False
            }
        }]})
    elif method == "tools/call":
        params = request.get("params", {})
        nonce = params.get("arguments", {}).get("nonce", "")
        if params.get("name") != "vibermate_probe" or not nonce:
            answer(request, error={"code": -32602, "message": "invalid probe call"})
        else:
            answer(request, {
                "content": [{"type": "text", "text": "VIBERMATE_MCP_RESULT:" + nonce}],
                "isError": False
            })
    elif method == "ping":
        answer(request, {})
    elif "id" in request:
        answer(request, error={"code": -32601, "message": "method not found"})
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return scriptPath, logPath
}

type deepApprovalResult struct {
	allowed bool
	err     error
}

func approveDeepClientRoot(
	ctx context.Context,
	approvals toolapproval.Controller,
) <-chan deepApprovalResult {
	result := make(chan deepApprovalResult, 1)
	go func() {
		defer close(result)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			page, err := approvals.ListApprovals(ctx, toolapproval.PageRequest{
				State: toolapproval.StatePending, Limit: 20,
			})
			if err != nil {
				if ctx.Err() != nil {
					result <- deepApprovalResult{}
					return
				}
				result <- deepApprovalResult{err: err}
				return
			}
			for _, pending := range page.Items {
				if pending.Kind != string(toolapproval.KindClientRootAsk) {
					continue
				}
				_, err := approvals.DecideApproval(ctx, toolapproval.DecisionCommand{
					ApprovalID: pending.ID, ExpectedRevision: pending.Revision,
					IdempotencyKey: "deep-current-login-root-approval-0001",
					Decision:       toolapproval.DecisionAllowOnce,
					Scope:          toolapproval.ScopeRequest,
				})
				result <- deepApprovalResult{allowed: err == nil, err: err}
				return
			}
			select {
			case <-ctx.Done():
				result <- deepApprovalResult{}
				return
			case <-ticker.C:
			}
		}
	}()
	return result
}

func onlyDeepCapture(t *testing.T, host *desktophost.Host) capturerun.View {
	t.Helper()
	page, err := host.Runtime().CaptureRunReader().ListRuns(
		context.Background(), capturerun.PageRequest{Limit: 20},
	)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("deep CaptureRuns = %+v, %v", page, err)
	}
	run := page.Items[0]
	if run.State != capturerun.StateFinished || run.Observation != capturerun.ObservationObserved {
		t.Fatalf("deep CaptureRun = %+v", run)
	}
	return run
}

func waitForDeepExchanges(
	t *testing.T,
	host *desktophost.Host,
	captureRunID string,
	scenario deepLiveScenario,
) []activity.Record {
	t.Helper()
	minimum := 1
	if scenario.mcp {
		minimum = 2
	}
	if scenario.minimumClaudeSubagentExchanges > 0 {
		minimum = max(minimum, scenario.minimumClaudeSubagentExchanges+1)
	}
	if scenario.minimumAgentActors > 0 {
		minimum = max(minimum, scenario.minimumAgentActors)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		page, err := host.Runtime().Activities().ListExchanges(
			context.Background(),
			activity.PageRequest{Limit: 200, CaptureRunID: captureRunID},
		)
		if err == nil && len(page.Items) >= minimum &&
			slices.ContainsFunc(page.Items, func(record activity.Record) bool {
				return record.Status == activity.StatusSucceeded
			}) {
			return page.Items
		}
		if time.Now().After(deadline) {
			t.Fatalf("deep Exchanges = %+v, %v", page, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func assertDeepEvidence(
	t *testing.T,
	host *desktophost.Host,
	scenario deepLiveScenario,
	run capturerun.View,
	records []activity.Record,
	terminalOutput string,
) {
	t.Helper()
	contents := make([]exchangecontent.Record, 0, len(records))
	successfulExchangeIDs := make([]string, 0, len(records))
	for _, record := range records {
		if record.CaptureRunID != run.ID || record.ManualCaptureID != "" ||
			record.EnvironmentID != scenario.environmentID ||
			record.AccountID != "" || record.AccountRevision != 0 ||
			record.CredentialEpoch != 0 {
			t.Fatalf("Exchange attribution escaped current-login Capture: %+v", record)
		}
		content, err := host.Runtime().ExchangeContents().Get(
			context.Background(), record.SubjectID,
		)
		if err != nil {
			if errors.Is(err, exchangecontent.ErrNotFound) &&
				record.Status != activity.StatusSucceeded {
				t.Logf(
					"content_not_retained exchange=%s status=%s reason=%s",
					record.SubjectID,
					record.Status,
					record.ReasonCode,
				)
				continue
			}
			t.Fatalf("read Exchange %+v: %v", record, err)
		}
		if content.Parent.CaptureRunID != run.ID ||
			content.Frozen.EnvironmentID != scenario.environmentID {
			t.Fatalf(
				"Exchange content attribution escaped Capture: exchange=%s capture=%s Environment=%s",
				record.SubjectID,
				content.Parent.CaptureRunID,
				content.Frozen.EnvironmentID,
			)
		}
		if content.Response == nil {
			if record.Status == activity.StatusSucceeded {
				t.Fatalf(
					"succeeded Exchange has no retained response: exchange=%s",
					record.SubjectID,
				)
			}
			t.Logf(
				"response_not_terminal exchange=%s status=%s reason=%s",
				record.SubjectID,
				record.Status,
				record.ReasonCode,
			)
			continue
		}
		contents = append(contents, content)
		if record.Status == activity.StatusSucceeded {
			successfulExchangeIDs = append(successfulExchangeIDs, record.SubjectID)
		}
	}
	assertRawHTTPEvidence(t, host, successfulExchangeIDs)
	assertConversationProjections(t, host, scenario, run, records)

	connections, err := host.Runtime().ConnectionEvents().List(
		context.Background(), connectionevent.PageRequest{Limit: 200},
	)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := originidentity.ParseClientOrigin(scenario.origin)
	if !slices.ContainsFunc(connections.Items, func(record connectionevent.Record) bool {
		return record.RequestedHost == origin.Host() &&
			record.Decryption == connectionevent.DecryptionMITM
	}) {
		t.Fatalf("no exact MITM connection for %s: %+v", origin.Host(), connections.Items)
	}

	attempts, err := host.Runtime().EgressAttempts().List(
		context.Background(), egressaudit.PageRequest{Limit: 200},
	)
	if err != nil {
		t.Fatal(err)
	}
	completedAttempts := 0
	for _, record := range attempts.Items {
		attempt := record.Attempt
		if attempt.Purpose() == egressaudit.PurposeProviderAttempt &&
			attempt.TargetOrigin() == origin.String() && attempt.Terminal() &&
			attempt.Outcome() == egressaudit.OutcomeCompleted &&
			attempt.BytesOut() > 0 && attempt.BytesIn() > 0 {
			completedAttempts++
		}
	}
	if completedAttempts == 0 {
		t.Fatalf("no completed provider attempt for %s: %+v", origin.String(), attempts.Items)
	}

	encoded, err := json.Marshal(contents)
	if err != nil {
		t.Fatal(err)
	}
	evidence := string(encoded)
	if !strings.Contains(evidence, scenario.terminalMarker) {
		t.Fatalf("semantic evidence omitted terminal marker %q", scenario.terminalMarker)
	}
	if scenario.baseline {
		return
	}
	semanticMarkers := slices.Clone(scenario.topologyMarkers)
	if scenario.mcp {
		semanticMarkers = append(semanticMarkers,
			"vibermate_probe",
			"VIBERMATE_MCP_RESULT:"+scenario.mcpNonce,
			scenario.subagentMarker,
		)
	}
	for _, marker := range semanticMarkers {
		if !strings.Contains(evidence, marker) {
			t.Logf(
				"semantic_marker_diagnostic marker=%q exchanges=%d contents=%d terminal=%t",
				marker,
				len(records),
				len(contents),
				strings.Contains(terminalOutput, marker),
			)
			for index, content := range contents {
				encodedContent, _ := json.Marshal(content)
				t.Logf(
					"semantic_record index=%d request_messages=%d response_blocks=%d marker=%t tool=%t",
					index,
					len(content.Request.Messages),
					len(content.Response.Blocks),
					strings.Contains(string(encodedContent), marker),
					strings.Contains(string(encodedContent), "vibermate_probe"),
				)
			}
			t.Logf("terminal_output_tail=%q", outputTail(terminalOutput, 4000))
			t.Fatalf("semantic evidence omitted %q", marker)
		}
		if !strings.Contains(terminalOutput, marker) {
			t.Fatalf("terminal output omitted %q", marker)
		}
	}
	hasKnownUsage := slices.ContainsFunc(contents, func(record exchangecontent.Record) bool {
		response := record.Response
		return response != nil && (response.Usage.InputUncached.Known ||
			response.Usage.CacheRead.Known || response.Usage.Output.Known)
	})
	if !hasKnownUsage {
		t.Fatal("no provider-observed usage was retained")
	}

	type semanticCoverage struct {
		ReasoningSummaries      int
		ReasoningBlocks         int
		OpaqueSignatures        int
		AgentMessages           int
		AgentNames              map[string]struct{}
		DirectedAgentEdges      map[string]struct{}
		ClaudeSubagentExchanges int
		MultiAgentCalls         int
		SubtaskCalls            int
		ParallelSubtaskCalls    int
	}
	coverage := semanticCoverage{
		AgentNames:         make(map[string]struct{}),
		DirectedAgentEdges: make(map[string]struct{}),
	}
	observeAgent := func(agent *exchangecontent.AgentContext) {
		if agent == nil {
			return
		}
		coverage.AgentMessages++
		if agent.AgentName != "" {
			coverage.AgentNames[agent.AgentName] = struct{}{}
		}
		if agent.Author != "" {
			coverage.AgentNames[agent.Author] = struct{}{}
		}
		if agent.Recipient != "" {
			coverage.AgentNames[agent.Recipient] = struct{}{}
		}
		if agent.Author != "" && agent.Recipient != "" {
			coverage.DirectedAgentEdges[agent.Author+"->"+agent.Recipient] = struct{}{}
		}
	}
	observeBlock := func(block exchangecontent.Block, request bool) {
		observeAgent(block.Agent)
		switch {
		case block.Kind == exchangecontent.BlockKindReasoning:
			coverage.ReasoningBlocks++
			if block.ProviderKind == "reasoning_summary" {
				coverage.ReasoningSummaries++
			}
		case block.Kind == "provider_extension" &&
			(strings.Contains(block.ProviderKind, "thinking") ||
				strings.Contains(block.ProviderKind, "reasoning")):
			coverage.OpaqueSignatures++
		case block.Kind == "tool_call" && block.ToolNamespace == "multi_agent":
			coverage.MultiAgentCalls++
		case request && block.Kind == "tool_call" && block.ToolNamespace == "" &&
			strings.EqualFold(block.ToolName, "Agent"):
			coverage.SubtaskCalls++
		}
	}
	for _, content := range contents {
		claudeSubagent := false
		for _, message := range content.Request.Messages {
			observeAgent(message.Agent)
			parallelSubtaskCalls := 0
			for _, block := range message.Blocks {
				if message.Role == "system" && block.Kind == "text" &&
					strings.Contains(block.Text, "cc_is_subagent=true") {
					claudeSubagent = true
				}
				if block.Kind == "tool_call" && block.ToolNamespace == "" &&
					strings.EqualFold(block.ToolName, "Agent") {
					parallelSubtaskCalls++
				}
				observeBlock(block, true)
			}
			coverage.ParallelSubtaskCalls = max(
				coverage.ParallelSubtaskCalls,
				parallelSubtaskCalls,
			)
		}
		if claudeSubagent {
			coverage.ClaudeSubagentExchanges++
		}
		if content.Response != nil {
			for _, block := range content.Response.Blocks {
				observeBlock(block, false)
			}
		}
	}
	agentNames := make([]string, 0, len(coverage.AgentNames))
	for name := range coverage.AgentNames {
		agentNames = append(agentNames, name)
	}
	slices.Sort(agentNames)
	agentEdges := make([]string, 0, len(coverage.DirectedAgentEdges))
	for edge := range coverage.DirectedAgentEdges {
		agentEdges = append(agentEdges, edge)
	}
	slices.Sort(agentEdges)
	t.Logf(
		"semantic_coverage client=%s reasoning_summaries=%d reasoning_blocks=%d opaque_signatures=%d agent_messages=%d agent_names=%q directed_edges=%q claude_subagent_exchanges=%d multi_agent_calls=%d unattributed_subtasks=%d parallel_subtasks=%d",
		scenario.executable,
		coverage.ReasoningSummaries,
		coverage.ReasoningBlocks,
		coverage.OpaqueSignatures,
		coverage.AgentMessages,
		agentNames,
		agentEdges,
		coverage.ClaudeSubagentExchanges,
		coverage.MultiAgentCalls,
		coverage.SubtaskCalls,
		coverage.ParallelSubtaskCalls,
	)
	// Thinking/reasoning is optional provider output, not a property the client
	// can demand for every request. The codec fixtures prove that visible
	// summaries and opaque signatures are retained when supplied; a live run
	// records what the provider actually emitted without inventing evidence when
	// it emitted none.
	if coverage.ReasoningBlocks == 0 && coverage.OpaqueSignatures == 0 {
		t.Log("provider emitted no optional thinking/reasoning evidence in this run")
	}
	switch scenario.executable {
	case "claude":
		if coverage.ClaudeSubagentExchanges < scenario.minimumClaudeSubagentExchanges ||
			coverage.SubtaskCalls < scenario.minimumSubtaskCalls ||
			coverage.ParallelSubtaskCalls < scenario.minimumParallelSubtaskCalls {
			t.Fatalf(
				"Claude subagent evidence is insufficient: asserted_exchanges=%d Agent_calls=%d parallel_calls=%d",
				coverage.ClaudeSubagentExchanges,
				coverage.SubtaskCalls,
				coverage.ParallelSubtaskCalls,
			)
		}
	case "codex":
		rootChildren := make(map[string]struct{})
		nestedActor := false
		for actor := range coverage.AgentNames {
			if agentActorDepth(actor) >= 3 {
				nestedActor = true
			}
		}
		for edge := range coverage.DirectedAgentEdges {
			author, recipient, found := strings.Cut(edge, "->")
			if found && agentActorDepth(author) == 1 &&
				agentActorDepth(recipient) == 2 &&
				strings.HasPrefix(recipient, strings.TrimSuffix(author, "/")+"/") {
				rootChildren[recipient] = struct{}{}
			}
		}
		if len(coverage.AgentNames) < scenario.minimumAgentActors ||
			len(coverage.DirectedAgentEdges) < scenario.minimumDirectedEdges ||
			len(rootChildren) < scenario.minimumRootChildren ||
			(scenario.requireNestedActor && !nestedActor) {
			t.Fatalf(
				"Codex subagent evidence topology is insufficient: actors=%q edges=%q root_children=%d nested=%t",
				agentNames,
				agentEdges,
				len(rootChildren),
				nestedActor,
			)
		}
	default:
		t.Fatalf("unsupported deep Agent client %q", scenario.executable)
	}
}

func assertRawHTTPEvidence(
	t *testing.T,
	host *desktophost.Host,
	exchangeIDs []string,
) {
	t.Helper()
	if len(exchangeIDs) == 0 {
		t.Fatal("no succeeded Exchange is available for Raw HTTP verification")
	}
	required := []rawevidence.Layer{
		rawevidence.LayerClientIngress,
		rawevidence.LayerProviderEgress,
		rawevidence.LayerProviderResponse,
		rawevidence.LayerClientDownstream,
	}
	verified := make(map[rawevidence.Layer]int)
	observedByExchange := make(map[string][]rawevidence.Layer, len(exchangeIDs))
	for _, exchangeID := range exchangeIDs {
		envelopes, err := host.Runtime().RawEvidence().ListExchange(
			context.Background(), exchangeID,
		)
		if err != nil {
			t.Fatalf("list Raw HTTP exchange=%s: %v", exchangeID, err)
		}
		for _, envelope := range envelopes {
			observedByExchange[exchangeID] = append(
				observedByExchange[exchangeID], envelope.Layer,
			)
			if !slices.Contains(required, envelope.Layer) {
				continue
			}
			if envelope.ExchangeID != exchangeID ||
				(envelope.PayloadState != rawevidence.PayloadCaptured &&
					envelope.PayloadState != rawevidence.PayloadTruncated) {
				t.Fatalf("Raw HTTP metadata is incomplete: %+v", envelope)
			}
			revealed, err := host.Runtime().RawEvidence().Reveal(
				context.Background(),
				rawevidence.RevealRequest{
					EnvelopeID: envelope.EnvelopeID,
					ActorID:    "deep-live-test",
				},
			)
			if err != nil {
				t.Fatalf(
					"reveal Raw HTTP exchange=%s layer=%s: %v",
					exchangeID,
					envelope.Layer,
					err,
				)
			}
			if revealed.Metadata.EnvelopeID != envelope.EnvelopeID ||
				len(revealed.Payload.Headers) == 0 ||
				len(revealed.Payload.Body) == 0 {
				t.Fatalf(
					"Raw HTTP payload omitted headers/body: exchange=%s layer=%s metadata=%+v payload=%+v",
					exchangeID,
					envelope.Layer,
					envelope,
					revealed.Payload,
				)
			}
			verified[envelope.Layer]++
		}
	}
	for _, layer := range required {
		if verified[layer] == 0 {
			t.Fatalf(
				"Raw HTTP boundary %s was not retained for succeeded Exchanges %q; retained layers=%v statistics=%+v",
				layer,
				exchangeIDs,
				observedByExchange,
				host.Runtime().RawEvidenceStatistics(),
			)
		}
	}
	t.Logf(
		"raw_http_coverage exchanges=%d client_ingress=%d provider_egress=%d provider_response=%d client_downstream=%d",
		len(exchangeIDs),
		verified[rawevidence.LayerClientIngress],
		verified[rawevidence.LayerProviderEgress],
		verified[rawevidence.LayerProviderResponse],
		verified[rawevidence.LayerClientDownstream],
	)
}

func assertConversationProjections(
	t *testing.T,
	host *desktophost.Host,
	scenario deepLiveScenario,
	run capturerun.View,
	records []activity.Record,
) {
	t.Helper()
	page := waitForConversationAPIProjection(t, host, scenario, run.ID)
	turns := 0
	kinds := make(map[string]int)
	actors := make(map[string]struct{})
	maximumActorTurns := 0
	nativeIdentityCount := 0
	for _, item := range page.Items {
		turns += item.TurnCount
		kinds[item.Conversation.Kind]++
		if item.Conversation.Actor != "" {
			actors[item.Conversation.Actor] = struct{}{}
			if item.TurnCount > maximumActorTurns {
				maximumActorTurns = item.TurnCount
			}
		}
		identity := item.Conversation.ClientIdentity
		if identity == nil {
			continue
		}
		if identity.Client != scenario.executable || identity.SessionID == "" ||
			!identity.SessionResumable || identity.Confidence != "exact" {
			t.Fatalf("client identity is not exact/resumable: %+v", identity)
		}
		nativeSessionName := scenario.executable + ".session_id"
		if !hasDeepClientEvidence(identity.ProtocolIDs, nativeSessionName, identity.SessionID) {
			t.Fatalf("native Session ID was not retained: %+v", identity)
		}
		if identity.ActorID != "" {
			nativeActorName := "claude.agent_id"
			if scenario.executable == "codex" {
				nativeActorName = "codex.thread_id"
			}
			if !hasDeepClientEvidence(identity.ProtocolIDs, nativeActorName, identity.ActorID) {
				t.Fatalf("native Actor ID was not retained: %+v", identity)
			}
		}
		nativeIdentityCount++
	}
	if turns != len(records) {
		t.Fatalf(
			"Capture Conversation Turn attribution is incomplete: conversations=%+v exchanges=%d",
			page.Items,
			len(records),
		)
	}
	if scenario.baseline {
		if nativeIdentityCount == 0 {
			t.Fatalf("baseline Conversation has no exact client identity: %+v", page.Items)
		}
		return
	}
	switch scenario.executable {
	case "claude":
		if kinds["main"] == 0 ||
			kinds["agent"] < scenario.minimumClaudeSubagentExchanges ||
			len(actors) < scenario.minimumClaudeSubagentExchanges {
			t.Fatalf(
				"Claude main/subagent Conversations were not separated: kinds=%v items=%+v",
				kinds,
				page.Items,
			)
		}
	case "codex":
		if kinds["main"] == 0 ||
			kinds["agent"] < scenario.minimumAgentConversations ||
			len(actors) < scenario.minimumAgentConversations {
			t.Fatalf(
				"Codex agent Conversations were not separated: kinds=%v actors=%v items=%+v",
				kinds,
				actors,
				page.Items,
			)
		}
	default:
		t.Fatalf("unsupported deep Agent client %q", scenario.executable)
	}
	if nativeIdentityCount == 0 {
		t.Fatalf("Conversation API returned no exact native identities: %+v", page.Items)
	}
	if scenario.minimumGroupedActorTurns > 0 &&
		maximumActorTurns < scenario.minimumGroupedActorTurns {
		t.Fatalf(
			"same native Agent was not grouped across Turns: max=%d want>=%d items=%+v",
			maximumActorTurns,
			scenario.minimumGroupedActorTurns,
			page.Items,
		)
	}
	t.Logf(
		"conversation_coverage client=%s conversations=%d kinds=%v actors=%d turns=%d native_identities=%d max_actor_turns=%d",
		scenario.executable,
		len(page.Items),
		kinds,
		len(actors),
		turns,
		nativeIdentityCount,
		maximumActorTurns,
	)
}

func waitForConversationAPIProjection(
	t *testing.T,
	host *desktophost.Host,
	scenario deepLiveScenario,
	captureRunID string,
) desktopcontrol.ConversationPage {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last desktopcontrol.ConversationPage
	for {
		response := controlRequest(
			t,
			host.AppSession().BaseURL,
			http.MethodGet,
			"/api/v1/conversations?limit=200&captureRunId="+url.QueryEscape(captureRunID),
			host.AppSession().ReadToken,
			"vibermate://desktop",
		)
		if response.StatusCode == http.StatusOK {
			decodeErr := json.NewDecoder(response.Body).Decode(&last)
			_ = response.Body.Close()
			if decodeErr != nil {
				t.Fatalf("decode Conversation API: %v", decodeErr)
			}
			if len(last.Items) != 0 && slices.ContainsFunc(
				last.Items,
				func(item desktopcontrol.ConversationSummary) bool {
					identity := item.Conversation.ClientIdentity
					return identity != nil && identity.Client == scenario.executable
				},
			) {
				return last
			}
		} else {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			if time.Now().After(deadline) {
				t.Fatalf("Conversation API status=%d body=%q", response.StatusCode, body)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Conversation API did not expose exact client identity: %+v", last.Items)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func hasDeepClientEvidence(
	values []agentconversation.ClientEvidenceValue,
	name string,
	value string,
) bool {
	for _, candidate := range values {
		if candidate.Name == name && candidate.Value == value {
			return true
		}
	}
	return false
}

func agentActorDepth(value string) int {
	depth := 0
	for _, part := range strings.Split(value, "/") {
		if part != "" {
			depth++
		}
	}
	return depth
}

func outputTail(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[len(value)-maximum:]
}

func assertMCPTranscript(t *testing.T, logPath, nonce string) {
	t.Helper()
	file, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open MCP transcript: %v", err)
	}
	defer file.Close()
	methods := make([]string, 0, 4)
	called := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		methods = append(methods, entry.Method)
		if entry.Method == "tools/call" {
			arguments, _ := entry.Params["arguments"].(map[string]any)
			called = entry.Params["name"] == "vibermate_probe" &&
				arguments["nonce"] == nonce
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"initialize", "tools/list", "tools/call"} {
		if !slices.Contains(methods, required) {
			t.Fatalf("MCP transcript omitted %s: %v", required, methods)
		}
	}
	if !called {
		t.Fatalf("MCP probe was not called with nonce %q", nonce)
	}
}

type boundedOutput struct {
	mu      sync.Mutex
	value   strings.Builder
	maximum int
	cut     bool
}

func (output *boundedOutput) Write(value []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	written := len(value)
	remaining := output.maximum - output.value.Len()
	if remaining <= 0 {
		output.cut = true
		return written, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		output.cut = true
	}
	_, _ = output.value.Write(value)
	return written, nil
}

func (output *boundedOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.value.String()
}

func (output *boundedOutput) tail(maximum int) string {
	value := output.String()
	if len(value) > maximum {
		return value[len(value)-maximum:]
	}
	return value
}

var _ io.Writer = (*boundedOutput)(nil)
