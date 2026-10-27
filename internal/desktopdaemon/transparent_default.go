//go:build !vibermate_transparent_emulation

package desktopdaemon

import "runtime"

// transparentCaptureSupported reports platforms with a capture extension.
// Only macOS has one; Linux will capture by network namespace (ADR 0024).
func transparentCaptureSupported() bool { return runtime.GOOS == "darwin" }
