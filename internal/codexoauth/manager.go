package codexoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"golang.org/x/sync/singleflight"
)

const (
	TokenURL = "https://auth.openai.com/oauth/token"
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	defaultRefreshTimeout = 20 * time.Second
	refreshWindow         = 5 * time.Minute
	refreshFallbackAge    = 8 * 24 * time.Hour
	maxRefreshBodyBytes   = 1 << 20
)

var (
	ErrRefreshUnavailable = errors.New("Codex OAuth refresh is unavailable")
	ErrReconnectRequired  = errors.New("Codex OAuth account must be connected again")
	ErrManagerClosing     = errors.New("Codex OAuth manager is closing")
)

type HTTPClient interface {
	Do(*http.Request, providerauth.AccountRef) (*http.Response, error)
}

type Clock interface {
	Now() time.Time
}

type Options struct {
	Secrets        secretstore.Store
	Client         HTTPClient
	Clock          Clock
	RefreshTimeout time.Duration
}

type State string

const (
	StateReady             State = "ready"
	StateRefreshDue        State = "refresh_due"
	StateReconnectRequired State = "reconnect_required"
)

type View struct {
	Profile Profile
	State   State
}

type Inspector interface {
	Inspect(
		context.Context,
		secretstore.Reference,
		secretstore.Revision,
	) (View, error)
	InspectAccessToken(context.Context, secretstore.Reference, secretstore.Revision) (Profile, error)
}

type Manager struct {
	secrets        secretstore.Store
	client         HTTPClient
	clock          Clock
	refreshTimeout time.Duration
	refreshes      singleflight.Group

	mu        sync.Mutex
	permanent map[credentialEpochKey]error
	pending   map[credentialEpochKey]*pendingRefresh
	closing   bool
	active    int
	changed   chan struct{}
}

type pendingRefresh struct {
	credential *Credential
	policy     providerauth.HeaderPolicy
}

type credentialEpochKey struct {
	reference string
	revision  secretstore.Revision
}

func (key credentialEpochKey) flightKey() string {
	return key.reference + "#" + strconv.FormatUint(uint64(key.revision), 10)
}

func NewManager(options Options) (*Manager, error) {
	if options.Secrets == nil || options.Client == nil || options.Clock == nil {
		return nil, errors.New("Codex OAuth manager dependencies are incomplete")
	}
	timeout := options.RefreshTimeout
	if timeout == 0 {
		timeout = defaultRefreshTimeout
	}
	if timeout < 0 {
		return nil, errors.New("Codex OAuth refresh timeout is invalid")
	}
	return &Manager{
		secrets: options.Secrets, client: options.Client, clock: options.Clock,
		refreshTimeout: timeout, permanent: make(map[credentialEpochKey]error),
		pending: make(map[credentialEpochKey]*pendingRefresh), changed: make(chan struct{}),
	}, nil
}

func (manager *Manager) Inspect(
	ctx context.Context,
	reference secretstore.Reference,
	revision secretstore.Revision,
) (View, error) {
	if manager == nil || ctx == nil || reference.String() == "" || revision == 0 {
		return View{}, ErrInvalidCredential
	}
	credential, _, err := manager.readCredential(ctx, reference, revision)
	if err != nil {
		return View{}, err
	}
	defer credential.Destroy()
	key := credentialEpochKey{reference: reference.String(), revision: revision}
	manager.forgetOtherPermanentEpochs(key)
	state := StateReady
	if credential.state == StateReconnectRequired || manager.permanentError(key) != nil {
		state = StateReconnectRequired
	} else if manager.hasPending(key) || credential.needsRefresh(manager.clock.Now().UTC()) {
		state = StateRefreshDue
	}
	return View{Profile: credential.Profile(), State: state}, nil
}

// InspectAccessToken decodes display metadata for a manually managed ChatGPT
// Bearer token at one exact credential epoch. It does not validate, refresh or
// otherwise change the credential, and never returns the token itself.
func (manager *Manager) InspectAccessToken(
	ctx context.Context,
	reference secretstore.Reference,
	revision secretstore.Revision,
) (Profile, error) {
	if manager == nil || ctx == nil || reference.String() == "" || revision == 0 || revision > secretstore.MaxRevision {
		return Profile{}, ErrInvalidCredential
	}
	material, err := manager.readMaterial(ctx, reference, revision)
	if err != nil {
		return Profile{}, err
	}
	defer material.Destroy()
	if err := material.HeaderPolicy().ValidateForDriver(providerauth.StaticHeaderDriverRef()); err != nil {
		return Profile{}, err
	}
	token := material.CredentialBytes()
	defer clear(token)
	return accessTokenProfile(token), nil
}

