//go:build darwin

package runlauncher

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const terminalFixtureModes = "\x1b[?1003h\x1b[?1006h"

func TestLauncherRestoresAnIsolatedTerminal(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"exit", "signal", "redirected"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "/usr/bin/script", "-q", "-t", "0", "/dev/null", executable, "-test.run=^TestLauncherTerminalFixture$")
			command.Env = []string{"VIBERMATE_TERMINAL_FIXTURE=supervisor", "VIBERMATE_TERMINAL_SCENARIO=" + scenario, "GORACE=atexit_sleep_ms=0", "PATH=/usr/bin:/bin"}
			output, err := command.CombinedOutput()
			if err != nil || !bytes.Contains(output, []byte("terminal-restored")) {
				t.Fatalf("PTY cleanup failed: %v %q", err, output)
			}
			wantReset := scenario != "redirected"
			if bytes.Contains(output, []byte(terminalExitReset)) != wantReset {
				t.Fatalf("terminal reset destination wrong: %q", output)
			}
		})
	}
}

func TestLauncherTerminalFixture(t *testing.T) {
	role := os.Getenv("VIBERMATE_TERMINAL_FIXTURE")
	if role == "" {
		return
	}
	scenario := os.Getenv("VIBERMATE_TERMINAL_SCENARIO")
	if role == "child" {
		if _, err := term.MakeRaw(int(os.Stdin.Fd())); err != nil {
			os.Exit(71)
		}
		_, _ = fmt.Fprint(os.Stdout, terminalFixtureModes)
		if scenario == "signal" {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		}
		os.Exit(23) // Intentionally leave raw input and mouse reporting enabled.
	}
	before, err := term.GetState(int(os.Stdin.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer term.Restore(int(os.Stdin.Fd()), before)
	beforeFlags, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TIOCGETA)
	if err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	child := exec.CommandContext(context.Background(), executable, "-test.run=^TestLauncherTerminalFixture$")
	child.Env = []string{"VIBERMATE_TERMINAL_FIXTURE=child", "VIBERMATE_TERMINAL_SCENARIO=" + scenario, "GORACE=atexit_sleep_ms=0"}
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	var redirected bytes.Buffer
	if scenario == "redirected" {
		child.Stdout, child.Stderr = &redirected, &redirected
	}
	restore := configureChild(child, 100*time.Millisecond, os.Stdin)
	defer restore()
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waitErr := waitChild(child, 100*time.Millisecond)
	restore()
	restore() // Idempotent: do not pop keyboard state twice.
	exitCode, err := childExit(waitErr)
	wantExit := 23
	if scenario == "signal" {
		wantExit = 128 + int(syscall.SIGKILL)
	}
	if err != nil || exitCode != wantExit {
		t.Fatalf("child exit changed: %d %v", exitCode, err)
	}
	afterFlags, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TIOCGETA)
	if err != nil {
		t.Fatal(err)
	}
	// Darwin sets this transient kernel flag when switching back to canonical
	// input: it asks the line discipline to reprocess pending input, not a mode
	// owned by the application. Every actual input/echo/output setting must match.
	beforeFlags.Lflag &^= unix.PENDIN
	afterFlags.Lflag &^= unix.PENDIN
	if !reflect.DeepEqual(beforeFlags, afterFlags) {
		t.Fatalf("termios not restored: before=%+v after=%+v", beforeFlags, afterFlags)
	}
	if scenario == "redirected" && redirected.String() != terminalFixtureModes {
		t.Fatalf("reset corrupted pipe output: %q", redirected.String())
	}
	_, _ = fmt.Fprintln(os.Stdout, "terminal-restored")
}
