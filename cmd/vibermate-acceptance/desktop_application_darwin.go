//go:build darwin

package main

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func inspectDesktopProcess(processID int) (desktopProcessSnapshot, error) {
	if processID <= 0 {
		return desktopProcessSnapshot{}, errors.New("Desktop process identity is invalid")
	}
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pid", processID)
	if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
		return desktopProcessSnapshot{}, errDesktopProcessUnavailable
	}
	if err != nil {
		return desktopProcessSnapshot{}, fmt.Errorf("inspect Desktop process: %w", err)
	}
	if len(processes) == 0 {
		return desktopProcessSnapshot{}, errDesktopProcessUnavailable
	}
	if len(processes) != 1 {
		return desktopProcessSnapshot{}, errors.New("Desktop process identity is ambiguous")
	}
	process := &processes[0]
	if int(process.Proc.P_pid) != processID ||
		process.Proc.P_starttime.Sec <= 0 {
		return desktopProcessSnapshot{}, errDesktopProcessUnavailable
	}
	return desktopProcessSnapshot{
		parentID: int(process.Eproc.Ppid),
		started: desktopProcessStart{
			seconds:      process.Proc.P_starttime.Sec,
			microseconds: int64(process.Proc.P_starttime.Usec),
		},
	}, nil
}
