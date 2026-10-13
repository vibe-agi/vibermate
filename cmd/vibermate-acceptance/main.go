// Command vibermate-acceptance runs the opt-in macOS arm64 packaged-app
// acceptance against packaged runtime executables. Credentialed mode accepts
// only a path to one private file and never accepts raw secret bytes in argv or
// environment variables.
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
		if config.nativeMCPOnly && config.reportPath != "" {
			if reportErr := writeNativeMCPReport(config.reportPath, newNativeMCPReport()); reportErr != nil {
				fmt.Fprintln(os.Stderr, "native acceptance report could not be preserved")
			}
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if config.nativeMCPOnly {
		if err := executeNativeMCP(ctx, config); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("native MCP exact-client acceptance passed")
		return
	}
	if config.bootstrapDiagnosticPath != "" {
		config.bootstrapDiagnostic = &bootstrapDiagnosticRecorder{
			value: initialBootstrapDiagnostic(daemonHomePolicy(config.diagnosticDaemonHome)),
		}
	}
	report, runErr := runAcceptance(ctx, config)
	if config.bootstrapDiagnosticPath != "" {
		runErr = errors.Join(runErr, writeBootstrapDiagnostic(config.bootstrapDiagnosticPath, config.bootstrapDiagnostic))
	}
	if config.reportPath != "" {
		if err := writeReport(config.reportPath, report); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, runErr)
		os.Exit(1)
	}
	fmt.Println("macOS arm64 packaged-app acceptance passed")
}
