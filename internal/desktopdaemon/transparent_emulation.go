//go:build vibermate_transparent_emulation

package desktopdaemon

import "runtime"

// transparentCaptureSupported also enables the broker on Linux in builds for
// tool/transparent-linux-e2e.sh, where a root emulator stands in for the
// macOS extension. Release builds never carry this tag.
func transparentCaptureSupported() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux"
}
