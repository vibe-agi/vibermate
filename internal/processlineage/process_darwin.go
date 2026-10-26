//go:build darwin

package processlineage

import (
	"errors"
	"fmt"
	"os"

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
	return Process{
		Identity: Identity{PID: pid, Start: start},
		Parent:   int(process.Eproc.Ppid),
		Group:    int(process.Eproc.Pgid),
		Stopped:  process.Proc.P_stat == sstop,
	}, nil
}

// Environment parses kern.procargs2, which only the process's own user may
// read: a 32-bit argc, the executable path, NUL padding, argc arguments and
// then the environment it was started with, each NUL-terminated.
func (systemReader) Environment(pid int) ([]string, error) {
	if pid <= 0 {
		return nil, ErrNotFound
	}
	data, err := unix.SysctlRaw("kern.procargs2", pid)
	if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.EINVAL) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read process %d arguments: %w", pid, err)
	}
	return procargsEnvironment(data)
}

// sstop is SSTOP from <sys/proc.h>: the process is stopped by a signal.
const sstop = 4

// Processes lists the calling user's processes.
func (systemReader) Processes() ([]int, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	pids := make([]int, 0, len(processes))
	for i := range processes {
		if pid := int(processes[i].Proc.P_pid); pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}
