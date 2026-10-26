//go:build vibermate_transparent_emulation

package runlauncher

import "runtime"

// transparentPlatformSupported also allows Linux in builds for
// tool/transparent-linux-e2e.sh; release builds never carry this tag.
func transparentPlatformSupported() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux"
}
