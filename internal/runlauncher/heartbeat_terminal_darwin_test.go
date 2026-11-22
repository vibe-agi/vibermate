//go:build darwin

package runlauncher_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"golang.org/x/term"
)

func TestLauncherHeartbeatTerminal(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"terminal", "file", "pipe", "buffer", "revoke"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			input, send, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer send.Close()
			command := exec.CommandContext(ctx, "/usr/bin/script", "-q", "-t", "0", "/dev/null", executable, "-test.run=^TestLauncherHeartbeatTerminalFixture$")
			command.Env = []string{"VIBERMATE_HEARTBEAT_TERMINAL_FIXTURE=" + scenario, "GORACE=atexit_sleep_ms=0", "PATH=/usr/bin:/bin"}
			command.Stdin = input
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			// Queue literal input in the isolated PTY while the child waits through
			// recovery. No supervisor redraw or input replay may consume it.
			if _, err := send.WriteString("heartbeat-input-sentinel\n"); err != nil {
				t.Fatal(err)
			}
			if err := command.Wait(); err != nil {
				t.Fatalf("isolated heartbeat PTY failed: %v %q", err, output.String())
			}
			transcript := output.String()
			t.Logf("isolated PTY transcript: %q", transcript)
			if !strings.Contains(transcript, "heartbeat-fixture-ok") {
				t.Fatalf("heartbeat fixture did not finish: %q", transcript)
			}
			if strings.Contains(transcript, "Runtime connection interrupted") || strings.Contains(transcript, "Runtime connection restored") {
				t.Fatalf("heartbeat diagnostics overwrote the child's terminal: %q", transcript)
			}
			if scenario == "terminal" || scenario == "revoke" {
				if strings.Count(transcript, "child-input-prompt>") != 1 {
					t.Fatalf("child prompt changed: %q", transcript)
				}
			}
			wantReceived := 1
			if scenario == "revoke" {
				wantReceived = 0
			}
			if strings.Count(transcript, "child-received:heartbeat-input-sentinel") != wantReceived {
				t.Fatalf("queued child input changed: %q", transcript)
			}
			if scenario == "revoke" && !strings.Contains(transcript, "fatal-supervision-preserved: captured process supervision failed after launch") {
				t.Fatalf("fatal supervision error disappeared: %q", transcript)
			}
		})
	}
}

