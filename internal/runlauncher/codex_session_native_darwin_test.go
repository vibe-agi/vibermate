//go:build darwin

package runlauncher

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"golang.org/x/sys/unix"
)

// An opt-in real-binary check: no account, user home, daemon, or model service
// is touched. The owner and TUI only share a disposable synthetic rollout.
func TestInstalledCodexLockedSessionChoices(t *testing.T) {
	binary := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if binary == "" {
		t.Skip("set VIBERMATE_CODEX_ACCEPTANCE to an absolute native Codex binary")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("native Codex binary must be absolute")
	}
	for _, entry := range []string{"id", "name", "last", "picker", "slash", "retry"} {
		t.Run(entry, func(t *testing.T) {
			directory, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			const id = "12345678-1234-4234-9234-123456789abc"
			const stamp = "2026-09-27T00:00:00Z"
			rollout := filepath.Join(directory, "sessions/2026/09/27/rollout-2026-09-27T00-00-00-"+id+".jsonl")
			if err := os.MkdirAll(filepath.Dir(rollout), 0700); err != nil {
				t.Fatal(err)
			}
			var seed bytes.Buffer
			encoder := json.NewEncoder(&seed)
			for _, record := range []map[string]any{
				{"type": "session_meta", "timestamp": stamp, "payload": map[string]any{"id": id, "session_id": id, "timestamp": stamp, "cwd": directory, "originator": "codex_cli_rs", "cli_version": "0.157.1", "source": "cli", "model_provider": "openai"}},
				{"type": "response_item", "timestamp": stamp, "payload": map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": "Synthetic locked session fixture"}}}},
				{"type": "event_msg", "timestamp": stamp, "payload": map[string]string{"type": "user_message", "message": "Synthetic locked session fixture", "kind": "plain"}},
			} {
				if err := encoder.Encode(record); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(rollout, seed.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "auth.json"), []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"synthetic-fixture-only"}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "config.toml"), []byte("[projects."+strconv.Quote(directory)+"]\ntrust_level=\"trusted\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || r.URL.Host != "codex-fixture.invalid" {
					http.NotFound(w, r)
					return
				}
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"synthetic reply through proxy\"}]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"created_at\":1,\"status\":\"completed\",\"model\":\"fixture\",\"output\":[]}}\n\n")
			}))
			defer proxy.Close()
			childEnv := []string{"PATH=/usr/bin:/bin", "TERM=xterm-256color", "CODEX_HOME=" + directory, "HOME=" + directory,
				"OPENAI_API_KEY=synthetic-fixture-only", "OPENAI_BASE_URL=http://codex-fixture.invalid/v1", "HTTP_PROXY=" + proxy.URL, "HTTPS_PROXY=http://127.0.0.1:9"}
			settings := []string{"--config", `analytics.enabled=false`, "--config", `feedback.enabled=false`,
				"--config", `check_for_update_on_startup=false`, "--config", `model="fixture"`}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			owner := exec.CommandContext(ctx, binary, append(append([]string{}, settings...), "app-server", "--listen", "stdio://")...)
			owner.Dir, owner.Env = directory, childEnv
			input, err := owner.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := owner.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var ownerErrors bytes.Buffer
			owner.Stderr = &ownerErrors
			if err := owner.Start(); err != nil {
				t.Fatal(err)
			}
			stopOwner := sync.OnceFunc(func() { _ = input.Close(); _ = owner.Process.Kill(); _ = owner.Wait() })
			defer stopOwner()
			rpcEncoder, rpcDecoder := json.NewEncoder(input), json.NewDecoder(output)
			call := func(method string, params any) json.RawMessage {
				t.Helper()
				if err := rpcEncoder.Encode(map[string]any{"id": 1, "method": method, "params": params}); err != nil {
					t.Fatal(err)
				}
				for {
					var response struct {
						ID     int             `json:"id"`
						Result json.RawMessage `json:"result"`
						Error  json.RawMessage `json:"error"`
					}
					if err := rpcDecoder.Decode(&response); err != nil {
						t.Fatalf("%s: %v", method, err)
					}
					if response.ID != 1 {
						continue
					}
					if len(response.Error) > 0 {
						t.Fatalf("%s: %s", method, response.Error)
					}
					return response.Result
				}
			}
			call("initialize", map[string]any{"clientInfo": map[string]string{"name": "vibermate_fixture", "version": "1"}})
			call("thread/resume", map[string]any{"threadId": id, "cwd": directory})
			if entry == "name" {
				call("thread/name/set", map[string]any{"threadId": id, "name": "ViberMate fixture"})
			}
			lock := filepath.Join(directory, "thread-writer-locks", id+".lock")
			if busy, err := codexWriterBusy(lock); err != nil || !busy {
				t.Fatalf("fixture owner did not hold native writer lock: busy=%t err=%v", busy, err)
			}
			before, err := os.ReadFile(rollout)
			if err != nil {
				t.Fatal(err)
			}
			invocation := append([]string{binary}, settings...)
			invocation = append(invocation, "--no-alt-screen")
			switch entry {
			case "id", "retry":
				invocation = append(invocation, "resume", id)
			case "name":
				invocation = append(invocation, "resume", "ViberMate fixture")
			case "last":
				invocation = append(invocation, "resume", "--last")
			case "picker":
				invocation = append(invocation, "resume")
			}
			arguments, err := buildChildArguments(invocation, childEnv, clientadapter.LaunchCodexResponsesHTTP)
			if err != nil {
				t.Fatal(err)
			}
			master, slave := codexTestPTY(t)
			terminalFD := int(master.Fd())
			if err := unix.SetNonblock(terminalFD, true); err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			tui := exec.CommandContext(ctx, binary, arguments...)
			tui.Dir, tui.Env = directory, childEnv
			tui.Stdin, tui.Stdout, tui.Stderr = slave, slave, slave
			tui.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			if err := tui.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				// Close both PTY ends before reaping: a Darwin TTY writer can be
				// asleep in kernel output when the test stops draining the screen.
				_ = master.Close()
				_ = slave.Close()
				_ = tui.Process.Kill()
				_ = tui.Wait()
			}()
			var screen strings.Builder
			waitFor := func(want string) {
				t.Helper()
				deadline := time.Now().Add(12 * time.Second)
				for !strings.Contains(screen.String(), want) {
					if time.Now().After(deadline) {
						text := screen.String()
						if len(text) > 6000 {
							text = text[:2000] + "\n...\n" + text[len(text)-4000:]
						}
						t.Fatalf("missing %q in terminal:\n%s", want, text)
					}
					poll := []unix.PollFd{{Fd: int32(terminalFD), Events: unix.POLLIN}}
					if n, err := unix.Poll(poll, 100); err != nil || n == 0 {
						continue
					}
					var buffer [32768]byte
					n, err := unix.Read(terminalFD, buffer[:])
					if err == unix.EAGAIN || err == unix.EINTR {
						continue
					}
					if err != nil {
						t.Fatal(err)
					}
					chunk := string(buffer[:n])
					screen.WriteString(chunk)
					if strings.Contains(chunk, "\x1b[6n") {
						_, _ = io.WriteString(master, "\x1b[1;1R")
					}
				}
			}
			if entry == "slash" {
				waitFor("Tip:")
				_, _ = io.WriteString(master, "/resume")
				waitFor("/resume")
				_, _ = io.WriteString(master, "\r")
			}
			if entry == "picker" || entry == "slash" {
				waitFor("Synthetic locked")
				_, _ = io.WriteString(master, "\r")
			}
			waitFor("This conversation is open in another app")
			screen.Reset()
			if entry == "retry" {
				stopOwner()
				if busy, err := codexWriterBusy(lock); err != nil || busy {
					t.Fatalf("fixture owner did not release lock: %t %v", busy, err)
				}
				_, _ = io.WriteString(master, "r")
				waitFor("Ask Codex to do anything")
				if busy, err := codexWriterBusy(lock); err != nil || !busy {
					t.Fatalf("retry did not reacquire original session: %t %v", busy, err)
				}
				return
			}
			_, _ = io.WriteString(master, "f")
			waitFor("Fork created.")
			if busy, err := codexWriterBusy(lock); err != nil || !busy {
				t.Fatalf("fork stole source lock: busy=%t err=%v", busy, err)
			}
			after, err := os.ReadFile(rollout)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("fork changed original history: %v", err)
			}
			forks := 0
			err = filepath.WalkDir(filepath.Join(directory, "sessions"), func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
					return nil
				}
				file, err := os.Open(path)
				if err != nil {
					return err
				}
				defer file.Close()
				var meta struct {
					Payload struct {
						ID         string `json:"id"`
						ForkedFrom string `json:"forked_from_id"`
					} `json:"payload"`
				}
				if err := json.NewDecoder(file).Decode(&meta); err != nil {
					return err
				}
				if meta.Payload.ForkedFrom == id && meta.Payload.ID != id {
					forks++
				}
				return nil
			})
			if err != nil || forks != 1 {
				t.Fatalf("expected exactly one new fork ID: forks=%d err=%v", forks, err)
			}
			if requests.Load() != 0 {
				t.Fatal("resuming/forking unexpectedly sent a model request")
			}
			if entry == "slash" {
				screen.Reset()
				_, _ = io.WriteString(master, "synthetic continuation")
				waitFor("synthetic continuation")
				_, _ = io.WriteString(master, "\r")
				waitFor("synthetic reply through proxy")
				if requests.Load() == 0 {
					t.Fatal("fork continuation did not reach the configured proxy")
				}
			}
		})
	}
}

func codexTestPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close() })
	for _, request := range []uint{unix.TIOCPTYGRANT, unix.TIOCPTYUNLK} {
		if err := unix.IoctlSetInt(int(master.Fd()), request, 0); err != nil {
			t.Fatal(err)
		}
	}
	var name [128]byte
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, master.Fd(), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		t.Fatal(errno)
	}
	slave, err := os.OpenFile(strings.TrimRight(string(name[:]), "\x00"), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 45, Col: 140}); err != nil {
		t.Fatal(err)
	}
	return master, slave
}

func codexWriterBusy(path string) (bool, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		if err == unix.EWOULDBLOCK {
			return true, nil
		}
		return false, err
	}
	return false, unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func TestCodexSessionGuidanceUsesOnlyInteractiveTerminals(t *testing.T) {
	for _, test := range []struct {
		language string
		recipe   clientadapter.LaunchRecipe
		piped    bool
		want     string
	}{
		{"LANG=en_US.UTF-8", clientadapter.LaunchCodexResponsesHTTP, false, "F to fork"},
		{"LANG=zh_CN.UTF-8", clientadapter.LaunchCodexResponsesHTTP, false, "\u539f\u4f1a\u8bdd\u4e0d\u53d8"},
		{"LANG=en_US.UTF-8", clientadapter.LaunchGeneric, false, ""},
		{"LANG=en_US.UTF-8", clientadapter.LaunchNodeEnvProxy, false, ""},
		{"LANG=en_US.UTF-8", clientadapter.LaunchCodexResponsesHTTP, true, ""},
	} {
		master, slave := codexTestPTY(t)
		launcher := &Launcher{config: Config{BaseEnvironment: []string{test.language}, Stdin: slave, Stderr: slave}}
		var piped bytes.Buffer
		if test.piped {
			launcher.config.Stderr = &piped
		}
		launcher.announceCodexSessions(test.recipe)
		poll := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
		n, err := pollCodexGuidance(poll, 20, unix.Poll, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		var actual string
		if n > 0 {
			var data [4096]byte
			n, err := master.Read(data[:])
			if err != nil {
				t.Fatal(err)
			}
			actual = string(data[:n])
		}
		_ = slave.Close()
		_ = master.Close()
		if piped.Len() != 0 || (test.want == "" && actual != "") || (test.want != "" && !strings.Contains(actual, test.want)) {
			t.Fatalf("terminal guidance mismatch: want=%q terminal=%q pipe=%q", test.want, actual, piped.String())
		}
	}
}

func pollCodexGuidance(fds []unix.PollFd, timeout int, poll func([]unix.PollFd, int) (int, error), now func() time.Time) (int, error) {
	deadline := now().Add(time.Duration(timeout) * time.Millisecond)
	for {
		n, err := poll(fds, timeout)
		if err != unix.EINTR {
			return n, err
		}
		remaining := deadline.Sub(now())
		if remaining <= 0 {
			return n, err
		}
		// Poll uses whole milliseconds; round up only the remaining budget.
		timeout = int((remaining + time.Millisecond - 1) / time.Millisecond)
	}
}

func TestCodexGuidancePollRetriesInterruptedPTYWait(t *testing.T) {
	master, slave := codexTestPTY(t)
	defer slave.Close()
	if _, err := io.WriteString(slave, "guidance fixture\n"); err != nil {
		t.Fatal(err)
	}
	fds := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
	interrupted := false
	n, err := pollCodexGuidance(fds, 20, func(fds []unix.PollFd, timeout int) (int, error) {
		if !interrupted {
			interrupted = true
			return -1, unix.EINTR
		}
		return unix.Poll(fds, timeout)
	}, time.Now)
	if err != nil || n != 1 || fds[0].Revents&unix.POLLIN == 0 {
		t.Fatalf("interrupted PTY wait did not reach readiness: n=%d revents=%d err=%v", n, fds[0].Revents, err)
	}
	var data [4096]byte
	n, err = master.Read(data[:])
	if err != nil || !strings.Contains(string(data[:n]), "guidance fixture") {
		t.Fatalf("ready PTY did not yield fixture: n=%d err=%v", n, err)
	}
}

func TestCodexGuidancePollPreservesHardErrorsAndTimeout(t *testing.T) {
	for _, test := range []struct {
		name string
		n    int
		err  error
	}{
		{"hard error", -1, unix.EBADF},
		{"uninterrupted timeout", 0, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			n, err := pollCodexGuidance(nil, 20, func([]unix.PollFd, int) (int, error) {
				return test.n, test.err
			}, time.Now)
			if n != test.n || err != test.err {
				t.Fatalf("poll result changed: n=%d err=%v", n, err)
			}
		})
	}
}

func TestCodexGuidancePollInterruptionsRespectOriginalDeadline(t *testing.T) {
	start := time.Unix(0, 0)
	clock := []time.Time{start, start.Add(7 * time.Millisecond), start.Add(20 * time.Millisecond)}
	now := func() time.Time {
		if len(clock) == 0 {
			t.Fatal("interrupted wait exceeded its original deadline")
		}
		current := clock[0]
		clock = clock[1:]
		return current
	}
	var timeouts []int
	n, err := pollCodexGuidance(nil, 20, func(_ []unix.PollFd, timeout int) (int, error) {
		timeouts = append(timeouts, timeout)
		if len(timeouts) > 2 {
			t.Fatal("interrupted wait polled past its original deadline")
		}
		return -1, unix.EINTR
	}, now)
	if n != -1 || err != unix.EINTR {
		t.Fatalf("exhausted interruption became a successful wait: n=%d err=%v", n, err)
	}
	if len(timeouts) != 2 || timeouts[0] != 20 || timeouts[1] != 13 || len(clock) != 0 {
		t.Fatalf("wait did not consume only its original budget: timeouts=%v clock=%v", timeouts, clock)
	}
}
