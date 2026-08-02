package desktopdaemon

import "github.com/vibe-agi/vibermate/internal/desktophost"

func Run(options any) error {
	_, err := desktophost.Start(options)
	return err
}

func ProductionOptions() (any, error) { return nil, nil }
