package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimepath"
)

const (
	installedSmokeCommand = "installed-smoke"
	installedSmokeSchema  = "vibermate.macos-installed-desktop-smoke/v1"
	installedSmokeReport  = "desktop-smoke.json"
)

type installedSmokeConfig struct {
	appPath    string
	homePath   string
	reportPath string
	timeout    time.Duration
}

type installedSmokeEvidence struct {
	Schema                string `json:"schema"`
	Status                string `json:"status"`
	Launches              int    `json:"launches"`
	Readiness             string `json:"readiness"`
	NavigationPersistence bool   `json:"navigationPersistence"`
	GracefulExit          bool   `json:"gracefulExit"`
	IsolatedHome          bool   `json:"isolatedHome"`
}

func runInstalledSmokeCommand(ctx context.Context, arguments []string) error {
	config, err := parseInstalledSmokeConfig(arguments, os.Getenv("RUNNER_TEMP"))
	if err != nil {
		return err
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("installed Desktop smoke requires macOS arm64")
	}
	appPath, err := appBundlePath(config.appPath)
	if err != nil {
		return fmt.Errorf("inspect installed Desktop App: %w", err)
	}
	if appPath != config.appPath {
		return errors.New("installed Desktop App path is not canonical")
	}
	if err := requirePrivateExistingDirectory(config.homePath); err != nil {
		return fmt.Errorf("inspect installed Desktop isolated home: %w", err)
	}
	stateRoot, err := openInstalledSmokeStateRoot(config.reportPath)
	if err != nil {
		return fmt.Errorf("open installed Desktop smoke state: %w", err)
	}
	defer stateRoot.Close()
	layout, err := runtimepath.FromAppCache(filepath.Join(
		config.homePath,
		"Library",
		"Caches",
		runtimepath.ApplicationID,
	))
	if err != nil {
		return err
	}
	runContext, cancel := context.WithTimeout(ctx, config.timeout)
	defer cancel()
	if err := exercisePackagedDesktopShell(
		runContext,
		appPath,
		layout,
		config.homePath,
	); err != nil {
		return fmt.Errorf("exercise installed Desktop App: %w", err)
	}
	return writeInstalledSmokeEvidence(stateRoot, installedSmokeEvidence{
		Schema:                installedSmokeSchema,
		Status:                "passed",
		Launches:              2,
		Readiness:             "launcher-discovery-and-router-mounted",
		NavigationPersistence: true,
		GracefulExit:          true,
		IsolatedHome:          true,
	})
}

func parseInstalledSmokeConfig(
	arguments []string,
	runnerTemp string,
) (installedSmokeConfig, error) {
	var config installedSmokeConfig
	flags := flag.NewFlagSet("vibermate-acceptance installed-smoke", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&config.appPath, "desktop-app", "", "installed VibeMate.app path")
	flags.StringVar(&config.homePath, "home", "", "isolated Desktop home")
	flags.StringVar(&config.reportPath, "report", "", "private smoke evidence path")
	flags.DurationVar(&config.timeout, "timeout", 2*time.Minute, "smoke deadline")
	if err := flags.Parse(arguments); err != nil {
		return installedSmokeConfig{}, err
	}
	if flags.NArg() != 0 {
		return installedSmokeConfig{}, errors.New("installed Desktop smoke accepts no positional arguments")
	}
	if config.timeout < 30*time.Second || config.timeout > 5*time.Minute {
		return installedSmokeConfig{}, errors.New("installed Desktop smoke timeout is out of bounds")
	}
	if err := validateInstalledSmokePaths(
		runnerTemp,
		config.appPath,
		config.homePath,
		config.reportPath,
	); err != nil {
		return installedSmokeConfig{}, err
	}
	return config, nil
}

func validateInstalledSmokePaths(runnerTemp, appPath, homePath, reportPath string) error {
	for label, path := range map[string]string{
		"runner temp":   runnerTemp,
		"installed App": appPath,
		"isolated home": homePath,
		"smoke report":  reportPath,
	} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("%s path must be absolute and clean", label)
		}
	}
	installRoot := filepath.Dir(filepath.Dir(appPath))
	if filepath.Base(filepath.Dir(appPath)) != "Applications" ||
		filepath.Base(appPath) != "VibeMate.app" ||
		!directRunnerChild(runnerTemp, installRoot, "vibermate-install-root-") ||
		!directRunnerChild(runnerTemp, homePath, "vibermate-install-home-") ||
		filepath.Base(reportPath) != installedSmokeReport ||
		!directRunnerChild(
			runnerTemp,
			filepath.Dir(reportPath),
			"vibermate-install-state-",
		) {
		return errors.New("installed Desktop smoke paths are outside their admitted runner roots")
	}
	return nil
}

func directRunnerChild(parent, child, prefix string) bool {
	return filepath.Dir(child) == parent &&
		strings.HasPrefix(filepath.Base(child), prefix) &&
		len(filepath.Base(child)) > len(prefix)
}

func requirePrivateExistingDirectory(path string) error {
	metadata, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if metadata.Mode()&os.ModeSymlink != 0 ||
		!metadata.IsDir() ||
		metadata.Mode().Perm() != 0o700 {
		return errors.New("directory is not private")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if canonical != path {
		return errors.New("directory path is not canonical")
	}
	return nil
}

func openInstalledSmokeStateRoot(reportPath string) (*os.Root, error) {
	directory := filepath.Dir(reportPath)
	if err := requirePrivateExistingDirectory(directory); err != nil {
		return nil, err
	}
	before, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("installed Desktop smoke state changed while opening")
	}
	return root, nil
}

func writeInstalledSmokeEvidence(root *os.Root, evidence installedSmokeEvidence) error {
	if evidence != (installedSmokeEvidence{
		Schema:                installedSmokeSchema,
		Status:                "passed",
		Launches:              2,
		Readiness:             "launcher-discovery-and-router-mounted",
		NavigationPersistence: true,
		GracefulExit:          true,
		IsolatedHome:          true,
	}) {
		return errors.New("installed Desktop smoke evidence is incomplete")
	}
	payload, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	randomSuffix := make([]byte, 16)
	if _, err := rand.Read(randomSuffix); err != nil {
		return err
	}
	temporaryName := fmt.Sprintf(".desktop-smoke-%x", randomSuffix)
	temporary, err := root.OpenFile(
		temporaryName,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return err
	}
	defer root.Remove(temporaryName)
	fail := func(root error) error {
		_ = temporary.Close()
		return root
	}
	if err := temporary.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := temporary.Write(payload); err != nil {
		return fail(err)
	}
	if err := temporary.Sync(); err != nil {
		return fail(err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := root.Rename(temporaryName, installedSmokeReport); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && !errors.Is(err, fs.ErrInvalid) {
		return err
	}
	return nil
}