func (manager *Manager) Prepare(
	ctx context.Context,
	driver providerauth.DriverRef,
	reference secretstore.Reference,
	scope providerauth.AccountRef,
	automaticRefresh bool,
) (secretstore.Revision, error) {
	return manager.prepare(ctx, driver, reference, scope, automaticRefresh, false)
}

// Refresh forces a refresh of one credential epoch even if its access token is
// still valid. It shares rotation/singleflight with automatic preparation, but
// never reports a transient failure as a successful manual refresh.
func (manager *Manager) Refresh(
	ctx context.Context,
	driver providerauth.DriverRef,
	reference secretstore.Reference,
	scope providerauth.AccountRef,
) (secretstore.Revision, error) {
	return manager.prepare(ctx, driver, reference, scope, true, true)
}

func (manager *Manager) prepare(
	ctx context.Context,
	driver providerauth.DriverRef,
	reference secretstore.Reference,
	scope providerauth.AccountRef,
	allowRefresh bool,
	force bool,
) (secretstore.Revision, error) {
	revision := secretstore.Revision(scope.CredentialEpoch)
	if manager == nil || ctx == nil || driver != providerauth.CodexOAuthDriverRef() ||
		reference.String() == "" || scope.Validate() != nil {
		return 0, ErrInvalidCredential
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	manager.mu.Lock()
	closing := manager.closing
	manager.mu.Unlock()
	if closing {
		return 0, ErrManagerClosing
	}
	key := credentialEpochKey{reference: reference.String(), revision: revision}
	usableAfterTransientFailure := false
	if !manager.hasPending(key) {
		if !allowRefresh {
			// A concurrent recovery may have committed before ProviderAccount
			// observed its new epoch. Read the authoritative epoch without
			// authorizing another token-endpoint rotation.
			metadata, err := manager.secrets.Inspect(ctx, reference)
			if err != nil {
				return 0, err
			}
			if metadata.Validate() != nil || metadata.State != secretstore.StateConfigured {
				return 0, ErrRefreshUnavailable
			}
			return metadata.Revision, nil
		}
		credential, _, err := manager.readCredential(ctx, reference, revision)
		if err != nil {
			if errors.Is(err, secretstore.ErrRevisionConflict) {
				return manager.currentPreparedRevision(ctx, reference)
			}
			return 0, err
		}
		now := manager.clock.Now().UTC()
		if credential.state == StateReconnectRequired {
			credential.Destroy()
			return 0, ErrReconnectRequired
		}
		needsRefresh := force || credential.needsRefresh(now)
		usableAfterTransientFailure = !force && credential.accessUsableAt(now)
		credential.Destroy()
		if !needsRefresh {
			return revision, nil
		}
	}
	manager.forgetOtherPermanentEpochs(key)
	if err := manager.permanentError(key); err != nil {
		return 0, err
	}
	result := manager.refreshes.DoChan(key.flightKey(), func() (any, error) {
		operation, cancel := context.WithTimeout(context.WithoutCancel(ctx), manager.refreshTimeout)
		defer cancel()
		rotated, refreshErr := manager.refresh(operation, reference, scope, allowRefresh, force)
		if errors.Is(refreshErr, ErrReconnectRequired) {
			manager.rememberPermanent(key, refreshErr)
		}
		return rotated, refreshErr
	})
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case outcome := <-result:
		if outcome.Err != nil {
			if usableAfterTransientFailure && errors.Is(outcome.Err, ErrRefreshUnavailable) && !manager.hasPending(key) {
				return revision, nil
			}
			return 0, outcome.Err
		}
		rotated, ok := outcome.Val.(secretstore.Revision)
		if !ok || rotated == 0 {
			return 0, ErrRefreshUnavailable
		}
		return rotated, nil
	}
}

func (manager *Manager) hasPending(key credentialEpochKey) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.pending[key] != nil
}

