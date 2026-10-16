package runlauncher

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/servertransport"
)

// Environments reads only the launch choices allowed to the CLI's existing
// local principal or saved Runtime User login. No management token is needed.
func (launcher *Launcher) Environments(ctx context.Context) (capturecontrol.CaptureEnvironments, error) {
	return launcher.environments(ctx, false)
}

func (launcher *Launcher) environments(ctx context.Context, readOnlyTrust bool) (capturecontrol.CaptureEnvironments, error) {
	if launcher == nil || ctx == nil {
		return capturecontrol.CaptureEnvironments{}, errors.New("launcher and context are required")
	}
	var client *controlClient
	var err error
	if remote := launcher.config.Remote; remote != nil {
		store, openErr := openRemoteLoginStore(remote.StateDirectory)
		if openErr != nil {
			return capturecontrol.CaptureEnvironments{}, openErr
		}
		login, loadErr := store.Load(remote.Target, remote.Clock.Now().UTC())
		if loadErr != nil {
			return capturecontrol.CaptureEnvironments{}, errors.Join(ErrRemoteLoginRequired, loadErr)
		}
		machineID, identityErr := remoteLoginMachine(ctx, remote.StateDirectory, remote.Random, remote.Clock.Now())
		if identityErr != nil {
			return capturecontrol.CaptureEnvironments{}, identityErr
		}
		transport, openErr := servertransport.Open(servertransport.Options{
			Target: remote.Target, TrustDirectory: filepath.Join(remote.StateDirectory, "trust"),
			Clock: remote.Clock, Timeout: launcher.config.ControlTimeout, ReadOnlyTrust: readOnlyTrust,
		})
		if openErr != nil {
			return capturecontrol.CaptureEnvironments{}, openErr
		}
		client, err = newRemoteControlClient(remote.Target.Origin(), login.SessionToken().Value(), transport, transport.Close)
		if err != nil {
			transport.Close()
		} else {
			_, err = authenticateRemoteLogin(ctx, client, login, machineID)
			if err != nil {
				client.close()
				return capturecontrol.CaptureEnvironments{}, err
			}
		}
	} else {
		session, loadErr := launcher.config.Discovery.Load()
		if loadErr != nil {
			return capturecontrol.CaptureEnvironments{}, errors.Join(ErrRuntimeUnavailable, loadErr)
		}
		client, err = newControlClient(session, launcher.config.ControlTimeout)
	}
	if err != nil {
		return capturecontrol.CaptureEnvironments{}, err
	}
	defer client.close()
	var page capturecontrol.CaptureEnvironments
	if err := client.jsonRequest(ctx, http.MethodGet, capturecontrol.EnvironmentsPath,
		client.credential, "", nil, http.StatusOK, &page); err != nil {
		var failure *ControlFailure
		if launcher.config.Remote != nil && errors.As(err, &failure) && failure.Status == http.StatusUnauthorized {
			err = errors.Join(ErrRemoteLoginRequired, err)
		}
		return capturecontrol.CaptureEnvironments{}, err
	}
	if page.Schema != capturecontrol.EnvironmentsSchema || page.Items == nil {
		return capturecontrol.CaptureEnvironments{}, errors.New("capture Environment catalog is invalid")
	}
	seen := make(map[environment.EnvironmentID]bool, len(page.Items))
	for _, item := range page.Items {
		if _, err := environment.NewEnvironmentID(item.ID.String()); err != nil || seen[item.ID] ||
			strings.TrimSpace(item.Name) == "" || len(item.Name) > 1024 {
			return capturecontrol.CaptureEnvironments{}, errors.New("capture Environment catalog is invalid")
		}
		seen[item.ID] = true
	}
	return page, nil
}
