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
