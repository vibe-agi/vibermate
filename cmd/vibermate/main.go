// Command vibermate launches one command through an already-running Desktop
// Host CaptureRun. It may select the run's initial Environment, but it never
// infers or mutates an Environment from the child command or workspace.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/runtimepath"
	"github.com/vibe-agi/vibermate/locales"
)

const (
	keyUsage              = "cli.usage"
	keyRuntimeUnavailable = "cli.error.runtimeUnavailable"
	keyRuntimePath        = "cli.error.runtimePathUnavailable"
	keyLaunchFailed       = "cli.error.launchFailed"
	reasonCatalogMissing  = "locale_catalog_unavailable"
	reasonRenderFailed    = "locale_render_unavailable"
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
	run, err := parseRun(arguments)
	if err != nil {
		return 2, keyUsage
	}
	layout, err := runtimepath.Default()
	if err != nil {
		return 1, keyRuntimePath
	}
	discovery, err := localdiscovery.NewFile(
		layout.CLIControlRecord,
		commandClock{},
	)
	if err != nil {
		return 1, keyRuntimePath
	}
	launcher, err := runlauncher.New(runlauncher.Config{
		Discovery:       discovery,
		BaseEnvironment: append([]string(nil), environment...),
		Stdin:           stdin,
		Stdout:          stdout,
		Stderr:          stderr,
	})
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
	if errors.Is(err, runlauncher.ErrRuntimeUnavailable) {
		return code, keyRuntimeUnavailable
	}
	return code, keyLaunchFailed
}

type runConfig struct {
	environmentID environment.EnvironmentID
	command       []string
}

func parseRun(arguments []string) (runConfig, error) {
	if len(arguments) == 0 || arguments[0] != "run" {
		return runConfig{}, errors.New("run command is required")
	}

	var environmentID environment.EnvironmentID
	index := 1
	if index < len(arguments) && arguments[index] == "--env" {
		if index+1 >= len(arguments) {
			return runConfig{}, errors.New("--env requires an Environment ID")
		}
		parsed, err := environment.NewEnvironmentID(arguments[index+1])
		if err != nil {
			return runConfig{}, err
		}
		environmentID = parsed
		index += 2
	}
	if index >= len(arguments) || arguments[index] != "--" ||
		index+1 >= len(arguments) || arguments[index+1] == "" {
		return runConfig{}, errors.New("run requires -- followed by a command")
	}
	return runConfig{
		environmentID: environmentID,
		command:       append([]string(nil), arguments[index+1:]...),
	}, nil
}

type commandClock struct{}

func (commandClock) Now() time.Time {
	return time.Now().UTC()
}
