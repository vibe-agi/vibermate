//go:build !vibermate_transparent_emulation

package runlauncher

import "runtime"

func transparentPlatformSupported() bool { return runtime.GOOS == "darwin" }
