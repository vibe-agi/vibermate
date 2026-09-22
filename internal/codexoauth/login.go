package codexoauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const AuthorizationURL = "https://auth.openai.com/oauth/authorize"

var (
	ErrLoginInvalid  = errors.New("Codex login input is invalid")
	ErrLoginNotFound = errors.New("Codex login is unavailable or expired")
	ErrLoginBusy     = errors.New("Codex login is already completing")
	ErrLoginCapacity = errors.New("Too many pending Codex logins")
)

// LoginAccount is frozen before authorization. Browser callbacks cannot choose
// the account destination or send credentials to a different provider origin.
type LoginAccount struct {
	ID          string
	EndpointID  string
	DisplayName string
}

type LoginView struct {
	ID               string    `json:"id"`
	State            string    `json:"state"`
	CallbackMode     string    `json:"callbackMode"`
	AuthorizationURL string    `json:"authorizationUrl"`
	ExpiresAt        time.Time `json:"expiresAt"`
	AccountID        string    `json:"accountId,omitempty"`
	Reason           string    `json:"reason,omitempty"`
}

type LoginController interface {
	Start(context.Context, string, LoginAccount, string) (LoginView, error)
	Status(context.Context, string, string) (LoginView, error)
	Complete(context.Context, string, string, string) (LoginView, error)
	Cancel(context.Context, string, string) error
}

type LoginOptions struct {
	Client   HTTPClient
	Clock    Clock
	Persist  func(context.Context, LoginAccount, *Credential) (string, error)
	Lifetime time.Duration
}

// LoginManager owns short-lived PKCE transactions, including loopback listeners.
// Its interface never returns codes, verifiers, or tokens. The trusted Persist
// adapter commits a complete credential through the existing account authority.
type LoginManager struct {
	mu      sync.Mutex
	options LoginOptions
	logins  map[string]*login
	closed  bool
	active  sync.WaitGroup
}

type login struct {
	view     LoginView
	owner    string
	account  LoginAccount
	state    string
	verifier []byte
	redirect string
	server   *http.Server
	timer    *time.Timer
	cancel   context.CancelFunc
}

func NewLoginManager(options LoginOptions) (*LoginManager, error) {
	if options.Client == nil || options.Clock == nil || options.Persist == nil {
		return nil, ErrLoginInvalid
	}
	if options.Lifetime == 0 {
		options.Lifetime = 15 * time.Minute
	}
	if options.Lifetime <= 0 || options.Lifetime > 15*time.Minute {
		return nil, ErrLoginInvalid
	}
	return &LoginManager{options: options, logins: make(map[string]*login)}, nil
}

func (manager *LoginManager) Start(ctx context.Context, owner string, account LoginAccount, mode string) (LoginView, error) {
	if ctx == nil || ctx.Err() != nil || owner == "" || account.ID == "" || account.EndpointID == "" ||
		(mode != "loopback" && mode != "manual") {
		return LoginView{}, ErrLoginInvalid
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return LoginView{}, ErrLoginNotFound
	}
	manager.pruneLocked()
	if len(manager.logins) >= 32 {
		return LoginView{}, ErrLoginCapacity
	}
	id, err := randomLoginValue(32)
	if err != nil {
		return LoginView{}, ErrLoginInvalid
	}
	state, err := randomLoginValue(32)
	if err != nil {
		return LoginView{}, ErrLoginInvalid
	}
	verifier, err := randomLoginValue(64)
	if err != nil {
		return LoginView{}, ErrLoginInvalid
	}
	pending := &login{owner: owner, account: account, state: state, verifier: []byte(verifier)}
	pending.view = LoginView{ID: id, State: "pending", CallbackMode: "manual", ExpiresAt: manager.options.Clock.Now().UTC().Add(manager.options.Lifetime)}
	pending.redirect = "http://localhost:1455/auth/callback"
	if mode == "loopback" {
		for _, port := range []int{1455, 1457} {
			listener, listenErr := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
			if listenErr != nil {
				continue
			}
			pending.redirect = "http://localhost:" + strconv.Itoa(port) + "/auth/callback"
			pending.view.CallbackMode = "loopback"
			pending.server = &http.Server{
				ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
				WriteTimeout: 30 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10,
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					manager.callback(w, r, id, owner, pending.redirect)
				}),
			}
			go func() { _ = pending.server.Serve(listener) }()
			break
		}
	}
	challenge := sha256.Sum256([]byte(verifier))
	params := url.Values{
		"response_type": {"code"}, "client_id": {ClientID}, "redirect_uri": {pending.redirect},
		"scope": {"openid profile email offline_access"}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
		"id_token_add_organizations": {"true"}, "codex_cli_simplified_flow": {"true"},
		"originator": {"vibermate"}, "prompt": {"login"},
	}
	pending.view.AuthorizationURL = AuthorizationURL + "?" + params.Encode()
	manager.logins[id] = pending
	pending.timer = time.AfterFunc(manager.options.Lifetime, func() {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		if manager.logins[id] != pending {
			return
		}
		if pending.view.State == "pending" {
			manager.finishLocked(pending, "expired", "login_expired")
		}
	})
	return pending.view, nil
}

