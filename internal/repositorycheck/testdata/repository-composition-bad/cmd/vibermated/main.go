package main

import (
	"github.com/vibe-agi/vibermate/internal/desktophost"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

// Reaches past the daemon, and never calls ProductionOptions or Run.
func main() {
	_, _ = desktophost.Start(nil)
	_, _ = productruntime.Start(nil)
}
