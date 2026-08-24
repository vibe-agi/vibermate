package servercontrol

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/serveradmin"
)

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

func TestAdminAccessKeyMintsBoundedManagementSession(t *testing.T) {
	clock := fixedClock{now: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)}
	authority, err := serveradmin.Open(serveradmin.Options{
		DataDirectory: filepath.Join(t.TempDir(), "admin"), Clock: clock,
		Random: bytes.NewReader(append(
			append(bytes.Repeat([]byte{0x11}, 32), bytes.Repeat([]byte{0x12}, 32)...),
			bytes.Repeat([]byte{0x13}, 32)...,
		)),
		SessionLifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewAdminSessions(AdminSessionsOptions{
		InstanceID: "runtime-test", Authority: authority,
	})
	if err != nil {
		t.Fatal(err)
	}
	keyPayload, err := os.ReadFile(authority.AccessKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	accessKey := strings.TrimSpace(string(keyPayload))
	payload, _ := json.Marshal(AdminLogin{Schema: AdminLoginSchema, AccessKey: accessKey})
	request := httptest.NewRequest(http.MethodPost, AdminSessionPath, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var session AdminSession
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if session.Schema != AdminSessionSchema || session.InstanceID != "runtime-test" ||
		session.ReadToken == "" || session.WriteToken == "" ||
		strings.Contains(response.Body.String(), accessKey) ||
		!authority.Authorize(session.ReadToken, serveradmin.ScopeRead) ||
		!authority.Authorize(session.WriteToken, serveradmin.ScopeWrite) {
		t.Fatalf("invalid admin session response: %+v", session)
	}
}

func TestAdminLoginDoesNotRevealWhyAccessWasDenied(t *testing.T) {
	authority, err := serveradmin.Open(serveradmin.Options{
		DataDirectory: filepath.Join(t.TempDir(), "admin"), Clock: fixedClock{now: time.Now()},
		Random: bytes.NewReader(bytes.Repeat([]byte{0x21}, 96)), SessionLifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := NewAdminSessions(AdminSessionsOptions{InstanceID: "runtime-test", Authority: authority})
	payload, _ := json.Marshal(AdminLogin{
		Schema: AdminLoginSchema, AccessKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	})
	request := httptest.NewRequest(http.MethodPost, AdminSessionPath, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized ||
		strings.Contains(response.Body.String(), "AccessKey") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
