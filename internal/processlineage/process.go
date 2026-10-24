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

// Process is one process's identity and its current parent pid.
type Process struct {
	Identity
	Parent int
}

// Reader reads the live process table.
type Reader interface {
	Read(pid int) (Process, error)
}

// ErrNotFound reports a pid with no live process.
var ErrNotFound = errors.New("process not found")

// System reads the operating system's process table.
func System() Reader { return systemReader{} }

type systemReader struct{}
