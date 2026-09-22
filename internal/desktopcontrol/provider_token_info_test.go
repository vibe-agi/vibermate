package desktopcontrol_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
)

func TestBearerTokenInfoIsReadableWithoutReturningCredentials(t *testing.T) {
	t.Parallel()
	runtime := startRuntime(t)
	defer shutdownRuntime(t, runtime)
	application, err := desktopcontrol.New(desktopcontrol.Options{
		Readiness: readyState(true), Status: runtime,
		Environments: runtime.Environments(), Assignments: runtime.CaptureAssignments(),
		Activities: runtime.Activities(), Contents: runtime.ExchangeContents(), Connections: runtime.ConnectionEvents(),
		Egress: runtime.EgressAttempts(), Approvals: runtime.ToolApprovals(),
		Endpoints: runtime.UpstreamEndpoints(), Accounts: runtime.ProviderAccounts(),
		CodexOAuth: runtime.CodexOAuthAccounts(), Offline: runtime,
		ManualCaptures: runtime.ManualCaptures(), Clock: desktopcontrol.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	token := controlJWT(t, map[string]any{
		"iat": now.Add(-2 * time.Hour).Unix(), "exp": now.Add(-time.Hour).Unix(),
		"https://api.openai.com/profile": map[string]any{"email": "manual@example.com"},
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "workspace-manual", "chatgpt_plan_type": "pro",
		},
		"private_claim": "do-not-project-this-claim",
	})
	body, err := json.Marshal(map[string]any{
		"id": "manual-jwt", "displayName": "Manual JWT", "unlinked": true,
		"upstreamEndpointId": "target.codex.official", "kind": "bearer_token", "secret": token,
		"setHeaders": map[string]string{"User-Agent": "private-user-agent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	created := environmentRequest(t, application, http.MethodPost, "/api/v1/provider-accounts", 0, "manual-jwt-create-0001", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d", created.Code)
	}
	assertInfo := func(body []byte) {
		t.Helper()
		for _, secret := range []string{token, "private-user-agent", "do-not-project-this-claim"} {
			assertProviderAccountResponseSafe(t, body, secret)
		}
		var account desktopcontrol.ProviderAccountResponse
		if err := json.Unmarshal(body, &account); err != nil {
			t.Fatal(err)
		}
		info := account.TokenInfo
		if info == nil || info.Email != "manual@example.com" || info.ChatGPTAccountID != "workspace-manual" ||
			info.PlanType != "pro" || info.AuthenticatedAt != "" ||
			info.IssuedAt != now.Add(-2*time.Hour).Format(time.RFC3339Nano) ||
			info.ExpiresAt != now.Add(-time.Hour).Format(time.RFC3339Nano) ||
			account.Kind != "bearer_token" || account.CodexOAuth != nil || account.CredentialEpoch != 1 {
			t.Fatalf("manual token display metadata = %+v", info)
		}
	}
	assertInfo(created.Body.Bytes())
	got := environmentRequest(t, application, http.MethodGet, "/api/v1/provider-accounts/manual-jwt", 0, "", nil)
	if got.Code != http.StatusOK {
		t.Fatalf("read status=%d", got.Code)
	}
	assertInfo(got.Body.Bytes())
	listed := environmentRequest(t, application, http.MethodGet, "/api/v1/provider-accounts", 0, "", nil)
	var page struct {
		Items []json.RawMessage `json:"items"`
	}
	if listed.Code != http.StatusOK || json.Unmarshal(listed.Body.Bytes(), &page) != nil || len(page.Items) != 1 {
		t.Fatalf("list status=%d items=%d", listed.Code, len(page.Items))
	}
	assertInfo(page.Items[0])

	// Replacing with an opaque token must clear the previous JWT's metadata,
	// while preserving the account's ordinary manual credential lifecycle.
	replaced := environmentRequest(t, application, http.MethodPut, "/api/v1/provider-accounts/manual-jwt/credential", 1,
		"manual-jwt-replace-0001", []byte(`{"secret":"opaque-replacement"}`))
	if replaced.Code != http.StatusOK {
		t.Fatalf("replace status=%d", replaced.Code)
	}
	got = environmentRequest(t, application, http.MethodGet, "/api/v1/provider-accounts/manual-jwt", 0, "", nil)
	if got.Code != http.StatusOK || bytes.Contains(got.Body.Bytes(), []byte(`"tokenInfo"`)) {
		t.Fatal("opaque token retained stale JWT metadata")
	}
	assertProviderAccountResponseSafe(t, got.Body.Bytes(), "opaque-replacement")
}
