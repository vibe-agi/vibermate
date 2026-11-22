package capturecontrol

import (
	"syscall"

	"golang.org/x/sys/windows"
)

var create = syscall.CreateProcess

var createAsUser = syscall.CreateProcessAsUser

var spawn = windows.CreateProcess

var spawnAsUser = windows.CreateProcessAsUser

var open = windows.ShellExecute
