package main

import (
	"reflect"
	"testing"
)

func TestACPCommandSeparatesWrapperOptionsFromEditorAndAuthArguments(t *testing.T) {
	got, err := parseACP([]string{"acp", "--record-content", "--server", "https://example.test:9666", "--", "/bin/claude-agent-acp", "--cli", "auth", "login", "--claudeai", "--env=agent-option"})
	if err != nil || !got.recordContent || !got.server.Valid() || !reflect.DeepEqual(got.command, []string{"/bin/claude-agent-acp", "--cli", "auth", "login", "--claudeai", "--env=agent-option"}) {
		t.Fatalf("parse: %+v %v", got, err)
	}
	for _, args := range [][]string{{"acp"}, {"acp", "codex-acp"}, {"acp", "--env", "work", "--", "codex-acp"}, {"acp", "--", ""}, {"acp", "--record-content", "--record-content", "--", "codex-acp"}} {
		if _, err := parseACP(args); err == nil {
			t.Fatalf("accepted invalid or unenforced options: %q", args)
		}
	}
}
