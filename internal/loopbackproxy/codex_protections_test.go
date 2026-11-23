package loopbackproxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// These are the daily proofs of the two Codex protections ADR 0026 lists for
// a publisher-recognized build. They run the installed release against the
// production bytes and decide nothing from what the client says about itself.
// Subtest names are a contract with the canary's proof checker: protected/...
// must hold for the protection to work, and control/... proves the protection
// still matters, so its failure is drift rather than breakage.

const codexProtectionReply = "protection fixture reply"

// codexUpgradeEvent is one request the C3 fixture saw, in arrival order.
type codexUpgradeEvent struct {
	kind string // "upgrade" once the 426 is written, "post", or "other"
	path string
}

// codexUpgradeFixture answers a Responses WebSocket upgrade as the production
// proxy does for every client, or never answers it, and completes every
// Responses POST through the production downstream.
type codexUpgradeFixture struct {
	server  *httptest.Server
	release chan struct{}
	mutex   sync.Mutex
	events  []codexUpgradeEvent
	arrived atomic.Int32 // upgrade requests read, answered or not
}

func newCodexUpgradeFixture(t *testing.T, answerUpgrade bool) *codexUpgradeFixture {
	t.Helper()
	fixture := &codexUpgradeFixture{release: make(chan struct{})}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && isWebSocketUpgrade(r) && strings.HasSuffix(r.URL.Path, "/responses"):
			fixture.arrived.Add(1)
			if !answerUpgrade {
				// Read and held: neither a 426 nor a 101 ever arrives.
				select {
				case <-r.Context().Done():
				case <-fixture.release:
				}
				return
			}
			writeReason(w, http.StatusUpgradeRequired, ReasonResponsesWebSocketUnsupported, "")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			fixture.record(codexUpgradeEvent{kind: "upgrade", path: r.URL.Path})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/responses"):
			fixture.record(codexUpgradeEvent{kind: "post", path: r.URL.Path})
			completeCodexProtectionTurn(t, w, r)
		default:
			fixture.record(codexUpgradeEvent{kind: "other", path: r.Method + " " + r.URL.Path})
			http.NotFound(w, r)
		}
	}))
	// Cleanups run last in, first out: the held handler is released before
	// Close waits for it.
	t.Cleanup(fixture.server.Close)
	t.Cleanup(func() { close(fixture.release) })
	return fixture
}

func (fixture *codexUpgradeFixture) record(event codexUpgradeEvent) {
	fixture.mutex.Lock()
	defer fixture.mutex.Unlock()
	fixture.events = append(fixture.events, event)
}

func (fixture *codexUpgradeFixture) snapshot() []codexUpgradeEvent {
	fixture.mutex.Lock()
	defer fixture.mutex.Unlock()
	return append([]codexUpgradeEvent(nil), fixture.events...)
}

// completeCodexProtectionTurn completes a Responses turn through the
// production downstream, as the managed path writes it.
func completeCodexProtectionTurn(t *testing.T, w http.ResponseWriter, r *http.Request) {
	downstream := newHTTPDownstream(w, httpDownstreamOptions{ClientDialect: protocolspec.DialectOpenAIResponses})
	defer func() { _ = downstream.Finalize() }()
	envelope, err := exchange.NewResponseEnvelope(exchange.ResponseModeEventStream, http.StatusOK,
		http.Header{"Content-Type": {"text/event-stream"}})
	if err != nil {
		t.Error(err)
		return
	}
	if err := downstream.Begin(r.Context(), envelope); err != nil {
		t.Error(err)
		return
	}
	for _, event := range codexProtectionTurnEvents() {
		if _, err := downstream.Write(r.Context(), event); err != nil {
			t.Errorf("complete the fixture turn: %v", err)
			return
		}
	}
}

func codexProtectionTurnEvents() [][]byte {
	return [][]byte{
		[]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"" + codexProtectionReply + "\"}]}}\n\n"),
		[]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"created_at\":1,\"status\":\"completed\",\"model\":\"fixture\",\"output\":[]}}\n\n"),
	}
}

// codexBuiltInProviderCommand launches the installed Codex on its built-in
// provider with exactly the settings vibermate run passes for an explicit base
// URL (invocation.go buildChildArguments). The remaining settings keep the run
// away from every host but the fixture.
func codexBuiltInProviderCommand(t *testing.T, endpoint string) *exec.Cmd {
	t.Helper()
	binary := installedCodexForProtections(t)
	directory := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	requireNoNativeCodexPluginSync(t, directory)
	args := []string{
		"-c", "openai_base_url=" + strconv.Quote(endpoint+"/v1"),
		"-c", "features.responses_websockets=false",
		"--disable", "enable_request_compression",
		"-c", "analytics.enabled=false", "-c", "feedback.enabled=false", "-c", "features.plugins=false",
		"exec", "--skip-git-repo-check", "--ephemeral", "--model", "fixture", "hello",
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = directory
	command.Env = []string{"PATH=/opt/homebrew/bin:/usr/bin:/bin", "CODEX_HOME=" + directory,
		"HOME=" + directory, "OPENAI_API_KEY=synthetic"}
	return command
}

func installedCodexForProtections(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("VIBERMATE_CODEX_ACCEPTANCE")
	if binary == "" {
		t.Skip("requires an explicitly selected local Codex binary")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("Codex binary must be absolute")
	}
	return binary
}

// runCodexProtectionCommand runs command to its end, or until limit, and says
// whether it was stopped at the limit.
func runCodexProtectionCommand(t *testing.T, command *exec.Cmd, limit time.Duration) (output []byte, stopped bool, err error) {
	t.Helper()
	var combined bytes.Buffer
	command.Stdin = strings.NewReader("")
	command.Stdout, command.Stderr = &combined, &combined
	command.WaitDelay = time.Second
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err = <-done:
	case <-time.After(limit):
		stopped = true
		_ = command.Process.Kill()
		err = <-done
	}
	return combined.Bytes(), stopped, err
}

