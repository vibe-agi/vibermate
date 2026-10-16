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
	root, err := os.UserConfigDir()
	if err != nil || root == "" || !filepath.IsAbs(root) {
		return "", errors.New("ViberMate client configuration directory is unavailable")
	}
	return filepath.Join(root, runtimepath.ApplicationID, "remote-client"), nil
}

// LegacyRemoteLoginDirectory enables the requested home-file compatibility only
// for the canonical default state directory. Custom state never reads home data.
func LegacyRemoteLoginDirectory(stateDirectory string) (string, error) {
	canonical, err := DefaultRemoteStateDirectory()
	if err != nil {
		return "", nil
	}
	if stateDirectory != canonical {
		return "", nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !filepath.IsAbs(home) {
		return "", errors.New("ViberMate client home directory is unavailable")
	}
	return filepath.Join(home, ".vibermate"), nil
}
