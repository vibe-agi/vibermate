package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

type config struct {
	desktopAppPath    string
	daemonPath        string
	launcherPath      string
	clientID          acceptanceClientID
	claudePath        string
	codexPath         string
	environmentID     string
	dataDirectory     string
	reportPath        string
	deterministicOnly bool
	keepData          bool
	timeout           time.Duration
}

func parseConfig(arguments []string) (config, error) {
	parsed := defaultConfig()
	flags := flag.NewFlagSet("vibermate-acceptance", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(
		&parsed.desktopAppPath,
		"desktop-app",
		"",
		"absolute packaged ViberMate.app path",
	)
	flags.StringVar(
		&parsed.daemonPath,
		"daemon",
		"",
		"optional packaged vibermated path; must be the selected App member",
	)
	flags.StringVar(
		&parsed.launcherPath,
		"launcher",
		"",
		"optional packaged vibermate path; must be the selected App member",
	)
	flags.StringVar(&parsed.claudePath, "claude", "", "absolute Claude Code 2.1.220 path")
	flags.StringVar(&parsed.codexPath, "codex", "", "absolute Codex CLI 0.145.0 path")
	flags.StringVar(
		&parsed.environmentID,
		"environment-id",
		parsed.environmentID,
		"acceptance Environment ID",
	)
	flags.StringVar(
		&parsed.dataDirectory,
		"data-dir",
		"",
		"optional absolute acceptance data directory",
	)
	flags.StringVar(
		&parsed.reportPath,
		"report",
		"",
		"optional absolute JSON evidence path",
	)
	flags.BoolVar(
		&parsed.deterministicOnly,
		"deterministic-only",
		false,
		"skip credentialed fixed-client traffic",
	)
	flags.BoolVar(
		&parsed.keepData,
		"keep-data",
		false,
		"retain an automatically-created data directory",
	)
	flags.DurationVar(
		&parsed.timeout,
		"timeout",
		parsed.timeout,
		"overall acceptance deadline",
	)
	if err := flags.Parse(arguments); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, errors.New("vibermate-acceptance does not accept positional arguments")
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return config{}, errors.New("packaged-app acceptance requires macOS arm64")
	}
	desktopAppPath, err := appBundlePath(parsed.desktopAppPath)
	if err != nil {
		return config{}, fmt.Errorf("Desktop app bundle: %w", err)
	}
	parsed.desktopAppPath = desktopAppPath
	packagedDaemon, err := appBundleExecutable(
		desktopAppPath,
		"vibermated",
	)
	if err != nil {
		return config{}, fmt.Errorf("packaged daemon: %w", err)
	}
	packagedLauncher, err := appBundleExecutable(
		desktopAppPath,
		"vibermate",
	)
	if err != nil {
		return config{}, fmt.Errorf("packaged launcher: %w", err)
	}
	for label, input := range map[string]struct {
		value    string
		packaged string
	}{
		"daemon": {
			value:    parsed.daemonPath,
			packaged: packagedDaemon,
		},
		"launcher": {
			value:    parsed.launcherPath,
			packaged: packagedLauncher,
		},
	} {
		if err := validatePackagedExecutableInput(
			label,
			input.value,
			input.packaged,
		); err != nil {
			return config{}, err
		}
	}
	parsed.daemonPath = packagedDaemon
	parsed.launcherPath = packagedLauncher
	if (parsed.claudePath == "") == (parsed.codexPath == "") {
		return config{}, errors.New(
			"exactly one fixed Claude or Codex executable is required",
		)
	}
	if parsed.codexPath != "" {
		parsed.clientID = acceptanceClientCodexCLI
	} else {
		parsed.clientID = acceptanceClientClaudeCode
	}
	client, err := selectedAcceptanceClient(parsed)
	if err != nil {
		return config{}, err
	}
	clientInvocation, err := clientInvocationPath(
		client.ExecutablePath,
		client.Release.InvocationLabel,
	)
	if err != nil {
		return config{}, fmt.Errorf(
			"%s executable: %w",
			client.ReportLabel,
			err,
		)
	}
	switch parsed.clientID {
	case acceptanceClientClaudeCode:
		parsed.claudePath = clientInvocation
	case acceptanceClientCodexCLI:
		parsed.codexPath = clientInvocation
	}
	if parsed.environmentID == "" ||
		strings.TrimSpace(parsed.environmentID) != parsed.environmentID ||
		parsed.timeout <= 0 {
		return config{}, errors.New(
			"Environment and timeout inputs are required",
		)
	}
	if _, err := environment.NewEnvironmentID(parsed.environmentID); err != nil {
		return config{}, fmt.Errorf("acceptance Environment: %w", err)
	}
	if _, err := assemblyEnvironment(parsed, 1); err != nil {
		return config{}, fmt.Errorf("acceptance Environment: %w", err)
	}
	for label, value := range map[string]string{
		"data directory": parsed.dataDirectory,
		"report path":    parsed.reportPath,
	} {
		if value == "" {
			continue
		}
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return config{}, fmt.Errorf("%s must be an absolute clean path", label)
		}
	}
	return parsed, nil
}

func defaultConfig() config {
	return config{
		clientID:      acceptanceClientClaudeCode,
		environmentID: "assembly-001",
		timeout:       8 * time.Minute,
	}
}

func validatePackagedExecutableInput(
	label, input, packaged string,
) error {
	if input == "" {
		return nil
	}
	canonical, err := executablePath(input)
	if err != nil {
		return fmt.Errorf("%s executable: %w", label, err)
	}
	if canonical != packaged {
		return fmt.Errorf(
			"%s executable must be the selected App bundle member",
			label,
		)
	}
	return nil
}

func appBundlePath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("path must be absolute and clean")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || filepath.Ext(canonical) != ".app" {
		return "", errors.New("path must resolve to an app bundle directory")
	}
	infoPath := filepath.Join(canonical, "Contents", "Info.plist")
	member, err := os.Lstat(infoPath)
	if err != nil || !member.Mode().IsRegular() {
		return "", errors.New("app bundle is incomplete")
	}
	for _, name := range []string{
		"vibermate-desktop",
		"vibermated",
		"vibermate",
	} {
		if _, err := appBundleExecutable(canonical, name); err != nil {
			return "", err
		}
	}
	return canonical, nil
}

func appBundleExecutable(bundle, name string) (string, error) {
	if bundle == "" ||
		!filepath.IsAbs(bundle) ||
		filepath.Clean(bundle) != bundle ||
		name == "" ||
		filepath.Base(name) != name {
		return "", errors.New("app bundle executable identity is invalid")
	}
	path := filepath.Join(bundle, "Contents", "MacOS", name)
	info, err := os.Lstat(path)
	if err != nil ||
		!info.Mode().IsRegular() ||
		info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("app bundle is incomplete")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return "", errors.New("app bundle executable must be a direct regular member")
	}
	return path, nil
}

func executablePath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("path must be absolute and clean")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("path must resolve to an executable regular file")
	}
	return canonical, nil
}

func clientInvocationPath(path, expectedLabel string) (string, error) {
	if _, err := executablePath(path); err != nil {
		return "", err
	}
	if expectedLabel == "" || filepath.Base(expectedLabel) != expectedLabel {
		return "", errors.New("fixed client invocation label is invalid")
	}
	if filepath.Base(path) != expectedLabel {
		return "", fmt.Errorf(
			"invocation path must be named %q so release selection cannot depend on its resolved target name",
			expectedLabel,
		)
	}
	return path, nil
}
