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

func persistCodexLogin(accounts provideraccount.Controller) func(context.Context, codexoauth.LoginAccount, *codexoauth.Credential) (string, error) {
	return func(ctx context.Context, target codexoauth.LoginAccount, credential *codexoauth.Credential) (string, error) {
		id, err := provideraccount.NewID(target.ID)
		if err != nil {
			return "", err
		}
		endpointID, err := upstreamendpoint.NewID(target.EndpointID)
		if err != nil {
			return "", err
		}
		encoded, err := credential.MarshalBinary()
		if err != nil {
			return "", err
		}
		defer clear(encoded)
		material, err := providerauth.NewMaterial(string(encoded), nil, nil)
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
		})
		if err != nil {
			return "", err
		}
		return view.Account.ID.String(), nil
	}
}

func (client codexOAuthHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if client.provider == nil {
		return nil, errors.New("Codex OAuth provider transport is unavailable")
	}
	return client.provider.DoCodexOAuthTokenRequest(request)
}
