package runlauncher_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/serverhost"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

type recoveryClock struct{}

func (recoveryClock) Now() time.Time { return time.Now() }

func TestRemoteIdentityRecoveryRequiresLoginBeforeCapture(t *testing.T) {
	for _, scenario := range []string{"bound-identity-recovery", "legacy-identity-recovery", "expired", "revoked"} {
		t.Run(scenario, func(t *testing.T) { testRemoteSessionBoundary(t, scenario) })
	}
}

func TestRemoteCaptureAcceptsValidDirectoryDisplayLabels(t *testing.T) {
	testRemoteSessionBoundary(t, "labels")
}

func testRemoteSessionBoundary(t *testing.T, scenario string) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	paths, err := productruntime.NewRuntimePaths(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	secretFactory, err := hostsecret.NewDevelopmentFileFactory(filepath.Join(root, "secrets", "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := secretFactory.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	options := serverhost.DefaultOptions(productruntime.Options{
		Paths: paths, Host: hostcontract.Server(), OfflineHold: gate, Secrets: secrets,
		Approvals: toolapproval.DefaultConfig(), ExchangeHold: exchange.DefaultHoldPolicy(),
		Clock: productruntime.SystemClock{}, InstanceIDs: productruntime.NewCryptographicInstanceIDSource(),
		SecurityRandom: rand.Reader, Lifecycle: productruntime.DefaultLifecycleOptions(),
	})
	options.ListenAddress = "127.0.0.1:0"
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	host, err := serverhost.Start(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if err := host.Shutdown(stop); err != nil {
			t.Error(err)
		}
	}()
	user, err := host.Runtime().RuntimeUsers().Create(ctx, runtimeuser.CreateCommand{Username: "fixture-user", Password: []byte("fixture-password")})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := runtimeuser.NewPolicy(true, nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Runtime().RuntimeUsers().SetPolicy(ctx, user.ID, policy); err != nil {
		t.Fatal(err)
	}
	target, err := serverconnection.ParseTarget("http://" + host.Status().ListenAddress)
	if err != nil {
		t.Fatal(err)
	}
	remote := runlauncher.RemoteConfig{Target: target, StateDirectory: filepath.Join(root, "client"), DisplayName: "fixture-device", Clock: recoveryClock{}, Random: rand.Reader}
	login := func() {
		if _, err := runlauncher.LoginRemote(ctx, runlauncher.RemoteLoginRequest{Config: remote, Username: "fixture-user", Password: []byte("fixture-password")}); err != nil {
			t.Fatal(err)
		}
	}
	login()
	var log strings.Builder
	cwd := root
	launcher, err := runlauncher.New(runlauncher.Config{Remote: &remote, BaseEnvironment: []string{}, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: &log,
		Getwd: func() (string, error) { return cwd, nil }, ControlTimeout: 2 * time.Second, CreateTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	request := runlauncher.LaunchRequest{EnvironmentID: environment.SystemTransparentID, Command: []string{"/usr/bin/true"}}
	if code, err := launcher.Run(ctx, request); err != nil || code != 0 {
		t.Fatalf("baseline code=%d err=%v", code, err)
	}

	if scenario == "labels" {
		for _, label := range []string{strings.Repeat("a", 121), strings.Repeat("a", 119) + "\u00e9", strings.Repeat("\u754c", 41), " project ", "   "} {
			t.Run(label, func(t *testing.T) {
				cwd = filepath.Join(root, label)
				if err := os.Mkdir(cwd, 0o700); err != nil {
					t.Fatal(err)
				}
				if code, err := launcher.Run(ctx, request); err != nil || code != 0 {
					t.Fatalf("valid cwd launch code=%d err=%v", code, err)
				}
			})
		}
		return
	}
	if _, err := launcher.Environments(ctx); err != nil {
		t.Fatalf("unchanged-identity profiles: %v", err)
	}
	if _, err := runlauncher.InspectRemote(ctx, target, remote.StateDirectory, recoveryClock{}, 2*time.Second); err != nil {
		t.Fatalf("unchanged-identity status: %v", err)
	}
	store, err := serverconnection.OpenLoginStore(filepath.Join(remote.StateDirectory, "login"))
	if err != nil {
		t.Fatal(err)
	}
	oldLogin, err := store.Load(target, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	switch scenario {
	case "legacy-identity-recovery":
		path := filepath.Join(remote.StateDirectory, "login", "login-sessions.json")
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(payload, &document); err != nil {
			t.Fatal(err)
		}
		document = map[string]any{"schema": "vibermate-runtime-server-logins-v1", "sessions": map[string]any{target.Origin(): map[string]any{
			"target": target.Origin(), "instanceId": oldLogin.InstanceID(), "userId": oldLogin.UserID(), "username": oldLogin.Username(), "sessionId": oldLogin.SessionID(), "sessionToken": oldLogin.SessionToken().Value(), "expiresAt": oldLogin.ExpiresAt(),
		}}}
		payload, err = json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		fallthrough
	case "bound-identity-recovery":
		// A backup/restore that loses private mode alone rotates MachineID.
		if err := os.Chmod(filepath.Join(remote.StateDirectory, "identity", "machine-identity.json"), 0o644); err != nil {
			t.Fatal(err)
		}
	case "expired":
		expired, err := serverconnection.NewLoginCredential(serverconnection.LoginCredentialInput{Target: target, InstanceID: oldLogin.InstanceID(), UserID: oldLogin.UserID(), Username: oldLogin.Username(), SessionID: oldLogin.SessionID(), SessionToken: oldLogin.SessionToken().Value(), ExpiresAt: time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save(expired); err != nil {
			t.Fatal(err)
		}
	case "revoked":
		if err := host.Runtime().RuntimeUsers().Logout(ctx, oldLogin.SessionToken().Value()); err != nil {
			t.Fatal(err)
		}
	}
	log.Reset()
	code, err := launcher.Run(ctx, request)
	if !errors.Is(err, runlauncher.ErrRemoteLoginRequired) {
		t.Errorf("recovered identity launch code=%d err=%v; want remote login required", code, err)
	}
	if strings.Contains(log.String(), "preparing Capture") {
		t.Errorf("created Capture after recovery: %q", log.String())
	}
	if _, err := launcher.Environments(ctx); !errors.Is(err, runlauncher.ErrRemoteLoginRequired) {
		t.Errorf("profiles err=%v; want remote login required", err)
	}
	if _, err := runlauncher.InspectRemote(ctx, target, remote.StateDirectory, recoveryClock{}, 2*time.Second); !errors.Is(err, runlauncher.ErrRemoteLoginRequired) {
		t.Errorf("status err=%v; want remote login required", err)
	}

	if scenario != "expired" {
		if err := runlauncher.LogoutRemote(ctx, runlauncher.RemoteLogoutRequest{Config: remote}); err != nil {
			t.Fatalf("old identity-bound session could not be revoked: %v", err)
		}
		if _, err := host.Runtime().RuntimeUsers().Authenticate(ctx, oldLogin.SessionToken().Value()); !errors.Is(err, runtimeuser.ErrInvalidSession) {
			t.Fatalf("logout did not revoke old session: %v", err)
		}
	}
	login()
	if code, err := launcher.Run(ctx, request); err != nil || code != 0 {
		t.Fatalf("re-login did not repair code=%d err=%v", code, err)
	}
}
