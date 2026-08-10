package main

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
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

func TestLaunchFailureKeyDistinguishesEnvironmentSelection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		want string
	}{
		{err: runlauncher.ErrEnvironmentNotFound, want: keyEnvironmentMissing},
		{err: runlauncher.ErrEnvironmentUnavailable, want: keyEnvironmentDown},
		{err: runlauncher.ErrRuntimeUnavailable, want: keyRuntimeUnavailable},
		{err: errors.New("other launch failure"), want: keyLaunchFailed},
	}
	for _, test := range tests {
		if got := launchFailureKey(test.err); got != test.want {
			t.Fatalf("launchFailureKey(%v)=%q want %q", test.err, got, test.want)
		}
	}
}

func TestParseRunLeavesDefaultSelectionToCoreOrUsesExplicitEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		arguments   []string
		environment environment.EnvironmentID
		command     []string
	}{
		{
			name:        "Core-selected default",
			arguments:   []string{"run", "--", "claude", "--print", "hello"},
			environment: "",
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
