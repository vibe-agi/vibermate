//go:build !darwin && !linux

package runlauncher

func pauseProcessGroup(int) error  { return ErrTransparentUnsupported }
func resumeProcessGroup(int) error { return ErrTransparentUnsupported }