func countCodexUpgradeEvents(events []codexUpgradeEvent, kind string) int {
	count := 0
	for _, event := range events {
		if event.kind == kind {
			count++
		}
	}
	return count
}

// TestInstalledCodexFallsBackToHTTPAfterProxy426 proves the client half of the
// WebSocket-to-HTTP fallback (ADR 0026). The proxy answers every catalogued
// Responses upgrade with the production 426 for every client; the release must
// then complete the turn over HTTP without asking again.
//
// Each unanswered-upgrade control first runs as launch/unanswered_upgrade/<arm>,
// which holds the fixture and the command, so a failure there reads as
// broken. The control itself judges only what the release did.
func TestInstalledCodexFallsBackToHTTPAfterProxy426(t *testing.T) {
	installedCodexForProtections(t)
	arms := []struct {
		name    string
		command func(*testing.T, string) *exec.Cmd
	}{
		// The built-in provider exactly as vibermate run configures it.
		{"production_shape", codexBuiltInProviderCommand},
		// A custom provider that declares WebSocket support, with no recipe
		// flag: the launcher already passes this key to pinned releases.
		{"websocket_capable", func(t *testing.T, endpoint string) *exec.Cmd {
			return nativeCodexFixtureCommand(t, endpoint, "model_providers.fixture.supports_websockets=true")
		}},
	}
	attempted := map[string]bool{}
	for _, arm := range arms {
		t.Run("protected/"+arm.name, func(t *testing.T) {
			fixture := newCodexUpgradeFixture(t, true)
			output, stopped, err := runCodexProtectionCommand(t, arm.command(t, fixture.server.URL), 15*time.Second)
			events := fixture.snapshot()
			upgrades := countCodexUpgradeEvents(events, "upgrade")
			posts := countCodexUpgradeEvents(events, "post")
			attempted[arm.name] = upgrades > 0
			t.Logf("%s: %d upgrade(s) answered 426, %d POST(s), requests in order %v", arm.name, upgrades, posts, events)
			if err != nil || stopped || !strings.Contains(string(output), codexProtectionReply) {
				t.Fatalf("the turn did not complete: err=%v stopped=%v requests=%v output=%s", err, stopped, events, output)
			}
			if upgrades > 1 {
				t.Fatalf("the release asked for a WebSocket %d times after the 426: %v", upgrades, events)
			}
			if posts == 0 {
				t.Fatalf("the turn completed without a Responses POST: %v", events)
			}
			if upgrades == 1 {
				for _, event := range events {
					if event.kind == "post" {
						t.Fatalf("the first POST arrived before the 426 was sent: %v", events)
					}
					if event.kind == "upgrade" {
						break
					}
				}
			}
		})
	}
	t.Run("control/websocket_attempted", func(t *testing.T) {
		if len(attempted) != len(arms) {
			t.Fatalf("an arm did not run: %v", attempted)
		}
		for _, arm := range arms {
			if attempted[arm.name] {
				return
			}
		}
		t.Fatal("no arm attempted a WebSocket upgrade: this release does not exercise the 426 path")
	})
	for _, arm := range arms {
		if !attempted[arm.name] {
			t.Logf("%s attempted no upgrade, so it has no unanswered-upgrade control", arm.name)
			continue
		}
		var arrived int32
		var completed bool
		var events []codexUpgradeEvent
		var output []byte
		launched := t.Run("launch/unanswered_upgrade/"+arm.name, func(t *testing.T) {
			fixture := newCodexUpgradeFixture(t, false)
			out, stopped, err := runCodexProtectionCommand(t, arm.command(t, fixture.server.URL), 5*time.Second)
			events, arrived, output = fixture.snapshot(), fixture.arrived.Load(), out
			completed = err == nil && !stopped && strings.Contains(string(out), codexProtectionReply)
			t.Logf("%s against an unanswered upgrade: %d upgrade(s) read, stopped at 5 s=%v, err=%v, requests in order %v",
				arm.name, arrived, stopped, err, events)
		})
		if !launched {
			continue
		}
		t.Run("control/unanswered_upgrade/"+arm.name, func(t *testing.T) {
			if arrived == 0 {
				t.Fatalf("the release asked for no WebSocket this time, so the control proves nothing: %v output=%s", events, output)
			}
			if completed {
				t.Fatalf("the turn completed within 5 s without an answer to its upgrade, so the 426 is not what makes the fallback prompt: %v output=%s", events, output)
			}
		})
	}
}

