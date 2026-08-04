// Command vibermate launches one command through an already-running Desktop
// Host CaptureRun. It does not select or mutate Access configuration.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/vibe-agi/vibermate/internal/localdiscovery"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
	"github.com/vibe-agi/vibermate/internal/runtimepath"
	"github.com/vibe-agi/vibermate/locales"
)

const (
	keyUsage              = "cli.usage.run"
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
	code, key := execute(
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
	if len(arguments) < 3 ||
		arguments[0] != "run" ||
		arguments[1] != "--" ||
		arguments[2] == "" {
		return 2, keyUsage
	}
	layout, err := runtimepath.Default()
	if err != nil {
		return 1, keyRuntimePath
	}
	discovery, err := localdiscovery.NewFile(
		layout.LauncherRecord,
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
	code, err := launcher.Run(context.Background(), arguments[2:])
	if err == nil {
		return code, ""
	}
	if errors.Is(err, runlauncher.ErrRuntimeUnavailable) {
		return code, keyRuntimeUnavailable
	}
	return code, keyLaunchFailed
}

type commandClock struct{}

func (commandClock) Now() time.Time {
	return time.Now().UTC()
}