func (manager *Manager) refresh(
	ctx context.Context,
	reference secretstore.Reference,
	scope providerauth.AccountRef,
	allowRefresh bool,
	force bool,
) (secretstore.Revision, error) {
	revision := secretstore.Revision(scope.CredentialEpoch)
	key := credentialEpochKey{reference: reference.String(), revision: revision}
	manager.mu.Lock()
	if manager.closing {
		manager.mu.Unlock()
		return 0, ErrManagerClosing
	}
	manager.active++
	pending := manager.pending[key]
	recovering := pending != nil
	if !recovering {
		pending = &pendingRefresh{}
		manager.pending[key] = pending
	}
	manager.mu.Unlock()
	defer func() {
		manager.mu.Lock()
		if manager.pending[key] == pending && pending.credential == nil {
			delete(manager.pending, key)
		}
		manager.mu.Unlock()
		manager.finishRefresh()
	}()
	if recovering {
		prepared, err := manager.persistRefresh(ctx, reference, key, pending)
		if err != nil || !allowRefresh {
			return prepared, err
		}
		// The store may have been locked longer than the new access token's
		// lifetime. Save its unique refresh token first, then let the ordinary
		// preparation path renew the committed epoch if it is due.
		scope.CredentialEpoch = uint64(prepared)
		return manager.prepare(ctx, providerauth.CodexOAuthDriverRef(), reference, scope, true, false)
	}
	credential, policy, err := manager.readCredential(ctx, reference, revision)
	if err != nil {
		if errors.Is(err, secretstore.ErrRevisionConflict) {
			return manager.currentPreparedRevision(ctx, reference)
		}
		return 0, err
	}
	defer credential.Destroy()
	if !force && !credential.needsRefresh(manager.clock.Now().UTC()) {
		return revision, nil
	}
	rotated, err := manager.exchange(ctx, credential, scope)
	if err != nil {
		if errors.Is(err, ErrReconnectRequired) {
			credential.state = StateReconnectRequired
			if _, persistErr := manager.replaceCredential(
				ctx, reference, revision, credential, policy,
			); persistErr != nil {
				if errors.Is(persistErr, secretstore.ErrRevisionConflict) {
					return manager.currentPreparedRevision(ctx, reference)
				}
				return 0, errors.Join(
					err,
					fmt.Errorf("persist Codex OAuth reconnect state: %w", persistErr),
				)
			}
		}
		return 0, err
	}
	// The upstream has consumed the old refresh token. Retain the replacement
	// until its CAS commits; retrying the token endpoint would lose this account.
	manager.mu.Lock()
	if manager.pending[key] != pending {
		manager.mu.Unlock()
		rotated.Destroy()
		return manager.currentPreparedRevision(ctx, reference)
	}
	pending.credential, pending.policy = rotated, policy
	manager.mu.Unlock()
	return manager.persistRefresh(ctx, reference, key, pending)
}

func (manager *Manager) persistRefresh(ctx context.Context, reference secretstore.Reference, key credentialEpochKey, pending *pendingRefresh) (secretstore.Revision, error) {
	manager.mu.Lock()
	if manager.pending[key] != pending {
		manager.mu.Unlock()
		return manager.currentPreparedRevision(ctx, reference)
	}
	// Own the bytes used for I/O so an owner replacement can erase the retained
	// copy concurrently, without keeping the manager lock across storage calls.
	credential := *pending.credential
	credential.idToken = bytes.Clone(credential.idToken)
	credential.accessToken = bytes.Clone(credential.accessToken)
	credential.refreshToken = bytes.Clone(credential.refreshToken)
	policy := pending.policy.Clone()
	manager.mu.Unlock()
	defer credential.Destroy()
	prepared, err := manager.replaceCredential(ctx, reference, key.revision, &credential, policy)
	if err == nil || errors.Is(err, secretstore.ErrRevisionConflict) {
		manager.mu.Lock()
		if manager.pending[key] == pending {
			delete(manager.pending, key)
			pending.credential.Destroy()
		}
		manager.mu.Unlock()
	}
	if errors.Is(err, secretstore.ErrRevisionConflict) {
		return manager.currentPreparedRevision(ctx, reference)
	}
	if err != nil {
		return 0, fmt.Errorf("persist refreshed Codex OAuth credential: %w", err)
	}
	return prepared, nil
}

// Forget discards provider state for credential epochs older than
// supersededBelow, after an explicit credential change committed that epoch.
// State of the committed epoch itself is kept: a rotation that already started
// from it has consumed the provider's refresh token, so dropping its result
// would leave the store holding a token that can never be redeemed again.
// SecretStore CAS still protects against a token exchange already in flight.
func (manager *Manager) Forget(reference secretstore.Reference, supersededBelow secretstore.Revision) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for key, pending := range manager.pending {
		if key.reference == reference.String() && key.revision < supersededBelow {
			pending.credential.Destroy()
			delete(manager.pending, key)
		}
	}
	for key := range manager.permanent {
		if key.reference == reference.String() && key.revision < supersededBelow {
			delete(manager.permanent, key)
		}
	}
}

