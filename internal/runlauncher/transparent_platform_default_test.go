//go:build !vibermate_transparent_emulation

package runlauncher

import (
	"errors"
	"runtime"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
)

// Release builds offer transparent capture only where the extension exists;
// Linux needs the emulation tag used by tool/transparent-linux-e2e.sh.
func TestReleaseBuildRefusesTransparentCaptureOffMacOS(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS supports transparent capture")
	}
	_, err := (&Launcher{}).startTransparent(capturecontrol.LaunchGrant{}, capturecontrol.LaunchGrant{})
	if !errors.Is(err, ErrTransparentUnsupported) {
		t.Fatalf("startTransparent() error = %v, want ErrTransparentUnsupported", err)
	}
}
