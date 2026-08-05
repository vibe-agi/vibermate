//go:build darwin || linux

package runlauncher

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func configureChild(
	command *exec.Cmd,
	timeout time.Duration,
	stdin io.Reader,
) func() {
	return configureChildWithTerminalCheck(command, timeout, stdin, term.IsTerminal)
}

func configureChildWithTerminalCheck(
	command *exec.Cmd,
	timeout time.Duration,
	stdin io.Reader,
	isTerminal func(int) bool,
) func() {
	attributes := &syscall.SysProcAttr{Setpgid: true}
	command.SysProcAttr = attributes
	command.WaitDelay = timeout
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	input, ok := stdin.(*os.File)
	if !ok || !isTerminal(int(input.Fd())) {
		return func() {}
	}

	// The child owns a separate process group so cancellation can terminate its
	// descendants. An interactive child must also own the terminal foreground;
	// otherwise its first read receives SIGTTIN and a CLI such as Claude appears
	// to hang after launch.
	attributes.Foreground = true
	attributes.Ctty = int(input.Fd())
	launcherProcessGroup := unix.Getpgrp()
	var once sync.Once
	return func() {
		once.Do(func() {
			// A process restoring itself from the background would normally receive
			// SIGTTOU. Preserve the only signal disposition that survives exec
			// (ignored), ignore it across this ioctl, then restore the prior state.
			wasIgnored := signal.Ignored(syscall.SIGTTOU)
			signal.Ignore(syscall.SIGTTOU)
			_ = unix.IoctlSetPointerInt(
				int(input.Fd()),
				unix.TIOCSPGRP,
				launcherProcessGroup,
			)
			if !wasIgnored {
				signal.Reset(syscall.SIGTTOU)
			}
		})
	}
}

func relaySignals(process *os.Process) func() {
	if process == nil {
		return func() {}
	}
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case forwarded := <-signals:
				unixSignal, ok := forwarded.(syscall.Signal)
				if ok {
					_ = syscall.Kill(-process.Pid, unixSignal)
				}
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			signal.Stop(signals)
			close(done)
		})
	}
}

func signaledExitCode(exit *exec.ExitError) int {
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return 1
	}
	return 128 + int(status.Signal())
}
