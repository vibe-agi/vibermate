package main

import (
	"bytes"
	"slices"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestExecuteRequiresOneExactRunSeparator(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		nil,
		{"run"},
		{"run", "claude"},
		{"run", "--"},
		{"run", "--env"},
		{"run", "--env", "work"},
		{"run", "--env", "bad ID", "--", "claude"},
		{"run", "--environment", "work", "--", "claude"},
		{"run", "--env", "work", "--env", "personal", "--", "claude"},
		{"status"},
	} {
		code, key := execute(
			arguments,
			[]string{"LANG=en_US.UTF-8"},
			&bytes.Buffer{},
			&bytes.Buffer{},
			&bytes.Buffer{},
		)
		if code != 2 || key != keyUsage {
			t.Fatalf("execute(%v) code=%d key=%q", arguments, code, key)
		}
	}
}

func TestParseRunFreezesExplicitOrTransparentInitialEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		arguments   []string
		environment environment.EnvironmentID
		command     []string
	}{
		{
			name:        "transparent default",
			arguments:   []string{"run", "--", "claude", "--print", "hello"},
			environment: environment.SystemTransparentID,
			command:     []string{"claude", "--print", "hello"},
		},
		{
			name:        "explicit Environment",
			arguments:   []string{"run", "--env", "work", "--", "codex", "exec"},
			environment: environment.EnvironmentID("work"),
			command:     []string{"codex", "exec"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			parsed, err := parseRun(test.arguments)
			if err != nil {
				t.Fatalf("parseRun() error = %v", err)
			}
			if parsed.environmentID != test.environment ||
				!slices.Equal(parsed.command, test.command) {
				t.Fatalf("parseRun() = %+v", parsed)
			}
		})
	}
}
