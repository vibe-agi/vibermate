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

	"github.com/vibe-agi/vibermate/internal/accessapply"
)

const (
	defaultProviderOrigin = "http://127.0.0.1:23333/v1"
	defaultProviderModel  = "dashscope:glm-5"
)

type config struct {
	desktopAppPath    string
	daemonPath        string
	launcherPath      string
	claudePath        string
	accessID          string
	providerOrigin    string
	providerModel     string
	secretRef         string
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
		"absolute packaged VibeMate.app path",
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
	flags.StringVar(
		&parsed.accessID,
		"access-id",
		parsed.accessID,
		"acceptance Access ID",
	)
	flags.StringVar(
		&parsed.providerOrigin,
		"provider-origin",
		parsed.providerOrigin,
		"OpenAI Chat provider origin",
	)
	flags.StringVar(
		&parsed.providerModel,
		"provider-model",
		parsed.providerModel,
		"fixed provider model",
	)
	flags.StringVar(
		&parsed.secretRef,
		"secret-ref",
		"",
		"provider SecretRef; defaults to the Access account reference",
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
		"skip credentialed Claude traffic",
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
		return config{}, errors.New("M0 assembly acceptance requires macOS arm64")
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
	if _, err := executablePath(parsed.claudePath); err != nil {
		return config{}, fmt.Errorf("claude executable: %w", err)
	}
	if parsed.accessID == "" ||
		strings.TrimSpace(parsed.accessID) != parsed.accessID ||
		parsed.providerOrigin == "" ||
		strings.TrimSpace(parsed.providerOrigin) != parsed.providerOrigin ||
		parsed.providerModel == "" ||
		strings.TrimSpace(parsed.providerModel) != parsed.providerModel ||
		parsed.timeout <= 0 {
		return config{}, errors.New("provider and timeout inputs are incomplete")
	}
	if parsed.secretRef == "" {
		parsed.secretRef = "secret://provider/" + parsed.accessID + "-account"
	}
	if strings.TrimSpace(parsed.secretRef) != parsed.secretRef {
		return config{}, errors.New("provider SecretRef is invalid")
	}
	if _, err := accessapply.BuildCommand(
		parsed.accessID,
		assemblyAccess(parsed, 0),
	); err != nil {
		return config{}, fmt.Errorf("acceptance Access: %w", err)
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
		accessID:       "assembly-001",
		providerOrigin: defaultProviderOrigin,
		providerModel:  defaultProviderModel,
		timeout:        8 * time.Minute,
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
