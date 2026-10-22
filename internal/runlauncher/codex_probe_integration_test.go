package runlauncher_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

func codexProbeLauncher(t *testing.T, script string, beforeFinish ...func()) (*runlauncher.Launcher, *controlFixture) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX client fixture")
	}
	directory := t.TempDir()
	executable := filepath.Join(directory, "codex")
	// This fixture models help failures, not the separate feature inventory.
	script = strings.Replace(script, "#!/bin/sh\n", "#!/bin/sh\nif [ \"$1\" = features ] && [ \"$2\" = list ]; then exit 0; fi\n", 1)
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	control := &controlFixture{t: t, executable: executable, workspace: directory, rootPath: filepath.Join(directory, "root.pem"),
		credential: capability(0x11), proxy: capability(0x22), run: capability(0x33), expectedCommand: []string{"codex"},
		recipe: clientadapter.LaunchCodexResponsesHTTP, recognition: clientadapter.RecognitionVerified,
		adapter: &capturecontrol.ClientLaunchAdapterView{ClientAdapterView: capturecontrol.ClientAdapterView{
			ID: "codex-cli", Revision: 1, Version: "0.160.0", CatalogRevision: 7,
			Source: capturecontrol.ClientAdapterSourcePrelaunchDigestCatalog, InstallShape: clientadapter.InstallNativeSingleBinary,
			LaunchRecipe: clientadapter.LaunchCodexResponsesHTTP,
		}, StreamingFallbackPolicy: clientadapter.StreamingFallbackClientDefault},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/actions/finish") {
			for _, callback := range beforeFinish {
				callback()
			}
		}
		control.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery:       fixedDiscovery{session: localdiscovery.Session{BaseURL: server.URL, ControlCredential: control.credential}},
		BaseEnvironment: []string{"PATH=/usr/bin:/bin"}, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
		Getwd: func() (string, error) { return directory, nil }, LookPath: func(string) (string, error) { return executable, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return launcher, control
}

func TestCodexProbeSlowCompatibleHelpLaunches(t *testing.T) {
	launcher, control := codexProbeLauncher(t, "#!/bin/sh\nif [ \"$1\" = --help ]; then sleep 2; printf '      --no-daemon\\n'; exit 0; fi\ntest \"$1\" = --no-daemon\n")
	code, err := launcher.Run(context.Background(), runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: control.expectedCommand})
	if code != 0 || err != nil || control.attachCalls != 1 || control.finishCalls != 1 {
		t.Fatalf("compatible slow help: code=%d err=%v attach=%d finish=%d", code, err, control.attachCalls, control.finishCalls)
	}
}

func TestCodexProbeStartFailureIsDistinctAndSanitized(t *testing.T) {
	launcher, control := codexProbeLauncher(t, "#!/missing/SECRET-interpreter\n")
	code, err := launcher.Run(context.Background(), runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: control.expectedCommand})
	var failure *runlauncher.CodexProbeFailure
	if !errors.As(err, &failure) || failure.Reason != "start_failed" || code != 1 || err == nil || errors.Is(err, runlauncher.ErrCodexIsolationUnsupported) || !strings.Contains(err.Error(), "reason=start_failed") || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), control.executable) || control.attachCalls != 0 || control.finishCalls != 1 {
		t.Fatalf("probe start failure: code=%d err=%v attach=%d finish=%d", code, err, control.attachCalls, control.finishCalls)
	}
}

func TestCodexProbeNonzeroFailureIsDistinctAndSanitized(t *testing.T) {
	launcher, control := codexProbeLauncher(t, "#!/bin/sh\nprintf '      --no-daemon\\nSECRET-child-output\\n'\nprintf 'SECRET-stderr\\n' >&2\nexit 7\n")
	code, err := launcher.Run(context.Background(), runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: control.expectedCommand})
	var failure *runlauncher.CodexProbeFailure
	if !errors.As(err, &failure) || failure.Reason != "exit_failed" || code != 1 || err == nil || errors.Is(err, runlauncher.ErrCodexIsolationUnsupported) || !strings.Contains(err.Error(), "reason=exit_failed") || strings.Contains(err.Error(), "SECRET") || control.attachCalls != 0 || control.finishCalls != 1 {
		t.Fatalf("probe exit failure: code=%d err=%v attach=%d finish=%d", code, err, control.attachCalls, control.finishCalls)
	}
}

