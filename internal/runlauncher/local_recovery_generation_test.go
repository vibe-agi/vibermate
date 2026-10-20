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
	"strings"
	"sync"
	"sync/atomic"
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

// Abrupt Runtime loss can retain a fresh Capture lease. A successful heartbeat
// at a reused control address must not authorize that lease in a new generation.
func TestLauncherDetectsLocalGenerationChangeDespiteSuccessfulOldHeartbeat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX child process")
	}
	directory := t.TempDir()
	executable := filepath.Join(directory, "agent")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nread line\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	boundary, err := environment.NewLaunchAuthorityBoundaryFromScopes(environment.SystemTransparentID, 1, environment.CandidateDigest{1}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	metadata := &capturecontrol.LocalRecoveryView{
		InitialEnvironmentID: boundary.InitialEnvironmentID(), InitialEnvironmentRevision: boundary.InitialEnvironmentRevision(),
		InitialEnvironmentDigest: boundary.InitialEnvironmentDigest().String(), LaunchAuthorityDigest: boundary.Digest().String(),
	}
	view := func(generation int, pid int) capturecontrol.CaptureRunView {
		value := (&controlFixture{workspace: directory, recognition: clientadapter.RecognitionUnknown}).runView(pid)
		value.ID = fmt.Sprintf("capture-generation-%d", generation)
		return value
	}
	type attachment struct{ generation, pid int }
	attached := make(chan attachment, 4)
	finished := make(chan string, 4)
	var generation, creates, successfulOldHeartbeats atomic.Int32
	generation.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := int(generation.Load())
		switch r.URL.Path {
		case "/api/v1/status":
			if r.Header.Get("Authorization") != "Bearer "+capability(byte(0x10+current)) {
				t.Error("generation status did not use fresh CLI authentication")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeControlJSON(w, 200, desktopcontrol.StatusResponse{
				Generation: capability(byte(current)), Ready: true, APIVersion: "v1", ProductBuild: "test-build", StatusKey: "runtime.state.initialized",
				Runtime: productruntime.RuntimeStatus{State: productruntime.RuntimeStateInitialized, InstanceID: capability(byte(current)), Host: hostcontract.KindDesktop, Storage: productruntime.StorageStateHealthy, StartedAt: time.Now().UTC()},
			})
		case "/api/v1/capture-runs":
			if r.Method != http.MethodPost || r.URL.RawQuery != "localRecovery=1" || r.Header.Get("Authorization") != "Bearer "+capability(byte(0x10+current)) {
				t.Error("replacement Capture did not use fresh negotiated CLI authority")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			creates.Add(1)
			writeControlJSON(w, 201, capturecontrol.LaunchGrant{
				Run: view(current, 0), CatalogRevision: 7, Recognition: clientadapter.RecognitionUnknown, LaunchRecipe: clientadapter.LaunchGeneric,
				ExecutablePath: executable, ProxyAddress: "http://" + r.Host, ProxyDelivery: capturecontrol.ProxyDeliveryLocalListener,
				ProxyToken: capability(byte(0x20 + current)), RunCapability: capability(byte(0x30 + current)),
				ProtectedAuthorities: []string{}, ManagedCredentialAuthorities: []string{}, LocalRecovery: metadata,
			})
		default:
			var runGeneration int
			parts := strings.Split(r.URL.Path, "/")
			if len(parts) != 7 || parts[5] != "actions" {
				w.WriteHeader(404)
				return
			}
			if _, err := fmt.Sscanf(parts[4], "capture-generation-%d", &runGeneration); err != nil || runGeneration < 1 || runGeneration > 2 || r.Header.Get(capturecontrol.RunCapabilityHeader) != capability(byte(0x30+runGeneration)) {
				t.Error("run action used a foreign capability")
				w.WriteHeader(403)
				return
			}
			switch parts[6] {
			case "heartbeat":
				// This deliberately models durable recovery after abrupt loss:
				// the old run remains valid, even at the replacement listener.
				if current == 2 && runGeneration == 1 {
					successfulOldHeartbeats.Add(1)
				}
				writeControlJSON(w, 200, view(runGeneration, 0))
			case "attach-process":
				var input capturecontrol.AttachRequest
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				attached <- attachment{runGeneration, input.ProcessID}
				writeControlJSON(w, 200, view(runGeneration, input.ProcessID))
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
		Discovery: discovery, BaseEnvironment: []string{"PATH=/usr/bin:/bin"}, Stdin: input, Stdout: io.Discard, Stderr: io.Discard,
		HeartbeatInterval: 10 * time.Millisecond, ControlTimeout: time.Second, TerminationTimeout: time.Second,
		Getwd: func() (string, error) { return directory, nil }, LookPath: func(string) (string, error) { return executable, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		code, err := launcher.Run(ctx, runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: []string{"agent"}})
		if err == nil && code != 0 {
			err = fmt.Errorf("child exit %d", code)
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
	var first attachment
	select {
	case first = <-attached:
	case <-ctx.Done():
		t.Fatal("initial child did not attach")
	}
	if first.generation != 1 || first.pid <= 0 {
		t.Fatalf("initial attachment=%+v", first)
	}
	// The URL remains exactly the same; only the private discovery generation
	// and current CLI credential change, while old run heartbeats still succeed.
	generation.Store(2)
	discovery.replace(localdiscovery.Session{Schema: localdiscovery.Schema, InstanceID: capability(2), ProcessID: os.Getpid(), BaseURL: server.URL, ControlCredential: capability(0x12), ExpiresAt: time.Now().Add(time.Hour)})
	select {
	case next := <-attached:
		if next.generation != 2 || next.pid != first.pid {
			t.Fatalf("replacement did not attach the same child: first=%+v next=%+v", first, next)
		}
	case err := <-done:
		joined = true
		t.Fatalf("generation change stopped the existing child: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("successful old heartbeats hid the new Runtime generation: creates=%d oldHeartbeat200=%d", creates.Load(), successfulOldHeartbeats.Load())
	}
	if creates.Load() != 2 {
		t.Fatalf("fresh authority count=%d", creates.Load())
	}
	if _, err := io.WriteString(command, "exit\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		joined = true
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("recovered child did not finalize")
	}
	select {
	case id := <-finished:
		if id != "capture-generation-2" {
			t.Fatalf("finalized old authority: %s", id)
		}
	default:
		t.Fatal("latest grant was not finalized")
	}
}

type generationChangeDiscovery struct {
	mu      sync.RWMutex
	session localdiscovery.Session
}

func (discovery *generationChangeDiscovery) Load() (localdiscovery.Session, error) {
	discovery.mu.RLock()
	defer discovery.mu.RUnlock()
	return discovery.session, nil
}

func (discovery *generationChangeDiscovery) replace(session localdiscovery.Session) {
	discovery.mu.Lock()
	discovery.session = session
	discovery.mu.Unlock()
}
