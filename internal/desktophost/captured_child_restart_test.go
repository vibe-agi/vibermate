package desktophost_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/desktophost"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

const childRecoveryEnvironment = "VIBERMATE_TEST_CHILD_RECOVERY"

type recoveryChildReport struct {
	PID    int    `json:"pid"`
	Status int    `json:"status"`
	Body   string `json:"body"`
	Proxy  string `json:"proxy"`
	Error  string `json:"error,omitempty"`
}

// This client lives across both Runtime generations. Its proxy environment and
// PID cannot be rewritten by restarting the App, exactly like a running Agent.
func runCapturedRecoveryChild(target string) int {
	proxy := os.Getenv("HTTP_PROXY")
	parsed, err := url.Parse(proxy)
	if err != nil {
		return 2
	}
	transport := &http.Transport{Proxy: http.ProxyURL(parsed)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	encoder := json.NewEncoder(os.Stdout)
	if encoder.Encode(recoveryChildReport{PID: os.Getpid(), Proxy: proxy}) != nil {
		return 2
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if scanner.Text() == "exit" {
			return 0
		}
		report := recoveryChildReport{PID: os.Getpid(), Proxy: proxy}
		response, err := client.Post(target, "text/plain", strings.NewReader(scanner.Text()))
		if err != nil {
			report.Error = "proxy request failed"
		} else {
			report.Status = response.StatusCode
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				report.Error = "response read failed"
			}
			report.Body = string(body)
		}
		if encoder.Encode(report) != nil {
			return 2
		}
	}
	return 0
}

func TestCapturedChildReconnectsAfterGracefulDesktopRestart(t *testing.T) {
	exerciseCapturedChildDesktopRestart(t, false)
}

func TestCapturedChildRecoveryRejectsChangedLaunchPolicy(t *testing.T) {
	exerciseCapturedChildDesktopRestart(t, true)
}

