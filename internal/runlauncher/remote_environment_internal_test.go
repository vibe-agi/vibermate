package runlauncher

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestRemoteLaunchEnvironmentRefusesCodeLoadingVariables(t *testing.T) {
	err := validateRemoteLaunchEnvironment(environment.LaunchEnvironmentPolicy{
		SetEnv: map[string]string{
			"MAX_THINKING_TOKENS": "4096",
			"NODE_OPTIONS":        "--require /tmp/x.js",
			"LD_PRELOAD":          "/tmp/x.so",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "LD_PRELOAD, NODE_OPTIONS") ||
		strings.Contains(err.Error(), "MAX_THINKING_TOKENS") {
		t.Fatalf("validateRemoteLaunchEnvironment() = %v, want LD_PRELOAD and NODE_OPTIONS refused", err)
	}
}

func TestRemoteLaunchEnvironmentAcceptsBehaviorSettingsAndDeletions(t *testing.T) {
	if err := validateRemoteLaunchEnvironment(environment.LaunchEnvironmentPolicy{
		SetEnv:    map[string]string{"ANTHROPIC_MODEL": "claude-sonnet-5", "DISABLE_TELEMETRY": "1"},
		DeleteEnv: []string{"NODE_OPTIONS", "LD_PRELOAD"},
	}); err != nil {
		t.Fatalf("validateRemoteLaunchEnvironment() = %v", err)
	}
}
