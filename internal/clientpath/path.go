// Package clientpath owns persistent state paths for the standalone ViberMate
// CLI companion. They are separate from Desktop's ephemeral discovery cache.
package clientpath

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/vibe-agi/vibermate/internal/runtimepath"
)

func DefaultRemoteStateDirectory() (string, error) {
	return defaultRemoteStateDirectory(os.UserConfigDir)
}

func defaultRemoteStateDirectory(configDirectory func() (string, error)) (string, error) {
	root, err := configDirectory()
	if err != nil || root == "" || !filepath.IsAbs(root) {
		return "", errors.New("ViberMate client configuration directory is unavailable")
	}
	return filepath.Join(root, runtimepath.ApplicationID, "remote-client"), nil
}

// LegacyRemoteLoginDirectory enables the requested home-file compatibility only
// for the canonical default state directory. Custom state never reads home data.
func LegacyRemoteLoginDirectory(stateDirectory string) (string, error) {
	return legacyRemoteLoginDirectory(stateDirectory, os.UserConfigDir, os.UserHomeDir)
}

// Keep OS directory resolution at a per-call boundary so config and home
// independence can be exercised on hosts with different directory conventions.
func legacyRemoteLoginDirectory(stateDirectory string, configDirectory, homeDirectory func() (string, error)) (string, error) {
	canonical, err := defaultRemoteStateDirectory(configDirectory)
	if err != nil {
		return "", nil
	}
	if stateDirectory != canonical {
		return "", nil
	}
	home, err := homeDirectory()
	if err != nil || home == "" || !filepath.IsAbs(home) {
		// Config may be independently available; this source is optional.
		return "", nil
	}
	return filepath.Join(home, ".vibermate"), nil
}
