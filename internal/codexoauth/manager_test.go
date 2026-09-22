package codexoauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func TestImportAuthJSONProducesSafeProfileAndRoundTripsCredential(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	accountID := "account-work"
	accessToken := testJWT(t, map[string]any{
		"exp": now.Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
			"chatgpt_user_id":    "user-42",
			"chatgpt_plan_type":  "pro",
		},
		"email": "engineer@example.com",
	})
	idToken := testJWT(t, map[string]any{
		"exp":   now.Add(24 * time.Hour).Unix(),
		"email": "engineer@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
			"chatgpt_user_id":    "user-42",
			"chatgpt_plan_type":  "pro",
		},
	})
	raw := testAuthJSON(t, idToken, accessToken, "refresh-secret", accountID, now)

	credential, err := ImportAuthJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	profile := credential.Profile()
	if profile.AccountID != accountID || profile.Email != "engineer@example.com" ||
		profile.UserID != "user-42" || profile.PlanType != "pro" ||
		!profile.ExpiresAt.Equal(now.Add(time.Hour)) || !profile.LastRefresh.Equal(now) {
		t.Fatalf("profile = %+v", profile)
	}
	encoded, err := credential.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	if bytes.Contains(encoded, []byte("auth_mode")) || bytes.Contains(encoded, []byte("OPENAI_API_KEY")) {
		t.Fatalf("managed credential retained unrelated auth.json fields: %s", encoded)
	}
	parsed, err := ParseCredential(encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer parsed.Destroy()
	authorization, err := parsed.Authorization()
	if err != nil {
		t.Fatal(err)
	}
	defer authorization.Destroy()
	token := authorization.AccessTokenBytes()
	defer clear(token)
	if string(token) != accessToken || authorization.AccountID() != accountID {
		t.Fatal("round-tripped authorization changed")
	}
}

func TestImportAuthJSONRejectsMismatchedAccountIdentity(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	accessToken := testJWT(t, map[string]any{
		"exp": now.Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "different-account",
		},
	})
	_, err := ImportAuthJSON(testAuthJSON(
		t, accessToken, accessToken, "refresh-secret", "account-work", now,
	))
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("ImportAuthJSON error = %v", err)
	}
}

func TestCredentialProfileSuppressesUnsafeUnverifiedClaims(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	accountID := "account-work"
	accessToken := testJWT(t, map[string]any{
		"exp":   int64(253402300800),
		"email": "unsafe\n@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
			"chatgpt_user_id":    strings.Repeat("u", maxIdentityBytes+1),
			"chatgpt_plan_type":  "pro",
		},
	})
	idToken := testJWT(t, map[string]any{
		"email": "safe@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
		},
	})
	credential, err := ImportAuthJSON(testAuthJSON(
		t, idToken, accessToken, "refresh-secret", accountID, now,
	))
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	profile := credential.Profile()
	if profile.Email != "safe@example.com" || profile.UserID != "" ||
		profile.PlanType != "pro" || !profile.ExpiresAt.IsZero() {
		t.Fatalf("safe profile = %+v", profile)
	}
}

