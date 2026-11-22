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

// writeLocalizedHeartbeatDiagnostic is writeHeartbeatDiagnostic for a
// message from the locale catalog.
func (launcher *Launcher) writeLocalizedHeartbeatDiagnostic(key string, parameters map[string]string) {
	if message, ok := launcher.localized(key, parameters); ok {
		launcher.writeHeartbeatDiagnostic(message)
	}
}