func (manager *LoginManager) Status(ctx context.Context, owner, id string) (LoginView, error) {
	if ctx == nil || ctx.Err() != nil {
		return LoginView{}, ErrLoginInvalid
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	pending, err := manager.lookupLocked(owner, id)
	if err != nil {
		return LoginView{}, err
	}
	return pending.view, nil
}

func (manager *LoginManager) Complete(ctx context.Context, owner, id, callback string) (LoginView, error) {
	if ctx == nil || ctx.Err() != nil || len(callback) > 16<<10 {
		return LoginView{}, ErrLoginInvalid
	}
	manager.mu.Lock()
	pending, err := manager.lookupLocked(owner, id)
	if err != nil {
		manager.mu.Unlock()
		return LoginView{}, err
	}
	if pending.view.State != "pending" {
		view := pending.view
		manager.mu.Unlock()
		if view.State == "exchanging" {
			return LoginView{}, ErrLoginBusy
		}
		return view, nil
	}
	parsed, err := url.Parse(callback)
	if err != nil || parsed.User != nil || parsed.Fragment != "" || parsed.RawPath != "" ||
		parsed.Scheme+"://"+parsed.Host+parsed.Path != pending.redirect {
		manager.mu.Unlock()
		return LoginView{}, ErrLoginInvalid
	}
	params, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(params["state"]) != 1 || len(params["code"]) > 1 || len(params["error"]) > 1 ||
		subtle.ConstantTimeCompare([]byte(params.Get("state")), []byte(pending.state)) != 1 {
		manager.mu.Unlock()
		return LoginView{}, ErrLoginInvalid
	}
	if params.Get("error") != "" {
		manager.finishLocked(pending, "failed", "login_denied")
		view := pending.view
		manager.mu.Unlock()
		return view, nil
	}
	code := params.Get("code")
	if !validToken([]byte(code)) {
		manager.mu.Unlock()
		return LoginView{}, ErrLoginInvalid
	}
	pending.view.State = "exchanging"
	pending.view.AuthorizationURL = ""
	pending.timer.Stop()
	// A browser closing its callback page must not lose an exchanged grant.
	operation, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	pending.cancel = cancel
	verifier := bytes.Clone(pending.verifier)
	clear(pending.verifier)
	pending.verifier = nil
	pending.state = ""
	manager.active.Add(1)
	manager.mu.Unlock()
	defer manager.active.Done()
	defer cancel()
	defer clear(verifier)
	credential, exchangeErr := manager.exchangeCode(operation, code, verifier, pending.redirect)
	accountID := ""
	reason := ""
	if exchangeErr != nil {
		reason = "login_exchange_failed"
	} else {
		defer credential.Destroy()
		accountID, err = manager.options.Persist(operation, pending.account, credential)
		if err != nil || accountID == "" {
			reason = "login_account_save_failed"
		}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if reason != "" {
		manager.finishLocked(pending, "failed", reason)
	} else {
		pending.view.AccountID = accountID
		manager.finishLocked(pending, "completed", "")
	}
	return pending.view, nil
}

func (manager *LoginManager) Cancel(ctx context.Context, owner, id string) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrLoginInvalid
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	pending, err := manager.lookupLocked(owner, id)
	if err != nil {
		return err
	}
	if pending.view.State == "exchanging" {
		return ErrLoginBusy
	}
	if pending.view.State == "pending" {
		manager.finishLocked(pending, "cancelled", "")
	}
	return nil
}

func (manager *LoginManager) lookupLocked(owner, id string) (*login, error) {
	if manager.closed || owner == "" {
		return nil, ErrLoginNotFound
	}
	manager.pruneLocked()
	pending := manager.logins[id]
	if pending == nil || pending.owner != owner {
		return nil, ErrLoginNotFound
	}
	if pending.view.State == "pending" && !manager.options.Clock.Now().Before(pending.view.ExpiresAt) {
		manager.finishLocked(pending, "expired", "login_expired")
	}
	return pending, nil
}

func (manager *LoginManager) pruneLocked() {
	now := manager.options.Clock.Now()
	for id, pending := range manager.logins {
		if pending.view.State != "exchanging" && now.After(pending.view.ExpiresAt.Add(5*time.Minute)) {
			manager.clearLocked(pending)
			delete(manager.logins, id)
		}
	}
}

func (manager *LoginManager) finishLocked(pending *login, state, reason string) {
	pending.view.State = state
	pending.view.Reason = reason
	pending.view.AuthorizationURL = ""
	manager.clearLocked(pending)
}

func (manager *LoginManager) clearLocked(pending *login) {
	clear(pending.verifier)
	pending.verifier = nil
	pending.state = ""
	if pending.timer != nil {
		pending.timer.Stop()
	}
	if pending.cancel != nil {
		pending.cancel()
	}
	if pending.server != nil {
		server := pending.server
		// Graceful shutdown lets the active callback send its static response.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if server.Shutdown(ctx) != nil {
				_ = server.Close()
			}
		}()
	}
}

