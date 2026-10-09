package productruntime

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type codexOAuthHTTPClient struct {
	provider providerRuntime
}

func persistCodexLogin(accounts provideraccount.Controller, secrets secretstore.Store) func(context.Context, codexoauth.LoginAccount, *codexoauth.Credential) (string, error) {
	return func(ctx context.Context, target codexoauth.LoginAccount, credential *codexoauth.Credential) (string, error) {
		id, err := provideraccount.NewID(target.ID)
		if err != nil {
			return "", err
		}
		var policy providerauth.HeaderPolicy
		if target.Mode == "reauthorize" {
			frozen := target.Reauthorization
			if frozen == nil || secrets == nil || frozen.CredentialEpoch == 0 || frozen.CreatedAt.IsZero() || frozen.SecretRef.String() == "" {
				return "", codexoauth.ErrLoginAccountChanged
			}
			profile := credential.Profile()
			if profile.AccountID != frozen.Profile.AccountID || frozen.Profile.UserID != "" && profile.UserID != frozen.Profile.UserID {
				return "", codexoauth.ErrIdentityMismatch
			}
			current, err := accounts.Get(ctx, id)
			if err != nil {
				if errors.Is(err, provideraccount.ErrAccountNotFound) {
					return "", codexoauth.ErrLoginAccountChanged
				}
				return "", err
			}
			if !current.Account.CreatedAt.Equal(frozen.CreatedAt) || current.Account.SecretRef != frozen.SecretRef ||
				current.Account.Driver != providerauth.CodexOAuthDriverRef() || current.Account.RealmID != frozen.Scope.RealmID ||
				!upstreamendpoint.IsChatGPTCodexOrigin(current.Account.Origin) ||
				(current.Account.State != provideraccount.StateDisabled && current.Health.CredentialEpoch != frozen.CredentialEpoch) {
				return "", codexoauth.ErrLoginAccountChanged
			}
			old, err := secretstore.ValidateReaderResult(secrets.ReadAtRevision(ctx, frozen.SecretRef, secretstore.Revision(frozen.CredentialEpoch)))
			if err != nil {
				if errors.Is(err, secretstore.ErrRevisionConflict) || errors.Is(err, secretstore.ErrNotFound) {
					return "", codexoauth.ErrLoginAccountChanged
				}
				return "", err
			}
			defer old.Destroy()
			oldBytes, err := old.CopyBytes()
			if err != nil {
				return "", err
			}
			defer clear(oldBytes)
			oldMaterial, err := providerauth.ParseMaterial(oldBytes)
			if err != nil {
				return "", err
			}
			defer oldMaterial.Destroy()
			policy = oldMaterial.HeaderPolicy()
		}
		encoded, err := credential.MarshalBinary()
		if err != nil {
			return "", err
		}
		defer clear(encoded)
		setHeaders := make(map[string]string, len(policy.Set))
		for _, assignment := range policy.Set {
			setHeaders[assignment.Name] = assignment.Value
		}
		defer clear(setHeaders)
		defer clear(policy.Set)
		defer clear(policy.Delete)
		material, err := providerauth.NewMaterial(string(encoded), setHeaders, policy.Delete)
		if err != nil {
			return "", err
		}
		defer material.Destroy()
		payload, err := material.MarshalBinary()
		if err != nil {
			return "", err
		}
		defer clear(payload)
		value, err := secretstore.NewValue(payload)
		if err != nil {
			return "", err
		}
		defer value.Destroy()
		if target.Mode == "reauthorize" {
			frozen := target.Reauthorization
			view, err := accounts.ReplaceSecret(ctx, provideraccount.ReplaceSecretCommand{ID: id,
				ExpectedCredentialEpoch: frozen.CredentialEpoch, Secret: value,
				Precondition: &provideraccount.ReplacementPrecondition{CreatedAt: frozen.CreatedAt, SecretRef: frozen.SecretRef}})
			if errors.Is(err, provideraccount.ErrAccountNotFound) || errors.Is(err, provideraccount.ErrRevisionConflict) || errors.Is(err, provideraccount.ErrOperationInProgress) {
				return "", codexoauth.ErrLoginAccountChanged
			}
			if err != nil {
				return "", err
			}
			return view.Account.ID.String(), nil
		}
		endpointID, err := upstreamendpoint.NewID(target.EndpointID)
		if err != nil {
			return "", err
		}
		name := strings.TrimSpace(target.DisplayName)
		if name == "" {
			name = credential.Profile().Email
			if name == "" || len(name) > 256 {
				name = "Codex"
			}
		}
		view, err := accounts.Create(ctx, provideraccount.CreateCommand{
			ID: id, DisplayName: name, UpstreamEndpointID: endpointID, Unlinked: true,
			Driver: providerauth.CodexOAuthDriverRef(), Secret: value,
			AutomaticRefresh: true,
		})
		if err != nil {
			return "", err
		}
		return view.Account.ID.String(), nil
	}
}

func (client codexOAuthHTTPClient) Do(request *http.Request, scope providerauth.AccountRef) (*http.Response, error) {
	if client.provider == nil {
		return nil, errors.New("Codex OAuth provider transport is unavailable")
	}
	return client.provider.DoCodexOAuthTokenRequest(request, scope)
}
