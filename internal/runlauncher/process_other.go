//go:build !darwin && !linux

package runlauncher

import (
	"io"
	"os"
	"os/exec"
	"time"
)

func configureChild(
	command *exec.Cmd,
	timeout time.Duration,
	_ io.Reader,
) func() {
	command.WaitDelay = timeout
	return func() {}
}

func relaySignals(*os.Process) func() {
	return func() {}
}

func signaledExitCode(*exec.ExitError) int {
	return 1
}