func TestCodexProbeCallerCancellationKeepsInterruptOutcome(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	launcher, control := codexProbeLauncher(t, "#!/bin/sh\nprintf ready > '"+ready+"'\nexec sleep 60\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		waitCodexIsolationFile(t, ready)
	}()
	start := time.Now()
	code, err := launcher.Run(ctx, runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: control.expectedCommand})
	<-done
	if code != 130 || !errors.Is(err, context.Canceled) || control.attachCalls != 0 || control.finishCalls != 1 || time.Since(start) > 3*time.Second {
		t.Fatalf("canceled probe: code=%d err=%v attach=%d finish=%d", code, err, control.attachCalls, control.finishCalls)
	}
}

func TestCodexProbeOutputOverflowIsDistinctAndSanitized(t *testing.T) {
	launcher, control := codexProbeLauncher(t, "#!/bin/sh\nprintf '      --no-daemon\\nSECRET-child-output\\n'\nhead -c 40000 /dev/zero\nhead -c 40000 /dev/zero >&2\n")
	code, err := launcher.Run(context.Background(), runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: control.expectedCommand})
	var failure *runlauncher.CodexProbeFailure
	if !errors.As(err, &failure) || failure.Reason != "output_limit" || code != 1 || err == nil || errors.Is(err, runlauncher.ErrCodexIsolationUnsupported) || !strings.Contains(err.Error(), "reason=output_limit") || strings.Contains(err.Error(), "SECRET") || control.attachCalls != 0 || control.finishCalls != 1 {
		t.Fatalf("probe overflow: code=%d err=%v attach=%d finish=%d", code, err, control.attachCalls, control.finishCalls)
	}
}

func TestCodexProbeCallerCancellationWinsDuringOverflowCleanup(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "overflow-cleanup")
	launcher, control := codexProbeLauncher(t, "#!/bin/sh\ntrap \"printf ready > '"+ready+"'; trap '' TERM; sleep 5\" TERM\nwhile :; do printf 'SECRET-child-output-0123456789\\n'; done\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		waitCodexIsolationFile(t, ready)
	}()
	code, err := launcher.Run(ctx, runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: control.expectedCommand})
	<-done
	if code != 130 || !errors.Is(err, context.Canceled) || control.attachCalls != 0 || control.finishCalls != 1 {
		t.Fatalf("caller cancellation lost to overflow cleanup: code=%d err=%v attach=%d finish=%d", code, err, control.attachCalls, control.finishCalls)
	}
}

func TestCodexProbeCallerCancellationWinsDuringCaptureFinalization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launcher, control := codexProbeLauncher(t, "#!/bin/sh\nexit 7\n", cancel)
	code, err := launcher.Run(ctx, runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: control.expectedCommand})
	if code != 130 || !errors.Is(err, context.Canceled) || control.attachCalls != 0 || control.finishCalls != 1 {
		t.Fatalf("caller cancellation lost during finalization: code=%d err=%v attach=%d finish=%d", code, err, control.attachCalls, control.finishCalls)
	}
}

func TestCodexProbeTimeoutIsDistinctAndBounded(t *testing.T) {
	launcher, control := codexProbeLauncher(t, "#!/bin/sh\nexec sleep 60\n")
	start := time.Now()
	code, err := launcher.Run(context.Background(), runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: control.expectedCommand})
	elapsed := time.Since(start)
	var failure *runlauncher.CodexProbeFailure
	if !errors.As(err, &failure) || failure.Reason != "timed_out" || code != 1 || err == nil || errors.Is(err, runlauncher.ErrCodexIsolationUnsupported) || !strings.Contains(err.Error(), "reason=timed_out") || elapsed < 29*time.Second || elapsed > 35*time.Second || control.attachCalls != 0 || control.finishCalls != 1 {
		t.Fatalf("probe timeout: code=%d err=%v elapsed=%v attach=%d finish=%d", code, err, elapsed, control.attachCalls, control.finishCalls)
	}
}
