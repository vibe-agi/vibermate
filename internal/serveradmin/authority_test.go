package serveradmin

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
