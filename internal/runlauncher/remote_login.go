package runlauncher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

type RemoteLoginRequest struct {
	Config   RemoteConfig
	Username string
	Password []byte
}

type RemoteLoginResult struct {
	Target         serverconnection.Target
	UserID         string
	Username       string
	ExpiresAt      time.Time
	FirstUse       bool
	TLSFingerprint string
}

func LoginRemote(
	ctx context.Context,
	request RemoteLoginRequest,
) (RemoteLoginResult, error) {
	if ctx == nil || request.Username == "" || len(request.Password) == 0 {
		return RemoteLoginResult{}, errors.New("remote Runtime Server login is incomplete")
	}
	config := request.Config
	if err := config.validate(); err != nil {
		return RemoteLoginResult{}, err
	}
	workspace, err := workspaceidentity.Open(
		ctx,
		filepath.Join(config.StateDirectory, "identity"),
		config.Random,
		config.Clock.Now().UTC(),
	)
	if err != nil {
		return RemoteLoginResult{}, fmt.Errorf("open remote companion identity: %w", err)
	}
	defer func() { _ = workspace.Shutdown(context.Background()) }()
	transport, err := openRemoteTransport(config, 15*time.Second)
	if err != nil {
		return RemoteLoginResult{}, err
	}
	defer transport.close()
	password := append([]byte(nil), request.Password...)
	defer clear(password)
	session, err := requestRuntimeUserSession(
		ctx,
		transport.httpClient,
		config.Target,
		servercontrol.RuntimeUserLogin{
			Schema: servercontrol.RuntimeUserLoginSchema, Username: request.Username,
			Password: string(password), MachineID: workspace.MachineID().String(),
			DeviceName: config.DisplayName,
		},
		config.Clock.Now().UTC(),
	)
	if err != nil {
		return RemoteLoginResult{}, err
	}
	credential, err := serverconnection.NewLoginCredential(
		serverconnection.LoginCredentialInput{
			Target: config.Target, InstanceID: session.InstanceID,
			UserID: session.User.ID, Username: session.User.Username,
			SessionID: session.SessionID, SessionToken: session.SessionToken,
			ExpiresAt: session.ExpiresAt,
		},
	)
	if err != nil {
		return RemoteLoginResult{}, errors.New("Runtime Server login response is invalid")
	}
	store, err := serverconnection.OpenLoginStore(filepath.Join(config.StateDirectory, "login"))
	if err != nil {
		return RemoteLoginResult{}, err
	}
	if err := store.Save(credential); err != nil {
		return RemoteLoginResult{}, fmt.Errorf("save Runtime Server login: %w", err)
	}
	firstUse, fingerprint := transport.trust()
	return RemoteLoginResult{
		Target: config.Target, UserID: session.User.ID, Username: session.User.Username,
		ExpiresAt: session.ExpiresAt, FirstUse: firstUse, TLSFingerprint: fingerprint,
	}, nil
}

func requestRuntimeUserSession(
	ctx context.Context,
	client *http.Client,
	target serverconnection.Target,
	input servercontrol.RuntimeUserLogin,
	now time.Time,
) (servercontrol.RuntimeUserSession, error) {
	payload, err := json.Marshal(input)
	input.Password = ""
	if err != nil {
		return servercontrol.RuntimeUserSession{}, err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		target.Origin()+servercontrol.RuntimeUserSessionPath,
		bytes.NewReader(payload),
	)
	if err != nil {
		return servercontrol.RuntimeUserSession{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, application/problem+json")
	response, err := client.Do(request)
	if err != nil {
		return servercontrol.RuntimeUserSession{}, fmt.Errorf("connect to Runtime Server: %w", err)
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxControlResponseBytes+1))
	if err != nil || len(encoded) > maxControlResponseBytes {
		return servercontrol.RuntimeUserSession{}, errors.New("Runtime Server login response is invalid")
	}
	if response.StatusCode != http.StatusCreated {
		return servercontrol.RuntimeUserSession{}, decodeControlFailure(response.StatusCode, encoded)
	}
	var session servercontrol.RuntimeUserSession
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&session); err != nil ||
		session.Schema != servercontrol.RuntimeUserSessionSchema ||
		session.InstanceID == "" || session.APIVersion != "v1" ||
		session.User.ID == "" || session.User.Username == "" ||
		session.SessionID == "" || session.SessionToken == "" ||
		!now.Before(session.ExpiresAt) {
		return servercontrol.RuntimeUserSession{}, errors.New("Runtime Server login response is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return servercontrol.RuntimeUserSession{}, errors.New("Runtime Server login response is invalid")
	}
	return session, nil
}
