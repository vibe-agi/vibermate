//go:build darwin || linux

package runlauncher

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

// Session delay stands in for a slow Runtime Server. The create rejection lets
// the test observe successful authentication without creating a Capture.
func remoteAuthBudgetFixture(t *testing.T, delay time.Duration, entered chan<- struct{}) (RemoteConfig, *atomic.Int32) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	directory := t.TempDir()
	machine, err := remoteLoginMachine(t.Context(), directory, rand.Reader, now)
	if err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x53}, 32))
	creates := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing saved-login authentication")
		}
		if r.URL.Path == servercontrol.RuntimeUserCurrentSessionPath {
			if entered != nil {
				entered <- struct{}{}
			}
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				return
			}
			_ = json.NewEncoder(w).Encode(servercontrol.RuntimeUserCurrentSession{Schema: servercontrol.RuntimeUserCurrentSessionSchema, APIVersion: "v1", ProductBuild: "test-build", InstanceID: "instance.test", SessionID: "login.test", MachineID: machine, User: servercontrol.RuntimeUserView{ID: "user.test", Username: "alice"}})
			return
		}
		if r.URL.Path != "/api/v1/capture-runs" {
			t.Errorf("unexpected action %s", r.URL.Path)
		}
		creates.Add(1)
		w.WriteHeader(503)
	}))
	t.Cleanup(server.Close)
	target, err := serverconnection.ParseTarget(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, err := serverconnection.OpenLoginStore(filepath.Join(directory, "login"))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := serverconnection.NewLoginCredential(serverconnection.LoginCredentialInput{Target: target, InstanceID: "instance.test", UserID: "user.test", Username: "alice", SessionID: "login.test", SessionToken: token, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(credential); err != nil {
		t.Fatal(err)
	}
	return RemoteConfig{Target: target, StateDirectory: directory, Clock: fixedRemoteClock{now: now}, Random: rand.Reader, DisplayName: "fixture"}, creates
}

func runAuthBudgetLaunch(t *testing.T, mode string, config Config, ctx context.Context) error {
	t.Helper()
	file, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	config.Stdin, config.Stdout, config.Stderr = file, file, file
	config.BaseEnvironment = []string{"PATH=/usr/bin:/bin"}
	cwd := t.TempDir()
	config.Getwd = func() (string, error) { return cwd, nil }
	launcher, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "acp" {
		_, err = launcher.RunACP(ctx, ACPLaunchRequest{Command: []string{"/usr/bin/true"}})
	} else {
		_, err = launcher.Run(ctx, LaunchRequest{Command: []string{"/usr/bin/true"}})
	}
	return err
}

func TestRemoteAuthenticationHonorsExplicitControlBudget(t *testing.T) {
	for _, mode := range []string{"run", "acp"} {
		t.Run(mode, func(t *testing.T) {
			remote, creates := remoteAuthBudgetFixture(t, 400*time.Millisecond, nil)
			err := runAuthBudgetLaunch(t, mode, Config{Remote: &remote, ControlTimeout: 50 * time.Millisecond}, t.Context())
			if !errors.Is(err, context.DeadlineExceeded) || creates.Load() != 0 {
				t.Fatalf("authentication exceeded explicit budget or created Capture: creates=%d err=%v", creates.Load(), err)
			}
		})
	}
}

func TestRemoteAuthenticationDefaultOutlastsControlAndShortCreateBudget(t *testing.T) {
	for _, mode := range []string{"run", "acp"} {
		for _, create := range []time.Duration{0, time.Second} {
			t.Run(mode+"/create="+create.String(), func(t *testing.T) {
				remote, creates := remoteAuthBudgetFixture(t, 5100*time.Millisecond, nil)
				err := runAuthBudgetLaunch(t, mode, Config{Remote: &remote, CreateTimeout: create}, t.Context())
				var failure *ControlFailure
				if creates.Load() != 1 || !errors.As(err, &failure) || failure.Stage != "capture_create" || failure.Status != 503 {
					t.Fatalf("default authentication cut short: creates=%d err=%v", creates.Load(), err)
				}
			})
		}
	}
}

func TestRemoteAuthenticationCancellationNeverCreatesCapture(t *testing.T) {
	for _, mode := range []string{"run", "acp"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			remote, creates := remoteAuthBudgetFixture(t, time.Minute, entered)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- runAuthBudgetLaunch(t, mode, Config{Remote: &remote}, ctx) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("session request did not start")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) || creates.Load() != 0 {
					t.Fatalf("cancel outcome: creates=%d err=%v", creates.Load(), err)
				}
			case <-time.After(time.Second):
				t.Fatal("authentication did not cancel")
			}
		})
	}
}
