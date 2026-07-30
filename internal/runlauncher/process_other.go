//go:build !darwin && !linux

package runlauncher

import (
	"os"
	"os/exec"
	"time"
)

func configureChild(command *exec.Cmd, timeout time.Duration) {
	command.WaitDelay = timeout
}

func relaySignals(*os.Process) func() {
	return func() {}
}

func signaledExitCode(*exec.ExitError) int {
	return 1
}
