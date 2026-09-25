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
	signals, stopSignals := subscribeChildSignals()
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
			stopSignals()
			close(done)
		})
	}
}

func subscribeChildSignals() (<-chan os.Signal, func()) {
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	return signals, func() { signal.Stop(signals) }
}

func signaledExitCode(exit *exec.ExitError) int {
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return 1
	}
	return 128 + int(status.Signal())
}

// finishChildGroup is called after the direct child is reaped. A wrapper may
// exit before its descendants and close all protocol pipes; pipe EOF alone is
// not evidence that its process group ended. Only a group created for our child
// is eligible here. Groups are lifecycle containment, not a security sandbox.
func finishChildGroup(process *os.Process, timeout time.Duration) error {
	if process == nil || process.Pid <= 0 {
		return nil
	}
	err := syscall.Kill(-process.Pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return errors.New("child process group termination failed")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if errors.Is(syscall.Kill(-process.Pid, 0), syscall.ESRCH) {
				return nil
			}
		case <-deadline.C:
			err := syscall.Kill(-process.Pid, syscall.SIGKILL)
			if err == nil || errors.Is(err, syscall.ESRCH) {
				return nil
			}
			return errors.New("child process group forced termination failed")
		}
	}
}
