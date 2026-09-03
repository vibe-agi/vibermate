package runtimeuser

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

func TestValidUsernameRejectsMissingOrNonCanonicalValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "AlIce", " alice", "ab"} {
		if ValidUsername(value) {
			t.Fatalf("ValidUsername(%q) = true", value)
		}
	}
	if !ValidUsername("alice") {
		t.Fatal("ValidUsername(\"alice\") = false")
	}
}

func TestCreatedRuntimeUserCanReuseLoginSessionUntilLogout(t *testing.T) {
	t.Parallel()
	clock := fixedClock{now: time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)}
	repository := newMemoryRepository()
	manager, err := New(Options{
		Repository:      repository,
		Clock:           clock,
		Random:          bytes.NewReader(bytes.Repeat([]byte{0x42}, 256)),
		SessionLifetime: 8 * time.Hour,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	created, err := manager.Create(context.Background(), CreateCommand{
		Username: "alice",
		Password: []byte("correct horse battery staple"),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Username != "alice" || created.ID == "" || created.State != StateActive {
		t.Fatalf("Create() = %#v", created)
	}

	machineID, err := workspaceidentity.ParseMachineID(
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x24}, 32)),
	)
	if err != nil {
		t.Fatalf("ParseMachineID() error = %v", err)
	}
	session, err := manager.Login(context.Background(), LoginCommand{
		Username:   "alice",
		Password:   []byte("correct horse battery staple"),
		MachineID:  machineID,
		DeviceName: "Alice's MacBook",
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if session.Token.Value() == "" || session.User != created ||
		session.MachineID != machineID || session.DeviceName != "Alice's MacBook" ||
		!session.ExpiresAt.Equal(clock.now.Add(8*time.Hour)) {
		t.Fatalf("Login() = %#v", session)
	}

	for attempt := 0; attempt < 2; attempt++ {
		identity, authErr := manager.Authenticate(
			context.Background(),
			session.Token.Value(),
		)
		if authErr != nil {
			t.Fatalf("Authenticate() attempt %d error = %v", attempt, authErr)
		}
		if identity.User != created || identity.MachineID != machineID ||
			identity.DeviceName != "Alice's MacBook" {
			t.Fatalf("Authenticate() attempt %d = %#v", attempt, identity)
		}
	}

	if err := manager.Logout(context.Background(), session.Token.Value()); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := manager.Authenticate(context.Background(), session.Token.Value()); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Authenticate() after Logout error = %v", err)
	}
	if err := manager.Logout(context.Background(), session.Token.Value()); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("second Logout() error = %v", err)
	}
}

func TestDisablingRuntimeUserRevokesEveryLoginSession(t *testing.T) {
	t.Parallel()
	clock := fixedClock{now: time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)}
	repository := newMemoryRepository()
	manager, err := New(Options{
		Repository: repository, Clock: &clock,
		Random:          bytes.NewReader(bytes.Repeat([]byte{0x53}, 512)),
		SessionLifetime: 8 * time.Hour,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	created, err := manager.Create(context.Background(), CreateCommand{
		Username: "alice", Password: []byte("test-disable-password"),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	machineID, _ := workspaceidentity.ParseMachineID(
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x25}, 32)),
	)
	session, err := manager.Login(context.Background(), LoginCommand{
		Username: "alice", Password: []byte("test-disable-password"),
		MachineID: machineID, DeviceName: "Test device",
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	clock.now = clock.now.Add(time.Minute)
	disabled, err := manager.Disable(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if disabled.State != StateDisabled || !disabled.UpdatedAt.Equal(clock.now) {
		t.Fatalf("Disable() = %#v", disabled)
	}
	if _, err := manager.Authenticate(context.Background(), session.Token.Value()); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Authenticate() disabled session error = %v", err)
	}
	users, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(users) != 1 || users[0] != disabled {
		t.Fatalf("List() = %#v", users)
	}
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type memoryRepository struct {
	usersByID       map[UserID]UserRecord
	userIDsByName   map[string]UserID
	sessionsByToken map[SessionDigest]SessionRecord
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		usersByID:       make(map[UserID]UserRecord),
		userIDsByName:   make(map[string]UserID),
		sessionsByToken: make(map[SessionDigest]SessionRecord),
	}
}

func (repository *memoryRepository) CreateUser(
	_ context.Context,
	record UserRecord,
) error {
	if _, exists := repository.userIDsByName[record.User.Username]; exists {
		return ErrUsernameConflict
	}
	repository.usersByID[record.User.ID] = record
	repository.userIDsByName[record.User.Username] = record.User.ID
	return nil
}

func (repository *memoryRepository) FindUserByUsername(
	_ context.Context,
	username string,
) (UserRecord, bool, error) {
	id, exists := repository.userIDsByName[username]
	if !exists {
		return UserRecord{}, false, nil
	}
	return repository.usersByID[id], true, nil
}

func (repository *memoryRepository) ListUsers(
	_ context.Context,
) ([]UserRecord, error) {
	records := make([]UserRecord, 0, len(repository.usersByID))
	for _, record := range repository.usersByID {
		records = append(records, record)
	}
	return records, nil
}

func (repository *memoryRepository) SetUserState(
	_ context.Context,
	id UserID,
	state State,
	updatedAt time.Time,
) (UserRecord, bool, error) {
	record, exists := repository.usersByID[id]
	if !exists {
		return UserRecord{}, false, nil
	}
	record.User.State = state
	record.User.UpdatedAt = updatedAt
	repository.usersByID[id] = record
	if state == StateDisabled {
		for digest, session := range repository.sessionsByToken {
			if session.UserID == id && session.RevokedAt.IsZero() {
				session.RevokedAt = updatedAt
				repository.sessionsByToken[digest] = session
			}
		}
	}
	return record, true, nil
}

func (repository *memoryRepository) CreateSession(
	_ context.Context,
	record SessionRecord,
) error {
	if _, exists := repository.sessionsByToken[record.TokenDigest]; exists {
		return errors.New("duplicate test session")
	}
	repository.sessionsByToken[record.TokenDigest] = record
	return nil
}

func (repository *memoryRepository) FindSession(
	_ context.Context,
	digest SessionDigest,
) (SessionRecord, UserRecord, bool, error) {
	session, exists := repository.sessionsByToken[digest]
	if !exists || !session.RevokedAt.IsZero() {
		return SessionRecord{}, UserRecord{}, false, nil
	}
	user, exists := repository.usersByID[session.UserID]
	if !exists {
		return SessionRecord{}, UserRecord{}, false, nil
	}
	return session, user, true, nil
}

func (repository *memoryRepository) RevokeSession(
	_ context.Context,
	digest SessionDigest,
	revokedAt time.Time,
) (bool, error) {
	session, exists := repository.sessionsByToken[digest]
	if !exists || !session.RevokedAt.IsZero() {
		return false, nil
	}
	session.RevokedAt = revokedAt
	repository.sessionsByToken[digest] = session
	return true, nil
}

var _ Repository = (*memoryRepository)(nil)
