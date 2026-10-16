package serverconnection

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"sync"
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

func TestLoginStoreSharesSessionAcrossDefaultPortSpellings(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	for _, pair := range []struct{ implicit, explicit string }{
		{"https://runtime.example.test", "https://runtime.example.test:443"},
		{"http://runtime.example.test", "http://runtime.example.test:80"},
	} {
		for _, reverse := range []bool{false, true} {
			saved, lookup := pair.implicit, pair.explicit
			if reverse {
				saved, lookup = lookup, saved
			}
			t.Run(saved+" to "+lookup, func(t *testing.T) {
				directory := filepath.Join(t.TempDir(), "login")
				store, err := OpenLoginStore(directory)
				if err != nil {
					t.Fatal(err)
				}
				credential := loginStoreTestCredential(t, saved, "alice", 0x73, now)
				if err := store.Save(credential); err != nil {
					t.Fatal(err)
				}
				reopened, err := OpenLoginStore(directory)
				if err != nil {
					t.Fatal(err)
				}
				target, err := ParseTarget(lookup)
				if err != nil {
					t.Fatal(err)
				}
				loaded, err := reopened.Load(target, now)
				if err != nil {
					t.Fatal(err)
				}
				if loaded.Target() != credential.Target() || loaded.SessionToken().Value() != credential.SessionToken().Value() || loaded.Username() != "alice" {
					t.Fatalf("loaded session does not match saved session: %#v", loaded)
				}
				for _, isolated := range []string{"http://runtime.example.test:443", "https://runtime.example.test:80", "https://runtime.example.test:9443"} {
					other, err := ParseTarget(isolated)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := reopened.Load(other, now); !errors.Is(err, ErrLoginRequired) {
						t.Fatalf("Load(%q) error = %v", isolated, err)
					}
				}
			})
		}
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

func TestLoginStoreMigrationPreservesCompleteDocumentAndLogout(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	legacyDirectory := filepath.Join(t.TempDir(), "legacy")
	legacy, err := OpenLoginStore(legacyDirectory)
	if err != nil {
		t.Fatal(err)
	}
	first := loginStoreTestCredential(t, "first.test:9666", "alice", 1, now)
	expired := loginStoreTestCredential(t, "expired.test:9666", "bob", 2, now.Add(-24*time.Hour))
	for _, login := range []LoginCredential{first, expired} {
		if err := legacy.Save(login); err != nil {
			t.Fatal(err)
		}
	}
	sourcePath := filepath.Join(legacyDirectory, loginStoreFileName)
	before, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "canonical")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store, err := OpenLoginStoreMigrating(directory, legacyDirectory)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := store.Load(first.Target(), now); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	store, err := OpenLoginStoreMigrating(directory, legacyDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(expired.Target(), now); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("migrated expiry bypassed: %v", err)
	}
	// The expired record is preserved for backwards-readable complete migration.
	payload, err := os.ReadFile(filepath.Join(directory, loginStoreFileName))
	if err != nil {
		t.Fatal(err)
	}
	records, err := decodeLoginSessions(payload)
	if err != nil || len(records) != 2 {
		t.Fatalf("records lost: %v", err)
	}
	if err := store.Remove(first.Target()); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenLoginStoreMigrating(directory, legacyDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Load(first.Target(), now); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("logout resurrected: %v", err)
	}
	after, err := os.ReadFile(sourcePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source changed")
	}
	info, err := os.Stat(filepath.Join(directory, loginStoreFileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("migration permissions")
	}
}

func TestLoginStoreMigrationCanonicalAlwaysWins(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	for _, scenario := range []string{"empty", "different-server", "expired", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			legacyDirectory := filepath.Join(t.TempDir(), "legacy")
			legacy, err := OpenLoginStore(legacyDirectory)
			if err != nil {
				t.Fatal(err)
			}
			login := loginStoreTestCredential(t, "runtime.test:9666", "alice", 1, now)
			if err := legacy.Save(login); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(t.TempDir(), "canonical")
			canonical, err := OpenLoginStore(directory)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "empty":
				if err := canonical.Save(login); err != nil {
					t.Fatal(err)
				}
				if err := canonical.Remove(login.Target()); err != nil {
					t.Fatal(err)
				}
			case "different-server":
				if err := canonical.Save(loginStoreTestCredential(t, "other.test:9666", "bob", 2, now)); err != nil {
					t.Fatal(err)
				}
			case "expired":
				if err := canonical.Save(loginStoreTestCredential(t, "runtime.test:9666", "alice", 2, now.Add(-24*time.Hour))); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(filepath.Join(directory, loginStoreFileName), []byte(`{"schema":"broken"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// Existing valid canonical data must ignore even an incompatible source.
			// A malformed canonical document must fail even with a valid source.
			if scenario != "malformed" {
				if err := os.WriteFile(filepath.Join(legacyDirectory, loginStoreFileName), []byte("invalid legacy data"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			migrated, err := OpenLoginStoreMigrating(directory, legacyDirectory)
			if scenario == "malformed" {
				if err == nil {
					t.Fatal("corrupt canonical hidden")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := migrated.Load(login.Target(), now); !errors.Is(err, ErrLoginRequired) {
				t.Fatalf("canonical did not win: %v", err)
			}
		})
	}
}

func TestLoginStoreMigrationRejectsMalformedCompatibilityDocuments(t *testing.T) {
	for _, payload := range []string{"garbage", `{"schema":"old","sessions":{}}`, `{"schema":"vibermate-runtime-server-logins-v1","sessions":null}`, `{"schema":"vibermate-runtime-server-logins-v1","sessions":{},"extra":true}`, `{"schema":"vibermate-runtime-server-logins-v1","sessions":{"http://bad:80":{}}}`} {
		t.Run(payload, func(t *testing.T) {
			legacy := t.TempDir()
			if err := os.WriteFile(filepath.Join(legacy, loginStoreFileName), []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			canonical := filepath.Join(t.TempDir(), "canonical")
			if _, err := OpenLoginStoreMigrating(canonical, legacy); err == nil {
				t.Fatal("incompatible document accepted")
			}
			if _, err := os.Stat(filepath.Join(canonical, loginStoreFileName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed migration published a document: %v", err)
			}
		})
	}
}

func TestLoginStoreMigrationCanImportSourcePlacedAfterAbsentSourceOpen(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "canonical")
	legacyDirectory := filepath.Join(t.TempDir(), "legacy")
	if _, err := OpenLoginStoreMigrating(directory, legacyDirectory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, loginStoreFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ordinary missing-source open initialized canonical document: %v", err)
	}
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	legacy, err := OpenLoginStore(legacyDirectory)
	if err != nil {
		t.Fatal(err)
	}
	login := loginStoreTestCredential(t, "runtime.test:9666", "alice", 1, now)
	if err := legacy.Save(login); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenLoginStoreMigrating(directory, legacyDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Load(login.Target(), now); err != nil {
		t.Fatalf("source placed after ordinary open was not imported: %v", err)
	}
}
