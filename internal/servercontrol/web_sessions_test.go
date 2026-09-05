package servercontrol_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/serveradmin"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

func TestWebSetupCreatesAnOwnerAndConsumesTheBootstrapKey(t *testing.T) {
	t.Parallel()

	handler, users, authority, adminDirectory := newWebSessionsHandler(t)
	bootstrapKey, err := serveradmin.ReadRecoveryKey(filepath.Join(t.TempDir(), "missing"))
	if err == nil || bootstrapKey != "" {
		t.Fatal("missing recovery material was accepted")
	}
	bootstrapKey, err = serveradmin.ReadRecoveryKey(adminDirectory)
	if err != nil {
		t.Fatal(err)
	}
	legacySession, err := authority.Login(bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}
	legacyCurrent := webRequest(
		t,
		handler,
		http.MethodGet,
		servercontrol.WebCurrentSessionPath,
		nil,
		legacySession.ReadToken.Value(),
	)
	if legacyCurrent.Code != http.StatusUnauthorized {
		t.Fatalf("legacy recovery session became a Web identity: %d %s", legacyCurrent.Code, legacyCurrent.Body.String())
	}

	auth := webRequest(t, handler, http.MethodGet, servercontrol.WebAuthPath, nil, "")
	if auth.Code != http.StatusOK || !bytes.Contains(auth.Body.Bytes(), []byte(`"setupRequired":true`)) {
		t.Fatalf("initial auth state = %d %s", auth.Code, auth.Body.String())
	}
	setup := webRequest(t, handler, http.MethodPost, servercontrol.WebSetupPath, map[string]any{
		"schema": servercontrol.WebSetupSchema, "recoveryKey": bootstrapKey,
		"username": "owner", "password": "correct horse battery staple",
	}, "")
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup = %d %s", setup.Code, setup.Body.String())
	}
	var session servercontrol.WebSession
	if err := json.Unmarshal(setup.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.Principal.Role != string(serveradmin.RoleOwner) ||
		session.Principal.Username != "owner" || session.ReadToken == session.WriteToken {
		t.Fatalf("owner session = %#v", session)
	}
	if authority.RecoveryKeyValid(bootstrapKey) {
		t.Fatal("bootstrap recovery key remained valid after setup")
	}
	owner, err := users.VerifyCredentials(
		context.Background(), "owner", []byte("correct horse battery staple"),
	)
	if err != nil || !authority.IsOwner(owner.ID) {
		t.Fatalf("owner binding = %#v, error = %v", owner, err)
	}

	current := webRequest(
		t, handler, http.MethodGet, servercontrol.WebCurrentSessionPath, nil, session.ReadToken,
	)
	if current.Code != http.StatusOK ||
		!bytes.Contains(current.Body.Bytes(), []byte(`"role":"owner"`)) {
		t.Fatalf("current session = %d %s", current.Code, current.Body.String())
	}
	auth = webRequest(t, handler, http.MethodGet, servercontrol.WebAuthPath, nil, "")
	if auth.Code != http.StatusOK || !bytes.Contains(auth.Body.Bytes(), []byte(`"setupRequired":false`)) {
		t.Fatalf("configured auth state = %d %s", auth.Code, auth.Body.String())
	}
}

