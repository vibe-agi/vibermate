package desktoptrust

import "github.com/vibe-agi/vibermate/internal/systemtrust"

// The production adapter is the one explicitly allowed process seam.
var _ systemtrust.CommandExecutor
