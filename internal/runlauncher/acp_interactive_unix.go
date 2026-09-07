//go:build darwin || linux

package runlauncher

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func interactiveACPInput(input *os.File) bool {
	connection, err := input.SyscallConn()
	if err != nil {
		return false
	}
	interactive := false
	_ = connection.Control(func(fd uintptr) { interactive = term.IsTerminal(int(fd)) })
	return interactive
}

// A terminal-auth invocation is not an ACP transcript. Preserve the exact child
// arguments and real terminal foreground; neither bytes nor credentials cross
// the relay/observer. This is based on the inherited TTY, not vendor flag names.
func runACPInteractive(ctx context.Context, config ACPProcessConfig) (ACPProcessResult, error) {
	result := ACPProcessResult{ExitCode: 1, Interactive: true}
	child := exec.CommandContext(context.WithoutCancel(ctx), config.Executable)
	child.Args = append([]string(nil), config.Arguments...)
	child.Dir = config.Directory
	child.Env = append([]string{}, config.Environment...)
	child.Stdin, child.Stdout, child.Stderr = config.Stdin, config.Stdout, config.Stderr
	restore := configureChild(child, config.ShutdownTimeout, config.Stdin)
	defer restore()
	signals, stopSignals := subscribeChildSignals()
	defer stopSignals()
	if err := child.Start(); err != nil {
		return result, &acpProcessError{"interactive start", err}
	}
	finished := make(chan error, 1)
	go func() { finished <- child.Wait() }()
	cancelled := ctx.Done()
	var expiry <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	var terminal error
	forward := func(value unix.Signal) {
		if err := unix.Kill(-child.Process.Pid, value); err != nil && !errors.Is(err, unix.ESRCH) && terminal == nil {
			terminal = &acpProcessError{"interactive signal forwarding", err}
		}
		if timer == nil {
			timer = time.NewTimer(config.ShutdownTimeout)
			expiry = timer.C
		}
	}
	for {
		select {
		case waitErr := <-finished:
			var err error
			result.ExitCode, err = childExit(waitErr)
			cleanupErr := finishChildGroup(child.Process, config.ShutdownTimeout)
			if terminal != nil {
				return result, terminal
			}
			if err != nil {
				return result, &acpProcessError{"interactive wait", err}
			}
			if cleanupErr != nil {
				return result, &acpProcessError{"interactive descendant cleanup", cleanupErr}
			}
			return result, nil
		case <-cancelled:
			cancelled = nil
			terminal = ctx.Err()
			forward(unix.SIGTERM)
		case value := <-signals:
			if value, ok := value.(unix.Signal); ok {
				if result.Signal == 0 {
					result.Signal = int(value)
				}
				forward(value)
			}
		case <-expiry:
			expiry = nil
			_ = unix.Kill(-child.Process.Pid, unix.SIGKILL)
		}
	}
}
