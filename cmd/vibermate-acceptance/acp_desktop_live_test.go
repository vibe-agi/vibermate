//go:build darwin

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimepath"
	_ "modernc.org/sqlite"
)

// Exercise the actual product path: LaunchServices starts the App, that App
// owns its native-secret daemon, and an editor starts the exact packaged CLI.
// Directly launching a newly ad-hoc-signed daemon from flutter_tester does not
// establish the same macOS application/Keychain launch context.
func TestPackagedACPThroughDesktopAppLive(t *testing.T) {
	app := os.Getenv("VIBERMATE_LIVE_TEST_APP")
	if app == "" {
		t.Skip("Set VIBERMATE_LIVE_TEST_APP to the exact packaged App.")
	}
	app, err := canonicalDesktopBundlePath(app)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	layout, err := runtimepath.FromAppCache(filepath.Join(home, "Library", "Caches", runtimepath.ApplicationID))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	err = exercisePackagedDesktopLaunch(ctx, app, layout, home, func(ctx context.Context, _ <-chan error) error {
		for _, content := range []bool{false, true} {
			if err := exercisePackagedACPPeer(ctx, filepath.Join(app, "Contents", "MacOS", "vibermate"), home, content); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Read only private copies of this test's declared roles after App shutdown.
	// No management token or personal data is inspected.
	root := filepath.Join(home, "Library", "Application Support", runtimepath.ApplicationID)
	evidence, err := readPackagedACPEvidence(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]bool{}
	for _, record := range evidence {
		policy, snapshot, state := record.Policy, record.Snapshot, record.State
		if modes[string(policy.Mode)] || state != "finished" || !snapshot.Final || snapshot.Incomplete || len(snapshot.Sessions) != 1 || len(snapshot.Prompts) != 1 || snapshot.Prompts[0].State != "completed" {
			t.Fatal("native App did not retain one completed ACP session and prompt")
		}
		modes[string(policy.Mode)] = true
		prompt := snapshot.Prompts[0]
		if policy.Mode == "full" {
			if prompt.UserText != "fixture question" || prompt.AgentText != "fixture answer" {
				t.Fatal("opted-in visible text was not retained")
			}
		} else if prompt.UserText != "" || prompt.AgentText != "" {
			t.Fatal("metadata-only run retained text")
		}
	}
	if len(modes) != 2 || !modes["full"] || !modes["metadata_only"] {
		t.Fatal("native App did not retain both recording modes")
	}
}

func exercisePackagedACPPeer(ctx context.Context, cli, home string, content bool) error {
	const peer = `
test "$ACP_FIXTURE_SETTING" = editor-owned || exit 70
test -z "$HTTP_PROXY" || exit 71
read -r request || exit 72
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentInfo":{"name":"native-app-fixture","version":"1"}}}'
read -r request || exit 73
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"editor-session"}}'
read -r request || exit 74
printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"editor-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"fixture answer"}}}}'
printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}'
cat >/dev/null
`
	args := []string{"acp"}
	if content {
		args = append(args, "--record-content")
	}
	args = append(args, "--", "/bin/sh", "-c", peer)
	command := exec.CommandContext(ctx, cli, args...)
	command.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "ACP_FIXTURE_SETTING=editor-owned"}
	command.Dir = home
	command.Stderr = io.Discard
	command.WaitDelay = 3 * time.Second
	input, err := command.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	defer output.Close()
	if err := command.Start(); err != nil {
		return err
	}
	defer func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	deadline, _ := ctx.Deadline()
	if file, ok := output.(*os.File); ok {
		_ = file.SetReadDeadline(deadline)
	}
	reader := bufio.NewReader(output)
	for index, request := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/fixture/project","mcpServers":[]}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"editor-session","prompt":[{"type":"text","text":"fixture question"}]}}`,
	} {
		if _, err := fmt.Fprintln(input, request); err != nil {
			return err
		}
		line, err := reader.ReadString('\n')
		if err != nil || !json.Valid([]byte(line)) {
			return errors.New("packaged ACP protocol response unavailable")
		}
		if index == 2 {
			line, err = reader.ReadString('\n')
			if err != nil || !strings.Contains(line, `"stopReason":"end_turn"`) {
				return errors.New("packaged ACP prompt did not return")
			}
		}
	}
	_ = input.Close()
	if _, err := reader.ReadByte(); err != io.EOF {
		return errors.New("packaged ACP stdout did not close cleanly")
	}
	return command.Wait()
}
