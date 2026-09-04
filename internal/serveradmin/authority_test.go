package serveradmin

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

type fixedClock struct{ now time.Time }

func (clock *fixedClock) Now() time.Time { return clock.now }

func TestAccessKeyMintsSeparateShortLivedAdminCapabilities(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)}
	directory := filepath.Join(t.TempDir(), "admin")
	authority, err := Open(Options{
		DataDirectory: directory, Clock: clock,
		Random:          randomSequence(0x41, 0x42, 0x43),
		SessionLifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(authority.AccessKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	accessKey := strings.TrimSpace(string(payload))
	session, err := authority.Login(accessKey)
	if err != nil {
		t.Fatal(err)
	}
	if session.ReadToken.Value() == session.WriteToken.Value() ||
		!authority.Authorize(session.ReadToken.Value(), ScopeRead) ||
		!authority.Authorize(session.WriteToken.Value(), ScopeWrite) ||
		authority.Authorize(session.ReadToken.Value(), ScopeWrite) ||
		authority.Authorize(session.WriteToken.Value(), ScopeRead) {
		t.Fatal("admin capabilities did not preserve exact scopes")
	}
	if strings.Contains(session.ReadToken.String(), session.ReadToken.Value()) ||
		strings.Contains(session.WriteToken.GoString(), session.WriteToken.Value()) {
		t.Fatal("admin credential formatting exposed a capability")
	}
	clock.now = session.ExpiresAt
	if authority.Authorize(session.ReadToken.Value(), ScopeRead) ||
		authority.Authorize(session.WriteToken.Value(), ScopeWrite) {
		t.Fatal("expired admin session remained authorized")
	}
}

func randomSequence(values ...byte) *bytes.Reader {
	var payload []byte
	for _, value := range values {
		payload = append(payload, bytes.Repeat([]byte{value}, credentialBytes)...)
	}
	return bytes.NewReader(payload)
}

func TestAdminAccessKeyIsPersistentAndOwnerOnly(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)}
	directory := filepath.Join(t.TempDir(), "admin")
	first, err := Open(Options{
		DataDirectory: directory, Clock: clock,
		Random:          bytes.NewReader(bytes.Repeat([]byte{0x31}, 128)),
		SessionLifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := os.ReadFile(first.AccessKeyPath())
	key := strings.TrimSpace(string(payload))
	info, err := os.Stat(first.AccessKeyPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("access key mode = %v, error = %v", info.Mode().Perm(), err)
	}
	second, err := Open(Options{
		DataDirectory: directory, Clock: clock,
		Random:          randomSequence(0x62, 0x63),
		SessionLifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Login(key); err != nil {
		t.Fatalf("persisted access key was not accepted: %v", err)
	}
}

func TestWrongAdminAccessKeyCannotMintSession(t *testing.T) {
	authority, err := Open(Options{
		DataDirectory: filepath.Join(t.TempDir(), "admin"), Clock: &fixedClock{now: time.Now()},
		Random:          bytes.NewReader(bytes.Repeat([]byte{0x33}, 128)),
		SessionLifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrong := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := authority.Login(wrong); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong access key error = %v", err)
	}
}

func TestClaimedOwnerAndMemberReceiveDistinctWebAuthority(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)}
	authority, err := Open(Options{
		DataDirectory: filepath.Join(t.TempDir(), "admin"), Clock: clock,
		Random:          randomSequence(0x31, 0x41, 0x42, 0x43, 0x44),
		SessionLifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	recoveryPayload, _ := os.ReadFile(authority.AccessKeyPath())
	recoveryKey := strings.TrimSpace(string(recoveryPayload))
	owner := testUser(0x51, "alice")
	member := testUser(0x52, "bob")
	if err := authority.ClaimOwner(recoveryKey, owner.ID); err != nil {
		t.Fatal(err)
	}
	if !authority.IsOwner(owner.ID) || authority.IsOwner(member.ID) {
		t.Fatal("owner identity was not exact")
	}
	if _, err := authority.Login(recoveryKey); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("configured recovery key minted a normal session: %v", err)
	}
	ownerSession, err := authority.LoginUser(owner)
	if err != nil {
		t.Fatal(err)
	}
	memberSession, err := authority.LoginUser(member)
	if err != nil {
		t.Fatal(err)
	}
	if ownerSession.Principal.Role != RoleOwner ||
		memberSession.Principal.Role != RoleMember {
		t.Fatalf("roles = %q, %q", ownerSession.Principal.Role, memberSession.Principal.Role)
	}
	if !authority.Authorize(ownerSession.ReadToken.Value(), ScopeRead) ||
		authority.Authorize(memberSession.ReadToken.Value(), ScopeRead) {
		t.Fatal("owner-only management authority was not preserved")
	}
	principal, valid := authority.Authenticate(memberSession.ReadToken.Value(), ScopeRead)
	if !valid || principal.UserID != member.ID || principal.Username != "bob" {
		t.Fatalf("member principal = %#v, %v", principal, valid)
	}
	authority.RevokeUserSessions(member.ID)
	if _, valid := authority.Authenticate(memberSession.ReadToken.Value(), ScopeRead); valid {
		t.Fatal("revoked member Web Session remained valid")
	}
}

func TestRecoveryKeyRotationPersistsAcrossOpen(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)}
	directory := filepath.Join(t.TempDir(), "admin")
	authority, err := Open(Options{
		DataDirectory: directory, Clock: clock,
		Random: randomSequence(0x31, 0x61), SessionLifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(authority.AccessKeyPath())
	if err := authority.RotateRecoveryKey(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(authority.AccessKeyPath())
	if bytes.Equal(before, after) || authority.RecoveryKeyValid(strings.TrimSpace(string(before))) ||
		!authority.RecoveryKeyValid(strings.TrimSpace(string(after))) {
		t.Fatal("recovery key was not rotated exactly")
	}
	reopened, err := Open(Options{
		DataDirectory: directory, Clock: clock,
		Random: randomSequence(0x71), SessionLifetime: time.Hour,
	})
	if err != nil || !reopened.RecoveryKeyValid(strings.TrimSpace(string(after))) {
		t.Fatalf("reopened recovery key error = %v", err)
	}
}

func TestEnsureOwnerPersistsTheFirstOwnerAndRotatesRecoveryKey(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)}
	directory := filepath.Join(t.TempDir(), "admin")
	authority, err := Open(Options{
		DataDirectory: directory, Clock: clock,
		Random: randomSequence(0x31, 0x61), SessionLifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(authority.AccessKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	owner := testUser(0x51, "alice")
	created, err := authority.EnsureOwner(owner.ID)
	if err != nil || !created || !authority.IsOwner(owner.ID) {
		t.Fatalf("ensure owner = %v, %v", created, err)
	}
	after, err := os.ReadFile(authority.AccessKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	oldKey := strings.TrimSpace(string(before))
	newKey := strings.TrimSpace(string(after))
	if oldKey == newKey || authority.RecoveryKeyValid(oldKey) ||
		!authority.RecoveryKeyValid(newKey) {
		t.Fatal("binding the first owner did not rotate recovery material")
	}

	reopened, err := Open(Options{
		DataDirectory: directory, Clock: clock,
		Random: randomSequence(0x71), SessionLifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.IsOwner(owner.ID) || !reopened.RecoveryKeyValid(newKey) {
		t.Fatal("owner or rotated recovery material did not survive reopen")
	}
	created, err = reopened.EnsureOwner(testUser(0x52, "bob").ID)
	if err != nil || created {
		t.Fatalf("second owner ensure = %v, %v", created, err)
	}
}

func TestReadRecoveryKeyUsesTheSameStrictProtectedFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	authority, err := Open(Options{
		DataDirectory:   directory,
		Clock:           &fixedClock{now: time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC)},
		Random:          bytes.NewReader(bytes.Repeat([]byte{0x61}, 512)),
		SessionLifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(authority.AccessKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSuffix(string(payload), "\n")
	got, err := ReadRecoveryKey(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatal("ReadRecoveryKey returned different recovery material")
	}
	if err := os.Chmod(authority.AccessKeyPath(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecoveryKey(directory); err == nil {
		t.Fatal("ReadRecoveryKey accepted a group-readable credential")
	}
}

func testUser(fill byte, username string) runtimeuser.User {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	return runtimeuser.User{
		ID:       runtimeuser.UserID("user." + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 20))),
		Username: username, State: runtimeuser.StateActive,
		CreatedAt: now, UpdatedAt: now,
	}
}
