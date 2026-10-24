//go:build darwin || linux

package runlauncher

import "syscall"

func pauseProcessGroup(group int) error  { return syscall.Kill(-group, syscall.SIGSTOP) }
func resumeProcessGroup(group int) error { return syscall.Kill(-group, syscall.SIGCONT) }
