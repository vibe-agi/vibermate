package main

import (
	"errors"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
)

type acceptanceClientID string

const (
	acceptanceClientClaudeCode acceptanceClientID = "claude-code"
	acceptanceClientCodexCLI   acceptanceClientID = "codex-cli"
)

type acceptanceClient struct {
	ID             acceptanceClientID
	Version        string
	ReportLabel    string
	ExecutablePath string
	ClientOrigin   string
	ClientProtocol environment.ClientProtocol
	Release        clientadapter.Release
}

func selectedAcceptanceClient(
	config config,
) (acceptanceClient, error) {
	switch config.clientID {
	case acceptanceClientClaudeCode:
		return acceptanceClient{
			ID:             acceptanceClientClaudeCode,
			Version:        "2.1.220",
			ReportLabel:    "Claude Code",
			ExecutablePath: config.claudePath,
			ClientOrigin:   "https://api.anthropic.com",
			ClientProtocol: environment.ClientProtocolAnthropicMessages,
			Release:        clientadapter.ClaudeCode221220DarwinARM64(),
		}, nil
	case acceptanceClientCodexCLI:
		return acceptanceClient{
			ID:             acceptanceClientCodexCLI,
			Version:        "0.145.0",
			ReportLabel:    "Codex CLI",
			ExecutablePath: config.codexPath,
			ClientOrigin:   "https://api.openai.com",
			ClientProtocol: environment.ClientProtocolOpenAIResponses,
			Release:        clientadapter.CodexCLI01450DarwinARM64(),
		}, nil
	default:
		return acceptanceClient{}, errors.New(
			"acceptance client identity is unsupported",
		)
	}
}