func TestMemberWebLoginPasswordChangeAndLogoutStaySelfScoped(t *testing.T) {
	t.Parallel()

	handler, users, authority, adminDirectory := newWebSessionsHandler(t)
	owner, err := users.Create(context.Background(), runtimeuser.CreateCommand{
		Username: "owner", Password: []byte("owner password 123"),
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := serveradmin.ReadRecoveryKey(adminDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.ClaimOwner(key, owner.ID); err != nil {
		t.Fatal(err)
	}
	member, err := users.Create(context.Background(), runtimeuser.CreateCommand{
		Username: "alice", Password: []byte("alice password 123"),
	})
	if err != nil {
		t.Fatal(err)
	}

	login := webRequest(t, handler, http.MethodPost, servercontrol.WebSessionPath, map[string]any{
		"schema":   servercontrol.WebLoginSchema,
		"username": "alice", "password": "alice password 123",
	}, "")
	session := decodeWebSession(t, login)
	if session.Principal.ID != string(member.ID) ||
		session.Principal.Role != string(serveradmin.RoleMember) ||
		authority.Authorize(context.Background(), session.ReadToken, serveradmin.ScopeRead) {
		t.Fatalf("member received owner authority: %#v", session)
	}
	if principal, valid := authority.Authenticate(context.Background(), session.ReadToken, serveradmin.ScopeRead); !valid || principal.UserID != member.ID {
		t.Fatalf("member read session = %#v, valid = %v", principal, valid)
	}

	changed := webRequest(t, handler, http.MethodPatch, servercontrol.WebPasswordPath, map[string]any{
		"schema":          servercontrol.WebPasswordSchema,
		"currentPassword": "alice password 123", "newPassword": "alice password 456",
	}, session.WriteToken)
	replacement := decodeWebSession(t, changed)
	if _, valid := authority.Authenticate(context.Background(), session.ReadToken, serveradmin.ScopeRead); valid {
		t.Fatal("old read token survived password replacement")
	}
	if _, err := users.VerifyCredentials(context.Background(), "alice", []byte("alice password 456")); err != nil {
		t.Fatalf("replacement password was rejected: %v", err)
	}

	loggedOut := webRequest(
		t, handler, http.MethodDelete, servercontrol.WebCurrentSessionPath, nil,
		replacement.WriteToken,
	)
	if loggedOut.Code != http.StatusNoContent {
		t.Fatalf("logout = %d %s", loggedOut.Code, loggedOut.Body.String())
	}
	if _, valid := authority.Authenticate(context.Background(), replacement.ReadToken, serveradmin.ScopeRead); valid {
		t.Fatal("paired read token survived logout")
	}
}

func TestInvalidRecoveryPasswordDoesNotConsumeTheRecoveryKey(t *testing.T) {
	t.Parallel()

	handler, users, authority, adminDirectory := newWebSessionsHandler(t)
	owner, err := users.Create(context.Background(), runtimeuser.CreateCommand{
		Username: "owner", Password: []byte("owner password 123"),
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := serveradmin.ReadRecoveryKey(adminDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.ClaimOwner(key, owner.ID); err != nil {
		t.Fatal(err)
	}

	recovery := webRequest(t, handler, http.MethodPost, servercontrol.WebRecoveryPath, map[string]any{
		"schema": servercontrol.WebRecoverySchema, "recoveryKey": key,
		"newPassword": "short",
	}, "")
	if recovery.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid recovery = %d %s", recovery.Code, recovery.Body.String())
	}
	if !authority.RecoveryKeyValid(key) {
		t.Fatal("an invalid replacement password consumed the recovery key")
	}
}

func newWebSessionsHandler(
	t *testing.T,
) (*servercontrol.WebSessionsHandler, *runtimeuser.Manager, *serveradmin.Authority, string) {
	t.Helper()
	root := t.TempDir()
	store, err := runtimepersistence.Open(context.Background(), runtimepersistence.Options{
		DatabasePath:           filepath.Join(root, "runtime.sqlite"),
		BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
		CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	clock := serverUsageClock{now: time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC)}
	users, err := runtimeuser.New(runtimeuser.Options{
		Repository: store.RuntimeUserRepository(), Clock: clock, Random: rand.Reader,
		SessionLifetime: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	adminDirectory := filepath.Join(root, "server-admin")
	authority, err := serveradmin.Open(serveradmin.Options{
		DataDirectory: adminDirectory, Clock: clock, Random: rand.Reader,
		SessionLifetime: time.Hour,
		LookupUser:      users.User,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := servercontrol.NewWebSessions(servercontrol.WebSessionsOptions{
		InstanceID: "instance.web-test", Users: users, Sessions: authority,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler, users, authority, adminDirectory
}

func webRequest(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body map[string]any,
	token string,
) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.RemoteAddr = "192.0.2.10:4567"
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeWebSession(t *testing.T, response *httptest.ResponseRecorder) servercontrol.WebSession {
	t.Helper()
	if response.Code != http.StatusCreated {
		t.Fatalf("web session = %d %s", response.Code, response.Body.String())
	}
	var session servercontrol.WebSession
	if err := json.Unmarshal(response.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	return session
}
