package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
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

type interruptedSessionRepository struct {
	runtimeuser.Repository
	beforeInsert func()
}

func (repository *interruptedSessionRepository) CreateSession(ctx context.Context, record runtimeuser.SessionRecord, expected time.Time) error {
	repository.beforeInsert()
	return repository.Repository.CreateSession(ctx, record, expected)
}

func TestCredentialChangesFenceSessionInsertionAndPasswordChanges(t *testing.T) {
	for _, action := range []string{"disable", "password-reset"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.sqlite"))
			t.Cleanup(func() { _ = store.Shutdown(ctx) })
			clock := &runtimeUserTestClock{now: time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC)}
			repository := &interruptedSessionRepository{Repository: store.RuntimeUserRepository()}
			users, err := runtimeuser.New(runtimeuser.Options{Repository: repository, Clock: clock, Random: rand.Reader, SessionLifetime: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			user, err := users.Create(ctx, runtimeuser.CreateCommand{Username: "alice", Password: []byte("test-old-password")})
			if err != nil {
				t.Fatal(err)
			}
			repository.beforeInsert = func() {
				var updated runtimeuser.User
				if action == "disable" {
					updated, err = users.Disable(ctx, user.ID)
				} else {
					updated, err = users.ReplacePassword(ctx, user.ID, []byte("test-new-password"))
				}
				if err != nil {
					t.Fatal(err)
				}
				if !updated.UpdatedAt.After(user.UpdatedAt) {
					t.Fatal("credential revision did not advance within the same clock tick")
				}
			}
			machine, _ := workspaceidentity.ParseMachineID(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x32}, 32)))
			_, err = users.Login(ctx, runtimeuser.LoginCommand{Username: user.Username, Password: []byte("test-old-password"), MachineID: machine, DeviceName: "Test device"})
			if !errors.Is(err, runtimeuser.ErrInvalidCredentials) {
				t.Fatalf("stale login result = %v", err)
			}
			if _, err := users.ChangePassword(ctx, user, []byte("test-stale-password")); !errors.Is(err, runtimeuser.ErrInvalidCredentials) {
				t.Fatalf("stale self-service change = %v", err)
			}
			// Wall-clock rollback must not recycle a previously authenticated revision.
			current, _ := users.User(ctx, user.ID)
			clock.now = clock.now.Add(-time.Hour)
			next, err := users.ReplacePassword(ctx, user.ID, []byte("test-final-password"))
			if err != nil || !next.UpdatedAt.After(current.UpdatedAt) {
				t.Fatalf("clock rollback revision = %v", err)
			}
		})
	}
}
