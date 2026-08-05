//go:build darwin || linux

package runlauncher_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

func TestLauncherRelaysSIGINTAndFinishesFixedCodexCaptureRun(
	t *testing.T,
) {
	deadline := time.Now().Add(30 * time.Second)
	directory := t.TempDir()
	readyPath := filepath.Join(directory, "ready")
	outputPath := filepath.Join(directory, "child-output")
	interruptedPath := filepath.Join(directory, "interrupted")
	rootPath := filepath.Join(directory, "root.pem")
	if err := os.WriteFile(rootPath, []byte("test Root"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "codex")
	script := `#!/bin/sh
{
  printf 'ssl=%s\n' "$SSL_CERT_FILE"
  printf 'credential=%s\n' "$CODEX_API_KEY"
  printf 'base=%s\n' "$OPENAI_BASE_URL"
} > "$LAUNCH_TEST_OUTPUT"
trap 'printf interrupted > "$LAUNCH_TEST_INTERRUPTED"; exit 42' INT
printf ready > "$LAUNCH_TEST_READY"
while :; do sleep 1; done
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	control := &controlFixture{
		t:               t,
		executable:      executable,
		workspace:       directory,
		rootPath:        rootPath,
		credential:      capability(0x61),
		proxy:           capability(0x62),
		run:             capability(0x63),
		expectedCommand: []string{"codex"},
		recipe:          clientadapter.LaunchSSLCertFile,
		recognition:     clientadapter.RecognitionVerified,
		adapter: &capturecontrol.ClientLaunchAdapterView{
			ClientAdapterView: capturecontrol.ClientAdapterView{
				ID:              "codex-cli",
				Revision:        1,
				Version:         "0.145.0",
				CatalogRevision: 7,
				Source: capturecontrol.
					ClientAdapterSourcePrelaunchDigestCatalog,
				InstallShape: clientadapter.InstallNPMWrapperNativeChild,
				LaunchRecipe: clientadapter.LaunchSSLCertFile,
			},
			StreamingFallbackPolicy: clientadapter.
				StreamingFallbackClientDefault,
		},
		authorities: []string{
			"api.openai.com:443",
			"ambient.invalid:443",
		},
	}
	server := httptest.NewServer(control)
	defer server.Close()
	discovery := fixedDiscovery{session: localdiscovery.Session{
		Schema:            localdiscovery.Schema,
		InstanceID:        base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x64}, 20)),
		ProcessID:         os.Getpid(),
		BaseURL:           server.URL,
		ControlCredential: control.credential,
		ExpiresAt:         time.Now().UTC().Add(time.Minute),
	}}
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery: discovery,
		BaseEnvironment: []string{
			"PATH=/usr/bin:/bin",
			"LAUNCH_TEST_OUTPUT=" + outputPath,
			"LAUNCH_TEST_READY=" + readyPath,
			"LAUNCH_TEST_INTERRUPTED=" + interruptedPath,
			"OPENAI_BASE_URL=https://ambient.invalid/v1",
			"CODEX_API_KEY=ambient-secret",
		},
		HeartbeatInterval: 10 * time.Millisecond,
		ControlTimeout:    time.Second,
		Getwd: func() (string, error) {
			return directory, nil
		},
		LookPath: func(name string) (string, error) {
			if name != "codex" {
				return "", fmt.Errorf(
					"unexpected executable label %q",
					name,
				)
			}
			return executable, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan launcherOutcome, 1)
	go func() {
		code, runErr := launcher.Run(context.Background(), []string{"codex"})
		finished <- launcherOutcome{code: code, err: runErr}
	}()
	waitForChildReady(
		t,
		readyPath,
		finished,
		remainingSignalTestBudget(t, deadline),
	)
	waitForHeartbeat(t, control, remainingSignalTestBudget(t, deadline))
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT to launcher: %v", err)
	}
	convergence := time.NewTimer(remainingSignalTestBudget(t, deadline))
	defer convergence.Stop()
	select {
	case outcome := <-finished:
		if outcome.err != nil || outcome.code != 42 {
			t.Fatalf(
				"fixed Codex signal outcome code=%d error=%v",
				outcome.code,
				outcome.err,
			)
		}
	case <-convergence.C:
		t.Fatal("fixed Codex child did not converge after SIGINT")
	}
	if _, err := os.Stat(interruptedPath); err != nil {
		t.Fatalf("child did not observe SIGINT: %v", err)
	}
	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := parseLines(string(output))
	if lines["ssl"] != rootPath ||
		lines["credential"] != "vibermate-local-proxy" ||
		lines["base"] != "" {
		t.Fatalf("fixed Codex child environment = %+v", lines)
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.createCalls != 1 ||
		control.attachCalls != 1 ||
		control.heartbeatCalls == 0 ||
		control.finishCalls != 1 {
		t.Fatalf("fixed Codex control lifecycle = %+v", control)
	}
}

func remainingSignalTestBudget(t *testing.T, deadline time.Time) time.Duration {
	t.Helper()
	remaining := time.Until(deadline)
	if remaining <= 0 {
		t.Fatal("fixed Codex signal integration exceeded its total deadline")
	}
	return remaining
}

type launcherOutcome struct {
	code int
	err  error
}

func waitForChildReady(
	t *testing.T,
	path string,
	finished <-chan launcherOutcome,
	timeout time.Duration,
) {
	t.Helper()

	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case outcome := <-finished:
			t.Fatalf(
				"launcher exited before child ready: code=%d error=%v",
				outcome.code,
				outcome.err,
			)
		case <-ticker.C:
			if _, err := os.Stat(path); err == nil {
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", filepath.Base(path))
		}
	}
}

func waitForHeartbeat(
	t *testing.T,
	control *controlFixture,
	timeout time.Duration,
) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		control.mu.Lock()
		count := control.heartbeatCalls
		control.mu.Unlock()
		if count > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for CaptureRun heartbeat")
}
