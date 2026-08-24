package runtimepersistence

import (
	"bytes"
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

func TestRuntimeUserLoginSessionSurvivesStoreReopen(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "runtime.sqlite")
	clock := runtimeUserTestClock{
		now: time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC),
	}
	store := openTestStore(t, databasePath)
	manager, err := runtimeuser.New(runtimeuser.Options{
		Repository:      store.RuntimeUserRepository(),
		Clock:           clock,
		Random:          bytes.NewReader(bytes.Repeat([]byte{0x51}, 256)),
		SessionLifetime: 8 * time.Hour,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	created, err := manager.Create(context.Background(), runtimeuser.CreateCommand{
		Username: "operator",
		Password: []byte("test-only-password"),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	machineID, err := workspaceidentity.ParseMachineID(
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x32}, 32)),
	)
	if err != nil {
		t.Fatalf("ParseMachineID() error = %v", err)
	}
	session, err := manager.Login(context.Background(), runtimeuser.LoginCommand{
		Username: "operator", Password: []byte("test-only-password"),
		MachineID: machineID, DeviceName: "Linux workstation",
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	reopened := openTestStore(t, databasePath)
	defer func() {
		if err := reopened.Shutdown(context.Background()); err != nil {
			t.Errorf("reopened Shutdown() error = %v", err)
		}
	}()
	recovered, err := runtimeuser.New(runtimeuser.Options{
		Repository:      reopened.RuntimeUserRepository(),
		Clock:           clock,
		Random:          bytes.NewReader(bytes.Repeat([]byte{0x61}, 128)),
		SessionLifetime: 8 * time.Hour,
	})
	if err != nil {
		t.Fatalf("reopened New() error = %v", err)
	}
	identity, err := recovered.Authenticate(context.Background(), session.Token.Value())
	if err != nil {
		t.Fatalf("Authenticate() after reopen error = %v", err)
	}
	if identity.User != created || identity.MachineID != machineID ||
		identity.DeviceName != "Linux workstation" {
		t.Fatalf("Authenticate() after reopen = %#v", identity)
	}
}

type runtimeUserTestClock struct{ now time.Time }

func (clock runtimeUserTestClock) Now() time.Time { return clock.now }
