//go:build !vibermate_transparent_emulation

package runlauncher

import (
	"errors"
	"runtime"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
)

// Release builds use the macOS extension's broker on macOS and a network
// namespace on Linux; the broker path is never offered on Linux.
func TestReleaseBuildPicksThePlatformCaptureMechanism(t *testing.T) {
	want := map[string]transparentKind{"darwin": transparentBroker, "linux": transparentNamespace}[runtime.GOOS]
	if got := transparentMode(); got != want {
		t.Fatalf("transparentMode() = %v, want %v", got, want)
	}
	if runtime.GOOS == "darwin" {
		return
	}
	_, err := (&Launcher{}).startTransparent(capturecontrol.LaunchGrant{}, capturecontrol.LaunchGrant{})
	if !errors.Is(err, ErrTransparentUnsupported) {
		t.Fatalf("broker capture off macOS = %v, want ErrTransparentUnsupported", err)
	}
}
