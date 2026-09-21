package codexoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
)

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
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
}

type Manager struct {
	secrets        secretstore.Store
	client         HTTPClient
	clock          Clock
	refreshTimeout time.Duration
	refreshes      singleflight.Group

	mu        sync.Mutex
	permanent map[credentialEpochKey]error
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
	} else if credential.needsRefresh(manager.clock.Now().UTC()) {
		state = StateRefreshDue
	}
	return View{Profile: credential.Profile(), State: state}, nil
}

func (manager *Manager) Prepare(
	ctx context.Context,
	driver providerauth.DriverRef,
	reference secretstore.Reference,
	revision secretstore.Revision,
) (secretstore.Revision, error) {
	if manager == nil || ctx == nil || driver != providerauth.CodexOAuthDriverRef() ||
		reference.String() == "" || revision == 0 || revision > secretstore.MaxRevision {
		return 0, ErrInvalidCredential
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
	needsRefresh := credential.needsRefresh(now)
	usableAfterTransientFailure := credential.accessUsableAt(now)
	credential.Destroy()
	if !needsRefresh {
		return revision, nil
	}
	key := credentialEpochKey{reference: reference.String(), revision: revision}
	manager.forgetOtherPermanentEpochs(key)
	if err := manager.permanentError(key); err != nil {
		return 0, err
	}
	result := manager.refreshes.DoChan(key.flightKey(), func() (any, error) {
		operation, cancel := context.WithTimeout(context.WithoutCancel(ctx), manager.refreshTimeout)
		defer cancel()
		rotated, refreshErr := manager.refresh(operation, reference, revision)
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
			if usableAfterTransientFailure && errors.Is(outcome.Err, ErrRefreshUnavailable) {
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

func (manager *Manager) refresh(
	ctx context.Context,
	reference secretstore.Reference,
	revision secretstore.Revision,
) (secretstore.Revision, error) {
	credential, policy, err := manager.readCredential(ctx, reference, revision)
	if err != nil {
		if errors.Is(err, secretstore.ErrRevisionConflict) {
			return manager.currentPreparedRevision(ctx, reference)
		}
		return 0, err
	}
	defer credential.Destroy()
	if !credential.needsRefresh(manager.clock.Now().UTC()) {
		return revision, nil
	}
	rotated, err := manager.exchange(ctx, credential)
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
	defer rotated.Destroy()
	prepared, err := manager.replaceCredential(ctx, reference, revision, rotated, policy)
	if errors.Is(err, secretstore.ErrRevisionConflict) {
		return manager.currentPreparedRevision(ctx, reference)
	}
	if err != nil {
		return 0, fmt.Errorf("persist refreshed Codex OAuth credential: %w", err)
	}
	return prepared, nil
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
	response, err := manager.client.Do(request)
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
	value, err := manager.secrets.ReadAtRevision(ctx, reference, revision)
	value, err = secretstore.ValidateReaderResult(value, err)
	if err != nil {
		return nil, providerauth.HeaderPolicy{}, err
	}
	defer value.Destroy()
	encoded, err := value.CopyBytes()
	if err != nil {
		return nil, providerauth.HeaderPolicy{}, err
	}
	defer clear(encoded)
	material, err := providerauth.ParseMaterial(encoded)
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
