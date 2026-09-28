package desktopcontrol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

type settingsAccountControl struct {
	provideraccount.Controller
	view  provideraccount.View
	calls int
}

func (control *settingsAccountControl) SetSettings(_ context.Context, command provideraccount.SettingsCommand) (provideraccount.View, error) {
	control.calls++
	if command.ID != control.view.Account.ID || command.ExpectedRevision != control.view.Account.SettingsRevision {
		return provideraccount.View{}, provideraccount.ErrRevisionConflict
	}
	control.view.Account.SettingsRevision++
	control.view.Account.EgressProfile = command.EgressProfile
	control.view.Account.AutomaticRefresh = command.AutomaticRefresh
	return control.view, nil
}

type settingsProfileControl struct{ egressprofile.Controller }

func (settingsProfileControl) GetRevision(_ context.Context, id egressprofile.ID, revision egressprofile.Revision) (egressprofile.ProfileRevision, error) {
	if id == egressprofile.DirectID && revision == 1 {
		return egressprofile.Direct(), nil
	}
	return egressprofile.ProfileRevision{}, egressprofile.ErrProfileNotFound
}

func TestAccountSettingsControlRequiresWriteScopeExplicitValuesAndCAS(t *testing.T) {
	origin, _ := originidentity.ParseProviderOrigin("https://api.openai.com")
	ref, _ := secretstore.ParseReference("secret://provider-account/configured")
	now := time.Now().UTC()
	accounts := &settingsAccountControl{view: provideraccount.View{Account: provideraccount.Account{ID: "configured", DisplayName: "Configured", Origin: origin, AssociationRevision: 1, RealmID: "openai.platform", Driver: providerauth.StaticHeaderDriverRef(), SecretRef: ref, State: provideraccount.StateActive, Revision: 1, SettingsRevision: 1, CreatedAt: now, UpdatedAt: now}, Health: provideraccount.Health{State: provideraccount.HealthReady, CredentialEpoch: 5}}}
	handler := &Handler{accounts: accounts, egressProfiles: settingsProfileControl{}, idempotent: newIdempotencyCache()}
	call := func(key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/api/v1/provider-accounts/configured/settings", strings.NewReader(body))
		r.SetPathValue("accountId", "configured")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("If-Match", "1")
		r.Header.Set("Idempotency-Key", "account-"+key)
		if handler.RequiredScope(r) != ScopeWrite {
			t.Fatal("settings did not require write scope")
		}
		w := httptest.NewRecorder()
		handler.setProviderAccountSettings(w, r)
		return w
	}
	body := `{"automaticRefresh":false,"egressProfile":{"id":"profile.direct","revision":1}}`
	first := call("settings-save", body)
	replay := call("settings-save", body)
	if first.Code != http.StatusOK || first.Body.String() != replay.Body.String() || accounts.calls != 1 {
		t.Fatalf("save/replay: %d %d %s", first.Code, accounts.calls, first.Body.String())
	}
	for _, part := range []string{`"settingsRevision":2`, `"automaticRefresh":false`, `"supportsAutomaticRefresh":false`, `"id":"profile.direct"`, `"revision":1`, `"credentialEpoch":5`} {
		if !strings.Contains(first.Body.String(), part) {
			t.Fatalf("missing projection %s", part)
		}
	}
	if first.Header().Get("Cache-Control") != "no-store" || strings.Contains(first.Body.String(), "secret://") {
		t.Fatal("unsafe settings projection")
	}
	if got := call("settings-stale", `{"automaticRefresh":false,"egressProfile":null}`); got.Code != http.StatusConflict {
		t.Fatalf("stale accepted: %d", got.Code)
	}
	for _, body := range []string{`{}`, `{"automaticRefresh":false}`, `{"egressProfile":null}`, `{"automaticRefresh":null,"egressProfile":null}`, `{"automaticRefresh":false,"egressProfile":{}}`, `{"automaticRefresh":false,"egressProfile":{"id":"profile.direct","revision":1,"policy":{}}}`} {
		if got := call("settings-invalid-"+body, body); got.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid accepted: %d %s", got.Code, body)
		}
	}
	if got := call("settings-missing", `{"automaticRefresh":false,"egressProfile":{"id":"profile.deleted","revision":1}}`); got.Code != http.StatusNotFound {
		t.Fatalf("missing silently inherited: %d", got.Code)
	}
}
