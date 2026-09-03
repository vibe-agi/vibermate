package desktophost_test

import (
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/runlauncher"
)

const testEnvironmentID = "system_transparent"

func transparentLaunch(command ...string) runlauncher.LaunchRequest {
	return runlauncher.LaunchRequest{
		EnvironmentID: environment.SystemTransparentID,
		Command:       append([]string(nil), command...),
	}
}
