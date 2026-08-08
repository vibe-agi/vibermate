// Command vibermate-acceptance runs the opt-in macOS arm64 packaged-app
// acceptance against packaged runtime executables. It never receives a secret
// value. Managed provider-account evidence remains blocked until the runtime
// assembles that authority.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()
	if len(os.Args) > 1 && os.Args[1] == installedSmokeCommand {
		if err := runInstalledSmokeCommand(ctx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("Installed macOS Desktop smoke passed")
		return
	}
	config, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	report, runErr := runAcceptance(ctx, config)
	if config.reportPath != "" {
		if err := writeReport(config.reportPath, report); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, runErr)
		var blocked *blockedError
		if errors.As(runErr, &blocked) {
			os.Exit(3)
		}
		os.Exit(1)
	}
	fmt.Println("macOS arm64 packaged-app acceptance passed")
}
