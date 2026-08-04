package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRequiresExplicitPrivateInputs(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run(context.Background(), nil, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "requires --cert, --key, and --output") {
		t.Fatalf("run() error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("run() stdout = %q", stdout.String())
	}
}

func TestRunRejectsPositionalArgumentsBeforeOpeningAListener(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run(
		context.Background(),
		[]string{"unexpected"},
		&stdout,
		&stderr,
	)
	if err == nil || !strings.Contains(err.Error(), "positional") {
		t.Fatalf("run() error = %v", err)
	}
}
