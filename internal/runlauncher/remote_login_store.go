package runlauncher

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientpath"
	"github.com/vibe-agi/vibermate/internal/productbuild"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

func remoteLoginMachine(ctx context.Context, directory string, random io.Reader, now time.Time) (string, error) {
	workspace, err := workspaceidentity.Open(ctx, filepath.Join(directory, "identity"), random, now.UTC())
	if err != nil {
		return "", err
	}
	defer workspace.Shutdown(context.Background())
	return workspace.MachineID().String(), nil
}

// The authenticated server response is the binding authority. Keep the v1
// login document unchanged so older CLI processes can still read it, and never
// write a stale observed session over a concurrent login or logout.
func authenticateRemoteLogin(ctx context.Context, client *controlClient, login serverconnection.LoginCredential, machineID string) (servercontrol.RuntimeUserCurrentSession, error) {
	var current servercontrol.RuntimeUserCurrentSession
	if err := client.jsonRequest(ctx, http.MethodGet, servercontrol.RuntimeUserCurrentSessionPath, client.credential, "", nil, http.StatusOK, &current); err != nil {
		var failure *ControlFailure
		if errors.As(err, &failure) && failure.Status == http.StatusUnauthorized {
			return current, ErrRemoteLoginRequired
		}
		return current, err
	}
	if current.Schema != servercontrol.RuntimeUserCurrentSessionSchema || current.APIVersion != "v1" || current.InstanceID != login.InstanceID() || !productbuild.Valid(current.ProductBuild) || current.User.ID != login.UserID() || current.User.Username != login.Username() || current.SessionID != login.SessionID() || current.MachineID != machineID {
		return current, ErrRemoteLoginRequired
	}
	return current, nil
}

func openRemoteLoginStore(stateDirectory string) (*serverconnection.LoginStore, error) {
	legacyDirectory, err := clientpath.LegacyRemoteLoginDirectory(stateDirectory)
	if err != nil {
		return nil, err
	}
	return serverconnection.OpenLoginStoreMigrating(filepath.Join(stateDirectory, "login"), legacyDirectory)
}
