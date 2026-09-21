package productruntime

import (
	"errors"
	"net/http"
)

type codexOAuthHTTPClient struct {
	provider providerRuntime
}

func (client codexOAuthHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if client.provider == nil {
		return nil, errors.New("Codex OAuth provider transport is unavailable")
	}
	return client.provider.DoCodexOAuthTokenRequest(request)
}
