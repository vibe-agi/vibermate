package captureadmission

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func start(path string) (int, uintptr, error) {
	return syscall.StartProcess(path, []string{path}, &syscall.ProcAttr{})
}

func replace(path string) error {
	return unix.Exec(path, []string{path}, nil)
}
