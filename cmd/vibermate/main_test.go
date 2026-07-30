package main

import (
	"bytes"
	"testing"
)

func TestExecuteRequiresOneExactRunSeparator(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		nil,
		{"run"},
		{"run", "claude"},
		{"run", "--"},
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
