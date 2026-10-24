//go:build darwin

package processlineage

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func (systemReader) Read(pid int) (Process, error) {
	if pid <= 0 {
		return Process{}, ErrNotFound
	}
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
		return Process{}, ErrNotFound
	}
	if err != nil {
		return Process{}, fmt.Errorf("read process %d: %w", pid, err)
	}
	if len(processes) != 1 || int(processes[0].Proc.P_pid) != pid {
		return Process{}, ErrNotFound
	}
	process := &processes[0]
	start := process.Proc.P_starttime.Sec*1_000_000 + int64(process.Proc.P_starttime.Usec)
	return Process{Identity: Identity{PID: pid, Start: start}, Parent: int(process.Eproc.Ppid)}, nil
}
