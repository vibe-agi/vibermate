package runlauncher

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vibe-agi/vibermate/internal/environment"
)

// remoteLaunchEnvironment lists the variables a remote Runtime Server may set
// on this device. A Server is another administrative domain: a variable that
// loads code or relocates client state (LD_PRELOAD, NODE_OPTIONS, CODEX_HOME,
// shell prefixes) would let it execute arbitrary programs here, so only
// settings that tune the agent's own behavior are accepted.
var remoteLaunchEnvironment = map[string]struct{}{
	"ANTHROPIC_DEFAULT_HAIKU_MODEL":            {},
	"ANTHROPIC_DEFAULT_OPUS_MODEL":             {},
	"ANTHROPIC_DEFAULT_SONNET_MODEL":           {},
	"ANTHROPIC_MODEL":                          {},
	"ANTHROPIC_SMALL_FAST_MODEL":               {},
	"API_TIMEOUT_MS":                           {},
	"BASH_DEFAULT_TIMEOUT_MS":                  {},
	"BASH_MAX_OUTPUT_LENGTH":                   {},
	"BASH_MAX_TIMEOUT_MS":                      {},
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": {},
	"CLAUDE_CODE_DISABLE_TERMINAL_TITLE":       {},
	"CLAUDE_CODE_MAX_OUTPUT_TOKENS":            {},
	"CLAUDE_CODE_SUBAGENT_MODEL":               {},
	"DISABLE_AUTOUPDATER":                      {},
	"DISABLE_BUG_COMMAND":                      {},
	"DISABLE_COST_WARNINGS":                    {},
	"DISABLE_ERROR_REPORTING":                  {},
	"DISABLE_NON_ESSENTIAL_MODEL_CALLS":        {},
	"DISABLE_PROMPT_CACHING":                   {},
	"DISABLE_TELEMETRY":                        {},
	"MAX_MCP_OUTPUT_TOKENS":                    {},
	"MAX_THINKING_TOKENS":                      {},
	"MCP_TIMEOUT":                              {},
	"MCP_TOOL_TIMEOUT":                         {},
	"RUST_LOG":                                 {},
}

// validateRemoteLaunchEnvironment refuses a Server-supplied overlay that sets
// any variable outside remoteLaunchEnvironment. Deleting variables only removes
// capability, so DeleteEnv is not restricted.
func validateRemoteLaunchEnvironment(policy environment.LaunchEnvironmentPolicy) error {
	var refused []string
	for name := range policy.SetEnv {
		if _, ok := remoteLaunchEnvironment[name]; !ok {
			refused = append(refused, name)
		}
	}
	if len(refused) == 0 {
		return nil
	}
	sort.Strings(refused)
	return fmt.Errorf(
		"Runtime Server Environment sets launch variables a remote Server may not set on this device: %s",
		strings.Join(refused, ", "),
	)
}
