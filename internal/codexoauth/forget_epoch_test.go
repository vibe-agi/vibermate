package codexoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/providerauth"
)

// rotatingTokenEndpoint consumes each refresh token once, like the provider,
// and counts any attempt to redeem a token it already consumed.
type rotatingTokenEndpoint struct {
	t        *testing.T
	now      time.Time
	started  chan struct{}
	gate     chan struct{}
	mu       sync.Mutex
	valid    map[string]bool
	consumed map[string]bool
	issued   int
	replays  int
}

func (endpoint *rotatingTokenEndpoint) access() string {
	return testJWT(endpoint.t, map[string]any{
		"exp":                         endpoint.now.Add(4 * time.Minute).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct"},
	})
}

func (endpoint *rotatingTokenEndpoint) Do(request *http.Request, _ providerauth.AccountRef) (*http.Response, error) {
	select {
	case endpoint.started <- struct{}{}:
	default:
	}
	<-endpoint.gate
	body, _ := io.ReadAll(request.Body)
	var form map[string]string
	_ = json.Unmarshal(body, &form)
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	token := form["refresh_token"]
	if !endpoint.valid[token] {
		if endpoint.consumed[token] {
			endpoint.replays++
		}
		payload, _ := json.Marshal(map[string]any{"error": "invalid_grant"})
		return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(payload))}, nil
	}
	delete(endpoint.valid, token)
	endpoint.consumed[token] = true
	endpoint.issued++
	next := fmt.Sprintf("rotated-%d", endpoint.issued)
	endpoint.valid[next] = true
	payload, _ := json.Marshal(map[string]any{"access_token": endpoint.access(), "refresh_token": next})
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(payload))}, nil
}

func (endpoint *rotatingTokenEndpoint) replayCount() int {
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	return endpoint.replays
}

// An Owner replacement commits a new credential epoch and then forgets older
// provider state. A rotation that already started from the new epoch is not
// superseded: dropping its result would leave the store holding a refresh
// token the provider has consumed.
func TestForgetKeepsRotationStartedFromTheCurrentEpoch(t *testing.T) {
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	endpoint := &rotatingTokenEndpoint{
		t: t, now: now, started: make(chan struct{}, 1), gate: make(chan struct{}),
		valid: map[string]bool{"owner-1": true}, consumed: map[string]bool{},
	}
	credential, err := ImportAuthJSON(testAuthJSON(t, endpoint.access(), endpoint.access(), "owner-1", "acct", now.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore(t, credential, nil)
	manager, err := NewManager(Options{Secrets: store, Client: endpoint, Clock: fixedClock{now: now}})
	if err != nil {
		t.Fatal(err)
	}
	reference, scope := testReference(t), testAccountScope()
	current := store.revision
	done := make(chan error, 1)
	go func() {
		_, err := manager.Prepare(context.Background(), providerauth.CodexOAuthDriverRef(), reference, scope, true)
		done <- err
	}()
	<-endpoint.started
	manager.Forget(reference, current)
	close(endpoint.gate)
	if err := <-done; err != nil {
		t.Fatalf("rotation from the current epoch: %v", err)
	}
	// The next lease prepares whatever epoch the store now holds.
	store.mu.Lock()
	scope.CredentialEpoch = uint64(store.revision)
	store.mu.Unlock()
	if _, err := manager.Prepare(context.Background(), providerauth.CodexOAuthDriverRef(), reference, scope, true); err != nil {
		t.Fatalf("prepare after rotation: %v", err)
	}
	if replays := endpoint.replayCount(); replays != 0 {
		t.Fatalf("consumed refresh token replayed %d times", replays)
	}
}

// State from an epoch older than the replacement is superseded and must not
// be committed over the Owner's new credential.
func TestForgetDiscardsRotationFromAnOlderEpoch(t *testing.T) {
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	endpoint := &rotatingTokenEndpoint{
		t: t, now: now, started: make(chan struct{}, 1), gate: make(chan struct{}),
		valid: map[string]bool{"owner-1": true}, consumed: map[string]bool{},
	}
	credential, err := ImportAuthJSON(testAuthJSON(t, endpoint.access(), endpoint.access(), "owner-1", "acct", now.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore(t, credential, nil)
	manager, err := NewManager(Options{Secrets: store, Client: endpoint, Clock: fixedClock{now: now}})
	if err != nil {
		t.Fatal(err)
	}
	reference, scope := testReference(t), testAccountScope()
	superseding := store.revision + 1
	done := make(chan error, 1)
	go func() {
		_, err := manager.Prepare(context.Background(), providerauth.CodexOAuthDriverRef(), reference, scope, true)
		done <- err
	}()
	<-endpoint.started
	manager.Forget(reference, superseding)
	close(endpoint.gate)
	<-done
	manager.mu.Lock()
	remaining := len(manager.pending)
	manager.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("superseded rotation left %d pending credentials", remaining)
	}
}
