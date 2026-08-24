package servercontrol_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

func TestRuntimeUserLoginSessionHTTPAuthorizesRepeatedCaptureControl(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{
		DatabasePath:           filepath.Join(t.TempDir(), "runtime.sqlite"),
		BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
		CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	}()
	clock := loginTestClock{
		now: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	}
	users, err := runtimeuser.New(runtimeuser.Options{
		Repository: store.RuntimeUserRepository(), Clock: clock,
		Random:          bytes.NewReader(bytes.Repeat([]byte{0x71}, 512)),
		SessionLifetime: 8 * time.Hour,
	})
	if err != nil {
		t.Fatalf("runtimeuser.New() error = %v", err)
	}
	created, err := users.Create(ctx, runtimeuser.CreateCommand{
		Username: "alice", Password: []byte("test-login-password"),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	handler, err := servercontrol.NewRuntimeUserSessions(
		servercontrol.RuntimeUserSessionsOptions{
			InstanceID: "instance.test", Users: users,
		},
	)
	if err != nil {
		t.Fatalf("NewRuntimeUserSessions() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	machineID := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x35}, 32))
	payload := map[string]any{
		"schema":     servercontrol.RuntimeUserLoginSchema,
		"username":   "alice",
		"password":   "test-login-password",
		"machineId":  machineID,
		"deviceName": "Alice's MacBook",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	response, err := http.Post(
		server.URL+servercontrol.RuntimeUserSessionPath,
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST login error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("POST login status = %d", response.StatusCode)
	}
	var session servercontrol.RuntimeUserSession
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&session); err != nil {
		t.Fatalf("decode login response error = %v", err)
	}
	if session.Schema != servercontrol.RuntimeUserSessionSchema ||
		session.InstanceID != "instance.test" || session.APIVersion != "v1" ||
		session.User.ID != string(created.ID) || session.User.Username != "alice" ||
		session.SessionID == "" || session.SessionToken == "" ||
		!session.ExpiresAt.Equal(clock.now.Add(8*time.Hour)) {
		t.Fatalf("login response = %#v", session)
	}
	if strings.Contains(string(body), session.SessionToken) {
		t.Fatal("test request unexpectedly contained issued session token")
	}
	for attempt := 0; attempt < 2; attempt++ {
		identity, authErr := users.Authenticate(ctx, session.SessionToken)
		if authErr != nil || identity.User.ID != created.ID ||
			identity.MachineID.String() != machineID {
			t.Fatalf("Authenticate() attempt %d = %#v, %v", attempt, identity, authErr)
		}
	}
	logout, err := http.NewRequest(
		http.MethodDelete,
		server.URL+servercontrol.RuntimeUserCurrentSessionPath,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	logout.Header.Set("Authorization", "Bearer "+session.SessionToken)
	logoutResponse, err := http.DefaultClient.Do(logout)
	if err != nil {
		t.Fatal(err)
	}
	logoutResponse.Body.Close()
	if logoutResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE current Login Session status = %d", logoutResponse.StatusCode)
	}
	if _, err := users.Authenticate(ctx, session.SessionToken); !errors.Is(err, runtimeuser.ErrInvalidSession) {
		t.Fatalf("Authenticate() after HTTP logout error = %v", err)
	}
}

type loginTestClock struct{ now time.Time }

func (clock loginTestClock) Now() time.Time { return clock.now }