func TestLauncherHeartbeatTerminalFixture(t *testing.T) {
	scenario := os.Getenv("VIBERMATE_HEARTBEAT_TERMINAL_FIXTURE")
	if scenario == "" {
		return
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
		t.Fatal("fixture needs actual TTY stdin and stderr")
	}
	directory := t.TempDir()
	executable := filepath.Join(directory, "agent")
	started, release := filepath.Join(directory, "started"), filepath.Join(directory, "release")
	child := "#!/bin/sh\nprintf 'child-input-prompt>' >&2\nprintf '%s\\n' \"$$\" >> " + strconv.Quote(started) + "\nwhile [ ! -e " + strconv.Quote(release) + " ]; do sleep 0.01; done\nIFS= read -r line\nprintf 'child-received:%s\\n' \"$line\"\nexit 7\n"
	if err := os.WriteFile(executable, []byte(child), 0o700); err != nil {
		t.Fatal(err)
	}
	control := &controlFixture{
		t: t, executable: executable, workspace: directory,
		credential: capability(0x11), proxy: capability(0x22), run: capability(0x33),
		expectedCommand: []string{"agent"}, recipe: clientadapter.LaunchGeneric, recognition: clientadapter.RecognitionUnknown,
	}
	var attempts atomic.Int32
	var revoked atomic.Bool
	recovered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			// Wait for the real child's prompt before inducing an outage. A slow
			// shell startup must not turn the revocation case into a launch test.
			if attempts.Load() == 0 {
				tick := time.NewTicker(time.Millisecond)
				defer tick.Stop()
				for {
					if _, err := os.Stat(started); err == nil {
						break
					}
					select {
					case <-tick.C:
					case <-r.Context().Done():
						return
					}
				}
			}
			if revoked.Load() {
				http.Error(w, "revoked", http.StatusForbidden)
				return
			}
			switch attempts.Add(1) {
			case 1:
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			case 2:
				<-r.Context().Done()
				return
			case 3:
				http.Error(w, "busy", http.StatusTooManyRequests)
				return
			case 5:
				close(recovered) // The previous successful heartbeat restored supervision.
			}
		}
		control.ServeHTTP(w, r)
	}))
	defer server.Close()
	var diagnostic bytes.Buffer
	var stderr io.Writer = os.Stderr
	var diagnosticFile *os.File
	var pipeOutput <-chan string
	switch scenario {
	case "file":
		var err error
		diagnosticFile, err = os.Create(filepath.Join(directory, "diagnostic"))
		if err != nil {
			t.Fatal(err)
		}
		defer diagnosticFile.Close()
		stderr = diagnosticFile
	case "pipe":
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer read.Close()
		defer write.Close()
		diagnosticFile, stderr = write, write
		captured := make(chan string, 1)
		pipeOutput = captured
		go func() {
			data, _ := io.ReadAll(read)
			captured <- string(data)
		}()
	case "buffer":
		stderr = &diagnostic
	}
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery:       fixedDiscovery{session: localdiscovery.Session{BaseURL: server.URL, ControlCredential: control.credential}},
		BaseEnvironment: []string{"PATH=/usr/bin:/bin"}, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: stderr,
		HeartbeatInterval: 10 * time.Millisecond, ControlTimeout: 100 * time.Millisecond, TerminationTimeout: 100 * time.Millisecond,
		Getwd: func() (string, error) { return directory, nil }, LookPath: func(string) (string, error) { return executable, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type outcome struct {
		code int
		err  error
	}
	finished := make(chan outcome, 1)
	go func() {
		code, err := launcher.Run(ctx, runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: []string{"agent"}})
		finished <- outcome{code, err}
	}()
	select {
	case <-recovered:
	case done := <-finished:
		t.Fatalf("temporary heartbeat failure stopped child: %d %v", done.code, done.err)
	case <-ctx.Done():
		t.Fatal("heartbeat did not reconnect")
	}
	if scenario == "revoke" {
		revoked.Store(true)
	} else if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	done := <-finished
	if scenario == "revoke" {
		if done.code != 1 || !errors.Is(done.err, runlauncher.ErrCaptureSupervisionFailed) {
			t.Fatalf("revocation did not fail supervision: %d %v", done.code, done.err)
		}
		// The real error remains available after the launcher releases the terminal.
		_, _ = fmt.Fprintln(os.Stderr, "fatal-supervision-preserved:", done.err)
	} else if done.code != 7 || done.err != nil {
		t.Fatalf("child result changed: %d %v", done.code, done.err)
	}
	control.mu.Lock()
	creates, attaches, finishes, pid := control.createCalls, control.attachCalls, control.finishCalls, control.attachedPID
	control.mu.Unlock()
	if creates != 1 || attaches != 1 || finishes != 1 {
		t.Fatalf("recovery recreated capture/child: create=%d attach=%d finish=%d", creates, attaches, finishes)
	}
	starts, err := os.ReadFile(started)
	if err != nil || string(starts) != strconv.Itoa(pid)+"\n" {
		t.Fatalf("child restarted or changed identity: %q %v; attached=%d", starts, err, pid)
	}
	if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		t.Fatal("finished child still exists")
	}
	var redirected string
	switch scenario {
	case "file":
		data, err := os.ReadFile(diagnosticFile.Name())
		if err != nil {
			t.Fatal(err)
		}
		redirected = string(data)
	case "pipe":
		_ = diagnosticFile.Close()
		redirected = <-pipeOutput
	case "buffer":
		redirected = diagnostic.String()
	}
	if scenario == "file" || scenario == "pipe" || scenario == "buffer" {
		if strings.Count(redirected, "Runtime connection interrupted") != 1 || strings.Count(redirected, "Runtime connection restored") != 1 || strings.Count(redirected, "child-input-prompt>") != 1 {
			t.Fatalf("redirected diagnostics or child stderr changed: %q", redirected)
		}
	}
	_, _ = fmt.Fprintln(os.Stdout, "heartbeat-fixture-ok")
}
