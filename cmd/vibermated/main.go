// Command vibermated runs the packaged Desktop Go sidecar.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/vibe-agi/vibermate/internal/desktopdaemon"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/serverdaemon"
	"github.com/vibe-agi/vibermate/internal/serverhost"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "server" {
		runServer(os.Args[2:])
		return
	}
	config, resources, err := parseArguments(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer resources.bootstrap.Close()
	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()
	parentOwnership, err := desktopdaemon.NewParentOwnership(
		ctx,
		resources.parentLifetime,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer parentOwnership.Close()
	ctx = parentOwnership.Context()
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
		resources.bootstrap,
		secretsFactory,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	options.Host.RemoteServerListenAddress = config.remoteServerListenAddress
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	managementUIRoot, err := packagedManagementUIRoot(executable)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	options.Host.RemoteServerManagementUIRoot = managementUIRoot
	if err := desktopdaemon.Run(ctx, options); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func packagedManagementUIRoot(executable string) (string, error) {
	if executable == "" || !filepath.IsAbs(executable) ||
		filepath.Clean(executable) != executable {
		return "", errors.New("vibermated executable path is invalid")
	}
	macOSDirectory := filepath.Dir(executable)
	contentsDirectory := filepath.Dir(macOSDirectory)
	if filepath.Base(macOSDirectory) != "MacOS" ||
		filepath.Base(contentsDirectory) != "Contents" {
		return "", nil
	}
	root := filepath.Join(contentsDirectory, "Resources", "vibermate-web")
	return directManagementUIRoot(root, false)
}

func adjacentServerManagementUIRoot(executable string) (string, error) {
	if executable == "" || !filepath.IsAbs(executable) ||
		filepath.Clean(executable) != executable {
		return "", errors.New("vibermated executable path is invalid")
	}
	return directManagementUIRoot(
		filepath.Join(filepath.Dir(executable), "vibermate-web"),
		true,
	)
}

func directManagementUIRoot(root string, optional bool) (string, error) {
	rootInfo, rootErr := os.Lstat(root)
	if optional && errors.Is(rootErr, os.ErrNotExist) {
		return "", nil
	}
	if rootErr != nil || rootInfo.Mode()&os.ModeSymlink != 0 ||
		!rootInfo.IsDir() {
		return "", errors.New("packaged Runtime Server Web UI is unavailable")
	}
	index, err := os.Lstat(filepath.Join(root, "index.html"))
	if err != nil || index.Mode()&os.ModeSymlink != 0 ||
		!index.Mode().IsRegular() {
		return "", errors.New("packaged Runtime Server Web UI is unavailable")
	}
	return root, nil
}

type serverCommandConfig struct {
	dataDirectory string
	listenAddress string
	webRoot       string
	transport     serverhost.TransportOptions
}

func runServer(arguments []string) {
	config, err := parseServerArguments(arguments)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if config.webRoot == "" {
		executable, executableErr := os.Executable()
		if executableErr != nil {
			fmt.Fprintln(os.Stderr, executableErr)
			os.Exit(1)
		}
		config.webRoot, err = adjacentServerManagementUIRoot(executable)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	ctx, cancel := signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM,
	)
	defer cancel()
	secretsFactory, err := hostsecret.NewServerFileFactory(
		config.dataDirectory,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	options, err := serverdaemon.ProductionOptions(
		ctx,
		config.dataDirectory,
		config.listenAddress,
		config.webRoot,
		config.transport,
		secretsFactory,
		os.Stdout,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := serverdaemon.Run(ctx, options); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseServerArguments(arguments []string) (serverCommandConfig, error) {
	config := serverCommandConfig{
		listenAddress: "0.0.0.0:9666",
		transport:     serverhost.TransportOptions{Mode: serverhost.TransportHTTP},
	}
	seen := make(map[string]struct{})
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		name, value, hasInline := strings.Cut(argument, "=")
		switch name {
		case "--data-dir", "--listen", "--web-root",
			"--transport", "--tls-cert", "--tls-key":
		default:
			return serverCommandConfig{}, errors.New("vibermated server received an unsupported argument")
		}
		if _, duplicate := seen[name]; duplicate {
			return serverCommandConfig{}, errors.New(name + " may only be specified once")
		}
		seen[name] = struct{}{}
		if !hasInline {
			index++
			if index >= len(arguments) {
				return serverCommandConfig{}, errors.New(name + " requires a value")
			}
			value = arguments[index]
		}
		if value == "" {
			return serverCommandConfig{}, errors.New(name + " requires a value")
		}
		switch name {
		case "--data-dir":
			config.dataDirectory = value
		case "--listen":
			config.listenAddress = value
		case "--web-root":
			config.webRoot = value
		case "--transport":
			config.transport.Mode = serverhost.TransportMode(value)
		case "--tls-cert":
			config.transport.CertificateFile = value
		case "--tls-key":
			config.transport.PrivateKeyFile = value
		}
	}
	if config.dataDirectory == "" {
		root, err := os.UserConfigDir()
		if err != nil || root == "" {
			return serverCommandConfig{}, errors.New("default Runtime Server data directory is unavailable")
		}
		config.dataDirectory = filepath.Join(root, "io.vibermate.server")
	}
	if !filepath.IsAbs(config.dataDirectory) ||
		filepath.Clean(config.dataDirectory) != config.dataDirectory ||
		(config.webRoot != "" && (!filepath.IsAbs(config.webRoot) ||
			filepath.Clean(config.webRoot) != config.webRoot)) ||
		!validServerTransport(config.transport) {
		return serverCommandConfig{}, errors.New("vibermated server configuration is invalid")
	}
	if _, _, err := net.SplitHostPort(config.listenAddress); err != nil {
		return serverCommandConfig{}, errors.New("vibermated server listen address is invalid")
	}
	return config, nil
}

func validServerTransport(transport serverhost.TransportOptions) bool {
	switch transport.Mode {
	case serverhost.TransportHTTP, serverhost.TransportSelfSignedTLS:
		return transport.CertificateFile == "" && transport.PrivateKeyFile == ""
	case serverhost.TransportTLSFiles:
		return transport.CertificateFile != "" && transport.PrivateKeyFile != "" &&
			filepath.IsAbs(transport.CertificateFile) &&
			filepath.Clean(transport.CertificateFile) == transport.CertificateFile &&
			filepath.IsAbs(transport.PrivateKeyFile) &&
			filepath.Clean(transport.PrivateKeyFile) == transport.PrivateKeyFile &&
			transport.CertificateFile != transport.PrivateKeyFile
	default:
		return false
	}
}

type commandConfig struct {
	appCacheDirectory         string
	dataDirectory             string
	webviewOrigin             string
	remoteServerListenAddress string
	bootstrapFD               int
	parentLifetimeFD          int
	parentLifetimeSet         bool
}

type commandResources struct {
	bootstrap      *os.File
	parentLifetime *os.File
}

func parseArguments(arguments []string) (commandConfig, commandResources, error) {
	var config commandConfig
	remoteServerListenSet := false
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
				return commandConfig{}, commandResources{}, errors.New("bootstrap file descriptor is invalid")
			}
			config.bootstrapFD = descriptor
		case strings.HasPrefix(argument, "--parent-lifetime-fd="):
			raw := strings.TrimPrefix(argument, "--parent-lifetime-fd=")
			descriptor, err := strconv.Atoi(raw)
			if err != nil || descriptor != 0 {
				return commandConfig{}, commandResources{}, errors.New("parent lifetime file descriptor is invalid")
			}
			config.parentLifetimeFD = descriptor
			config.parentLifetimeSet = true
		case strings.HasPrefix(argument, "--webview-origin="):
			config.webviewOrigin = strings.TrimPrefix(
				argument,
				"--webview-origin=",
			)
		case strings.HasPrefix(argument, "--remote-server-listen="):
			if remoteServerListenSet {
				return commandConfig{}, commandResources{}, errors.New(
					"remote Server listen address may only be specified once",
				)
			}
			remoteServerListenSet = true
			config.remoteServerListenAddress = strings.TrimPrefix(
				argument,
				"--remote-server-listen=",
			)
		default:
			return commandConfig{}, commandResources{}, errors.New("vibermated received an unsupported argument")
		}
	}
	if config.appCacheDirectory == "" ||
		config.dataDirectory == "" ||
		config.webviewOrigin != "vibermate://desktop" ||
		config.bootstrapFD == 0 ||
		!config.parentLifetimeSet {
		return commandConfig{}, commandResources{}, errors.New(
			"vibermated requires app cache, data, Webview origin, bootstrap, and parent lifetime descriptors",
		)
	}
	if !remoteServerListenSet {
		config.remoteServerListenAddress = "0.0.0.0:9666"
	}
	remoteHost, remotePort, remoteErr := net.SplitHostPort(
		config.remoteServerListenAddress,
	)
	remotePortNumber, remotePortErr := strconv.ParseUint(remotePort, 10, 16)
	if remoteErr != nil || remoteHost == "" || remotePort == "" ||
		remotePortErr != nil || remotePortNumber > 65535 {
		return commandConfig{}, commandResources{}, errors.New(
			"remote Server listen address is invalid",
		)
	}
	bootstrap := os.NewFile(uintptr(config.bootstrapFD), "vibermate-bootstrap")
	parentLifetime := os.NewFile(
		uintptr(config.parentLifetimeFD),
		"vibermate-parent-lifetime",
	)
	if bootstrap == nil || parentLifetime == nil {
		return commandConfig{}, commandResources{}, errors.New("inherited file descriptor is unavailable")
	}
	return config, commandResources{
		bootstrap:      bootstrap,
		parentLifetime: parentLifetime,
	}, nil
}
