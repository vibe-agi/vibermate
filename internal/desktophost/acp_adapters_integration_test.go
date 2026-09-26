//go:build darwin || linux

package desktophost_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

// Explicit opt-in: run with the pinned adapters in an isolated, network-disabled
// container. No host HOME, provider credential or editor settings are inherited.
// This validates actual published adapter startup/auth negotiation, not paid
// provider execution or a particular GUI editor build.
func TestACPPublishedAdaptersThroughRegisteredWrapper(t *testing.T) {
	directory := os.Getenv("VIBERMATE_ACP_ACCEPTANCE_ADAPTERS")
	if directory == "" {
		t.Skip("requires isolated, explicitly installed ACP adapters")
	}
	for _, agent := range []string{"codex-acp", "claude-agent-acp"} {
		t.Run(agent, func(t *testing.T) {
			root := t.TempDir()
			paths := newHostPaths(t, filepath.Join(root, "cache"))
			host := startHost(t, hostOptions(t, paths, filepath.Join(root, "data")))
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
			stderr, err := os.CreateTemp(root, "stderr-")
			if err != nil {
				t.Fatal(err)
			}
			defer stderr.Close()
			privateHome := filepath.Join(root, "empty-home")
			if err := os.Mkdir(privateHome, 0700); err != nil {
				t.Fatal(err)
			}
			launcher, err := runlauncher.New(runlauncher.Config{
				Discovery: discovery, Getwd: func() (string, error) { return root, nil },
				BaseEnvironment: []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + privateHome, "XDG_CONFIG_HOME=" + privateHome, "NO_BROWSER=1"},
				Stdin:           input, Stdout: output, Stderr: stderr,
				ControlTimeout: 3 * time.Second, TerminationTimeout: 3 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			_ = editorOutput.SetReadDeadline(deadline)
			done := make(chan error, 1)
			go func() {
				code, err := launcher.RunACP(ctx, runlauncher.ACPLaunchRequest{Command: []string{filepath.Join(directory, agent)}})
				output.Close()
				if err == nil && code != 0 {
					err = fmt.Errorf("adapter exit %d", code)
				}
				done <- err
			}()
			joined := false
			defer func() {
				cancel()
				if !joined {
					<-done
				}
			}()
			reader := bufio.NewReader(editorOutput)
			exchange := func(id int, method string, params any) map[string]json.RawMessage {
				t.Helper()
				data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
				if _, err := editorInput.Write(append(data, '\n')); err != nil {
					t.Fatal(err)
				}
				for {
					line, err := reader.ReadBytes('\n')
					if err != nil {
						t.Fatalf("adapter closed during %s: %v", method, err)
					}
					var message map[string]json.RawMessage
					if json.Unmarshal(line, &message) != nil {
						t.Fatal("non-JSON diagnostic contaminated protocol stdout")
					}
					if string(message["id"]) == fmt.Sprint(id) && len(message["method"]) == 0 {
						return message
					}
					if len(message["id"]) != 0 && len(message["method"]) != 0 {
						t.Fatal("unexpected reverse request before any prompt")
					}
				}
			}
			init := exchange(1, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{"auth": map[string]any{"terminal": true}}, "clientInfo": map[string]string{"name": "vibermate-acceptance", "version": "1"}})
			var initialized struct {
				ProtocolVersion int `json:"protocolVersion"`
				AgentInfo       struct {
					Version string `json:"version"`
				} `json:"agentInfo"`
			}
			if json.Unmarshal(init["result"], &initialized) != nil || initialized.ProtocolVersion != 1 || initialized.AgentInfo.Version == "" {
				t.Fatal("initialize failed")
			}
			created := exchange(2, "session/new", map[string]any{"cwd": root, "mcpServers": []any{}})
			if agent == "codex-acp" {
				var failure struct {
					Code int `json:"code"`
				}
				if json.Unmarshal(created["error"], &failure) != nil || failure.Code != -32000 {
					t.Fatal("expected unauthenticated Codex auth-required boundary")
				}
			} else if len(created["result"]) == 0 {
				t.Fatal("Claude session/new failed")
			}
			editorInput.Close()
			if _, err := io.Copy(io.Discard, reader); err != nil {
				t.Fatal(err)
			}
			err = <-done
			joined = true
			if err != nil {
				t.Fatal(err)
			}
			page, err := host.Runtime().CaptureRunReader().ListRuns(ctx, capturerun.PageRequest{Limit: 20})
			if err != nil || len(page.Items) != 1 || page.Items[0].State != capturerun.StateFinished {
				t.Fatal("wrapper did not finish its Capture")
			}
			record, err := host.Runtime().ACPObservations().Read(ctx, page.Items[0].ID)
			if err != nil || !record.Snapshot.Final || record.Snapshot.Agent.Version != initialized.AgentInfo.Version || len(record.Snapshot.Prompts) != 0 {
				t.Fatal("adapter evidence mismatch")
			}
			if agent == "claude-agent-acp" && len(record.Snapshot.Sessions) != 1 {
				t.Fatal("native Claude session was not projected")
			}
			t.Logf("published %s %s: initialize, native session/auth boundary, EOF and durable final observation passed", agent, initialized.AgentInfo.Version)
		})
	}
}
