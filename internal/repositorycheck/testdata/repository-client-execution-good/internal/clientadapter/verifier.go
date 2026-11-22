package clientadapter

import (
	"os"
	"syscall"
)

// Selectors named like the forbidden calls on other values are not calls.
type process struct{}

func (process) StartProcess() {}

func inspect(path string) (os.FileInfo, error) {
	var other process
	other.StartProcess()
	_ = syscall.Getpid()
	return os.Stat(path)
}
