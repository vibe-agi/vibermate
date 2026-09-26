package main

import (
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/runtimedata"
)

func TestBackupRestoreCommandGrammarIsClosed(t *testing.T) {
	for _, arguments := range [][]string{
		nil,
		{"--source=/source"},
		{"--target=/target"},
		{"--source=/source", "--target=/target", "extra"},
		{"--source=/source", "--target=/target", "--unknown=value"},
	} {
		if _, _, err := parseSourceTarget("backup-data", arguments); !errors.Is(err, runtimedata.ErrTarget) {
			t.Fatalf("arguments %q error = %v", arguments, err)
		}
	}
	source, target, err := parseSourceTarget(
		"restore-data", []string{"--source=/source", "--target=/target"},
	)
	if err != nil || source != "/source" || target != "/target" {
		t.Fatalf("parsed = %q, %q, %v", source, target, err)
	}
}
