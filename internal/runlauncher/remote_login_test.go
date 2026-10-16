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
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
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
			APIVersion: "v1", ProductBuild: "test-build",
			SessionID: "login.test", SessionToken: token,
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
		result.ProductBuild != "test-build" ||
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
	assertPreviousCLICanReadLoginStore(t, filepath.Join(stateDirectory, "login", "login-sessions.json"))

}

// Frozen consumer schema from the previous CLI. Adding record keys here would
// hide a downgrade/coexistence failure in its DisallowUnknownFields decoder.
func assertPreviousCLICanReadLoginStore(t *testing.T, path string) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Schema   string `json:"schema"`
		Sessions map[string]struct {
			Target       string    `json:"target"`
			InstanceID   string    `json:"instanceId"`
			UserID       string    `json:"userId"`
			Username     string    `json:"username"`
			SessionID    string    `json:"sessionId"`
			SessionToken string    `json:"sessionToken"`
			ExpiresAt    time.Time `json:"expiresAt"`
		} `json:"sessions"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("previous CLI cannot read newly persisted v1 login document: %v", err)
	}
	if document.Schema != "vibermate-runtime-server-logins-v1" || len(document.Sessions) != 1 {
		t.Fatal("new login changed previous v1 document contract")
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
				APIVersion: "v1", ProductBuild: "test-build",
				SessionID: "login.test", SessionToken: token,
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
	if err := os.Chmod(filepath.Join(config.StateDirectory, "identity", "machine-identity.json"), 0o644); err != nil {
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
			APIVersion: "v1", ProductBuild: "test-build",
			SessionID: "login.test", MachineID: machineID,
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
	identity, err := workspaceidentity.Open(context.Background(), filepath.Join(stateDirectory, "identity"), bytes.NewReader(bytes.Repeat([]byte{0x37}, 64)), now)
	if err != nil {
		t.Fatal(err)
	}
	defer identity.Shutdown(context.Background())
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
		inspection.ProductBuild != "test-build" ||
		inspection.Encrypted {
		t.Fatalf("InspectRemote() = %#v", inspection)
	}
}

type fixedRemoteClock struct{ now time.Time }

func (clock fixedRemoteClock) Now() time.Time { return clock.now }

func TestLegacyRemoteSessionAuthenticatedBinding(t *testing.T) {
	for _, scenario := range []string{"matching", "different-machine", "invalid-machine", "revoked", "logout-during-verification", "login-during-verification", "wrong-session", "expired", "unavailable", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
			directory := t.TempDir()
			identity, err := workspaceidentity.Open(ctx, filepath.Join(directory, "identity"), rand.Reader, now)
			if err != nil {
				t.Fatal(err)
			}
			defer identity.Shutdown(ctx)
			machine := identity.MachineID().String()
			var store *serverconnection.LoginStore
			var login serverconnection.LoginCredential
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "expired" {
					t.Error("expired credential reached server")
				}
				if r.URL.Path != servercontrol.RuntimeUserCurrentSessionPath || r.Header.Get("Authorization") != "Bearer "+login.SessionToken().Value() {
					t.Errorf("incorrect authentication path")
				}
				if scenario == "timeout" {
					<-r.Context().Done()
					return
				}
				if scenario == "unavailable" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if scenario == "revoked" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if scenario == "logout-during-verification" {
					if err := store.Remove(login.Target()); err != nil {
						t.Error(err)
					}
				}
				if scenario == "login-during-verification" {
					newer, err := serverconnection.NewLoginCredential(serverconnection.LoginCredentialInput{Target: login.Target(), InstanceID: login.InstanceID(), UserID: login.UserID(), Username: login.Username(), SessionID: "newer", SessionToken: login.SessionToken().Value(), ExpiresAt: login.ExpiresAt()})
					if err != nil {
						t.Error(err)
					}
					if err := store.Save(newer); err != nil {
						t.Error(err)
					}
				}
				remoteMachine := machine
				if scenario == "different-machine" {
					remoteMachine = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x99}, 32))
				}
				if scenario == "invalid-machine" {
					remoteMachine = "invalid machine"
				}
				sessionID := login.SessionID()
				if scenario == "wrong-session" {
					sessionID = "other"
				}
				_ = json.NewEncoder(w).Encode(servercontrol.RuntimeUserCurrentSession{Schema: servercontrol.RuntimeUserCurrentSessionSchema, InstanceID: login.InstanceID(), APIVersion: "v1", ProductBuild: "test-build", User: servercontrol.RuntimeUserView{ID: login.UserID(), Username: login.Username()}, SessionID: sessionID, MachineID: remoteMachine})
			}))
			defer server.Close()
			target, err := serverconnection.ParseTarget(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			store, err = serverconnection.OpenLoginStore(filepath.Join(directory, "login"))
			if err != nil {
				t.Fatal(err)
			}
			expiry := now.Add(time.Hour)
			if scenario == "expired" {
				expiry = now
			}
			login, err = serverconnection.NewLoginCredential(serverconnection.LoginCredentialInput{Target: target, InstanceID: "instance.test", UserID: "user.test", Username: "alice", SessionID: "legacy", SessionToken: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, 32)), ExpiresAt: expiry})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save(login); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(directory, "login", "login-sessions.json"))
			if err != nil {
				t.Fatal(err)
			}
			timeout := time.Second
			if scenario == "timeout" {
				timeout = 30 * time.Millisecond
			}
			started := time.Now()
			_, err = InspectRemote(ctx, target, directory, fixedRemoteClock{now: now}, timeout)
			if scenario == "timeout" && time.Since(started) > time.Second {
				t.Fatal("current-session preflight exceeded its timeout")
			}
			if scenario == "unavailable" || scenario == "timeout" {
				if !errors.Is(err, ErrRemoteRuntimeUnavailable) {
					t.Fatalf("server outage lost existing inspection classification: %v", err)
				}
				return
			}
			if scenario == "matching" || scenario == "logout-during-verification" || scenario == "login-during-verification" {
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "matching" {
					saved, err := store.Load(target, now)
					if err != nil {
						t.Fatal(err)
					}
					if saved.SessionID() != login.SessionID() || saved.SessionToken().Value() != login.SessionToken().Value() {
						t.Fatal("authenticated status changed legacy credential")
					}
					after, err := os.ReadFile(filepath.Join(directory, "login", "login-sessions.json"))
					if err != nil || !bytes.Equal(before, after) {
						t.Fatal("authenticated lookup changed legacy document")
					}
					assertPreviousCLICanReadLoginStore(t, filepath.Join(directory, "login", "login-sessions.json"))
				}
			} else {
				if !errors.Is(err, ErrRemoteLoginRequired) {
					t.Fatalf("want login required; got %v", err)
				}
			}
			if scenario == "logout-during-verification" {
				if _, err := store.Load(target, now); !errors.Is(err, serverconnection.ErrLoginRequired) {
					t.Fatalf("logout resurrected: %v", err)
				}
			}
			if scenario == "login-during-verification" {
				saved, err := store.Load(target, now)
				if err != nil || saved.SessionID() != "newer" {
					t.Fatalf("newer login overwritten: %+v %v", saved, err)
				}
			}
		})
	}
}
