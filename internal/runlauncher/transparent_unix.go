//go:build darwin || linux

package runlauncher

import (
	"os"
	"syscall"

	"github.com/vibe-agi/vibermate/internal/processlineage"
)

// pausedRun is what pauseRun stopped, so resume continues exactly that:
// never a process the user had already stopped, nor a pid since reused.
type pausedRun struct {
	group  int
	marked []processlineage.Identity
}

// pauseRun stops the Agent's process group and every other process carrying
// the run's marker: descendants that left the group (setsid daemons, orphans)
// could otherwise open connections nothing would intercept.
func pauseRun(group int, marker string) pausedRun {
	paused := pausedRun{group: group}
	_ = syscall.Kill(-group, syscall.SIGSTOP)
	processes, err := processlineage.Marked(marker)
	if err != nil {
		return paused
	}
	for _, process := range processes {
		if process.PID == os.Getpid() || process.Group == group || process.Stopped {
			continue
		}
		if syscall.Kill(process.PID, syscall.SIGSTOP) == nil {
			paused.marked = append(paused.marked, process.Identity)
		}
	}
	return paused
}

func (paused pausedRun) resume() {
	_ = syscall.Kill(-paused.group, syscall.SIGCONT)
	for _, identity := range paused.marked {
		if process, err := processlineage.System().Read(identity.PID); err == nil && process.Identity == identity {
			_ = syscall.Kill(identity.PID, syscall.SIGCONT)
		}
	}
}
