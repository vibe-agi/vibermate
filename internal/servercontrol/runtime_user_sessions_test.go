package servercontrol_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	current, err := http.NewRequest(
		http.MethodGet,
		server.URL+servercontrol.RuntimeUserCurrentSessionPath,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	current.Header.Set("Authorization", "Bearer "+session.SessionToken)
	currentResponse, err := http.DefaultClient.Do(current)
	if err != nil {
		t.Fatal(err)
	}
	defer currentResponse.Body.Close()
	if currentResponse.StatusCode != http.StatusOK {
		t.Fatalf("GET current Login Session status = %d", currentResponse.StatusCode)
	}
	var currentSession servercontrol.RuntimeUserCurrentSession
	currentDecoder := json.NewDecoder(currentResponse.Body)
	currentDecoder.DisallowUnknownFields()
	if err := currentDecoder.Decode(&currentSession); err != nil {
		t.Fatalf("decode current Login Session error = %v", err)
	}
	if currentSession.Schema != servercontrol.RuntimeUserCurrentSessionSchema ||
		currentSession.InstanceID != "instance.test" || currentSession.APIVersion != "v1" ||
		currentSession.User.ID != string(created.ID) || currentSession.User.Username != "alice" ||
		currentSession.SessionID != session.SessionID || currentSession.MachineID != machineID ||
		currentSession.DeviceName != "Alice's MacBook" {
		t.Fatalf("current Login Session = %#v", currentSession)
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

func TestRuntimeUserLoginRateLimitsOneUnauthenticatedPeerBeforePasswordWork(t *testing.T) {
	t.Parallel()
	users := &rejectingRuntimeUserSessions{}
	handler, err := servercontrol.NewRuntimeUserSessions(
		servercontrol.RuntimeUserSessionsOptions{
			InstanceID: "instance.test", Users: users,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	rateLimited := false
	for attempt := 0; attempt < 20; attempt++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, runtimeUserLoginRequest("192.0.2.10:45000"))
		if response.Code == http.StatusTooManyRequests {
			rateLimited = true
			if response.Header().Get("Retry-After") == "" {
				t.Fatal("rate-limited login has no Retry-After")
			}
			break
		}
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d", attempt, response.Code)
		}
	}
	if !rateLimited {
		t.Fatal("20 immediate login attempts were all admitted")
	}
	if calls := users.calls.Load(); calls <= 0 || calls >= 20 {
		t.Fatalf("password verifier calls = %d", calls)
	}
}

func TestRuntimeUserLoginBoundsConcurrentPasswordWork(t *testing.T) {
	t.Parallel()
	users := &blockingRuntimeUserSessions{
		entered: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
	handler, err := servercontrol.NewRuntimeUserSessions(
		servercontrol.RuntimeUserSessionsOptions{
			InstanceID: "instance.test", Users: users,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	responses := make(chan int, 5)
	for attempt := 0; attempt < 5; attempt++ {
		go func(port int) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(
				response,
				runtimeUserLoginRequest(fmt.Sprintf("198.51.100.%d:%d", port+1, 46000+port)),
			)
			responses <- response.Code
		}(attempt)
	}

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for users.active.Load() < 4 {
		select {
		case <-users.entered:
		case <-deadline.C:
			close(users.release)
			t.Fatalf("only %d password checks entered", users.active.Load())
		}
	}
	select {
	case status := <-responses:
		if status != http.StatusTooManyRequests {
			close(users.release)
			t.Fatalf("fifth concurrent login status = %d", status)
		}
	case <-deadline.C:
		close(users.release)
		t.Fatal("fifth concurrent login blocked instead of being rejected")
	}
	close(users.release)
	for completed := 1; completed < 5; completed++ {
		<-responses
	}
	if maximum := users.maximum.Load(); maximum > 4 {
		t.Fatalf("concurrent password checks = %d, want at most 4", maximum)
	}
}

func runtimeUserLoginRequest(remoteAddress string) *http.Request {
	machineID := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x35}, 32))
	payload, _ := json.Marshal(map[string]any{
		"schema": servercontrol.RuntimeUserLoginSchema, "username": "alice",
		"password": "test-login-password", "machineId": machineID,
		"deviceName": "test device",
	})
	request := httptest.NewRequest(
		http.MethodPost,
		servercontrol.RuntimeUserSessionPath,
		bytes.NewReader(payload),
	)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = remoteAddress
	return request
}

type rejectingRuntimeUserSessions struct{ calls atomic.Int32 }

func (users *rejectingRuntimeUserSessions) Login(
	context.Context,
	runtimeuser.LoginCommand,
) (runtimeuser.LoginSession, error) {
	users.calls.Add(1)
	return runtimeuser.LoginSession{}, runtimeuser.ErrInvalidCredentials
}

func (*rejectingRuntimeUserSessions) Logout(context.Context, string) error { return nil }

func (*rejectingRuntimeUserSessions) Authenticate(
	context.Context,
	string,
) (runtimeuser.Identity, error) {
	return runtimeuser.Identity{}, runtimeuser.ErrInvalidSession
}

type blockingRuntimeUserSessions struct {
	active  atomic.Int32
	maximum atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (users *blockingRuntimeUserSessions) Login(
	context.Context,
	runtimeuser.LoginCommand,
) (runtimeuser.LoginSession, error) {
	active := users.active.Add(1)
	for {
		maximum := users.maximum.Load()
		if active <= maximum || users.maximum.CompareAndSwap(maximum, active) {
			break
		}
	}
	users.entered <- struct{}{}
	<-users.release
	users.active.Add(-1)
	return runtimeuser.LoginSession{}, runtimeuser.ErrInvalidCredentials
}

func (*blockingRuntimeUserSessions) Logout(context.Context, string) error { return nil }

func (*blockingRuntimeUserSessions) Authenticate(
	context.Context,
	string,
) (runtimeuser.Identity, error) {
	return runtimeuser.Identity{}, runtimeuser.ErrInvalidSession
}

type loginTestClock struct{ now time.Time }

func (clock loginTestClock) Now() time.Time { return clock.now }
