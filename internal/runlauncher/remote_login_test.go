package runlauncher

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

func TestRemoteLoginAuthenticatesAndPersistsExactServerSession(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 14, 0, 0, 0, time.UTC)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x45}, 32))
	var observed servercontrol.RuntimeUserLogin
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost ||
			request.URL.Path != servercontrol.RuntimeUserSessionPath {
			http.NotFound(writer, request)
			return
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&observed); err != nil {
			t.Errorf("decode login request: %v", err)
			http.Error(writer, "invalid", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(servercontrol.RuntimeUserSession{
			Schema: servercontrol.RuntimeUserSessionSchema, InstanceID: "instance.test",
			APIVersion: "v1", SessionID: "login.test", SessionToken: token,
			User:      servercontrol.RuntimeUserView{ID: "user.test", Username: "alice"},
			ExpiresAt: now.Add(8 * time.Hour),
		})
	}))
	defer server.Close()
	target, err := serverconnection.ParseTarget(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	stateDirectory := filepath.Join(t.TempDir(), "remote-client")
	result, err := LoginRemote(context.Background(), RemoteLoginRequest{
		Config: RemoteConfig{
			Target: target, StateDirectory: stateDirectory, DisplayName: "test-device",
			Clock:  fixedRemoteClock{now: now},
			Random: bytes.NewReader(bytes.Repeat([]byte{0x67}, 512)),
		},
		Username: "alice", Password: []byte("test-remote-password"),
	})
	if err != nil {
		t.Fatalf("LoginRemote() error = %v", err)
	}
	if result.Username != "alice" || result.Target != target ||
		!result.ExpiresAt.Equal(now.Add(8*time.Hour)) {
		t.Fatalf("LoginRemote() = %#v", result)
	}
	if observed.Schema != servercontrol.RuntimeUserLoginSchema ||
		observed.Username != "alice" || observed.Password != "test-remote-password" ||
		observed.DeviceName != "test-device" || observed.MachineID == "" {
		t.Fatalf("observed login request = %#v", observed)
	}
	store, err := serverconnection.OpenLoginStore(filepath.Join(stateDirectory, "login"))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Load(target, now)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SessionToken().Value() != token || stored.Username() != "alice" {
		t.Fatalf("stored Login credential = %#v", stored)
	}
}

func TestRemoteLogoutRevokesServerSessionBeforeRemovingLocalCredential(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 16, 0, 0, 0, time.UTC)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x46}, 32))
	var logoutAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == servercontrol.RuntimeUserSessionPath:
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(servercontrol.RuntimeUserSession{
				Schema: servercontrol.RuntimeUserSessionSchema, InstanceID: "instance.test",
				APIVersion: "v1", SessionID: "login.test", SessionToken: token,
				User:      servercontrol.RuntimeUserView{ID: "user.test", Username: "alice"},
				ExpiresAt: now.Add(8 * time.Hour),
			})
		case request.Method == http.MethodDelete && request.URL.Path == servercontrol.RuntimeUserCurrentSessionPath:
			logoutAuthorization = request.Header.Get("Authorization")
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	target, err := serverconnection.ParseTarget(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	config := RemoteConfig{
		Target: target, StateDirectory: filepath.Join(t.TempDir(), "remote-client"),
		DisplayName: "test-device", Clock: fixedRemoteClock{now: now},
		Random: bytes.NewReader(bytes.Repeat([]byte{0x68}, 512)),
	}
	if _, err := LoginRemote(context.Background(), RemoteLoginRequest{
		Config: config, Username: "alice", Password: []byte("test-remote-password"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := LogoutRemote(context.Background(), RemoteLogoutRequest{Config: config}); err != nil {
		t.Fatalf("LogoutRemote() error = %v", err)
	}
	if logoutAuthorization != "Bearer "+token {
		t.Fatalf("logout Authorization = %q", logoutAuthorization)
	}
	store, err := serverconnection.OpenLoginStore(filepath.Join(config.StateDirectory, "login"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(target, now); !errors.Is(err, serverconnection.ErrLoginRequired) {
		t.Fatalf("Load() after logout error = %v", err)
	}
}

func TestInspectRemoteVerifiesTheStoredRuntimeUserSession(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 17, 0, 0, 0, time.UTC)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x47}, 32))
	machineID := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x37}, 32))
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodGet ||
			request.URL.Path != servercontrol.RuntimeUserCurrentSessionPath ||
			request.Header.Get("Authorization") != "Bearer "+token {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(servercontrol.RuntimeUserCurrentSession{
			Schema: servercontrol.RuntimeUserCurrentSessionSchema, InstanceID: "instance.test",
			APIVersion: "v1", SessionID: "login.test", MachineID: machineID,
			DeviceName: "test-device",
			User:       servercontrol.RuntimeUserView{ID: "user.test", Username: "alice"},
		})
	}))
	defer server.Close()
	target, err := serverconnection.ParseTarget(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	stateDirectory := filepath.Join(t.TempDir(), "remote-client")
	store, err := serverconnection.OpenLoginStore(filepath.Join(stateDirectory, "login"))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := serverconnection.NewLoginCredential(serverconnection.LoginCredentialInput{
		Target: target, InstanceID: "instance.test", UserID: "user.test", Username: "alice",
		SessionID: "login.test", SessionToken: token, ExpiresAt: now.Add(8 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(credential); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectRemote(
		context.Background(), target, stateDirectory, fixedRemoteClock{now: now}, time.Second,
	)
	if err != nil {
		t.Fatalf("InspectRemote() error = %v", err)
	}
	if inspection.Origin != target.Origin() || inspection.InstanceID != "instance.test" ||
		inspection.UserID != "user.test" || inspection.Username != "alice" ||
		inspection.SessionID != "login.test" || inspection.APIVersion != "v1" ||
		inspection.Encrypted {
		t.Fatalf("InspectRemote() = %#v", inspection)
	}
}

type fixedRemoteClock struct{ now time.Time }

func (clock fixedRemoteClock) Now() time.Time { return clock.now }
