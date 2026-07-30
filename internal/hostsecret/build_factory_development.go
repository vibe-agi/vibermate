//go:build !vibermate_native_secrets

package hostsecret

import (
	"os"
	"path/filepath"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

const developmentSecretDirectory = "development-secrets"

// NewBuildFactory returns the cross-platform development backend. Native
// release secrets must be selected explicitly at build time.
func NewBuildFactory() (secretstore.Factory, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	return NewDevelopmentFileFactory(filepath.Join(
		root,
		"io.vibermate.desktop",
		developmentSecretDirectory,
		"store.json",
	))
}
