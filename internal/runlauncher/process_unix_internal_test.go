//go:build darwin || linux

package runlauncher

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestInteractiveChildOwnsTheTerminalForeground(t *testing.T) {
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()

	command := exec.Command("ignored")
	restore := configureChildWithTerminalCheck(
		command,
		time.Second,
		input,
		func(int) bool { return true },
	)
	t.Cleanup(restore)

	if command.SysProcAttr == nil ||
		!command.SysProcAttr.Setpgid ||
		!command.SysProcAttr.Foreground ||
		command.SysProcAttr.Ctty != int(input.Fd()) {
		t.Fatalf(
			"interactive process attributes = %+v",
			command.SysProcAttr,
		)
	}
}

func TestNonInteractiveChildRetainsOnlyProcessGroupIsolation(t *testing.T) {
	command := exec.Command("ignored")
	restore := configureChildWithTerminalCheck(
		command,
		time.Second,
		os.Stdin,
		func(int) bool { return false },
	)
	restore()

	if command.SysProcAttr == nil ||
		!command.SysProcAttr.Setpgid ||
		command.SysProcAttr.Foreground ||
		command.SysProcAttr.Ctty != 0 {
		t.Fatalf(
			"non-interactive process attributes = %+v",
			command.SysProcAttr,
		)
	}
}