func (manager *Manager) finishRefresh() {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.active--
	close(manager.changed)
	manager.changed = make(chan struct{})
}

// Shutdown drains token exchanges before retrying any uncommitted storage.
// A failed save remains in memory and is reported, never silently destroyed;
// another call can retry it after storage recovers. No alternate secret file is
// written: process loss while the physical store is unavailable is unrecoverable.
func (manager *Manager) Shutdown(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("Codex OAuth shutdown context is nil")
	}
	manager.mu.Lock()
	manager.closing = true
	for manager.active != 0 {
		changed := manager.changed
		manager.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		manager.mu.Lock()
	}
	manager.active++ // Serialize concurrent shutdown saves with each other.
	pending := maps.Clone(manager.pending)
	manager.mu.Unlock()
	defer manager.finishRefresh()
	var failures []error
	for key, refresh := range pending {
		reference, err := secretstore.ParseReference(key.reference)
		if err == nil {
			_, err = manager.persistRefresh(ctx, reference, key, refresh)
		}
		if err != nil && manager.hasPending(key) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (manager *Manager) replaceCredential(
	ctx context.Context,
	reference secretstore.Reference,
	revision secretstore.Revision,
	credential *Credential,
	policy providerauth.HeaderPolicy,
) (secretstore.Revision, error) {
	encodedCredential, err := credential.MarshalBinary()
	if err != nil {
		return 0, err
	}
	defer clear(encodedCredential)
	setHeaders := make(map[string]string, len(policy.Set))
	for _, assignment := range policy.Set {
		setHeaders[assignment.Name] = assignment.Value
	}
	material, err := providerauth.NewMaterial(
		string(encodedCredential), setHeaders, append([]string(nil), policy.Delete...),
	)
	clearHeaderValues(setHeaders)
	if err != nil {
		return 0, err
	}
	defer material.Destroy()
	encodedMaterial, err := material.MarshalBinary()
	if err != nil {
		return 0, err
	}
	defer clear(encodedMaterial)
	value, err := secretstore.NewValue(encodedMaterial)
	if err != nil {
		return 0, err
	}
	defer value.Destroy()
	metadata, err := manager.secrets.Replace(ctx, secretstore.ReplaceCommand{
		Reference: reference, ExpectedRevision: revision, Value: value,
	})
	if err != nil {
		return 0, err
	}
	if metadata.Validate() != nil || metadata.State != secretstore.StateConfigured {
		return 0, ErrRefreshUnavailable
	}
	return metadata.Revision, nil
}

func (manager *Manager) exchange(
	ctx context.Context,
	credential *Credential,
	scope providerauth.AccountRef,
) (*Credential, error) {
	payload, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     ClientID,
		"refresh_token": string(credential.refreshToken),
	})
	if err != nil {
		return nil, ErrRefreshUnavailable
	}
	defer clear(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, bytes.NewReader(payload))
	if err != nil {
		return nil, ErrRefreshUnavailable
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := manager.client.Do(request, scope)
	if err != nil {
		return nil, fmt.Errorf("%w: token endpoint request failed", ErrRefreshUnavailable)
	}
	if response == nil || response.Body == nil {
		return nil, ErrRefreshUnavailable
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRefreshBodyBytes+1))
	if err != nil || len(body) > maxRefreshBodyBytes {
		clear(body)
		return nil, ErrRefreshUnavailable
	}
	defer clear(body)
	if response.StatusCode < 200 || response.StatusCode > 299 {
		code := refreshProblemCode(body)
		if response.StatusCode == http.StatusUnauthorized ||
			permanentRefreshCode(code) {
			return nil, ErrReconnectRequired
		}
		return nil, fmt.Errorf("%w: token endpoint status %d", ErrRefreshUnavailable, response.StatusCode)
	}
	var result struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(body, &result) != nil || result.AccessToken == "" {
		return nil, ErrRefreshUnavailable
	}
	rotated := &Credential{
		idToken:      bytes.Clone(credential.idToken),
		accessToken:  []byte(result.AccessToken),
		refreshToken: bytes.Clone(credential.refreshToken),
		accountID:    credential.accountID,
		lastRefresh:  manager.clock.Now().UTC(),
		state:        StateReady,
	}
	if result.IDToken != "" {
		clear(rotated.idToken)
		rotated.idToken = []byte(result.IDToken)
	}
	if result.RefreshToken != "" {
		clear(rotated.refreshToken)
		rotated.refreshToken = []byte(result.RefreshToken)
	}
	if err := rotated.validate(); err != nil {
		rotated.Destroy()
		if errors.Is(err, ErrIdentityMismatch) {
			// A successful refresh response that changes account identity cannot
			// be retried safely: the provider may already have consumed and
			// rotated the old refresh token. Freeze this credential epoch in the
			// reconnect state instead of replaying that token on every request.
			return nil, errors.Join(ErrReconnectRequired, ErrIdentityMismatch)
		}
		return nil, ErrRefreshUnavailable
	}
	return rotated, nil
}

