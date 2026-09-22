package codexoauth

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func TestAccessTokenProfileProjectsBoundedClaimsWithoutInventingSignInTime(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	claims := map[string]any{
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		"https://api.openai.com/profile": map[string]any{"email": "engineer@example.com"},
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "workspace-42", "chatgpt_user_id": "user-42", "chatgpt_plan_type": "pro",
		},
		"unrelated_secret": "must-not-project",
	}
	profile := accessTokenProfile([]byte(testJWT(t, claims)))
	if profile.Email != "engineer@example.com" || profile.AccountID != "workspace-42" ||
		profile.UserID != "user-42" || profile.PlanType != "pro" ||
		!profile.IssuedAt.Equal(now) || !profile.ExpiresAt.Equal(now.Add(time.Hour)) ||
		!profile.AuthenticatedAt.IsZero() || !profile.LastRefresh.IsZero() {
		t.Fatalf("profile = %+v", profile)
	}
	claims["auth_time"] = now.Add(-24 * time.Hour).Unix()
	profile = accessTokenProfile([]byte(testJWT(t, claims)))
	if !profile.AuthenticatedAt.Equal(now.Add(-24*time.Hour)) || !profile.IssuedAt.Equal(now) {
		t.Fatal("auth_time and iat must remain distinct")
	}

	for _, token := range []string{"opaque-token", "a.b.c", "a..c", testJWT(t, map[string]any{}), strings.Repeat("x", maxTokenBytes+1)} {
		if got := accessTokenProfile([]byte(token)); got != (Profile{}) {
			t.Fatal("unreadable or empty JWT must not synthesize display metadata")
		}
	}
	unsafe := testJWT(t, map[string]any{
		"email": "unsafe\n@example.com", "iat": -1, "auth_time": int64(253402300800), "exp": int64(253402300800),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": " account", "chatgpt_user_id": strings.Repeat("x", maxIdentityBytes+1),
		},
	})
	if got := accessTokenProfile([]byte(unsafe)); got != (Profile{}) {
		t.Fatalf("unsafe claims leaked into profile: %+v", got)
	}
}

func TestOAuthProfileUsesAccessTokenLifetimeAndActualAuthenticationTime(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	access := testJWT(t, map[string]any{"iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	id := testJWT(t, map[string]any{
		"iat": now.Add(-time.Hour).Unix(), "exp": now.Add(24 * time.Hour).Unix(),
		"auth_time": now.Add(-48 * time.Hour).Unix(),
	})
	credential, err := ImportAuthJSON(testAuthJSON(t, id, access, "synthetic-refresh", "workspace-42", now))
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	profile := credential.Profile()
	if !profile.IssuedAt.Equal(now) || !profile.ExpiresAt.Equal(now.Add(time.Hour)) ||
		!profile.AuthenticatedAt.Equal(now.Add(-48*time.Hour)) || !profile.LastRefresh.Equal(now) {
		t.Fatalf("token lifetimes were confused: %+v", profile)
	}
}

func TestInspectAccessTokenIsReadOnlyAndPinnedToCredentialEpoch(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	token := testJWT(t, map[string]any{"email": "expired@example.com", "exp": now.Add(-time.Hour).Unix()})
	material, err := providerauth.NewMaterial(token, map[string]string{"User-Agent": "private-header"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	wire, err := material.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(wire)
	store := &memoryStore{revision: 3, value: bytes.Clone(wire)}
	defer clear(store.value)
	client := &recordingClient{}
	manager, err := NewManager(Options{Secrets: store, Client: client, Clock: fixedClock{now: now}})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := manager.InspectAccessToken(context.Background(), testReference(t), 3)
	if err != nil || profile.Email != "expired@example.com" || !profile.ExpiresAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("profile = %+v, err = %v", profile, err)
	}
	if store.revision != 3 || !bytes.Equal(store.value, wire) || client.Calls() != 0 {
		t.Fatal("displaying an expired manual token must not refresh or change its bytes")
	}
	if _, err := manager.InspectAccessToken(context.Background(), testReference(t), 2); !errors.Is(err, secretstore.ErrRevisionConflict) {
		t.Fatalf("stale epoch returned metadata: %v", err)
	}
}
