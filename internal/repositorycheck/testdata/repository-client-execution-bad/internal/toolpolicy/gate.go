package toolpolicy

import "syscall"

var replace = syscall.Exec

func fork(path string) (int, error) {
	return syscall.ForkExec(path, nil, nil)
}
