package serverdaemon

import "github.com/vibe-agi/vibermate/internal/serverhost"

func Run(options any) error {
	_, err := serverhost.Start(options)
	return err
}

func ProductionOptions() (any, error) { return nil, nil }
