// Command vibermate launches one command through an already-running Desktop
// Host CaptureRun. It may select the run's initial Environment, but it never
// infers or mutates an Environment from the child command or workspace.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientpath"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/runtimepath"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/locales"
)

const (
	keyUsage                      = "cli.usage"
	keyRuntimeUnavailable         = "cli.error.runtimeUnavailable"
	keyCapturePreparationTimedOut = "cli.error.capturePreparationTimedOut"
	keyRuntimePath                = "cli.error.runtimePathUnavailable"
	keyLaunchFailed               = "cli.error.launchFailed"
	keyEnvironmentMissing         = "cli.error.environmentNotFound"
	keyEnvironmentDown            = "cli.error.environmentUnavailable"
	keyRemoteLoginRequired        = "cli.error.remoteLoginRequired"
	reasonCatalogMissing          = "locale_catalog_unavailable"
	reasonRenderFailed            = "locale_render_unavailable"
)

func main() {
	catalogs, err := locales.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, reasonCatalogMissing)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	code, key := executeContext(
		ctx,
		os.Args[1:],
		os.Environ(),
		os.Stdin,
		os.Stdout,
		os.Stderr,
	)
	if key != "" {
		message, renderErr := catalogs.Render(
			locales.Detect(os.Environ()),
			key,
			map[string]string{},
		)
		if renderErr != nil {
			fmt.Fprintln(os.Stderr, reasonRenderFailed)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, message)
	}
	os.Exit(code)
}

func execute(
	arguments []string,
	environment []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) (int, string) {
	return executeContext(
		context.Background(),
		arguments,
		environment,
		stdin,
		stdout,
		stderr,
	)
}

func executeContext(
	ctx context.Context,
	arguments []string,
	environment []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) (int, string) {
	if ctx == nil {
		return 1, keyLaunchFailed
	}
	if len(arguments) > 0 && arguments[0] == "capture" {
		return executeCapture(
			arguments[1:],
			environment,
			stdin,
			stdout,
			stderr,
		)
	}
	if len(arguments) > 0 && arguments[0] == "terminal-command" {
		return executeTerminalCommand(arguments[1:], stdout)
	}
	if len(arguments) > 0 && arguments[0] == "login" {
		login, err := parseLogin(arguments)
		if err != nil {
			return 2, keyLoginUsage
		}
		stateDirectory, pathErr := clientpath.DefaultRemoteStateDirectory()
		if pathErr != nil {
			return 1, keyRuntimePath
		}
		displayName, hostnameErr := os.Hostname()
		if hostnameErr != nil || displayName == "" {
			displayName = "vibermate-client"
		}
		return executeRemoteLogin(
			ctx, login, stateDirectory, displayName, commandClock{}, rand.Reader,
			stdin, stdout, stderr,
		)
	}
	if len(arguments) > 0 && arguments[0] == "logout" {
		logout, err := parseLogout(arguments)
		if err != nil {
			return 2, keyLogoutUsage
		}
		stateDirectory, pathErr := clientpath.DefaultRemoteStateDirectory()
		if pathErr != nil {
			return 1, keyRuntimePath
		}
		displayName, hostnameErr := os.Hostname()
		if hostnameErr != nil || displayName == "" {
			displayName = "vibermate-client"
		}
		return executeRemoteLogout(
			ctx, logout, stateDirectory, displayName, commandClock{}, rand.Reader, stdout,
		)
	}
	run, err := parseRun(arguments)
	if err != nil {
		return 2, keyUsage
	}
	launcherConfig := runlauncher.Config{
		BaseEnvironment: append([]string(nil), environment...),
		Stdin:           stdin,
		Stdout:          stdout,
		Stderr:          stderr,
	}
	if run.server.Valid() {
		stateDirectory, pathErr := clientpath.DefaultRemoteStateDirectory()
		if pathErr != nil {
			return 1, keyRuntimePath
		}
		displayName, hostnameErr := os.Hostname()
		if hostnameErr != nil || displayName == "" {
			displayName = "vibermate-client"
		}
		launcherConfig.Remote = &runlauncher.RemoteConfig{
			Target: run.server, StateDirectory: stateDirectory,
			DisplayName: displayName, Clock: commandClock{}, Random: rand.Reader,
		}
	} else {
		layout, pathErr := runtimepath.Default()
		if pathErr != nil {
			return 1, keyRuntimePath
		}
		discovery, discoveryErr := localdiscovery.NewFile(
			layout.CLIControlRecord,
			commandClock{},
		)
		if discoveryErr != nil {
			return 1, keyRuntimePath
		}
		launcherConfig.Discovery = discovery
	}
	launcher, err := runlauncher.New(launcherConfig)
	if err != nil {
		return 1, keyLaunchFailed
	}
	code, err := launcher.Run(ctx, runlauncher.LaunchRequest{
		EnvironmentID: run.environmentID,
		Command:       run.command,
	})
	if err == nil {
		return code, ""
	}
	if errors.Is(err, context.Canceled) {
		return 130, ""
	}
	return code, launchFailureKey(err)
}

func launchFailureKey(err error) string {
	if errors.Is(err, runlauncher.ErrRuntimeUnavailable) {
		return keyRuntimeUnavailable
	}
	if errors.Is(err, runlauncher.ErrCapturePreparationTimedOut) {
		return keyCapturePreparationTimedOut
	}
	if errors.Is(err, runlauncher.ErrRemoteLoginRequired) {
		return keyRemoteLoginRequired
	}
	if errors.Is(err, runlauncher.ErrEnvironmentNotFound) {
		return keyEnvironmentMissing
	}
	if errors.Is(err, runlauncher.ErrEnvironmentUnavailable) {
		return keyEnvironmentDown
	}
	return keyLaunchFailed
}

type runConfig struct {
	environmentID environment.EnvironmentID
	server        serverconnection.Target
	command       []string
}

func parseRun(arguments []string) (runConfig, error) {
	if len(arguments) == 0 || arguments[0] != "run" {
		return runConfig{}, errors.New("run command is required")
	}

	var environmentID environment.EnvironmentID
	var server serverconnection.Target
	seenEnvironment := false
	seenServer := false
	index := 1
	for index < len(arguments) && arguments[index] != "--" {
		option := arguments[index]
		if index+1 >= len(arguments) {
			return runConfig{}, errors.New(option + " requires a value")
		}
		value := arguments[index+1]
		switch option {
		case "--env":
			if seenEnvironment {
				return runConfig{}, errors.New("--env may only be specified once")
			}
			parsed, err := environment.NewEnvironmentID(value)
			if err != nil {
				return runConfig{}, err
			}
			environmentID = parsed
			seenEnvironment = true
		case "--server":
			if seenServer {
				return runConfig{}, errors.New("--server may only be specified once")
			}
			parsed, err := serverconnection.ParseTarget(value)
			if err != nil {
				return runConfig{}, err
			}
			server = parsed
			seenServer = true
		default:
			return runConfig{}, errors.New("unknown run option: " + option)
		}
		index += 2
	}
	if index >= len(arguments) || arguments[index] != "--" ||
		index+1 >= len(arguments) || arguments[index+1] == "" {
		return runConfig{}, errors.New("run requires -- followed by a command")
	}
	return runConfig{
		environmentID: environmentID,
		server:        server,
		command:       append([]string(nil), arguments[index+1:]...),
	}, nil
}

type commandClock struct{}

func (commandClock) Now() time.Time {
	return time.Now().UTC()
}
