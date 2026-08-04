//go:build darwin

package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestDesktopProcessInspectionIncludesParentAndBirthIdentity(t *testing.T) {
	t.Parallel()

	process, err := inspectDesktopProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if process.parentID <= 0 || process.started.seconds <= 0 {
		t.Fatalf("Desktop process snapshot = %+v", process)
	}
}

func TestDesktopProcessInspectionDistinguishesAMissingProcess(t *testing.T) {
	t.Parallel()

	_, err := inspectDesktopProcess(int(^uint32(0) >> 1))
	if !errors.Is(err, errDesktopProcessUnavailable) {
		t.Fatalf("missing Desktop process error = %v", err)
	}
}

func TestDesktopApplicationGuardianScriptParsesBeforeFailingClosed(t *testing.T) {
	t.Parallel()

	command := exec.Command(
		"/usr/bin/osascript",
		"-l",
		"JavaScript",
		"-e",
		desktopApplicationGuardianScript(desktopApplicationIdentity{
			ProcessID:      int(^uint32(0) >> 1),
			BundlePath:     "/nonexistent/VibeMate.app",
			ExecutablePath: "/nonexistent/VibeMate.app/Contents/MacOS/VibeMate",
		}),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("unbound Desktop guardian unexpectedly started")
	}
	message := string(output)
	if !strings.Contains(message, "Desktop application instance count changed") &&
		!strings.Contains(message, "Desktop application process identity changed") {
		t.Fatalf("Desktop guardian failed before its closed identity checks: %s", message)
	}
}

func TestDesktopApplicationGuardianBindsOneObjectWithoutActing(t *testing.T) {
	t.Parallel()

	const finderBundleID = "com.apple.finder"
	query := exec.Command(
		"/usr/bin/osascript",
		"-l",
		"JavaScript",
		"-e",
		desktopApplicationsScriptForBundle(finderBundleID),
	)
	output, err := query.Output()
	if err != nil {
		t.Fatal(err)
	}
	applications, err := parseDesktopApplications(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(applications) != 1 {
		t.Skipf("Finder application count = %d", len(applications))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	guardian := exec.CommandContext(
		ctx,
		"/usr/bin/osascript",
		"-l",
		"JavaScript",
		"-e",
		desktopApplicationGuardianScriptForBundle(
			finderBundleID,
			applications[0],
		),
	)
	input, err := guardian.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	guardianOutput, err := guardian.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := guardian.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(guardianOutput)
	ready, err := reader.ReadString('\n')
	if err != nil || ready != "ready\n" {
		t.Fatalf("Desktop guardian readiness = %q", ready)
	}
	if _, err := io.WriteString(input, "invalid\n"); err != nil {
		t.Fatal(err)
	}
	refused, err := reader.ReadString('\n')
	if err != nil || refused != "refused\n" {
		t.Fatalf("Desktop guardian refusal = %q, %v", refused, err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := guardian.Wait(); err != nil {
		t.Fatal(err)
	}
}
