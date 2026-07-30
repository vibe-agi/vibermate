// Command vibermated runs the packaged Desktop Go sidecar.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/vibe-agi/vibermate/internal/desktopdaemon"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
)

func main() {
	config, bootstrapFile, err := parseArguments(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer bootstrapFile.Close()
	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()
	secretsFactory, err := hostsecret.NewBuildFactory()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	options, err := desktopdaemon.ProductionOptions(
		ctx,
		config.appCacheDirectory,
		config.dataDirectory,
		config.webviewOrigin,
		bootstrapFile,
		secretsFactory,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := desktopdaemon.Run(ctx, options); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type commandConfig struct {
	appCacheDirectory string
	dataDirectory     string
	webviewOrigin     string
	bootstrapFD       int
}

func parseArguments(arguments []string) (commandConfig, *os.File, error) {
	var config commandConfig
	for _, argument := range arguments {
		switch {
		case strings.HasPrefix(argument, "--app-cache-dir="):
			config.appCacheDirectory = strings.TrimPrefix(
				argument,
				"--app-cache-dir=",
			)
		case strings.HasPrefix(argument, "--data-dir="):
			config.dataDirectory = strings.TrimPrefix(argument, "--data-dir=")
		case strings.HasPrefix(argument, "--bootstrap-fd="):
			raw := strings.TrimPrefix(argument, "--bootstrap-fd=")
			descriptor, err := strconv.Atoi(raw)
			if err != nil || descriptor < 1 {
				return commandConfig{}, nil, errors.New("bootstrap file descriptor is invalid")
			}
			config.bootstrapFD = descriptor
		case strings.HasPrefix(argument, "--webview-origin="):
			config.webviewOrigin = strings.TrimPrefix(
				argument,
				"--webview-origin=",
			)
		default:
			return commandConfig{}, nil, errors.New("vibermated received an unsupported argument")
		}
	}
	if config.appCacheDirectory == "" ||
		config.dataDirectory == "" ||
		(config.webviewOrigin != "tauri://localhost" &&
			config.webviewOrigin != "http://127.0.0.1:1420") ||
		config.bootstrapFD == 0 {
		return commandConfig{}, nil, errors.New(
			"vibermated requires app cache, data, Webview origin, and bootstrap descriptors",
		)
	}
	file := os.NewFile(uintptr(config.bootstrapFD), "vibermate-bootstrap")
	if file == nil {
		return commandConfig{}, nil, errors.New("bootstrap file descriptor is unavailable")
	}
	return config, file, nil
}
