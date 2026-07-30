package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/launcherdiscovery"
	"github.com/vibe-agi/vibermate/internal/runtimepath"
)

const desktopBundleID = "io.vibermate.desktop"

type wallClock struct{}

func (wallClock) Now() time.Time {
	return time.Now().UTC()
}

func exercisePackagedDesktopShell(
	ctx context.Context,
	appPath string,
	layout runtimepath.Layout,
	homeDirectory string,
) error {
	if homeDirectory == "" ||
		!filepath.IsAbs(homeDirectory) ||
		filepath.Clean(homeDirectory) != homeDirectory {
		return errors.New("packaged Desktop home directory is invalid")
	}
	discovery, err := launcherdiscovery.NewFile(
		layout.LauncherRecord,
		wallClock{},
	)
	if err != nil {
		return err
	}
	previous, previousErr := discovery.Load()
	if previousErr != nil &&
		!errors.Is(previousErr, os.ErrNotExist) &&
		!errors.Is(previousErr, launcherdiscovery.ErrExpired) {
		return fmt.Errorf("inspect prior Desktop generation: %w", previousErr)
	}

	command := exec.Command(
		"/usr/bin/open",
		desktopOpenArguments(appPath, homeDirectory)...,
	)
	command.Env = acceptanceEnvironment(os.Environ())
	stdout := newBoundedBuffer(64 << 10)
	stderr := newBoundedBuffer(64 << 10)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start packaged Desktop app: %w", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	var generation launcherdiscovery.Session
	cleaned := false
	defer func() {
		if cleaned {
			return
		}
		_ = requestDesktopQuit(context.Background())
		if generation.ProcessID > 0 {
			if process, findErr := os.FindProcess(generation.ProcessID); findErr == nil {
				_ = process.Kill()
			}
		}
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}()

	startupContext, cancelStartup := context.WithTimeout(ctx, 30*time.Second)
	defer cancelStartup()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for generation.InstanceID == "" {
		current, loadErr := discovery.Load()
		if loadErr == nil &&
			current.InstanceID != "" &&
			current.InstanceID != previous.InstanceID {
			generation = current
			break
		}
		if loadErr != nil &&
			!errors.Is(loadErr, os.ErrNotExist) &&
			!errors.Is(loadErr, launcherdiscovery.ErrExpired) {
			return fmt.Errorf("load packaged Desktop generation: %w", loadErr)
		}
		select {
		case waitErr := <-done:
			return prematureDesktopExit("before discovery", waitErr)
		case <-ticker.C:
		case <-startupContext.Done():
			return errors.New("packaged Desktop discovery deadline exceeded")
		}
	}

	stability := time.NewTimer(750 * time.Millisecond)
	select {
	case waitErr := <-done:
		stability.Stop()
		return prematureDesktopExit("after discovery", waitErr)
	case <-stability.C:
	case <-ctx.Done():
		stability.Stop()
		return ctx.Err()
	}
	current, err := discovery.Load()
	if err != nil || current.InstanceID != generation.InstanceID {
		return errors.New("packaged Desktop generation did not remain stable")
	}
	if err := requestDesktopQuit(ctx); err != nil {
		return err
	}
	exitContext, cancelExit := context.WithTimeout(ctx, 35*time.Second)
	defer cancelExit()
	select {
	case waitErr := <-done:
		if waitErr != nil {
			return fmt.Errorf(
				"packaged Desktop graceful exit: %w",
				normalizeWaitError(waitErr),
			)
		}
	case <-exitContext.Done():
		return errors.New("packaged Desktop graceful exit deadline exceeded")
	}
	for {
		removed, err := discoveryRemoved(layout.LauncherRecord)
		if err != nil {
			return err
		}
		if removed {
			cleaned = true
			return nil
		}
		select {
		case <-ticker.C:
		case <-exitContext.Done():
			return errors.New("packaged Desktop left owned discovery after exit")
		}
	}
}

func desktopOpenArguments(
	appPath string,
	homeDirectory string,
) []string {
	return []string{
		"-n",
		"-W",
		"--env",
		"HOME=" + homeDirectory,
		appPath,
	}
}

func prematureDesktopExit(stage string, waitErr error) error {
	if waitErr == nil {
		return fmt.Errorf(
			"packaged Desktop exited successfully %s",
			stage,
		)
	}
	return fmt.Errorf(
		"packaged Desktop exited %s: %w",
		stage,
		normalizeWaitError(waitErr),
	)
}

func requestDesktopQuit(ctx context.Context) error {
	if ctx == nil {
		return errors.New("Desktop quit context is nil")
	}
	quitContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(
		quitContext,
		"/usr/bin/osascript",
		"-e",
		`tell application id "`+desktopBundleID+`" to quit`,
	)
	command.Stdout = nil
	command.Stderr = newBoundedBuffer(16 << 10)
	if err := command.Run(); err != nil {
		return fmt.Errorf("request packaged Desktop quit: %w", err)
	}
	return nil
}
