// Command vibermate-acceptance runs the opt-in macOS arm64 M0 assembly
// acceptance against packaged runtime executables. It never receives a secret
// value; provider credentials remain behind SecretRef and the selected Store.
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
	config, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()
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
	fmt.Println("M0 assembly acceptance passed")
}
