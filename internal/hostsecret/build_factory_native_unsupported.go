//go:build vibermate_native_secrets && !darwin

package hostsecret

import "github.com/vibe-agi/vibermate/internal/secretstore"

// NewBuildFactory refuses rather than degrading. Design 06 requires a missing
// platform backend to be explicitly unsupported: a release that silently fell
// back to a file would put secrets somewhere the person did not choose.
func NewBuildFactory() (secretstore.Factory, error) {
	return nil, ErrUnsupportedPlatform
}
