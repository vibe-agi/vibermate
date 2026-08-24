package serverconnection

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoginStoreKeepsSessionBoundToExactServerTarget(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "login")
	store, err := OpenLoginStore(directory)
	if err != nil {
		t.Fatalf("OpenLoginStore() error = %v", err)
	}
	httpTarget, err := ParseTarget("192.168.1.20:9666")
	if err != nil {
		t.Fatal(err)
	}
	httpsTarget, err := ParseTarget("https://192.168.1.20:9666")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	credential, err := NewLoginCredential(LoginCredentialInput{
		Target: httpTarget, InstanceID: "instance.test",
		UserID: "user.test", Username: "alice", SessionID: "login.test",
		SessionToken: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x73}, 32)),
		ExpiresAt:    now.Add(8 * time.Hour),
	})
	if err != nil {
		t.Fatalf("NewLoginCredential() error = %v", err)
	}
	if err := store.Save(credential); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	reopened, err := OpenLoginStore(directory)
	if err != nil {
		t.Fatalf("reopen LoginStore error = %v", err)
	}
	loaded, err := reopened.Load(httpTarget, now)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Target() != httpTarget || loaded.Username() != "alice" ||
		loaded.SessionToken().Value() != credential.SessionToken().Value() {
		t.Fatalf("Load() = %#v", loaded)
	}
	if _, err := reopened.Load(httpsTarget, now); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("Load() with different transport error = %v", err)
	}
	if _, err := reopened.Load(httpTarget, now.Add(9*time.Hour)); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("Load() after expiry error = %v", err)
	}
	info, err := os.Stat(filepath.Join(directory, "login-sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("login store permissions = %o", permissions)
	}
}
