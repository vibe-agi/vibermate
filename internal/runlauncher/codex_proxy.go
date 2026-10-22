package runlauncher

import (
	"context"
	"os"
	"strings"
	"time"
)

// prepareCodexProxyArguments constrains ordinary user/profile/CLI settings to
// the Capture proxy. Native trusted requirements remain authoritative: this is
// not process confinement and cannot override a requirement forcing OS routing.
func prepareCodexProxyArguments(ctx context.Context, executable string, prepared, environment []string) ([]string, error) {
	home, err := os.MkdirTemp("", "vibermate-codex-features-")
	if err != nil {
		return nil, &CodexProbeFailure{Reason: "start_failed"}
	}
	defer os.RemoveAll(home)
	// The feature inventory must not load the person's auth.json, shell
	// credentials or user configuration. This is capability discovery only.
	probeEnvironment := []string{"PATH=" + environmentValue(environment, "PATH"), "HOME=" + home, "CODEX_HOME=" + home}
	output, err := probeCodex(ctx, executable, []string{"features", "list"}, probeEnvironment, home, 10*time.Second)
	if err != nil {
		return nil, err
	}
	var guard []string
	seen := make(map[string]bool, 2)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || (fields[len(fields)-1] != "true" && fields[len(fields)-1] != "false") {
			continue
		}
		name := fields[0]
		if (name == "respect_system_proxy" || name == "system_proxy_fallback") && !seen[name] {
			guard = append(guard, "--disable", name)
			seen[name] = true
		}
	}
	if len(guard) == 0 {
		return prepared, nil
	}
	// Global --disable values have the same scope-loss behavior as -c.
	// Put the guard in the deepest parsed scope and retain all existing
	// disables there, so adding the guard cannot erase an earlier user choice.
	end := len(prepared)
	var disables []string
	if _, _, err := walkCodexInvocationWithLiteral(prepared, func(name, value string) error {
		if name == "--disable" {
			disables = append(disables, "--disable", value)
		}
		return nil
	}, &end); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(prepared)+len(disables)+len(guard))
	result = append(result, prepared[:end]...)
	result = append(result, disables...)
	result = append(result, guard...)
	result = append(result, prepared[end:]...)
	return result, nil
}
