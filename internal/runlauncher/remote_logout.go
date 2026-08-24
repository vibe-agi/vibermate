package runlauncher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

type RemoteLogoutRequest struct {
	Config RemoteConfig
}

// LogoutRemote revokes the current Login Session at its exact Runtime Server
// before removing the local credential. A transport failure deliberately keeps
// the credential so the caller can retry revocation instead of assuming it
// succeeded.
func LogoutRemote(ctx context.Context, request RemoteLogoutRequest) error {
	if ctx == nil {
		return errors.New("remote Runtime Server logout is incomplete")
	}
	config := request.Config
	if err := config.validate(); err != nil {
		return err
	}
	store, err := serverconnection.OpenLoginStore(filepath.Join(config.StateDirectory, "login"))
	if err != nil {
		return err
	}
	credential, err := store.Load(config.Target, config.Clock.Now().UTC())
	if err != nil {
		return err
	}
	transport, err := openRemoteTransport(config, 15*time.Second)
	if err != nil {
		return err
	}
	defer transport.close()
	requestContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(
		requestContext,
		http.MethodDelete,
		config.Target.Origin()+servercontrol.RuntimeUserCurrentSessionPath,
		nil,
	)
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+credential.SessionToken().Value())
	httpRequest.Header.Set("Accept", "application/problem+json")
	response, err := transport.httpClient.Do(httpRequest)
	httpRequest.Header.Del("Authorization")
	if err != nil {
		return fmt.Errorf("connect to Runtime Server for logout: %w", err)
	}
	defer response.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, maxControlResponseBytes+1))
	if readErr != nil || len(payload) > maxControlResponseBytes {
		return errors.New("Runtime Server logout response is invalid")
	}
	if response.StatusCode != http.StatusNoContent || len(payload) != 0 {
		if response.StatusCode == http.StatusUnauthorized {
			return store.Remove(config.Target)
		}
		return decodeControlFailure(response.StatusCode, payload)
	}
	if err := store.Remove(config.Target); err != nil {
		return fmt.Errorf("remove local Runtime Server Login Session: %w", err)
	}
	return nil
}
