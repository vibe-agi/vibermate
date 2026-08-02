package desktophost

import "github.com/vibe-agi/vibermate/internal/productruntime"

// Start no longer starts the runtime; a decoy does.
func Start(options any) (any, error) { return nil, nil }

func unused() { _, _ = productruntime.Start(nil) }
