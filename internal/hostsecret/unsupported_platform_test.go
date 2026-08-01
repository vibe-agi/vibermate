//go:build vibermate_native_secrets && !darwin

package hostsecret_test

import (
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/hostsecret"
)

// A platform with no SecretStore refuses rather than degrading. Design 06
// requires a missing platform backend to be explicitly unsupported: a release
// that quietly fell back to a file would put secrets somewhere nobody chose.
//
// The refusal happens where the daemon asks for the backend, before anything
// starts, so a person sees it instead of discovering it at the moment a
// credential is needed.
func TestAPlatformWithoutASecretStoreRefuses(t *testing.T) {
	t.Parallel()

	factory, err := hostsecret.NewBuildFactory()
	if !errors.Is(err, hostsecret.ErrUnsupportedPlatform) {
		t.Fatalf("error = %v", err)
	}
	if factory != nil {
		t.Fatal("an unsupported platform returned a factory")
	}
}