func (manager *Manager) readCredential(
	ctx context.Context,
	reference secretstore.Reference,
	revision secretstore.Revision,
) (*Credential, providerauth.HeaderPolicy, error) {
	material, err := manager.readMaterial(ctx, reference, revision)
	if err != nil {
		return nil, providerauth.HeaderPolicy{}, err
	}
	defer material.Destroy()
	credentialBytes := material.CredentialBytes()
	defer clear(credentialBytes)
	credential, err := ParseCredential(credentialBytes)
	if err != nil {
		return nil, providerauth.HeaderPolicy{}, err
	}
	return credential, material.HeaderPolicy(), nil
}

func (manager *Manager) readMaterial(
	ctx context.Context,
	reference secretstore.Reference,
	revision secretstore.Revision,
) (providerauth.Material, error) {
	value, err := manager.secrets.ReadAtRevision(ctx, reference, revision)
	value, err = secretstore.ValidateReaderResult(value, err)
	if err != nil {
		return providerauth.Material{}, err
	}
	defer value.Destroy()
	encoded, err := value.CopyBytes()
	if err != nil {
		return providerauth.Material{}, err
	}
	defer clear(encoded)
	return providerauth.ParseMaterial(encoded)
}

func (manager *Manager) currentPreparedRevision(
	ctx context.Context,
	reference secretstore.Reference,
) (secretstore.Revision, error) {
	metadata, err := manager.secrets.Inspect(ctx, reference)
	if err != nil || metadata.Validate() != nil || metadata.State != secretstore.StateConfigured {
		return 0, ErrRefreshUnavailable
	}
	credential, _, err := manager.readCredential(ctx, reference, metadata.Revision)
	if err != nil {
		return 0, err
	}
	defer credential.Destroy()
	if credential.state == StateReconnectRequired {
		return 0, ErrReconnectRequired
	}
	if credential.needsRefresh(manager.clock.Now().UTC()) {
		return 0, secretstore.ErrRevisionConflict
	}
	return metadata.Revision, nil
}

func (credential *Credential) needsRefresh(now time.Time) bool {
	profile := credential.Profile()
	if !profile.ExpiresAt.IsZero() {
		return !profile.ExpiresAt.After(now.Add(refreshWindow))
	}
	return !credential.lastRefresh.Add(refreshFallbackAge).After(now)
}

func (credential *Credential) accessUsableAt(now time.Time) bool {
	profile := credential.Profile()
	return profile.ExpiresAt.IsZero() || profile.ExpiresAt.After(now)
}

func (manager *Manager) permanentError(key credentialEpochKey) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.permanent[key]
}

func (manager *Manager) rememberPermanent(key credentialEpochKey, err error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for existing := range manager.permanent {
		if existing.reference == key.reference && existing != key {
			delete(manager.permanent, existing)
		}
	}
	manager.permanent[key] = err
}

func (manager *Manager) forgetOtherPermanentEpochs(key credentialEpochKey) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for existing := range manager.permanent {
		if existing.reference == key.reference && existing != key {
			delete(manager.permanent, existing)
		}
	}
}

func permanentRefreshCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "invalid_grant", "refresh_token_expired", "refresh_token_reused", "refresh_token_invalidated":
		return true
	default:
		return false
	}
}

func refreshProblemCode(body []byte) string {
	var problem struct {
		Error json.RawMessage `json:"error"`
		Code  string          `json:"code"`
	}
	if json.Unmarshal(body, &problem) != nil {
		return ""
	}
	var direct string
	if json.Unmarshal(problem.Error, &direct) == nil && strings.TrimSpace(direct) != "" {
		return direct
	}
	var nested struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(problem.Error, &nested) == nil && strings.TrimSpace(nested.Code) != "" {
		return nested.Code
	}
	return problem.Code
}

func clearHeaderValues(headers map[string]string) {
	for name := range headers {
		headers[name] = ""
	}
}
