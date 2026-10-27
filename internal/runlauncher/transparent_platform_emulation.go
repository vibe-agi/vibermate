//go:build vibermate_transparent_emulation

package runlauncher

import "runtime"

// transparentMode uses the broker on Linux too, so
// tool/transparent-linux-e2e.sh can drive the macOS path with an emulator of
// the extension; release builds never carry this tag.
func transparentMode() transparentKind {
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		return transparentBroker
	}
	return transparentNone
}
