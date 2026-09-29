package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"golang.org/x/term"
)

const (
	keyLoginUsage       = "cli.usage.login"
	keyLoginFailed      = "cli.error.loginFailed"
	keyLoginHTTPWarning = "cli.login.httpWarning"
	keyLogoutUsage      = "cli.usage.logout"
	keyLogoutFailed     = "cli.error.logoutFailed"
	keyTrustUsage       = "cli.usage.trust"
	keyTrustFailed      = "cli.error.trustFailed"

	maxLoginInputBytes = 2 << 10
)

type loginConfig struct {
	server      serverconnection.Target
	environment []string
}

type trustConfig struct {
	server        serverconnection.Target
	caFingerprint string
}

func parseTrust(arguments []string) (trustConfig, error) {
	if len(arguments) < 4 || arguments[0] != "trust" || arguments[1] != "--server" {
		return trustConfig{}, errors.New(
			"trust requires --server https://host:port and one trust mode",
		)
	}
	target, err := serverconnection.ParseTarget(arguments[2])
	if err != nil || target.Transport() != serverconnection.TransportHTTPS {
		return trustConfig{}, errors.New("trust requires an HTTPS Runtime Server")
	}
	config := trustConfig{server: target}
	if len(arguments) == 4 && arguments[3] == "--system-roots" {
		return config, nil
	}
	if len(arguments) == 5 && arguments[3] == "--ca-fingerprint" {
		decoded, err := hex.DecodeString(arguments[4])
		if err == nil && len(decoded) == sha256.Size {
			config.caFingerprint = hex.EncodeToString(decoded)
			return config, nil
		}
	}
	return trustConfig{}, errors.New("trust requires --system-roots or --ca-fingerprint with a SHA-256 hex digest")
}

func parseLogin(arguments []string) (loginConfig, error) {
	if len(arguments) != 3 || arguments[0] != "login" || arguments[1] != "--server" {
		return loginConfig{}, errors.New("login requires one --server host:port")
	}
	target, err := serverconnection.ParseTarget(arguments[2])
	if err != nil {
		return loginConfig{}, err
	}
	return loginConfig{server: target}, nil
}

func parseLogout(arguments []string) (loginConfig, error) {
	if len(arguments) != 3 || arguments[0] != "logout" || arguments[1] != "--server" {
		return loginConfig{}, errors.New("logout requires one --server host:port")
	}
	target, err := serverconnection.ParseTarget(arguments[2])
	if err != nil {
		return loginConfig{}, err
	}
	return loginConfig{server: target}, nil
}

func executeRemoteLogin(
	ctx context.Context,
	config loginConfig,
	stateDirectory string,
	displayName string,
	clock runlauncher.RemoteClock,
	random io.Reader,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) (int, string) {
	if ctx == nil || !config.server.Valid() || stateDirectory == "" ||
		displayName == "" || clock == nil || random == nil || stdin == nil ||
		stdout == nil || stderr == nil {
		return 1, keyLoginFailed
	}
	if config.server.Transport() == serverconnection.TransportHTTP {
		if err := renderCLIMessage(
			config.environment,
			stderr,
			keyLoginHTTPWarning,
			nil,
		); err != nil {
			return 1, reasonRenderFailed
		}
	}
	username, password, err := readLoginCredentials(stdin, stderr)
	if err != nil {
		clear(password)
		return 1, keyLoginFailed
	}
	defer clear(password)
	loginContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result, err := runlauncher.LoginRemote(loginContext, runlauncher.RemoteLoginRequest{
		Config: runlauncher.RemoteConfig{
			Target: config.server, StateDirectory: stateDirectory,
			DisplayName: displayName, Clock: clock, Random: random,
		},
		Username: username,
		Password: password,
	})
	if err != nil {
		return 1, keyLoginFailed
	}
	if config.server.Transport() != serverconnection.TransportHTTP && result.FirstUse {
		_, _ = fmt.Fprintf(
			stderr,
			"Recorded first-use trust for this Runtime Server (current leaf SHA-256 %s).\n",
			result.TLSFingerprint,
		)
	}
	_, _ = fmt.Fprintf(
		stdout,
		"Logged in to %s as %s. Session expires %s.\n",
		result.Target.Origin(), result.Username,
		result.ExpiresAt.UTC().Format(time.RFC3339),
	)
	return 0, ""
}

func executeRemoteLogout(
	ctx context.Context,
	config loginConfig,
	stateDirectory string,
	displayName string,
	clock runlauncher.RemoteClock,
	random io.Reader,
	stdout io.Writer,
) (int, string) {
	if ctx == nil || !config.server.Valid() || stateDirectory == "" ||
		displayName == "" || clock == nil || random == nil || stdout == nil {
		return 1, keyLogoutFailed
	}
	if err := runlauncher.LogoutRemote(ctx, runlauncher.RemoteLogoutRequest{
		Config: runlauncher.RemoteConfig{
			Target: config.server, StateDirectory: stateDirectory,
			DisplayName: displayName, Clock: clock, Random: random,
		},
	}); err != nil {
		return 1, keyLogoutFailed
	}
	_, _ = fmt.Fprintf(stdout, "Logged out from %s.\n", config.server.Origin())
	return 0, ""
}

func executeRemoteTrust(
	ctx context.Context,
	config trustConfig,
	stateDirectory string,
	clock runlauncher.RemoteClock,
	stdout io.Writer,
) (int, string) {
	if ctx == nil || !config.server.Valid() || stateDirectory == "" ||
		clock == nil || stdout == nil {
		return 1, keyTrustFailed
	}
	trustContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	fingerprint, err := runlauncher.TrustRemote(
		trustContext,
		config.server,
		stateDirectory,
		clock,
		15*time.Second,
		config.caFingerprint,
	)
	if err != nil {
		return 1, keyTrustFailed
	}
	if config.caFingerprint != "" {
		_, _ = fmt.Fprintf(stdout, "Verified %s and saved its private CA trust (CA SHA-256 %s; leaf SHA-256 %s).\n",
			config.server.Origin(), config.caFingerprint, fingerprint)
		return 0, ""
	}
	_, _ = fmt.Fprintf(
		stdout,
		"Verified %s with system roots and migrated its saved trust (SHA-256 %s).\n",
		config.server.Origin(),
		fingerprint,
	)
	return 0, ""
}

func readLoginCredentials(input io.Reader, prompts io.Writer) (string, []byte, error) {
	reader := bufio.NewReaderSize(io.LimitReader(input, maxLoginInputBytes+1), 256)
	_, _ = io.WriteString(prompts, "Username: ")
	usernameLine, err := readLoginLine(reader)
	if err != nil || usernameLine == "" {
		return "", nil, errors.New("Runtime User username is required")
	}
	_, _ = io.WriteString(prompts, "Password: ")
	var password []byte
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		password, err = term.ReadPassword(int(file.Fd()))
		_, _ = io.WriteString(prompts, "\n")
	} else {
		var passwordLine string
		passwordLine, err = readLoginLine(reader)
		password = []byte(passwordLine)
	}
	if err != nil || len(password) == 0 || len(password) > maxLoginInputBytes {
		clear(password)
		return "", nil, errors.New("Runtime User password is required")
	}
	return usernameLine, password, nil
}

func readLoginLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", err
	}
	if len(line) > maxLoginInputBytes {
		return "", errors.New("Runtime User credential input is too long")
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	if strings.ContainsAny(line, "\r\n") {
		return "", errors.New("Runtime User credential input is invalid")
	}
	return line, nil
}
