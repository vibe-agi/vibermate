package desktopcontrol

import (
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

type rejectingCodexOAuthInspector struct{}

func (rejectingCodexOAuthInspector) InspectAccessToken(context.Context, secretstore.Reference, secretstore.Revision) (codexoauth.Profile, error) {
	panic("unavailable credentials must not be inspected")
}

func (rejectingCodexOAuthInspector) Inspect(
	context.Context,
	secretstore.Reference,
	secretstore.Revision,
) (codexoauth.View, error) {
	panic("unavailable credentials must not be inspected")
}

func TestUnavailableCodexOAuthAccountRemainsListable(t *testing.T) {
	t.Parallel()
	reference, err := secretstore.ParseReference("secret://provider-account/codex-unavailable")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	origin, err := originidentity.ParseProviderOrigin("https://chatgpt.com")
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{codexOAuth: rejectingCodexOAuthInspector{}}
	response, err := handler.providerAccountResponse(context.Background(), provideraccount.View{
		Account: provideraccount.Account{
			ID: "codex-unavailable", DisplayName: "Codex unavailable",
			Origin: origin, AssociationRevision: 1,
			RealmID: "openai.chatgpt",
			Driver:  providerauth.CodexOAuthDriverRef(), SecretRef: reference,
			State: provideraccount.StateActive, Revision: 1,
			CreatedAt: now, UpdatedAt: now,
		},
		Health: provideraccount.Health{
			State: provideraccount.HealthUnavailable, CredentialEpoch: 7,
		},
	})
	if err != nil || response.CredentialState != provideraccount.HealthUnavailable ||
		response.CredentialEpoch != 7 || response.CodexOAuth != nil {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}
