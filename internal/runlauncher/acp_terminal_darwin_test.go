//go:build darwin

package runlauncher_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"golang.org/x/term"
)

func TestACPProcessGivesTerminalAuthenticationARealTTY(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/script", "-q", "-t", "0", "/dev/null", executable, "-test.run=^TestACPTerminalFixture$")
	command.Env = []string{"VIBERMATE_ACP_TERMINAL_TEST=supervisor", "GORACE=atexit_sleep_ms=0", "PATH=/usr/bin:/bin"}
	input, writer := acpProcessPipe(t)
	command.Stdin = input
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	var transcript strings.Builder
	for {
		line, readErr := reader.ReadString('\n')
		transcript.WriteString(line)
		if strings.Contains(line, "login-ready") {
			_, _ = io.WriteString(writer, "temporary login\n")
			break
		}
		if readErr != nil {
			_ = command.Wait()
			t.Fatalf("terminal authentication was not interactive: %s %s", transcript.String(), stderr.String())
		}
	}
	trailing, _ := io.ReadAll(reader)
	transcript.Write(trailing)
	err = command.Wait()
	if err != nil || !strings.Contains(transcript.String(), "terminal-auth-ok") {
		t.Fatalf("terminal authentication failed: %v; %s %s", err, transcript.String(), stderr.String())
	}
}

func TestACPTerminalFixture(t *testing.T) {
	switch os.Getenv("VIBERMATE_ACP_TERMINAL_TEST") {
	case "agent":
		if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) ||
			strings.Join(os.Args[3:], " ") != "--cli auth login --claudeai" {
			_, _ = io.WriteString(os.Stderr, "terminal or appended authentication arguments were lost\n")
			os.Exit(73)
		}
		_, _ = io.WriteString(os.Stdout, "login-ready\n")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil || strings.TrimSpace(line) != "temporary login" {
			os.Exit(74)
		}
		os.Exit(23)
	case "supervisor":
		executable, _ := os.Executable()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		result, err := runlauncher.RunACPProcess(ctx, runlauncher.ACPProcessConfig{
			Executable:  executable,
			Arguments:   []string{executable, "-test.run=^TestACPTerminalFixture$", "--", "--cli", "auth", "login", "--claudeai"},
			Environment: []string{"VIBERMATE_ACP_TERMINAL_TEST=agent", "GORACE=atexit_sleep_ms=0"},
			Stdin:       os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
			ShutdownTimeout: 100 * time.Millisecond,
		})
		if err != nil || result.ExitCode != 23 || result.ClientToAgentBytes != 0 || result.AgentToClientBytes != 0 {
			_, _ = fmt.Fprintf(os.Stderr, "terminal outcome = %+v, %v\n", result, err)
			os.Exit(75)
		}
		_, _ = io.WriteString(os.Stdout, "terminal-auth-ok\n")
		os.Exit(0)
	}
}