func TestManagerPrepareRefreshesNearExpiryAndAtomicallyRotatesMaterial(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	accountID := "account-work"
	oldAccess := testJWT(t, map[string]any{
		"exp":                         now.Add(4 * time.Minute).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	})
	newAccess := testJWT(t, map[string]any{
		"exp": now.Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
			"chatgpt_plan_type":  "team",
		},
	})
	credential, err := ImportAuthJSON(testAuthJSON(
		t, oldAccess, oldAccess, "refresh-old", accountID, now.Add(-time.Hour),
	))
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	store := newMemoryStore(t, credential, map[string]string{"User-Agent": "managed-agent"})
	client := &recordingClient{response: httpResponse(t, http.StatusOK, map[string]any{
		"access_token":  newAccess,
		"refresh_token": "refresh-new",
	})}
	manager, err := NewManager(Options{
		Secrets: store,
		Client:  client,
		Clock:   fixedClock{now: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	reference := testReference(t)
	revision, err := manager.Prepare(
		context.Background(), providerauth.CodexOAuthDriverRef(), reference, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if revision != 2 || client.Calls() != 1 {
		t.Fatalf("Prepare revision=%d calls=%d", revision, client.Calls())
	}
	request := client.Request()
	if request == nil || request.Method != http.MethodPost || request.URL.String() != TokenURL ||
		request.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("refresh request = %+v", request)
	}
	requestBody, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(requestBody)
	var payload map[string]string
	if json.Unmarshal(requestBody, &payload) != nil ||
		payload["grant_type"] != "refresh_token" ||
		payload["client_id"] != ClientID || payload["refresh_token"] != "refresh-old" ||
		len(payload) != 3 {
		t.Fatalf("refresh payload = %#v", payload)
	}

	stored, err := store.ReadAtRevision(context.Background(), reference, revision)
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Destroy()
	materialBytes, err := stored.CopyBytes()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(materialBytes)
	material, err := providerauth.ParseMaterial(materialBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	policy := material.HeaderPolicy()
	if len(policy.Set) != 1 || policy.Set[0].Name != "User-Agent" ||
		policy.Set[0].Value != "managed-agent" {
		t.Fatalf("rotated header policy = %+v", policy)
	}
	credentialBytes := material.CredentialBytes()
	defer clear(credentialBytes)
	rotated, err := ParseCredential(credentialBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer rotated.Destroy()
	authorization, err := rotated.Authorization()
	if err != nil {
		t.Fatal(err)
	}
	defer authorization.Destroy()
	token := authorization.AccessTokenBytes()
	defer clear(token)
	if string(token) != newAccess || authorization.AccountID() != accountID ||
		rotated.Profile().PlanType != "team" ||
		!rotated.Profile().LastRefresh.Equal(now) {
		t.Fatalf("rotated profile = %+v", rotated.Profile())
	}
}

func TestManagerPrepareCoalescesConcurrentAutomaticAndManualRefreshes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	accountID := "account-work"
	oldAccess := testJWT(t, map[string]any{
		"exp":                         now.Add(time.Minute).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	})
	newAccess := testJWT(t, map[string]any{
		"exp":                         now.Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	})
	credential, err := ImportAuthJSON(testAuthJSON(
		t, oldAccess, oldAccess, "refresh-old", accountID, now.Add(-time.Hour),
	))
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	store := newMemoryStore(t, credential, nil)
	client := &recordingClient{
		response: httpResponse(t, http.StatusOK, map[string]any{"access_token": newAccess}),
		gate:     make(chan struct{}),
		started:  make(chan struct{}),
	}
	manager, err := NewManager(Options{Secrets: store, Client: client, Clock: fixedClock{now: now}})
	if err != nil {
		t.Fatal(err)
	}
	reference := testReference(t)
	results := make(chan struct {
		revision secretstore.Revision
		err      error
	}, 2)
	for index := range 2 {
		go func() {
			prepare := manager.Prepare
			if index == 1 {
				prepare = manager.Refresh
			}
			revision, prepareErr := prepare(
				context.Background(), providerauth.CodexOAuthDriverRef(), reference, 1,
			)
			results <- struct {
				revision secretstore.Revision
				err      error
			}{revision: revision, err: prepareErr}
		}()
	}
	<-client.started
	close(client.gate)
	for range 2 {
		result := <-results
		if result.err != nil || result.revision != 2 {
			t.Fatalf("concurrent Prepare = revision %d err %v", result.revision, result.err)
		}
	}
	if client.Calls() != 1 {
		t.Fatalf("refresh calls = %d", client.Calls())
	}
}

func TestManagerPrepareCachesPermanentRefreshFailureForCredentialEpoch(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	accountID := "account-work"
	access := testJWT(t, map[string]any{
		"exp":                         now.Add(time.Minute).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	})
	credential, err := ImportAuthJSON(testAuthJSON(
		t, access, access, "refresh-old", accountID, now.Add(-time.Hour),
	))
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	store := newMemoryStore(t, credential, nil)
	client := &recordingClient{response: httpResponse(t, http.StatusBadRequest, map[string]any{
		"error":             "invalid_grant",
		"error_description": "refresh token is no longer valid",
	})}
	manager, err := NewManager(Options{Secrets: store, Client: client, Clock: fixedClock{now: now}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, prepareErr := manager.Prepare(
			context.Background(), providerauth.CodexOAuthDriverRef(), testReference(t), 1,
		)
		if !errors.Is(prepareErr, ErrReconnectRequired) ||
			strings.Contains(prepareErr.Error(), "refresh-old") {
			t.Fatalf("Prepare error = %v", prepareErr)
		}
	}
	if client.Calls() != 1 {
		t.Fatalf("permanent refresh calls = %d", client.Calls())
	}
}

func TestManagerPrepareRecognizesNestedPermanentRefreshFailure(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	accountID := "account-work"
	access := testJWT(t, map[string]any{
		"exp":                         now.Add(time.Minute).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	})
	credential, err := ImportAuthJSON(testAuthJSON(
		t, access, access, "refresh-old", accountID, now.Add(-time.Hour),
	))
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	store := newMemoryStore(t, credential, nil)
	client := &recordingClient{response: httpResponse(t, http.StatusBadRequest, map[string]any{
		"error": map[string]any{
			"code":    "REFRESH_TOKEN_INVALIDATED",
			"message": "the imported token was revoked",
		},
	})}
	manager, err := NewManager(Options{Secrets: store, Client: client, Clock: fixedClock{now: now}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Prepare(
		context.Background(), providerauth.CodexOAuthDriverRef(), testReference(t), 1,
	)
	if !errors.Is(err, ErrReconnectRequired) || client.Calls() != 1 ||
		strings.Contains(err.Error(), "the imported token was revoked") {
		t.Fatalf("Prepare calls=%d err=%v", client.Calls(), err)
	}
}

func TestManagerPrepareTreatsRefreshedAccountIdentityChangeAsPermanent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	oldAccess := testJWT(t, map[string]any{
		"exp": now.Add(time.Minute).Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "account-work",
		},
	})
	otherAccess := testJWT(t, map[string]any{
		"exp": now.Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "account-other",
		},
	})
	credential, err := ImportAuthJSON(testAuthJSON(
		t, oldAccess, oldAccess, "refresh-old", "account-work", now.Add(-time.Hour),
	))
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	store := newMemoryStore(t, credential, nil)
	client := &recordingClient{response: httpResponse(t, http.StatusOK, map[string]any{
		"access_token":  otherAccess,
		"refresh_token": "refresh-consumed",
	})}
	manager, err := NewManager(Options{Secrets: store, Client: client, Clock: fixedClock{now: now}})
	if err != nil {
		t.Fatal(err)
	}
	reference := testReference(t)
	_, firstErr := manager.Prepare(
		context.Background(), providerauth.CodexOAuthDriverRef(), reference, 1,
	)
	if !errors.Is(firstErr, ErrReconnectRequired) ||
		!errors.Is(firstErr, ErrIdentityMismatch) {
		t.Fatalf("first Prepare error = %v", firstErr)
	}
	_, secondErr := manager.Prepare(
		context.Background(), providerauth.CodexOAuthDriverRef(), reference, 1,
	)
	if !errors.Is(secondErr, ErrReconnectRequired) {
		t.Fatalf("second Prepare error = %v", secondErr)
	}
	metadata, err := store.Inspect(context.Background(), reference)
	if err != nil || metadata.Revision != 2 {
		t.Fatalf("persisted reconnect metadata=%+v err=%v", metadata, err)
	}
	restarted, err := NewManager(Options{
		Secrets: store, Client: client, Clock: fixedClock{now: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := restarted.Inspect(context.Background(), reference, metadata.Revision)
	if err != nil || view.State != StateReconnectRequired || client.Calls() != 1 {
		t.Fatalf("Inspect view=%+v calls=%d err=%v", view, client.Calls(), err)
	}
}

func TestManagerPrepareKeepsStillValidAccessTokenAfterTransientRefreshFailure(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	accountID := "account-work"
	access := testJWT(t, map[string]any{
		"exp":                         now.Add(4 * time.Minute).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	})
	credential, err := ImportAuthJSON(testAuthJSON(
		t, access, access, "refresh-old", accountID, now.Add(-time.Hour),
	))
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	store := newMemoryStore(t, credential, nil)
	client := &recordingClient{err: errors.New("temporary network failure")}
	manager, err := NewManager(Options{Secrets: store, Client: client, Clock: fixedClock{now: now}})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := manager.Prepare(
		context.Background(), providerauth.CodexOAuthDriverRef(), testReference(t), 1,
	)
	if err != nil || revision != 1 || client.Calls() != 1 {
		t.Fatalf("Prepare revision=%d calls=%d err=%v", revision, client.Calls(), err)
	}
}

func TestManagerPrepareRefusesExpiredAccessTokenAfterTransientRefreshFailure(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	accountID := "account-work"
	access := testJWT(t, map[string]any{
		"exp":                         now.Add(-time.Second).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	})
	credential, err := ImportAuthJSON(testAuthJSON(
		t, access, access, "refresh-old", accountID, now.Add(-time.Hour),
	))
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	store := newMemoryStore(t, credential, nil)
	client := &recordingClient{err: errors.New("temporary network failure")}
	manager, err := NewManager(Options{Secrets: store, Client: client, Clock: fixedClock{now: now}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Prepare(
		context.Background(), providerauth.CodexOAuthDriverRef(), testReference(t), 1,
	)
	if !errors.Is(err, ErrRefreshUnavailable) || client.Calls() != 1 {
		t.Fatalf("Prepare calls=%d err=%v", client.Calls(), err)
	}
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type recordingClient struct {
	mu       sync.Mutex
	response *http.Response
	request  *http.Request
	calls    int
	gate     chan struct{}
	started  chan struct{}
	err      error
}

func (client *recordingClient) Do(request *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(request.Body)
	request.Body = io.NopCloser(bytes.NewReader(body))
	stored := request.Clone(request.Context())
	stored.Body = io.NopCloser(bytes.NewReader(body))
	client.mu.Lock()
	client.calls++
	client.request = stored
	if client.started != nil && client.calls == 1 {
		close(client.started)
	}
	client.mu.Unlock()
	if client.gate != nil {
		<-client.gate
	}
	if client.err != nil {
		return nil, client.err
	}
	return cloneHTTPResponse(client.response), nil
}

func (client *recordingClient) Calls() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.calls
}

func (client *recordingClient) Request() *http.Request {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.request
}

func httpResponse(t *testing.T, status int, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func cloneHTTPResponse(response *http.Response) *http.Response {
	clone := *response
	body, _ := io.ReadAll(response.Body)
	response.Body = io.NopCloser(bytes.NewReader(body))
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.Header = response.Header.Clone()
	return &clone
}

type memoryStore struct {
	mu       sync.Mutex
	revision secretstore.Revision
	value    []byte
}

func newMemoryStore(
	t *testing.T,
	credential *Credential,
	setHeaders map[string]string,
) *memoryStore {
	t.Helper()
	encoded, err := credential.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	material, err := providerauth.NewMaterial(string(encoded), setHeaders, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	wire, err := material.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return &memoryStore{revision: 1, value: wire}
}

func (store *memoryStore) Read(
	_ context.Context,
	_ secretstore.Reference,
) (*secretstore.Value, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return secretstore.NewValue(store.value)
}

func (store *memoryStore) ReadAtRevision(
	_ context.Context,
	_ secretstore.Reference,
	revision secretstore.Revision,
) (*secretstore.Value, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if revision != store.revision {
		return nil, secretstore.ErrRevisionConflict
	}
	return secretstore.NewValue(store.value)
}

func (store *memoryStore) Inspect(
	_ context.Context,
	_ secretstore.Reference,
) (secretstore.Metadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return secretstore.Metadata{State: secretstore.StateConfigured, Revision: store.revision}, nil
}

func (store *memoryStore) Replace(
	_ context.Context,
	command secretstore.ReplaceCommand,
) (secretstore.Metadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if command.ExpectedRevision != store.revision {
		return secretstore.Metadata{}, secretstore.ErrRevisionConflict
	}
	value, err := command.Value.CopyBytes()
	if err != nil {
		return secretstore.Metadata{}, err
	}
	clear(store.value)
	store.value = value
	store.revision++
	return secretstore.Metadata{State: secretstore.StateConfigured, Revision: store.revision}, nil
}

func (*memoryStore) Delete(context.Context, secretstore.Reference) error { return nil }

func testReference(t *testing.T) secretstore.Reference {
	t.Helper()
	reference, err := secretstore.ParseReference("secret://provider-account/codex-work")
	if err != nil {
		t.Fatal(err)
	}
	return reference
}

func testAuthJSON(
	t *testing.T,
	idToken string,
	accessToken string,
	refreshToken string,
	accountID string,
	lastRefresh time.Time,
) []byte {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      idToken,
			"access_token":  accessToken,
			"refresh_token": refreshToken,
			"account_id":    accountID,
		},
		"last_refresh": lastRefresh.Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	return encode(header) + "." + encode(payload) + ".signature"
}
