//go:build darwin

package runlauncher_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"golang.org/x/sys/unix"
)

// The launcher itself must own a controlling terminal for its foreground-child
// supervision. A disposable Go test subprocess supplies that terminal, while
// the parent retains the loopback control/proxy fixture and observes the screen.
type codexProviderNativeTUIInput struct {
	Binary, Home, BaseURL, Credential, Environment string
	Command                                        []string
}

func TestCodexProviderNativeTUIProcess(t *testing.T) {
	input := os.Getenv("VIBERMATE_TEST_CODEX_PROVIDER_TUI")
	if input == "" {
		t.Skip("PTY subprocess helper")
	}
	var config codexProviderNativeTUIInput
	if err := json.Unmarshal([]byte(input), &config); err != nil {
		t.Fatal(err)
	}
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery:       fixedDiscovery{session: localdiscovery.Session{BaseURL: config.BaseURL, ControlCredential: config.Credential}},
		BaseEnvironment: codexProviderNativeEnvironment(config.Home), Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Getwd: func() (string, error) { return config.Home, nil }, LookPath: func(string) (string, error) { return config.Binary, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := launcher.Run(context.Background(), runlauncher.LaunchRequest{EnvironmentID: environment.EnvironmentID(config.Environment), ManagedAuth: true, Command: config.Command})
	if err != nil {
		t.Fatalf("TUI launcher: %v", err)
	}
	os.Exit(code)
}

func TestInstalledCodexManagedAuthTUIWithoutLogin(t *testing.T) {
	binary := codexProviderNativeBinary(t)
	for _, resume := range []bool{false, true} {
		name := "fresh_composer"
		if resume {
			name = "openai_resume_uuid"
		}
		t.Run(name, func(t *testing.T) {
			home, config := codexProviderNativeHome(t, "")
			var id, historyPath string
			var before []byte
			const oldMarker = "TUI_PRIOR_CONVERSATION_MARKER"
			if resume {
				if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"synthetic-file-api-key"}`), 0600); err != nil {
					t.Fatal(err)
				}
				seed := newCodexProviderNativeFixture(t, binary, home, environment.SystemTransparentID.String(), "api.openai.com:443", codexProviderNativeExec("", oldMarker), 0xd1, false)
				id = codexProviderNativeRun(t, seed, binary, home, false)
				seed.assertRequest("synthetic-file-api-key", oldMarker)
				historyPath, before = codexProviderNativeHistory(t, home, id, "openai")
				if err := os.Remove(filepath.Join(home, "auth.json")); err != nil {
					t.Fatal(err)
				}
			}
			command := []string{"codex", "--no-daemon", "--no-alt-screen", "--model", "fixture"}
			if resume {
				command = append(command, "resume", id)
			}
			fixture := newCodexProviderNativeFixture(t, binary, home, "work", "chatgpt.com:443", command, 0xe1, true)
			prompt := "TUI_CONTINUATION_UNIQUE_MARKER"
			screen := codexProviderNativeTUI(t, fixture, binary, home, prompt, resume)
			for _, login := range []string{"Sign in with ChatGPT", "Sign in with OpenAI", "Enter your API key"} {
				if strings.Contains(screen, login) {
					t.Fatalf("empty-login TUI showed onboarding %q", login)
				}
			}
			markers := []string{prompt}
			if resume {
				markers = append(markers, oldMarker, "synthetic reply for system_transparent")
			}
			fixture.assertRequest("vibermate-local-proxy", markers...)
			if resume {
				path, after := codexProviderNativeHistory(t, home, id, "openai")
				if path != historyPath || !strings.HasPrefix(string(after), string(before)) || !strings.Contains(string(after), prompt) {
					t.Fatal("interactive resume changed original thread/history path")
				}
			}
			codexProviderNativeUnchanged(t, home, config, nil)
		})
	}
}

func codexProviderNativeTUI(t *testing.T, fixture *codexProviderNativeFixture, binary, home, prompt string, resume bool) string {
	t.Helper()
	master, slave := runlauncher.CodexNativeTestPTY(t)
	defer master.Close()
	defer slave.Close()
	fd := int(master.Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(codexProviderNativeTUIInput{Binary: binary, Home: home, BaseURL: fixture.server.URL, Credential: fixture.control.credential, Environment: fixture.control.expectedEnvironment, Command: fixture.control.expectedCommand})
	if err != nil {
		t.Fatal(err)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, testBinary, "-test.run=^TestCodexProviderNativeTUIProcess$")
	process.Dir = home
	process.Env = append(codexProviderNativeEnvironment(home), "VIBERMATE_TEST_CODEX_PROVIDER_TUI="+string(encoded))
	process.Stdin, process.Stdout, process.Stderr = slave, slave, slave
	process.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		fixture.control.mu.Lock()
		nativePID := fixture.control.attachedPID
		finished := fixture.control.finishCalls == 1
		fixture.control.mu.Unlock()
		if nativePID > 0 && !finished {
			_ = syscall.Kill(-nativePID, syscall.SIGKILL)
		}
		_ = master.Close()
		_ = slave.Close()
		_ = process.Process.Kill()
		_ = process.Wait()
	}()
	var screen strings.Builder
	readScreen := func() {
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if n, err := unix.Poll(poll, 100); err != nil || n == 0 {
			return
		}
		var buffer [32768]byte
		n, err := unix.Read(fd, buffer[:])
		if err == unix.EAGAIN || err == unix.EINTR || err == unix.EIO {
			return
		}
		if err != nil {
			t.Fatalf("PTY read: %v", err)
		}
		chunk := string(buffer[:n])
		screen.WriteString(chunk)
		if strings.Contains(chunk, "\x1b[6n") {
			_, _ = io.WriteString(master, "\x1b[1;1R")
		}
	}
	waitForAfter := func(want string, from int) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !strings.Contains(codexProviderNativeScreenText(screen.String()[from:]), codexProviderNativeScreenText(want)) {
			if time.Now().After(deadline) {
				t.Fatalf("native TUI missing %q:\n%s", want, codexProviderNativeScreenText(screen.String()))
			}
			readScreen()
		}
	}
	waitForAfter("Tip:", 0)
	if resume {
		waitForAfter("TUI_PRIOR_CONVERSATION_MARKER", 0)
	}
	_, _ = io.WriteString(master, prompt)
	waitForAfter(prompt, 0)
	turnStart := screen.Len()
	_, _ = io.WriteString(master, "\r")
	waitForAfter("synthetic reply for work", turnStart)
	// Wait for the turn to finish before asking the native composer to exit.
	// Continue draining the terminal during shutdown so native cleanup can write.
	waitForAfter("Worked for", turnStart)
	_, _ = io.WriteString(master, "\x04")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fixture.control.mu.Lock()
		finished := fixture.control.finishCalls == 1
		fixture.control.mu.Unlock()
		if finished {
			return screen.String()
		}
		readScreen()
	}

	t.Fatalf("interactive launch did not finalize after Ctrl-D:\n%s", screen.String())
	return ""
}

var codexProviderNativeTerminalEscape = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[ -/]*[@-~])`)

func codexProviderNativeScreenText(screen string) string {
	return strings.Join(strings.Fields(codexProviderNativeTerminalEscape.ReplaceAllString(screen, "")), "")
}
