// Package processlineage attributes a process to the Capture Run whose
// launcher it descends from. A process is identified by pid plus start time,
// so a reused pid is never mistaken for the process that held it before.
package processlineage

import "errors"

// Identity names one process instance. Start is an opaque, platform-defined
// start time that differs between two processes that shared a pid.
type Identity struct {
	PID   int
	Start int64
}

// Process is one process's identity, its current parent pid and process
// group, and whether it is stopped by a signal.
type Process struct {
	Identity
	Parent  int
	Group   int
	Stopped bool
}

// Reader reads the live process table.
type Reader interface {
	Read(pid int) (Process, error)
}

// EnvironmentReader reads the environment a process was started with. The
// operating system only allows it for processes of the same user.
type EnvironmentReader interface {
	Environment(pid int) ([]string, error)
}

// Lister enumerates the pids of the calling user's processes.
type Lister interface {
	Processes() ([]int, error)
}

// Marked returns the calling user's live processes whose start environment
// contains marker, an exact KEY=value entry.
func Marked(marker string) ([]Process, error) {
	reader := systemReader{}
	pids, err := reader.Processes()
	if err != nil {
		return nil, err
	}
	var marked []Process
	for _, pid := range pids {
		environment, err := reader.Environment(pid)
		if err != nil {
			continue // Exited, or not ours to read.
		}
		for _, entry := range environment {
			if entry != marker {
				continue
			}
			if process, err := reader.Read(pid); err == nil {
				marked = append(marked, process)
			}
			break
		}
	}
	return marked, nil
}

// ErrNotFound reports a pid with no live process.
var ErrNotFound = errors.New("process not found")

// System reads the operating system's process table.
func System() Reader { return systemReader{} }

type systemReader struct{}
