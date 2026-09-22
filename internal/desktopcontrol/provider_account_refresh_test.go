package desktopcontrol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

type refreshAccountControl struct {
	provideraccount.Controller
	view  provideraccount.View
	err   error
	calls int
}

func (control *refreshAccountControl) RefreshCredential(_ context.Context, id provideraccount.ID, epoch uint64) (provideraccount.View, error) {
	control.calls++
	if id != control.view.Account.ID || epoch != 1 {
		return provideraccount.View{}, provideraccount.ErrRevisionConflict
	}
	return control.view, control.err
}

type refreshInspector struct{ codexoauth.Inspector }

func (refreshInspector) Inspect(context.Context, secretstore.Reference, secretstore.Revision) (codexoauth.View, error) {
	return codexoauth.View{State: codexoauth.StateReady, Profile: codexoauth.Profile{AccountID: "workspace", LastRefresh: time.Now().UTC()}}, nil
}

func TestManualCredentialRefreshIsOwnerScopedIdempotentAndBodyless(t *testing.T) {
	origin, _ := originidentity.ParseProviderOrigin("https://chatgpt.com")
	ref, _ := secretstore.ParseReference("secret://provider-account/managed")
	now := time.Now().UTC()
	accounts := &refreshAccountControl{view: provideraccount.View{
		Account: provideraccount.Account{ID: "managed", DisplayName: "Managed", Origin: origin,
			AssociationRevision: 1, RealmID: "realm.chatgpt", Driver: providerauth.CodexOAuthDriverRef(),
			SecretRef: ref, State: provideraccount.StateActive, Revision: 1, CreatedAt: now, UpdatedAt: now},
		Health: provideraccount.Health{State: provideraccount.HealthReady, CredentialEpoch: 2},
	}}
	handler := &Handler{accounts: accounts, codexOAuth: refreshInspector{}, idempotent: newIdempotencyCache()}
	call := func(path, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.SetPathValue("accountId", "managed")
		r.Header.Set("If-Match", "1")
		r.Header.Set("Idempotency-Key", key)
		if handler.RequiredScope(r) != ScopeWrite {
			t.Fatal("refresh admitted read scope")
		}
		w := httptest.NewRecorder()
		handler.refreshProviderAccountCredential(w, r)
		return w
	}
	const path = "/api/v1/provider-accounts/managed/credential/refresh"
	first := call(path, "manual-refresh-idempotency", "")
	second := call(path, "manual-refresh-idempotency", "")
	if first.Code != http.StatusOK || second.Body.String() != first.Body.String() || accounts.calls != 1 {
		t.Fatalf("refresh did not deduplicate: status=%d calls=%d", first.Code, accounts.calls)
	}
	for _, forbidden := range []string{"access_token", "refresh_token", "secret://"} {
		if strings.Contains(first.Body.String(), forbidden) {
			t.Fatal("refresh response exposed a credential")
		}
	}
	if first.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("refresh response is cacheable")
	}
	for _, input := range []struct{ path, body string }{{path + "?url=https://example.invalid", ""}, {path, `{"refreshToken":"nope"}`}} {
		if got := call(input.path, "manual-refresh-invalid", input.body); got.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid refresh admitted: %d", got.Code)
		}
	}
	accounts.err = codexoauth.ErrReconnectRequired
	if got := call(path, "manual-refresh-reconnect", ""); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "credential_reconnect_required") {
		t.Fatal("missing reconnect guidance")
	}
	accounts.err = codexoauth.ErrRefreshUnavailable
	if got := call(path, "manual-refresh-transient", ""); got.Code != http.StatusBadGateway || !strings.Contains(got.Body.String(), "credential_refresh_failed") {
		t.Fatal("transient refresh reported success")
	}
}
