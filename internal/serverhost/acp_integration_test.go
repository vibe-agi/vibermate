//go:build darwin || linux

package serverhost_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serverhost"
)

func TestRemoteACPUsesRuntimeLoginAndOwnerOnlyManagement(t *testing.T) {
	for _, plainHTTP := range []bool{false, true} {
		t.Run(fmt.Sprint("http=", plainHTTP), func(t *testing.T) {
			root := t.TempDir()
			options := serverOptions(t, root)
			if plainHTTP {
				options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
			}
			host, err := serverhost.Start(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			defer shutdownServer(t, host)
			origin := host.Status().Scheme + "://" + host.Status().ListenAddress
			config := runlauncher.RemoteConfig{Target: mustServerTarget(t, origin), StateDirectory: filepath.Join(root, "client-state"), DisplayName: "ACP test editor", Clock: productruntime.SystemClock{}, Random: rand.Reader}
			loginRemoteTestUser(t, host, config)
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
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			launcher, err := runlauncher.New(runlauncher.Config{Remote: &config, Getwd: func() (string, error) { return root, nil }, BaseEnvironment: []string{"VIBERMATE_ACP_REMOTE_FIXTURE=1", "GORACE=atexit_sleep_ms=0"}, Stdin: input, Stdout: output, Stderr: stderr, ControlTimeout: 2 * time.Second, TerminationTimeout: time.Second, HeartbeatInterval: 50 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			_ = editorOutput.SetReadDeadline(deadline)
			done := make(chan error, 1)
			go func() {
				code, err := launcher.RunACP(ctx, runlauncher.ACPLaunchRequest{Command: []string{executable, "-test.run=^TestACPRemoteAgentFixture$"}})
				output.Close()
				if err == nil && code != 0 {
					err = fmt.Errorf("exit %d", code)
				}
				done <- err
			}()
			if _, err := io.WriteString(editorInput, "{\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":1}}\n"); err != nil {
				t.Fatal(err)
			}
			line, err := bufio.NewReader(editorOutput).ReadString('\n')
			if err != nil || !strings.Contains(line, `"protocolVersion":1`) {
				cancel()
				launchErr := <-done
				t.Fatalf("remote ACP initialize: %v; launch: %v", err, launchErr)
			}
			editorInput.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			page, err := host.Runtime().CaptureRunReader().ListRuns(ctx, capturerun.PageRequest{Limit: 20})
			if err != nil || len(page.Items) != 1 {
				t.Fatal("missing remote Capture")
			}
			run := page.Items[0]
			if run.State != capturerun.StateFinished || run.RuntimeUsername != "integration-user" || run.LoginSessionID == "" || run.WorkspaceID == "" {
				t.Fatal("remote ACP attribution missing")
			}
			client := tlsHTTPClient(t)
			url := origin + "/api/v1/captures/managed_run:" + run.ID + "/acp"
			unauthorized := sendJSON(t, client, http.MethodGet, url, "", nil)
			unauthorized.Body.Close()
			if unauthorized.StatusCode != http.StatusUnauthorized {
				t.Fatal("ACP management did not require owner authority")
			}
			key, err := os.ReadFile(host.Status().RecoveryKeyPath)
			if err != nil {
				t.Fatal(err)
			}
			login := postJSON(t, client, origin+servercontrol.AdminSessionPath, "", servercontrol.AdminLogin{Schema: servercontrol.AdminLoginSchema, AccessKey: strings.TrimSpace(string(key))})
			var admin servercontrol.AdminSession
			if login.StatusCode != http.StatusCreated || json.NewDecoder(login.Body).Decode(&admin) != nil {
				t.Fatal("test owner login failed")
			}
			login.Body.Close()
			response := sendJSON(t, client, http.MethodGet, url, admin.ReadToken, nil)
			defer response.Body.Close()
			var record acpobservation.Record
			if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&record) != nil || !record.Snapshot.Final || record.Snapshot.Agent.Name != "remote-fixture" {
				t.Fatal("Web owner cannot read remote ACP evidence")
			}
		})
	}
}

func TestACPRemoteAgentFixture(t *testing.T) {
	if os.Getenv("VIBERMATE_ACP_REMOTE_FIXTURE") != "1" {
		return
	}
	if os.Getenv("HTTP_PROXY") != "" || os.Getenv("NODE_EXTRA_CA_CERTS") != "" {
		os.Exit(70)
	}
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		fmt.Println(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentInfo":{"name":"remote-fixture","version":"1"}}}`)
	}
	os.Exit(0)
}
