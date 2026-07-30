// Package runtimepath is the single authority for the Desktop app-cache
// rendezvous layout shared by the native shell, daemon, and standalone CLI.
package runtimepath

import (
	"errors"
	"os"
	"path/filepath"
)

const (
	ApplicationID        = "io.vibermate.desktop"
	runtimeDirectoryName = "runtime"
	lockFileName         = "daemon.lock"
	launcherFileName     = "launcher-v1.json"
)

type Layout struct {
	AppCacheDirectory string
	RuntimeDirectory  string
	GenerationLock    string
	LauncherRecord    string
}

func Default() (Layout, error) {
	userCache, err := os.UserCacheDir()
	if err != nil {
		return Layout{}, err
	}
	return FromAppCache(filepath.Join(userCache, ApplicationID))
}

// FromAppCache validates the exact directory resolved by the native shell. It
// computes paths without creating any filesystem object.
func FromAppCache(appCacheDirectory string) (Layout, error) {
	if appCacheDirectory == "" ||
		!filepath.IsAbs(appCacheDirectory) ||
		filepath.Clean(appCacheDirectory) != appCacheDirectory ||
		appCacheDirectory == filepath.VolumeName(appCacheDirectory)+string(filepath.Separator) {
		return Layout{}, errors.New("Desktop app cache directory must be an absolute clean non-root path")
	}
	runtimeDirectory := filepath.Join(appCacheDirectory, runtimeDirectoryName)
	return Layout{
		AppCacheDirectory: appCacheDirectory,
		RuntimeDirectory:  runtimeDirectory,
		GenerationLock:    filepath.Join(runtimeDirectory, lockFileName),
		LauncherRecord:    filepath.Join(runtimeDirectory, launcherFileName),
	}, nil
}
