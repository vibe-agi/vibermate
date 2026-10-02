//go:build darwin || linux

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestCommandSIGTERMCancelsPreparation(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(deadline, binary, "-test.run=^TestCommandSIGTERMFixture$")
	child.Env = append(os.Environ(), "VIBERMATE_TEST_COMMAND_SIGTERM=1")
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	ready, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || ready != "ready\n" {
		t.Fatalf("signal fixture readiness: %q, %v", ready, err)
	}
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("TERM did not reach command cleanup: %v", err)
	}
	acp, stop := commandContext([]string{"acp", "--", "adapter"})
	defer stop()
	if acp.Done() != nil {
		t.Fatal("ACP must keep its signal relay instead of command cancellation")
	}
}

func TestCommandSIGTERMFixture(t *testing.T) {
	if os.Getenv("VIBERMATE_TEST_COMMAND_SIGTERM") != "1" {
		return
	}
	ctx, stop := commandContext([]string{"run", "--", "codex"})
	defer stop()
	fmt.Fprintln(os.Stdout, "ready")
	<-ctx.Done()
}
