package runlauncher_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

// A publisher-recognized Claude Code launched on an Upstream-only target is
// told to leave stream recovery to ViberMate (ADR 0026). That decision reads
// only fields restart recovery already holds equal, so when the Desktop
// restarts under a running command the recomputed child environment is the
// one the child was started with, and the same process continues with a
// fresh Capture. A restarted Runtime that changes the signer, or the
// authorities the decision rests on, is refused and the child is stopped.
func TestRecognizedClaudeKeepsItsFallbackAcrossDesktopRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX child process")
	}
	anthropic := []string{"api.anthropic.com:443"}
	for _, test := range []struct {
		name string
		// replace turns the restarted Runtime's grant into this variant.
		replace func(*capturecontrol.LaunchGrant, func(managed []string) *capturecontrol.LocalRecoveryView)
		refusal string
	}{
		// A catalog publication may change during an App upgrade; the
		// signer, recipe and authorities stay, so the decision does too.
		{name: "catalog_revision_changed", replace: func(grant *capturecontrol.LaunchGrant, _ func([]string) *capturecontrol.LocalRecoveryView) {
			grant.CatalogRevision, grant.Run.CatalogRevision, grant.Signer.CatalogRevision = 8, 8, 8
		}},
		{name: "signer_revision_changed", replace: func(grant *capturecontrol.LaunchGrant, _ func([]string) *capturecontrol.LocalRecoveryView) {
			grant.Signer.Revision = 2
		}, refusal: "local Runtime restart changed client launch behavior"},
		{name: "managed_authority_removed", replace: func(grant *capturecontrol.LaunchGrant, recovery func([]string) *capturecontrol.LocalRecoveryView) {
			grant.ManagedCredentialAuthorities = []string{}
			grant.LocalRecovery = recovery([]string{})
		}, refusal: "immutable launch policy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			executable := filepath.Join(directory, "claude")
			recorded := filepath.Join(directory, "fallback")
			script := "#!/bin/sh\nprintf '%s' \"${CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK-unset}\" > " +
				strconv.Quote(recorded) + "\nread line\nexit 0\n"
			if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			rootPath := filepath.Join(directory, "root.pem")
			if err := os.WriteFile(rootPath, []byte("-----BEGIN CERTIFICATE-----\nZml4dHVyZQ==\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			recovery := func(managed []string) *capturecontrol.LocalRecoveryView {
				boundary, err := environment.NewLaunchAuthorityBoundaryFromScopes("work", 1, environment.CandidateDigest{1}, anthropic, managed)
				if err != nil {
					t.Fatal(err)
				}
				return &capturecontrol.LocalRecoveryView{
					InitialEnvironmentID: boundary.InitialEnvironmentID(), InitialEnvironmentRevision: boundary.InitialEnvironmentRevision(),
					InitialEnvironmentDigest: boundary.InitialEnvironmentDigest().String(), LaunchAuthorityDigest: boundary.Digest().String(),
				}
			}
			grantFor := func(generation int, host string) capturecontrol.LaunchGrant {
				run := (&controlFixture{workspace: directory, recognition: clientadapter.RecognitionRecognized}).runView(0)
				run.ID, run.ExecutableLabel = fmt.Sprintf("capture-generation-%d", generation), "claude"
				grant := capturecontrol.LaunchGrant{
					Run: run, CatalogRevision: run.CatalogRevision, Recognition: clientadapter.RecognitionRecognized,
					LaunchRecipe: clientadapter.LaunchNodeEnvProxy,
					Signer: &capturecontrol.ClientSignerView{ID: "claude-code", Revision: 1, CatalogRevision: run.CatalogRevision,
						InstallShape: clientadapter.InstallNativeSingleBinary, LaunchRecipe: clientadapter.LaunchNodeEnvProxy, SignedPath: executable},
					ExecutablePath: executable, ProxyAddress: "http://" + host, ProxyDelivery: capturecontrol.ProxyDeliveryLocalListener,
					ProxyToken: capability(byte(0x20 + generation)), RunCapability: capability(byte(0x30 + generation)), RootPEMPath: rootPath,
					ProtectedAuthorities: append([]string{}, anthropic...), ManagedCredentialAuthorities: append([]string{}, anthropic...),
					LocalRecovery: recovery(anthropic),
				}
				if generation == 2 {
					test.replace(&grant, recovery)
				}
				return grant
			}
			type attachment struct{ generation, pid int }
			attached := make(chan attachment, 4)
			finished := make(chan string, 4)
			var generation atomic.Int32
			generation.Store(1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				current := int(generation.Load())
				switch r.URL.Path {
				case "/api/v1/status":
					writeControlJSON(w, 200, desktopcontrol.StatusResponse{
						Generation: capability(byte(current)), Ready: true, APIVersion: "v1", ProductBuild: "test-build", StatusKey: "runtime.state.initialized",
						Runtime: productruntime.RuntimeStatus{State: productruntime.RuntimeStateInitialized, InstanceID: capability(byte(current)), Host: hostcontract.KindDesktop, Storage: productruntime.StorageStateHealthy, StartedAt: time.Now().UTC()},
					})
				case "/api/v1/capture-runs":
					if r.Method != http.MethodPost || r.URL.RawQuery != "localRecovery=1" || r.Header.Get("Authorization") != "Bearer "+capability(byte(0x10+current)) {
						t.Error("a Capture was created without the current generation's CLI authority")
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					writeControlJSON(w, 201, grantFor(current, r.Host))
				default:
					parts := strings.Split(r.URL.Path, "/")
					var runGeneration int
					if len(parts) != 7 || parts[5] != "actions" {
						w.WriteHeader(404)
						return
					}
					if _, err := fmt.Sscanf(parts[4], "capture-generation-%d", &runGeneration); err != nil || runGeneration < 1 || runGeneration > 2 ||
						r.Header.Get(capturecontrol.RunCapabilityHeader) != capability(byte(0x30+runGeneration)) {
						t.Error("a run action used a foreign capability")
						w.WriteHeader(403)
						return
					}
					view := grantFor(runGeneration, r.Host).Run
					switch parts[6] {
					case "heartbeat":
						writeControlJSON(w, 200, view)
					case "attach-process":
						var input capturecontrol.AttachRequest
						if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
							t.Error(err)
							w.WriteHeader(400)
							return
						}
						attached <- attachment{runGeneration, input.ProcessID}
						view.ProcessID = input.ProcessID
						writeControlJSON(w, 200, view)
					case "finish":
						finished <- parts[4]
						w.WriteHeader(204)
					default:
						w.WriteHeader(404)
					}
				}
			}))
			defer server.Close()
			discovery := &generationChangeDiscovery{session: localdiscovery.Session{Schema: localdiscovery.Schema, InstanceID: capability(1), ProcessID: os.Getpid(), BaseURL: server.URL, ControlCredential: capability(0x11), ExpiresAt: time.Now().Add(time.Hour)}}
			input, command, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer command.Close()
			launcher, err := runlauncher.New(runlauncher.Config{
				Discovery: discovery, BaseEnvironment: []string{"PATH=/usr/bin:/bin", "CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=0"},
				Stdin: input, Stdout: io.Discard, Stderr: io.Discard,
				HeartbeatInterval: 10 * time.Millisecond, ControlTimeout: time.Second, TerminationTimeout: time.Second,
				Getwd: func() (string, error) { return directory, nil }, LookPath: func(string) (string, error) { return executable, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			type outcome struct {
				code int
				err  error
			}
			done := make(chan outcome, 1)
			go func() {
				code, err := launcher.Run(ctx, runlauncher.LaunchRequest{EnvironmentID: "work", Command: []string{"claude"}})
				done <- outcome{code, err}
			}()
			joined := false
			defer func() {
				cancel()
				if !joined {
					<-done
				}
			}()
			var first attachment
			select {
			case first = <-attached:
			case <-ctx.Done():
				t.Fatal("the initial child did not attach")
			}
			if first.generation != 1 || first.pid <= 0 {
				t.Fatalf("initial attachment = %+v", first)
			}
			value := ""
			for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
				if data, err := os.ReadFile(recorded); err == nil && len(data) != 0 {
					value = string(data)
					break
				}
			}
			if value != "1" {
				t.Fatalf("the child started with CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=%q, want the launcher's 1 in place of the inherited 0", value)
			}

			generation.Store(2)
			discovery.replace(localdiscovery.Session{Schema: localdiscovery.Schema, InstanceID: capability(2), ProcessID: os.Getpid(), BaseURL: server.URL, ControlCredential: capability(0x12), ExpiresAt: time.Now().Add(time.Hour)})
			if test.refusal != "" {
				select {
				case result := <-done:
					joined = true
					if result.code != 1 || result.err == nil || !strings.Contains(result.err.Error(), test.refusal) {
						t.Fatalf("the replacement was not refused for %q: code=%d err=%v", test.refusal, result.code, result.err)
					}
				case next := <-attached:
					t.Fatalf("a replacement that changed the launch decision attached the child: %+v", next)
				case <-ctx.Done():
					t.Fatal("the refused replacement left the child running")
				}
				// The child was stopped, not left behind: Run waits for it, and
				// the refused Capture and the initial one were both finished.
				if process, err := os.FindProcess(first.pid); err == nil && process.Signal(syscall.Signal(0)) == nil {
					t.Fatalf("the child %d is still running after the refusal", first.pid)
				}
				finishedRuns := map[string]bool{}
				for len(finishedRuns) < 2 {
					select {
					case id := <-finished:
						finishedRuns[id] = true
					case <-time.After(2 * time.Second):
						t.Fatalf("finished runs = %v, want both generations", finishedRuns)
					}
				}
				return
			}
			select {
			case next := <-attached:
				if next.generation != 2 || next.pid != first.pid {
					t.Fatalf("recovery did not attach the same child: first=%+v next=%+v", first, next)
				}
			case result := <-done:
				joined = true
				t.Fatalf("the restart stopped the recognized child: code=%d err=%v", result.code, result.err)
			case <-time.After(3 * time.Second):
				t.Fatal("the recognized child was not recovered after the restart")
			}
			if _, err := io.WriteString(command, "exit\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				joined = true
				if result.err != nil || result.code != 0 {
					t.Fatalf("the recovered child ended with code=%d err=%v", result.code, result.err)
				}
			case <-ctx.Done():
				t.Fatal("the recovered child did not finalize")
			}
			select {
			case id := <-finished:
				if id != "capture-generation-2" {
					t.Fatalf("finalized %s, want the fresh Capture", id)
				}
			default:
				t.Fatal("the fresh Capture was not finalized")
			}
		})
	}
}
