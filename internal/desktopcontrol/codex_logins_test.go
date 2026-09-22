package desktopcontrol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type loginHTTPFunc func(*http.Request) (*http.Response, error)

func (f loginHTTPFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestCodexLoginControlUsesWriteSessionAndPersistsIndependentAccount(t *testing.T) {
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	token := controlJWT(t, map[string]any{"email": "login@example.com", "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "workspace-login", "chatgpt_plan_type": "pro"}})
	tokens, _ := json.Marshal(map[string]string{"id_token": token, "access_token": token, "refresh_token": "refresh-private-sentinel"})
	var calls atomic.Int32
	logins, err := codexoauth.NewLoginManager(codexoauth.LoginOptions{
		Clock: desktopcontrol.SystemClock{},
		Client: loginHTTPFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(tokens))}, nil
		}),
		Persist: func(ctx context.Context, target codexoauth.LoginAccount, credential *codexoauth.Credential) (string, error) {
			encoded, err := credential.MarshalBinary()
			if err != nil {
				return "", err
			}
			defer clear(encoded)
			material, err := providerauth.NewMaterial(string(encoded), nil, nil)
			if err != nil {
				return "", err
			}
			defer material.Destroy()
			payload, err := material.MarshalBinary()
			if err != nil {
				return "", err
			}
			defer clear(payload)
			value, err := secretstore.NewValue(payload)
			if err != nil {
				return "", err
			}
			defer value.Destroy()
			view, err := runtime.ProviderAccounts().Create(ctx, provideraccount.CreateCommand{
				ID: provideraccount.ID(target.ID), DisplayName: target.DisplayName,
				UpstreamEndpointID: upstreamendpoint.ID(target.EndpointID), Unlinked: true,
				Driver: providerauth.CodexOAuthDriverRef(), Secret: value,
			})
			return view.Account.ID.String(), err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer logins.Shutdown(context.Background())
	app, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime, Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(),
		Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(), Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(), CodexOAuth: runtime.CodexOAuthAccounts(), CodexLogins: logins,
		Offline: runtime, Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(owner, method, path string, payload any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(payload)
		if payload == nil {
			body = nil
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r = r.WithContext(desktopcontrol.WithOAuthSession(r.Context(), owner))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("If-Match", "0")
		r.Header.Set("Idempotency-Key", "login-control-key-"+method+path)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	input := map[string]any{"accountId": "account.codex.login", "displayName": "Codex Login", "upstreamEndpointId": "target.codex.official", "callbackMode": "manual"}
	unauthorized := request("", http.MethodPost, desktopcontrol.CodexLoginPath, input)
	if unauthorized.Code != 401 {
		t.Fatal("login admitted without an authenticated session")
	}
	started := request("session-one", http.MethodPost, desktopcontrol.CodexLoginPath, input)
	if started.Code != 201 {
		t.Fatalf("start status=%d body=%s", started.Code, started.Body)
	}
	var view codexoauth.LoginView
	if err := json.Unmarshal(started.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	path := desktopcontrol.CodexLoginPath + "/" + view.ID
	if app.RequiredScope(httptest.NewRequest(http.MethodGet, path, nil)) != desktopcontrol.ScopeWrite {
		t.Fatal("login status accepts a read-only token")
	}
	replayed := request("session-one", http.MethodPost, desktopcontrol.CodexLoginPath, input)
	if !bytes.Equal(started.Body.Bytes(), replayed.Body.Bytes()) {
		t.Fatal("idempotent start created a second login")
	}
	foreign := request("session-two", http.MethodGet, path, nil)
	if foreign.Code != 404 {
		t.Fatal("login state leaked across sessions")
	}
	parsed, _ := url.Parse(view.AuthorizationURL)
	callback := parsed.Query().Get("redirect_uri") + "?" + url.Values{"code": {"private-code-sentinel"}, "state": {parsed.Query().Get("state")}}.Encode()
	foreign = request("session-two", http.MethodPost, path+"/callback", map[string]string{"callbackUrl": callback})
	if foreign.Code != 404 || calls.Load() != 0 {
		t.Fatal("another session completed a login")
	}
	completed := request("session-one", http.MethodPost, path+"/callback", map[string]string{"callbackUrl": callback})
	if completed.Code != 200 || calls.Load() != 1 {
		t.Fatalf("complete status=%d calls=%d", completed.Code, calls.Load())
	}
	account := request("session-one", http.MethodGet, "/api/v1/provider-accounts/account.codex.login", nil)
	if account.Code != 200 {
		t.Fatalf("account not saved: %s", account.Body)
	}
	var result desktopcontrol.ProviderAccountResponse
	if err := json.Unmarshal(account.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.LinkedEndpointIDs) != 0 || result.CredentialEpoch != 1 || result.CodexOAuth == nil || result.CodexOAuth.Email != "login@example.com" || result.CodexOAuth.ChatGPTAccountID != "workspace-login" {
		t.Fatal("saved account lacks independent links or safe OAuth identity")
	}
	for _, w := range []*httptest.ResponseRecorder{completed, account} {
		if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "private-") || strings.Contains(w.Body.String(), token) {
			t.Fatal("OAuth control response exposed credential material")
		}
	}

	t.Run("router binds OAuth before consuming the authorized credential", func(t *testing.T) {
		readToken, writeToken := capability(11), capability(12)
		authenticator, err := desktopcontrol.NewAuthenticator(desktopcontrol.CapabilityGrant{
			ReadToken: readToken, WriteToken: writeToken, ExpiresAt: time.Now().Add(time.Hour),
		}, desktopcontrol.SystemClock{})
		if err != nil {
			t.Fatal(err)
		}
		const authority = "127.0.0.1:43129"
		router, err := desktopcontrol.NewRouter(desktopcontrol.RouterOptions{
			Authority: authority, AllowedOrigins: []string{"vibermate://desktop"},
			Authenticator: authenticator, Application: app, Bootstrap: emptyBootstrap(),
			CLIControl: http.NotFoundHandler(), ManualCaptures: rejectingManualCaptureHandler{},
			DesktopPrincipal: desktopManualPrincipal(t),
		})
		if err != nil {
			t.Fatal(err)
		}
		route := func(method, path, token string, payload any) *httptest.ResponseRecorder {
			body, _ := json.Marshal(payload)
			if payload == nil {
				body = nil
			}
			r := newRequest(method, authority, path, token, body)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("If-Match", "0")
			r.Header.Set("Idempotency-Key", "router-login-control-"+method+path)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if r.Header.Get("Authorization") != "" {
				t.Fatal("router passed its credential to a business handler")
			}
			return w
		}
		created := route(http.MethodPost, desktopcontrol.CodexLoginPath, writeToken, map[string]any{
			"accountId": "account.codex.routed", "displayName": "Routed login",
			"upstreamEndpointId": "target.codex.official", "callbackMode": "manual",
		})
		if created.Code != http.StatusCreated {
			t.Fatalf("authorized login lost its session: status=%d", created.Code)
		}
		var routed codexoauth.LoginView
		decodeResponse(t, created, &routed)
		path := desktopcontrol.CodexLoginPath + "/" + routed.ID
		if route(http.MethodGet, path, readToken, nil).Code != http.StatusUnauthorized {
			t.Fatal("read-only credential could access the authorization link")
		}
		if route(http.MethodGet, path, writeToken, nil).Code != http.StatusOK {
			t.Fatal("write session lost its pending login")
		}
		if route(http.MethodDelete, path, writeToken, nil).Code != http.StatusNoContent {
			t.Fatal("write session could not cancel its login")
		}
	})
}
