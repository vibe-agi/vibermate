package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serveridentity"
)

func TestTrustCommandExplicitlyMigratesPrivateServerLeafPin(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	clock := fixedCommandClock{now: time.Now().UTC().Truncate(time.Millisecond)}
	options := localca.DefaultOptions(filepath.Join(t.TempDir(), "root"), ctx)
	options.Clock = clock
	root, err := localca.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Shutdown(context.Background()) })
	certificate := func() tls.Certificate {
		t.Helper()
		manager, err := serveridentity.OpenManager(ctx, t.TempDir(), rand.Reader, clock.Now, root, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		pair, err := manager.Current().Certificate()
		if err != nil {
			t.Fatal(err)
		}
		return pair
	}
	first, renewed := certificate(), certificate()
	var active atomic.Pointer[tls.Certificate]
	active.Store(&renewed)
	var logins atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != servercontrol.RuntimeUserSessionPath {
			http.NotFound(writer, request)
			return
		}
		logins.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(servercontrol.RuntimeUserSession{
			Schema: servercontrol.RuntimeUserSessionSchema, InstanceID: "instance.test",
			APIVersion: "v1", ProductBuild: "test-build", SessionID: "login.test",
			SessionToken: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x45}, 32)),
			User:         servercontrol.RuntimeUserView{ID: "user.test", Username: "alice"},
			ExpiresAt:    clock.Now().Add(8 * time.Hour),
		})
	}))
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS13,
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{*active.Load()}}, nil
		},
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	target, err := serverconnection.ParseTarget(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	stateDirectory := filepath.Join(t.TempDir(), "client")
	store, err := serverconnection.OpenPinStore(filepath.Join(stateDirectory, "trust"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Verify(target.Address(), first.Certificate, clock.Now()); err != nil {
		t.Fatal(err)
	}
	login := runlauncher.RemoteLoginRequest{
		Config: runlauncher.RemoteConfig{
			Target: target, StateDirectory: stateDirectory, DisplayName: "trust-test",
			Clock: clock, Random: rand.Reader,
		},
		Username: "alice", Password: []byte("synthetic-password"),
	}
	if _, err := runlauncher.LoginRemote(ctx, login); !errors.Is(err, serverconnection.ErrServerIdentityChanged) {
		t.Fatalf("exact leaf pin changed without approval: %v", err)
	}
	config, err := parseTrust([]string{"trust", "--server", server.URL, "--ca-fingerprint", root.Identity().Fingerprint()})
	if err != nil {
		t.Fatalf("private CA trust command: %v", err)
	}
	var output strings.Builder
	wrongCA := config
	wrongCA.caFingerprint = strings.Repeat("0", 64)
	if code, _ := executeRemoteTrust(ctx, wrongCA, stateDirectory, clock, &output); code == 0 {
		t.Fatal("trust command accepted a different CA fingerprint")
	}
	if _, err := runlauncher.LoginRemote(ctx, login); !errors.Is(err, serverconnection.ErrServerIdentityChanged) {
		t.Fatalf("failed verification changed the saved pin: %v", err)
	}
	if code, key := executeRemoteTrust(ctx, config, stateDirectory, clock, &output); code != 0 || key != "" {
		t.Fatalf("trust command = %d %q", code, key)
	}
	if logins.Load() != 0 {
		t.Fatal("trust verification sent an application request before enrollment")
	}
	if _, err := runlauncher.LoginRemote(ctx, login); err != nil {
		t.Fatalf("login after explicit migration: %v", err)
	}
	third := certificate()
	active.Store(&third)
	if _, err := runlauncher.LoginRemote(ctx, login); err != nil {
		t.Fatalf("explicit CA trust did not survive the next leaf change: %v", err)
	}
	if logins.Load() != 2 {
		t.Fatalf("received logins = %d", logins.Load())
	}
}