// TestInstalledCodexAcceptsTheDataKeepalive proves that the release renews its
// stream idle budget on the exact data frame a recognized Codex receives on
// /v1/responses (ADR 0026), and that the comment it would otherwise receive
// does not, so the frame still matters.
//
// Each arm first runs as launch/<arm>, which holds the fixture's own checks
// (the frozen wire, the response it began, one POST, an end within 15 s), so
// a failure there reads as broken. The arm's own subtest judges only whether
// the release completed the turn or gave up on it.
func TestInstalledCodexAcceptsTheDataKeepalive(t *testing.T) {
	installedCodexForProtections(t)
	dataFrame, err := openairesponses.EncodeKeepalive()
	if err != nil {
		t.Fatal(err)
	}
	if string(dataFrame) != "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n" || len(dataFrame) != 45 {
		t.Fatalf("the production data frame changed: %q", dataFrame)
	}
	for _, arm := range []struct {
		kind      string // protected or control
		name      string
		data      bool
		wire      []byte
		completes bool
	}{
		{"protected", "data_frame", true, dataFrame, true},
		{"control", "comment_only", false, []byte(": keepalive\n\n"), false},
	} {
		var completed bool
		var runErr error
		var output []byte
		// closed is the first error a write met when the release closed the
		// stream.
		var closedMutex sync.Mutex
		var closed error
		launched := t.Run("launch/"+arm.name, func(t *testing.T) {
			var posts atomic.Int32
			var keepalives atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
					http.NotFound(w, r)
					return
				}
				posts.Add(1)
				downstream := newHTTPDownstream(w, httpDownstreamOptions{
					responsesDataKeepalive: arm.data,
					ClientDialect:          protocolspec.DialectOpenAIResponses,
				})
				defer func() { _ = downstream.Finalize() }()
				if !bytes.Equal(downstream.keepaliveWire, arm.wire) {
					t.Errorf("the downstream would send %q, want %q", downstream.keepaliveWire, arm.wire)
					return
				}
				envelope, err := exchange.NewResponseEnvelope(exchange.ResponseModeEventStream, http.StatusOK,
					http.Header{"Content-Type": {"text/event-stream"}})
				if err != nil {
					t.Error(err)
					return
				}
				if err := downstream.Begin(r.Context(), envelope); err != nil {
					t.Error(err)
					return
				}
				// A client that gives up on the stream is the control's expected
				// outcome, so a write it refuses is not a fixture failure; the
				// arm judges it, and the exit status reports it too.
				gone := func(err error) {
					closedMutex.Lock()
					defer closedMutex.Unlock()
					if closed == nil {
						closed = err
					}
				}
				for range 8 {
					select {
					case <-r.Context().Done():
						gone(r.Context().Err())
						return
					case <-time.After(50 * time.Millisecond):
					}
					if err := downstream.Keepalive(r.Context()); err != nil {
						gone(err)
						return
					}
					keepalives.Add(1)
				}
				for _, event := range codexProtectionTurnEvents() {
					if _, err := downstream.Write(r.Context(), event); err != nil {
						gone(err)
						return
					}
				}
			}))
			defer server.Close()
			command := nativeCodexFixtureCommand(t, server.URL,
				`model_providers.fixture.stream_idle_timeout_ms=180`, `model_providers.fixture.stream_max_retries=0`)
			out, stopped, err := runCodexProtectionCommand(t, command, 15*time.Second)
			output, runErr = out, err
			completed = err == nil && !stopped && strings.Contains(string(out), codexProtectionReply)
			t.Logf("%s: %d keepalive(s) written of 8, %d POST(s), completed=%v, err=%v", arm.name, keepalives.Load(), posts.Load(), completed, err)
			if stopped {
				t.Fatalf("the release neither completed nor gave up within 15 s: output=%s", out)
			}
			if posts.Load() != 1 {
				t.Fatalf("the release sent %d POSTs, want exactly one: err=%v output=%s", posts.Load(), err, out)
			}
		})
		if !launched {
			continue
		}
		t.Run(arm.kind+"/"+arm.name, func(t *testing.T) {
			if arm.completes {
				closedMutex.Lock()
				err := closed
				closedMutex.Unlock()
				if err != nil {
					t.Errorf("the release closed a stream kept alive by the data frame: %v", err)
				}
				if !completed {
					t.Fatalf("the release gave up on a stream kept alive by the data frame: err=%v output=%s", runErr, output)
				}
				return
			}
			if runErr == nil {
				t.Fatalf("the release completed a stream whose only liveness was comments, so it renews its idle budget on them and the data frame is not load-bearing: output=%s", output)
			}
		})
	}
}