func exerciseCapturedChildDesktopRestart(t *testing.T, changePolicy bool) {
	t.Helper()
	var deliveries atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("proxy credential leaked to origin")
		}
		deliveries.Add(1)
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "reached:%s", body)
	}))
	defer origin.Close()
	root := t.TempDir()
	paths := newHostPaths(t, filepath.Join(root, "cache"))
	options := hostOptions(t, paths, filepath.Join(root, "data"))
	host := startHost(t, options)
	defer func() {
		if host != nil {
			shutdownHost(t, host)
		}
	}()
	launch := transparentLaunch(os.Args[0])
	if changePolicy {
		launch.EnvironmentID = publishRecoveryPolicy(t, host, 0)
	}
	firstSession, err := localdiscovery.NewFile(paths.DiscoveryPath(), productruntime.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := firstSession.Load()
	if err != nil {
		t.Fatal(err)
	}
	input, commands, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer commands.Close()
	reports, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reports.Close()
	defer output.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	reports.SetReadDeadline(deadline)
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery:       firstSession,
		BaseEnvironment: []string{"PATH=/usr/bin:/bin", childRecoveryEnvironment + "=" + origin.URL},
		Stdin:           input, Stdout: output, Stderr: os.Stderr,
		HeartbeatInterval: 10 * time.Millisecond, ControlTimeout: time.Second,
		TerminationTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		code, runErr := launcher.Run(ctx, launch)
		if runErr == nil && code != 0 {
			runErr = fmt.Errorf("child exit %d", code)
		}
		output.Close()
		done <- runErr
	}()
	joined := false
	defer func() {
		cancel()
		if !joined {
			if err := <-done; err != nil {
				t.Logf("launcher result during cleanup: %v", err)
			}
		}
	}()
	decoder := json.NewDecoder(reports)
	var ready recoveryChildReport
	if err := decoder.Decode(&ready); err != nil {
		t.Fatal(err)
	}
	initialRun := waitRecoveryChildAttached(t, ctx, host, ready.PID, "")
	fmt.Fprintln(commands, "first")
	var first recoveryChildReport
	if err := decoder.Decode(&first); err != nil || first.Status != 200 || first.Body != "reached:first" {
		t.Fatalf("initial request: %+v, %v", first, err)
	}
	if changePolicy {
		publishRecoveryPolicy(t, host, 1)
	}
	shutdownHost(t, host)
	host = nil
	// Restart the actual composition over the same durable store and discovery.
	host = startHost(t, hostOptions(t, paths, filepath.Join(root, "data")))
	after, err := firstSession.Load()
	if err != nil {
		t.Fatal(err)
	}
	if before.InstanceID == after.InstanceID || before.BaseURL == after.BaseURL {
		t.Fatal("fixture did not rotate both Runtime generation and control endpoint")
	}
	if changePolicy {
		select {
		case err := <-done:
			joined = true
			if !errors.Is(err, runlauncher.ErrCaptureSupervisionFailed) {
				t.Fatalf("changed launch policy did not reject recovery: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("changed launch policy left an endless reconnect loop")
		}
		if deliveries.Load() != 1 {
			t.Fatal("changed policy admitted or replayed traffic")
		}
		page, err := host.Runtime().CaptureRunReader().ListRuns(ctx, capturerun.PageRequest{})
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range page.Items {
			if run.State == capturerun.StateCreated || run.State == capturerun.StateAttached {
				t.Fatal("rejected recovery left a fresh grant active")
			}
		}
		return
	}
	replacement := waitRecoveryChildAttached(t, ctx, host, ready.PID, initialRun.ID)
	old, err := host.Runtime().CaptureRunReader().GetRun(ctx, initialRun.ID)
	if err != nil || old.State != capturerun.StateRevoked {
		t.Fatalf("old grant revived: %+v %v", old, err)
	}
	fmt.Fprintln(commands, "second")
	var second recoveryChildReport
	if err := decoder.Decode(&second); err != nil || second.Status != 200 || second.Body != "reached:second" {
		t.Fatalf("request after restart: %+v, %v", second, err)
	}
	if first.PID != ready.PID || second.PID != ready.PID || first.Proxy != ready.Proxy || second.Proxy != ready.Proxy {
		t.Fatal("recovery replaced the child or its immutable proxy environment")
	}
	if replacement.ID == initialRun.ID || deliveries.Load() != 2 {
		t.Fatal("recovery reused old authority or replayed traffic")
	}
	fmt.Fprintln(commands, "exit")
	select {
	case err := <-done:
		joined = true
		if err != nil {
			t.Fatalf("recovered child finalization: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("recovered launcher did not finish")
	}
	finished, err := host.Runtime().CaptureRunReader().GetRun(ctx, replacement.ID)
	if err != nil || finished.State != capturerun.StateFinished {
		t.Fatalf("latest Capture was not finalized: %+v %v", finished, err)
	}
}

func publishRecoveryPolicy(t *testing.T, host *desktophost.Host, previous environment.Revision) environment.EnvironmentID {
	t.Helper()
	const id environment.EnvironmentID = "recovery.test"
	snapshot, err := host.Runtime().Environments().Resolve(environment.SystemTransparentID)
	if previous != 0 {
		snapshot, err = host.Runtime().Environments().Resolve(id)
	}
	if err != nil {
		t.Fatal(err)
	}
	candidate := snapshot.Aggregate()
	candidate.ID, candidate.Name, candidate.Revision = id, "Recovery test", previous+1
	if previous == 0 {
		for i := range candidate.ClientEndpoints {
			candidate.ClientEndpoints[i].Revision = 1
			for j := range candidate.ClientEndpoints[i].ProtocolPlans {
				candidate.ClientEndpoints[i].ProtocolPlans[j].Revision = 1
			}
		}
	}
	if previous != 0 {
		candidate.LaunchEnvironment.SetEnv = map[string]string{"RECOVERY_POLICY_CHANGED": "1"}
	}
	draft, err := host.Runtime().Environments().SaveDraft(t.Context(), environment.DraftCommand{ExpectedBaseRevision: previous, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := host.Runtime().Environments().Preview(t.Context(), id, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Runtime().Environments().Publish(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	return id
}

func waitRecoveryChildAttached(t *testing.T, ctx context.Context, host *desktophost.Host, pid int, exclude string) capturerun.View {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		page, err := host.Runtime().CaptureRunReader().ListRuns(ctx, capturerun.PageRequest{})
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range page.Items {
			if run.ID != exclude && run.ProcessID == pid && run.State == capturerun.StateAttached {
				return run
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("same captured process did not attach to the new Runtime generation: %v", ctx.Err())
		case <-tick.C:
		}
	}
}
