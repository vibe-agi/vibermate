package codexoauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func loginFixture(t *testing.T) (*LoginManager, *recordingClient, *atomic.Int32) {
	t.Helper()
	token := testJWT(t, map[string]any{"email": "test@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace-login", "chatgpt_plan_type": "pro"}})
	body, _ := json.Marshal(map[string]string{"id_token": token, "access_token": token, "refresh_token": "private-refresh-sentinel"})
	client := &recordingClient{response: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}}
	saves := &atomic.Int32{}
	manager, err := NewLoginManager(LoginOptions{Client: client, Clock: fixedClock{now: time.Now().UTC()}, Persist: func(ctx context.Context, target LoginAccount, credential *Credential) (string, error) {
		if target.ID != "account.codex.test" || target.EndpointID != "target.codex.official" || credential.Profile().AccountID != "workspace-login" || credential.Profile().Email != "test@example.com" || credential.Profile().PlanType != "pro" {
			t.Error("login lost its frozen account destination or identity")
		}
		saves.Add(1)
		return target.ID, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return manager, client, saves
}

func startLogin(t *testing.T, manager *LoginManager, mode string) LoginView {
	t.Helper()
	view, err := manager.Start(context.Background(), "owner-session", LoginAccount{ID: "account.codex.test", EndpointID: "target.codex.official"}, mode)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func loginCallback(t *testing.T, view LoginView) string {
	t.Helper()
	authorization, err := url.Parse(view.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	return authorization.Query().Get("redirect_uri") + "?" + url.Values{"state": {authorization.Query().Get("state")}, "code": {"private-code-sentinel"}}.Encode()
}

func TestLoginCompletesPKCEOnceAndOnlyReturnsSafeState(t *testing.T) {
	manager, client, saves := loginFixture(t)
	view := startLogin(t, manager, "manual")
	authorization, _ := url.Parse(view.AuthorizationURL)
	params := authorization.Query()
	if params.Get("client_id") != ClientID || params.Get("code_challenge_method") != "S256" || params.Get("response_type") != "code" || params.Get("scope") != "openid profile email offline_access" || len(params.Get("state")) != 43 {
		t.Fatal("invalid PKCE authorization contract")
	}
	callback := loginCallback(t, view)
	completed, err := manager.Complete(context.Background(), "owner-session", view.ID, callback)
	if err != nil || completed.State != "completed" || completed.AccountID != "account.codex.test" || saves.Load() != 1 {
		t.Fatalf("complete: state=%s saves=%d err=%v", completed.State, saves.Load(), err)
	}
	request := client.Request()
	body, _ := io.ReadAll(request.Body)
	form, _ := url.ParseQuery(string(body))
	challenge := sha256.Sum256([]byte(form.Get("code_verifier")))
	if request.URL.String() != TokenURL || request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
		form.Get("grant_type") != "authorization_code" || form.Get("client_id") != ClientID || form.Get("code") != "private-code-sentinel" ||
		form.Get("redirect_uri") != params.Get("redirect_uri") || base64.RawURLEncoding.EncodeToString(challenge[:]) != params.Get("code_challenge") {
		t.Fatal("invalid authorization-code grant contract")
	}
	encoded, _ := json.Marshal(completed)
	for _, secret := range []string{"private-code-sentinel", "private-refresh-sentinel", form.Get("code_verifier"), params.Get("state")} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("completed login exposes secret material")
		}
	}
	again, err := manager.Complete(context.Background(), "owner-session", view.ID, callback)
	if err != nil || again != completed || saves.Load() != 1 || client.Calls() != 1 {
		t.Fatal("callback replay exchanged or saved twice")
	}
}

func TestLoginRejectsWrongOwnerStateDestinationAndDuplicateParameters(t *testing.T) {
	manager, client, saves := loginFixture(t)
	view := startLogin(t, manager, "manual")
	callback := loginCallback(t, view)
	if _, err := manager.Status(context.Background(), "other-session", view.ID); !errors.Is(err, ErrLoginNotFound) {
		t.Fatal("login visible to another session")
	}
	if err := manager.Cancel(context.Background(), "other-session", view.ID); !errors.Is(err, ErrLoginNotFound) {
		t.Fatal("another session can cancel login")
	}
	if _, err := manager.Complete(context.Background(), "other-session", view.ID, callback); !errors.Is(err, ErrLoginNotFound) {
		t.Fatal("another session can complete login")
	}
	other := startLogin(t, manager, "manual")
	for _, raw := range []string{
		strings.Replace(callback, "localhost", "example.com", 1),
		strings.Replace(callback, "/auth/callback", "/other", 1),
		callback + "&state=another", callback + "&code=another", callback + "#fragment",
		strings.Replace(callback, "state=", "state=wrong", 1),
		strings.Replace(callback, "private-code-sentinel", "%00", 1),
		loginCallback(t, other),
	} {
		if _, err := manager.Complete(context.Background(), "owner-session", view.ID, raw); !errors.Is(err, ErrLoginInvalid) {
			t.Fatal("invalid callback accepted")
		}
	}
	if client.Calls() != 0 || saves.Load() != 0 {
		t.Fatal("invalid callback reached credential exchange")
	}
	status, _ := manager.Status(context.Background(), "owner-session", view.ID)
	if status.State != "pending" {
		t.Fatal("wrong state poisoned another pending login")
	}
}

func TestLoginConcurrentCompletionCannotDoubleSaveOrCancelMidCommit(t *testing.T) {
	manager, client, saves := loginFixture(t)
	view := startLogin(t, manager, "manual")
	client.started = make(chan struct{})
	client.gate = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := manager.Complete(context.Background(), "owner-session", view.ID, loginCallback(t, view))
		done <- err
	}()
	<-client.started
	_, err := manager.Complete(context.Background(), "owner-session", view.ID, loginCallback(t, view))
	cancelErr := manager.Cancel(context.Background(), "owner-session", view.ID)
	close(client.gate)
	if !errors.Is(err, ErrLoginBusy) || !errors.Is(cancelErr, ErrLoginBusy) {
		t.Fatal("concurrent completion/cancel admitted")
	}
	if err := <-done; err != nil || saves.Load() != 1 || client.Calls() != 1 {
		t.Fatal("login was not committed exactly once")
	}
}

func TestLoginCancellationDenialExpiryAndFailuresDoNotSaveAccounts(t *testing.T) {
	for _, scenario := range []string{"cancel", "deny", "expire", "transport", "malformed", "provider", "persistence"} {
		t.Run(scenario, func(t *testing.T) {
			manager, client, saves := loginFixture(t)
			view := startLogin(t, manager, "manual")
			callback := loginCallback(t, view)
			switch scenario {
			case "cancel":
				_ = manager.Cancel(context.Background(), "owner-session", view.ID)
			case "deny":
				callback = strings.Replace(callback, "code=private-code-sentinel", "error=access_denied", 1)
			case "expire":
				manager.options.Clock = fixedClock{now: view.ExpiresAt.Add(time.Second)}
			case "transport":
				client.err = errors.New("network error with private-code-sentinel")
			case "malformed":
				client.response.Body = io.NopCloser(strings.NewReader(`{"access_token":"private-token"}`))
			case "provider":
				client.response.StatusCode = http.StatusBadRequest
			case "persistence":
				manager.options.Persist = func(context.Context, LoginAccount, *Credential) (string, error) {
					return "", errors.New("secret store failure")
				}
			}
			result, err := manager.Complete(context.Background(), "owner-session", view.ID, callback)
			if err != nil || result.State == "completed" || result.State == "pending" || result.AuthorizationURL != "" || result.AccountID != "" || saves.Load() != 0 {
				t.Fatalf("unexpected terminal: state=%s err=%v", result.State, err)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "private-") {
				t.Fatal("login failure leaked a token or upstream error")
			}
		})
	}
}

func TestLoginLoopbackCallbackPersistsAndDoesNotReflectSecrets(t *testing.T) {
	manager, _, saves := loginFixture(t)
	view := startLogin(t, manager, "loopback")
	if view.CallbackMode != "loopback" {
		t.Skip("registered loopback ports are already in use")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(loginCallback(t, view))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || saves.Load() != 1 || response.Header.Get("Cache-Control") != "no-store" || strings.Contains(string(body), "private-") {
		t.Fatal("loopback callback did not safely complete")
	}
}
