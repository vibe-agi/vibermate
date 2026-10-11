package runlauncher

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

func (launcher *Launcher) writeHeartbeatDiagnostic(message string) {
	if output, ok := launcher.config.Stderr.(*os.File); ok && term.IsTerminal(int(output.Fd())) {
		// The active child inherits this terminal and owns its screen.
		return
	}
	_, _ = fmt.Fprintln(launcher.config.Stderr, message)
}
