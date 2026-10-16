package clientpath

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimepath"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
)

func TestDefaultRemoteStateDirectoryIsAbsoluteAndClientScoped(t *testing.T) {
	directory, err := DefaultRemoteStateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(directory) || filepath.Base(directory) != "remote-client" {
		t.Fatalf("directory = %q", directory)
	}
}

func TestCustomRemoteStateNeverRequiresAHomeFallback(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	directory, err := LegacyRemoteLoginDirectory(filepath.Join(t.TempDir(), "custom"))
	if err != nil || directory != "" {
		t.Fatalf("custom state consulted unavailable home fallback: %q %v", directory, err)
	}
}

// These cases simulate OS directory APIs on the host: Linux XDG_CONFIG_HOME
// and Windows APPDATA may succeed independently of HOME/USERPROFILE. They do
// not claim native execution of those platforms.
func TestIndependentConfigDirectoryDoesNotRequireOptionalLegacyHome(t *testing.T) {
	for _, platform := range []string{"linux-XDG_CONFIG_HOME", "windows-APPDATA"} {
		for _, canonicalState := range []string{"valid", "absent", "corrupt"} {
			t.Run(platform+"/"+canonicalState, func(t *testing.T) {
				configDirectory := t.TempDir()
				canonical := filepath.Join(configDirectory, runtimepath.ApplicationID, "remote-client")
				directory := filepath.Join(canonical, "login")
				store, err := serverconnection.OpenLoginStore(directory)
				if err != nil {
					t.Fatal(err)
				}
				now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
				target, err := serverconnection.ParseTarget("http://127.0.0.1:9666")
				if err != nil {
					t.Fatal(err)
				}
				credential, err := serverconnection.NewLoginCredential(serverconnection.LoginCredentialInput{Target: target, InstanceID: "instance.fixture", UserID: "user.fixture", Username: "fixture", SessionID: "session.fixture", SessionToken: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x51}, 32)), ExpiresAt: now.Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				if canonicalState == "valid" {
					if err := store.Save(credential); err != nil {
						t.Fatal(err)
					}
				}
				if canonicalState == "corrupt" {
					if err := os.WriteFile(filepath.Join(directory, "login-sessions.json"), []byte(`{"schema":"invalid"}`), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				legacy, err := legacyRemoteLoginDirectory(canonical,
					func() (string, error) { return configDirectory, nil },
					func() (string, error) { return "", errors.New("OS home directory is unavailable") },
				)
				if err != nil || legacy != "" {
					t.Fatalf("optional missing home blocked canonical %s state: legacy=%q err=%v", canonicalState, legacy, err)
				}
				reopened, err := serverconnection.OpenLoginStoreMigrating(directory, legacy)
				if canonicalState == "corrupt" {
					if err == nil || errors.Is(err, serverconnection.ErrLoginRequired) {
						t.Fatalf("canonical corruption hidden: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				loaded, err := reopened.Load(target, now)
				if canonicalState == "absent" {
					if !errors.Is(err, serverconnection.ErrLoginRequired) {
						t.Fatalf("absent canonical should require login: %v", err)
					}
					if _, err := os.Stat(filepath.Join(directory, "login-sessions.json")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("absent canonical document was created: %v", err)
					}
				} else if err != nil || loaded.SessionToken().Value() != credential.SessionToken().Value() {
					t.Fatalf("valid canonical credential blocked: %v", err)
				}
			})
		}
	}
}
