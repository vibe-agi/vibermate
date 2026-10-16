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

	"github.com/vibe-agi/vibermate/internal/clientpath"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

func TestDefaultRemoteLoginMigratesCompatibleHomeDocument(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	directory, err := clientpath.DefaultRemoteStateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	identity, err := workspaceidentity.Open(context.Background(), filepath.Join(directory, "identity"), rand.Reader, now)
	if err != nil {
		t.Fatal(err)
	}
	defer identity.Shutdown(context.Background())
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x55}, 32))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != servercontrol.RuntimeUserCurrentSessionPath || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("incorrect authenticated lookup")
		}
		_ = json.NewEncoder(w).Encode(servercontrol.RuntimeUserCurrentSession{Schema: servercontrol.RuntimeUserCurrentSessionSchema, InstanceID: "instance.test", APIVersion: "v1", ProductBuild: "test-build", User: servercontrol.RuntimeUserView{ID: "user.test", Username: "alice"}, SessionID: "session.test", MachineID: identity.MachineID().String()})
	}))
	defer server.Close()
	target, err := serverconnection.ParseTarget(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	legacyDirectory := filepath.Join(home, ".vibermate")
	source, err := serverconnection.OpenLoginStore(legacyDirectory)
	if err != nil {
		t.Fatal(err)
	}
	login, err := serverconnection.NewLoginCredential(serverconnection.LoginCredentialInput{Target: target, InstanceID: "instance.test", UserID: "user.test", Username: "alice", SessionID: "session.test", SessionToken: token, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Save(login); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(legacyDirectory, "login-sessions.json")
	original, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InspectRemote(context.Background(), target, filepath.Join(t.TempDir(), "custom"), fixedRemoteClock{now: now}, time.Second); !errors.Is(err, ErrRemoteLoginRequired) {
		t.Fatalf("custom state consulted home compatibility file: %v", err)
	}
	if _, err := InspectRemote(context.Background(), target, directory, fixedRemoteClock{now: now}, time.Second); err != nil {
		t.Fatalf("default login lookup did not migrate compatible document: %v", err)
	}
	canonical, err := serverconnection.OpenLoginStore(filepath.Join(directory, "login"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := canonical.Load(target, now)
	if err != nil || loaded.SessionToken().Value() != token {
		t.Fatalf("migration/authenticated lookup failed: %v", err)
	}
	after, err := os.ReadFile(sourcePath)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("migration changed source")
	}
	info, err := os.Stat(filepath.Join(directory, "login", "login-sessions.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("canonical store is not private")
	}
}
