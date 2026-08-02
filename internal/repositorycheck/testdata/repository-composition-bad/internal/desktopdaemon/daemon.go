package desktopdaemon

import "github.com/vibe-agi/vibermate/internal/desktophost"

// Run no longer starts the host.
func Run(options any) error { return nil }

// A decoy: the call exists in the package but not where it is owed.
func unused() { _, _ = desktophost.Start(nil) }

func ProductionOptions() (any, error) { return nil, nil }
