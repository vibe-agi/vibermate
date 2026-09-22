package desktopcontrol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

type noteAccountControl struct {
	provideraccount.Controller
	view  provideraccount.View
	calls int
}

func (control *noteAccountControl) SetNote(_ context.Context, command provideraccount.NoteCommand) (provideraccount.View, error) {
	control.calls++
	if command.ID != control.view.Account.ID || command.ExpectedRevision != control.view.Account.NoteRevision {
		return provideraccount.View{}, provideraccount.ErrRevisionConflict
	}
	control.view.Account.Note = strings.TrimSpace(command.Note)
	control.view.Account.NoteRevision++
	return control.view, nil
}

func TestAccountNoteRequiresWriteScopeAndUsesIndependentCAS(t *testing.T) {
	origin, _ := originidentity.ParseProviderOrigin("https://api.openai.com")
	ref, _ := secretstore.ParseReference("secret://provider-account/noted")
	now := time.Now().UTC()
	accounts := &noteAccountControl{view: provideraccount.View{
		Account: provideraccount.Account{ID: "noted", DisplayName: "Work", Origin: origin, AssociationRevision: 1, RealmID: "openai.platform", Driver: providerauth.StaticHeaderDriverRef(), SecretRef: ref, State: provideraccount.StateActive, Revision: 1, CreatedAt: now, UpdatedAt: now},
		Health:  provideraccount.Health{State: provideraccount.HealthReady, CredentialEpoch: 5},
	}}
	handler := &Handler{accounts: accounts, idempotent: newIdempotencyCache()}
	call := func(key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/api/v1/provider-accounts/noted/note", strings.NewReader(body))
		r.SetPathValue("accountId", "noted")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("If-Match", "0")
		r.Header.Set("Idempotency-Key", key)
		if handler.RequiredScope(r) != ScopeWrite {
			t.Fatal("note update admitted read scope")
		}
		w := httptest.NewRecorder()
		handler.setProviderAccountNote(w, r)
		return w
	}
	first := call("account-note-operation", `{"note":"\u7814\u53d1\u7528"}`)
	replay := call("account-note-operation", `{"note":"\u7814\u53d1\u7528"}`)
	if first.Code != http.StatusOK || first.Body.String() != replay.Body.String() || accounts.calls != 1 {
		t.Fatalf("idempotency: %d %d", first.Code, accounts.calls)
	}
	for _, part := range []string{"\"note\":\"\u7814\u53d1\u7528\"", `"noteRevision":1`, `"revision":1`, `"credentialEpoch":5`} {
		if !strings.Contains(first.Body.String(), part) {
			t.Fatalf("missing projection %s", part)
		}
	}
	if first.Header().Get("Cache-Control") != "no-store" || strings.Contains(first.Body.String(), "secret://") {
		t.Fatal("unsafe note response")
	}
	if got := call("account-note-stale", `{"note":"overwrite"}`); got.Code != http.StatusConflict {
		t.Fatalf("stale note admitted: %d", got.Code)
	}
	for _, body := range []string{`{}`, `{"note":null}`, `{"note":1}`, `{"note":"x","secret":"nope"}`} {
		if got := call("account-note-invalid", body); got.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid note input admitted: %d", got.Code)
		}
	}
}
