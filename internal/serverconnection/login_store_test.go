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

func TestIndependentLoginStoresMergeSaveAndRemoveTransactions(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "login")
	left, err := OpenLoginStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	right, err := OpenLoginStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	first := loginStoreTestCredential(t, "first.server.test:9666", "alice", 0x31, now)
	second := loginStoreTestCredential(t, "second.server.test:9666", "bob", 0x32, now)
	if err := left.Save(first); err != nil {
		t.Fatal(err)
	}
	if err := right.Save(second); err != nil {
		t.Fatal(err)
	}
	merged, err := OpenLoginStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []LoginCredential{first, second} {
		if _, loadErr := merged.Load(credential.Target(), now); loadErr != nil {
			t.Fatalf("merged Load(%s) error = %v", credential.Target().Origin(), loadErr)
		}
	}

	remover, err := OpenLoginStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	staleSaver, err := OpenLoginStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	third := loginStoreTestCredential(t, "third.server.test:9666", "carol", 0x33, now)
	if err := staleSaver.Save(third); err != nil {
		t.Fatal(err)
	}
	if err := remover.Remove(first.Target()); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenLoginStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Load(first.Target(), now); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("removed first Login error = %v", err)
	}
	for _, credential := range []LoginCredential{second, third} {
		loaded, loadErr := reopened.Load(credential.Target(), now)
		if loadErr != nil || loaded.Username() != credential.Username() {
			t.Fatalf("Load(%s) = %#v, %v", credential.Target().Origin(), loaded, loadErr)
		}
	}
}

func TestLoginStoreLoadDoesNotHidePersistenceCorruptionAsMissingLogin(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "login")
	store, err := OpenLoginStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	target, err := ParseTarget("runtime.test:9666")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, loginStoreFileName),
		[]byte(`{"schema":"broken"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	_, err = store.Load(target, time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC))
	if err == nil || errors.Is(err, ErrLoginRequired) {
		t.Fatalf("Load() corruption error = %v", err)
	}
}

func loginStoreTestCredential(
	t *testing.T,
	targetValue string,
	username string,
	tokenByte byte,
	now time.Time,
) LoginCredential {
	t.Helper()
	target, err := ParseTarget(targetValue)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := NewLoginCredential(LoginCredentialInput{
		Target: target, InstanceID: "instance." + username,
		UserID: "user." + username, Username: username,
		SessionID: "login." + username,
		SessionToken: base64.RawURLEncoding.EncodeToString(
			bytes.Repeat([]byte{tokenByte}, 32),
		),
		ExpiresAt: now.Add(8 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return credential
}
