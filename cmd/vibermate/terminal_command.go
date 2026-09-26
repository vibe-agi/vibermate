package main

import (
	"encoding/json"
	"io"

	"github.com/vibe-agi/vibermate/internal/cliinstall"
	"github.com/vibe-agi/vibermate/internal/productbuild"
)

const (
	terminalCommandSchema       = "vibermate-terminal-command/v1"
	keyTerminalCommandUsage     = "cli.usage.terminalCommand"
	keyTerminalCommandFailed    = "cli.error.terminalCommandFailed"
	terminalCommandOutputFormat = "--json"
)

type terminalCommandView struct {
	Schema         string               `json:"schema"`
	State          cliinstall.LinkState `json:"state"`
	SourcePath     string               `json:"sourcePath"`
	TargetPath     string               `json:"targetPath"`
	SourceBuild    string               `json:"sourceBuild"`
	InstalledBuild string               `json:"installedBuild,omitempty"`
	Detail         string               `json:"detail,omitempty"`
}

func executeTerminalCommand(arguments []string, stdout io.Writer) (int, string) {
	if len(arguments) != 2 || arguments[1] != terminalCommandOutputFormat {
		return 2, keyTerminalCommandUsage
	}
	command, err := cliinstall.NewDefaultUserCommand(
		packagedTerminalCommandVersion(),
		nil,
	)
	if err != nil {
		return 1, keyTerminalCommandFailed
	}
	switch arguments[0] {
	case "status":
	case "install":
		if _, err := command.Install(); err != nil {
			return 1, keyTerminalCommandFailed
		}
	case "refresh":
		if _, err := command.Refresh(); err != nil {
			return 1, keyTerminalCommandFailed
		}
	case "repair":
		if _, err := command.Repair(); err != nil {
			return 1, keyTerminalCommandFailed
		}
	case "remove":
		result, err := command.Remove()
		if err != nil || result.State == cliinstall.RemoveConflict {
			return 1, keyTerminalCommandFailed
		}
	default:
		return 2, keyTerminalCommandUsage
	}
	observation, err := command.Inspect()
	if err != nil {
		return 1, keyTerminalCommandFailed
	}
	spec := command.Spec()
	view := terminalCommandView{
		Schema:      terminalCommandSchema,
		State:       observation.State,
		SourcePath:  spec.SourcePath,
		TargetPath:  spec.TargetPath,
		SourceBuild: spec.Version,
		Detail:      observation.Detail,
	}
	if observation.Receipt != nil {
		view.InstalledBuild = observation.Receipt.Version
	}
	if err := json.NewEncoder(stdout).Encode(view); err != nil {
		return 1, keyTerminalCommandFailed
	}
	return 0, ""
}

func packagedTerminalCommandVersion() string {
	return productbuild.Label()
}
