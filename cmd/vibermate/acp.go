package main

import (
	"errors"

	"github.com/vibe-agi/vibermate/internal/serverconnection"
)

type acpConfig struct {
	server        serverconnection.Target
	command       []string
	recordContent bool
}

func parseACP(arguments []string) (acpConfig, error) {
	var config acpConfig
	if len(arguments) == 0 || arguments[0] != "acp" {
		return config, errors.New("ACP command required")
	}
	for index := 1; index < len(arguments); index++ {
		switch arguments[index] {
		case "--":
			if index+1 >= len(arguments) || arguments[index+1] == "" {
				return config, errors.New("ACP agent command required")
			}
			config.command = append([]string{}, arguments[index+1:]...)
			return config, nil
		case "--record-content":
			if config.recordContent {
				return config, errors.New("duplicate ACP option")
			}
			config.recordContent = true
		case "--server":
			if config.server.Valid() || index+1 >= len(arguments) {
				return config, errors.New("invalid ACP server option")
			}
			index++
			server, err := serverconnection.ParseTarget(arguments[index])
			if err != nil {
				return config, err
			}
			config.server = server
		default:
			return config, errors.New("unsupported ACP option; HTTP traffic policy is not applied by ACP observation")
		}
	}
	return config, errors.New("ACP requires -- followed by the agent command")
}
