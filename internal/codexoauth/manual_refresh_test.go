package codexoauth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/providerauth"
)

func TestManualRefreshRotatesAStillValidCredentialAndPreservesPolicy(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC)
	access := testJWT(t, map[string]any{"exp": now.Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace"}})
	credential, err := ImportAuthJSON(testAuthJSON(t, access, access, "refresh-before", "workspace", now.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	store := newMemoryStore(t, credential, map[string]string{"User-Agent": "managed-agent"})
	client := &recordingClient{response: httpResponse(t, http.StatusOK, map[string]any{
		"access_token": access, "refresh_token": "refresh-after",
	})}
	manager, err := NewManager(Options{Secrets: store, Client: client, Clock: fixedClock{now: now}})
	if err != nil {
		t.Fatal(err)
	}
	ref := testReference(t)
	if epoch, err := manager.Prepare(context.Background(), providerauth.CodexOAuthDriverRef(), ref, 1); err != nil || epoch != 1 || client.Calls() != 0 {
		t.Fatalf("automatic preparation unexpectedly refreshed: epoch=%d error=%v", epoch, err)
	}
	for range 2 { // A stale caller must observe the winner, never replay its token.
		epoch, err := manager.Refresh(context.Background(), providerauth.CodexOAuthDriverRef(), ref, 1)
		if err != nil || epoch != 2 {
			t.Fatalf("manual refresh epoch=%d error=%v", epoch, err)
		}
	}
	if client.Calls() != 1 {
		t.Fatalf("refresh exchanges=%d", client.Calls())
	}
	rotated, policy, err := manager.readCredential(context.Background(), ref, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer rotated.Destroy()
	if string(rotated.refreshToken) != "refresh-after" || !rotated.lastRefresh.Equal(now) || len(policy.Set) != 1 || policy.Set[0].Value != "managed-agent" {
		t.Fatal("rotation lost the refresh token, timestamp or overwrite policy")
	}
}

func TestManualRefreshDoesNotReportTransientFailureAsSuccess(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC)
	access := testJWT(t, map[string]any{"exp": now.Add(4 * time.Minute).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace"}})
	credential, err := ImportAuthJSON(testAuthJSON(t, access, access, "refresh-before", "workspace", now.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	store := newMemoryStore(t, credential, nil)
	client := &recordingClient{err: errors.New("synthetic network failure")}
	manager, err := NewManager(Options{Secrets: store, Client: client, Clock: fixedClock{now: now}})
	if err != nil {
		t.Fatal(err)
	}
	ref := testReference(t)
	if epoch, err := manager.Prepare(context.Background(), providerauth.CodexOAuthDriverRef(), ref, 1); err != nil || epoch != 1 {
		t.Fatalf("valid access token should survive automatic transient failure: %d %v", epoch, err)
	}
	if _, err := manager.Refresh(context.Background(), providerauth.CodexOAuthDriverRef(), ref, 1); !errors.Is(err, ErrRefreshUnavailable) {
		t.Fatalf("manual failure incorrectly swallowed: %v", err)
	}
	if _, _, err := manager.readCredential(context.Background(), ref, 1); err != nil {
		t.Fatal("transient failure changed saved credential")
	}
}
