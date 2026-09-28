//go:build darwin || linux

package desktophost_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

func TestACPRegisteredEditorFlowReachesAppWithoutProxyInjection(t *testing.T) {
	for _, scenario := range []struct{ content, sleep bool }{{}, {content: true}, {sleep: true}} {
		t.Run(fmt.Sprintf("content=%t/sleep=%t", scenario.content, scenario.sleep), func(t *testing.T) {
			content := scenario.content
			root := t.TempDir()
			paths := newHostPaths(t, filepath.Join(root, "cache"))
			options := hostOptions(t, paths, filepath.Join(root, "data"))
			options.AppSessionTTL = 12 * time.Hour // independent of the short Capture lease
			clock := &acpSleepClock{}
			options.Runtime.Clock = clock
			host := startHost(t, options)
			defer shutdownHost(t, host)
			discovery, err := localdiscovery.NewFile(paths.DiscoveryPath(), productruntime.SystemClock{})
			if err != nil {
				t.Fatal(err)
			}
			input, editorInput, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer editorInput.Close()
			editorOutput, output, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer editorOutput.Close()
			defer output.Close()
			stderr, err := os.CreateTemp(t.TempDir(), "stderr-")
			if err != nil {
				t.Fatal(err)
			}
			defer stderr.Close()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			heartbeatInterval := 50 * time.Millisecond
			if scenario.sleep {
				heartbeatInterval = time.Hour // observation must repair the expired lease first
			}
			launcher, err := runlauncher.New(runlauncher.Config{Discovery: discovery, BaseEnvironment: []string{"VIBERMATE_ACP_HOST_FIXTURE=1", "GORACE=atexit_sleep_ms=0", "HTTP_PROXY=http://editor-proxy.invalid:1234", "ACP_CUSTOM=editor value", "USER=editor-user", "HOME=/editor/home", "TZ=UTC"}, Stdin: input, Stdout: output, Stderr: stderr, HeartbeatInterval: heartbeatInterval, ControlTimeout: 2 * time.Second, TerminationTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			_ = editorOutput.SetReadDeadline(deadline)
			done := make(chan error, 1)
			go func() {
				code, err := launcher.RunACP(ctx, runlauncher.ACPLaunchRequest{Command: []string{executable, "-test.run=^TestACPHostAgentFixture$"}, RecordContent: content})
				output.Close()
				if err == nil && code != 0 {
					err = fmt.Errorf("exit %d", code)
				}
				done <- err
			}()
			reader := bufio.NewReader(editorOutput)
			exchange := func(request string, contains string) {
				t.Helper()
				if _, err := io.WriteString(editorInput, request+"\n"); err != nil {
					t.Fatal(err)
				}
				line, err := reader.ReadString('\n')
				if err != nil || !strings.Contains(line, contains) {
					cancel()
					launchErr := <-done
					data, _ := os.ReadFile(stderr.Name())
					t.Fatalf("protocol response: %q %v; launcher: %v (%v); stderr: %s", line, err, launchErr, errors.Unwrap(launchErr), data)
				}
			}
			exchange(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`, `"protocolVersion":1`)
			exchange(`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/editor/project","mcpServers":[]}}`, `"sessionId":"native-session"`)
			if scenario.sleep {
				clock.offset.Store(int64(3 * time.Hour))
				page, err := host.Runtime().CaptureRunReader().ListRuns(ctx, capturerun.PageRequest{Limit: 20})
				if err != nil || len(page.Items) != 1 || page.Items[0].State != capturerun.StateExpired {
					t.Fatalf("sleep did not expire capture: %+v, %v", page, err)
				}
				// Poll the isolated Runtime until the 1s observation publisher has
				// revalidated the original supervisor and renewed the same run.
				deadline := time.Now().Add(5 * time.Second)
				for {
					view, err := host.Runtime().CaptureRunReader().GetRun(ctx, page.Items[0].ID)
					if err != nil {
						t.Fatal(err)
					}
					if view.State == capturerun.StateAttached {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("ACP did not resume its expired capture")
					}
					select {
					case err := <-done:
						t.Fatalf("sleep stopped ACP: %v", err)
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			exchange(`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"native-session","prompt":[{"type":"text","text":"hello private prompt"}]}}`, `"method":"session/request_permission"`)
			exchange(`{"jsonrpc":"2.0","id":3,"result":{"outcome":{"outcome":"selected","optionId":"allow"}}}`, `"sessionUpdate":"agent_message_chunk"`)
			line, err := reader.ReadString('\n')
			if err != nil || !strings.Contains(line, `"stopReason":"end_turn"`) {
				t.Fatalf("outcome: %q %v", line, err)
			}
			editorInput.Close()
			if err := <-done; err != nil {
				data, _ := os.ReadFile(stderr.Name())
				t.Fatalf("launcher: %v, stderr: %s", err, data)
			}
			page, err := host.Runtime().CaptureRunReader().ListRuns(ctx, capturerun.PageRequest{Limit: 20})
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("capture registration: %+v %v", page, err)
			}
			run := page.Items[0]
			if run.State != capturerun.StateFinished || run.ProcessID <= 0 || run.Observation != capturerun.ObservationWaitingForTraffic {
				t.Fatalf("ACP mislabeled as HTTP: %+v", run)
			}
			if run.LocalUserLabel != "editor-user" || run.Runtime.LocalUserName != "editor-user" || run.Runtime.HomeDirectory != "/editor/home" || run.Runtime.TimeZone != "UTC" {
				t.Fatalf("ACP launch context used server environment: %+v", run.Runtime)
			}
			app := host.AppSession()
			response := controlRequest(t, app.BaseURL, http.MethodGet, "/api/v1/captures/managed_run:"+run.ID+"/acp", app.ReadToken, "vibermate://desktop")
			defer response.Body.Close()
			var record acpobservation.Record
			if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&record) != nil {
				t.Fatalf("App ACP read status %d", response.StatusCode)
			}
			if !record.Snapshot.Final || len(record.Snapshot.Sessions) != 1 || len(record.Snapshot.Prompts) != 1 || record.Snapshot.Prompts[0].State != "completed" {
				t.Fatalf("App evidence: %+v", record)
			}
			prompt := record.Snapshot.Prompts[0]
			if content && (prompt.UserText != "hello private prompt" || prompt.AgentText != "hello private answer") {
				t.Fatalf("opt-in content: %+v", prompt)
			}
			if !content && (prompt.UserText != "" || prompt.AgentText != "") {
				t.Fatal("metadata-only recorded content")
			}
			if record.Snapshot.Sessions[0].CWD != "/editor/project" || run.CWD == "/editor/project" {
				t.Fatal("native workspace claim changed launch authority")
			}
		})
	}
}

type acpSleepClock struct{ offset atomic.Int64 }

func (clock *acpSleepClock) Now() time.Time {
	return time.Now().UTC().Add(time.Duration(clock.offset.Load()))
}

func TestACPHostAgentFixture(t *testing.T) {
	if os.Getenv("VIBERMATE_ACP_HOST_FIXTURE") != "1" {
		return
	}
	if os.Getenv("HTTP_PROXY") != "http://editor-proxy.invalid:1234" || os.Getenv("ACP_CUSTOM") != "editor value" || os.Getenv("NODE_EXTRA_CA_CERTS") != "" {
		os.Exit(70)
	}
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		var request struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		if json.Unmarshal(reader.Bytes(), &request) != nil {
			os.Exit(71)
		}
		switch request.Method {
		case "initialize":
			fmt.Println(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentInfo":{"name":"fixture","version":"1"}}}`)
		case "session/new":
			fmt.Println(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"native-session"}}`)
		case "session/prompt":
			fmt.Println(`{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{"sessionId":"native-session","options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}}`)
		case "":
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello private answer"}}}}`)
			fmt.Println(`{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`)
		}
	}
	os.Exit(0)
}
