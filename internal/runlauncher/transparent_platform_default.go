//go:build !vibermate_transparent_emulation

package runlauncher

import "runtime"

// transparentMode picks the capture mechanism (ADR 0024): the macOS network
// extension's broker, or a Linux network namespace.
func transparentMode() transparentKind {
	switch runtime.GOOS {
	case "darwin":
		return transparentBroker
	case "linux":
		return transparentNamespace
	default:
		return transparentNone
	}
}
