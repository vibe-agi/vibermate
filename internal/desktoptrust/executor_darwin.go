//go:build darwin

// Package desktoptrust owns the Desktop Host's operating-system trust adapter.
// The systemtrust package remains platform-neutral planning and parsing logic.
package desktoptrust

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/vibe-agi/vibermate/internal/systemtrust"
)

var errCommandOutputOverflow = errors.New("system trust command output exceeded limit")

type productionCommandExecutor struct{}

func NewProductionCommandExecutor() (systemtrust.CommandExecutor, error) {
	return productionCommandExecutor{}, nil
}

func (productionCommandExecutor) Execute(
	ctx context.Context,
	spec systemtrust.CommandSpec,
) (systemtrust.CommandResult, error) {
	if ctx == nil || !spec.Valid() {
		return systemtrust.CommandResult{}, systemtrust.ErrCommandInvalid
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := boundedCommandBuffer{limit: 64 << 10, cancel: cancel}
	stderr := boundedCommandBuffer{limit: 64 << 10, cancel: cancel}
	command := exec.CommandContext(
		runContext,
		spec.Executable(),
		spec.Arguments()...,
	)
	// security is absolute; pin the locale so its inspection grammar is
	// deterministic and never inherit arbitrary user command hooks.
	command.Env = []string{
		"PATH=/usr/bin:/bin",
		"LANG=C",
		"LC_ALL=C",
	}
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	if stdout.overflow || stderr.overflow {
		return commandResult(systemtrust.CommandOutcomeFailed, stdout.Bytes(), stderr.Bytes())
	}
	if err := ctx.Err(); err != nil {
		outcome := systemtrust.CommandOutcomeUserCancelled
		if errors.Is(err, context.DeadlineExceeded) {
			outcome = systemtrust.CommandOutcomeTimedOut
		}
		return commandResult(outcome, stdout.Bytes(), stderr.Bytes())
	}
	if runErr == nil {
		return commandResult(systemtrust.CommandOutcomeSucceeded, stdout.Bytes(), stderr.Bytes())
	}
	if commandUserCancelled(stderr.Bytes()) {
		return commandResult(systemtrust.CommandOutcomeUserCancelled, stdout.Bytes(), stderr.Bytes())
	}
	if commandPermissionDenied(stderr.Bytes()) {
		return commandResult(systemtrust.CommandOutcomePermissionDenied, stdout.Bytes(), stderr.Bytes())
	}
	return commandResult(systemtrust.CommandOutcomeFailed, stdout.Bytes(), stderr.Bytes())
}

func commandUserCancelled(stderr []byte) bool {
	value := strings.ToLower(string(stderr))
	return strings.Contains(value, "user canceled") ||
		strings.Contains(value, "user cancelled") ||
		strings.Contains(value, "authorization was canceled") ||
		strings.Contains(value, "authorization was cancelled")
}

func commandResult(
	outcome systemtrust.CommandOutcome,
	stdout, stderr []byte,
) (systemtrust.CommandResult, error) {
	return systemtrust.NewCommandResult(outcome, stdout, stderr)
}

type boundedCommandBuffer struct {
	bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	overflow bool
}

func (buffer *boundedCommandBuffer) Write(value []byte) (int, error) {
	if buffer == nil || buffer.limit < 0 || buffer.Len()+len(value) > buffer.limit {
		if buffer != nil {
			buffer.overflow = true
			if buffer.cancel != nil {
				buffer.cancel()
			}
		}
		return 0, errCommandOutputOverflow
	}
	return buffer.Buffer.Write(value)
}

func commandPermissionDenied(stderr []byte) bool {
	value := strings.ToLower(string(stderr))
	for _, marker := range []string{
		"not authorized",
		"authorization denied",
		"authorization was denied",
		"operation not permitted",
		"user interaction is not allowed",
		"permission denied",
		"user name or passphrase you entered is not correct",
		"authentication failed",
	} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
