//go:build vibermate_native_secrets && darwin

package hostsecret

import "github.com/vibe-agi/vibermate/internal/secretstore"

// keychainService is where every item is filed. It is a fixed name rather
// than a path: the reference expresses a namespace and a logical ID, and the
// Host decides where that lives.
const keychainService = "io.vibermate.desktop"

// NewBuildFactory returns the release backend for this platform.
func NewBuildFactory() (secretstore.Factory, error) {
	return NewKeychainFactory(keychainService)
}