func (manager *LoginManager) Shutdown(ctx context.Context) error {
	manager.mu.Lock()
	manager.closed = true
	for _, pending := range manager.logins {
		manager.clearLocked(pending)
	}
	clear(manager.logins)
	manager.mu.Unlock()
	done := make(chan struct{})
	go func() { manager.active.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *LoginManager) callback(w http.ResponseWriter, r *http.Request, id, owner, redirect string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	expected, _ := url.Parse(redirect)
	if r.Method != http.MethodGet || r.Host != expected.Host || r.URL.Path != expected.Path || r.URL.RawPath != "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "Invalid login callback.")
		return
	}
	view, err := manager.Complete(r.Context(), owner, id, redirect+"?"+r.URL.RawQuery)
	if err != nil || view.State != "completed" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "Login did not complete. Return to ViberMate to retry.")
		return
	}
	_, _ = io.WriteString(w, "Codex account saved. You can close this page and return to ViberMate.")
}

func (manager *LoginManager) exchangeCode(ctx context.Context, code string, verifier []byte, redirect string) (*Credential, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {ClientID}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {string(verifier)}}
	body := []byte(form.Encode())
	defer clear(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, ErrLoginInvalid
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := manager.options.Client.Do(request)
	if err != nil || response == nil {
		return nil, ErrLoginInvalid
	}
	if response.Body == nil {
		return nil, ErrLoginInvalid
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrLoginInvalid
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxCredentialBytes+1))
	defer clear(encoded)
	if err != nil || len(encoded) > maxCredentialBytes {
		return nil, ErrLoginInvalid
	}
	var tokens struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(encoded, &tokens) != nil {
		return nil, ErrLoginInvalid
	}
	credential := &Credential{idToken: []byte(tokens.IDToken), accessToken: []byte(tokens.AccessToken), refreshToken: []byte(tokens.RefreshToken), lastRefresh: manager.options.Clock.Now().UTC(), state: StateReady}
	if claims, ok := parseJWTClaims(credential.idToken); ok {
		credential.accountID = claims.Auth.AccountID
	}
	if credential.validate() != nil {
		credential.Destroy()
		return nil, ErrLoginInvalid
	}
	return credential, nil
}

func randomLoginValue(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	defer clear(value)
	return strings.TrimRight(base64.URLEncoding.EncodeToString(value), "="), nil
}
