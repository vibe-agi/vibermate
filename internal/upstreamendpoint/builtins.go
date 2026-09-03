package upstreamendpoint

import (
	"fmt"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
)

const (
	AnthropicOfficialID ID = "target.claude.official"
	OpenAIPlatformID    ID = "target.openai.official"
	ChatGPTOfficialID   ID = "target.codex.official"
)

func BuiltInCommands() ([]CreateCommand, error) {
	definitions := []struct {
		id        ID
		name      string
		origin    string
		realm     string
		protocols []string
		drivers   []providerauth.DriverRef
	}{
		{
			id: AnthropicOfficialID, name: "Anthropic API", origin: "https://api.anthropic.com",
			realm: "anthropic.official", protocols: []string{"anthropic_messages"},
			drivers: []providerauth.DriverRef{
				providerauth.AnthropicAPIKeyDriverRef(), providerauth.StaticHeaderDriverRef(),
			},
		},
		{
			id: OpenAIPlatformID, name: "OpenAI API", origin: "https://api.openai.com",
			realm: "openai.platform", protocols: []string{"openai_chat", "openai_responses"},
			drivers: []providerauth.DriverRef{providerauth.StaticHeaderDriverRef()},
		},
		{
			id: ChatGPTOfficialID, name: "ChatGPT", origin: "https://chatgpt.com",
			realm: "openai.chatgpt", protocols: []string{"openai_responses"},
			drivers: []providerauth.DriverRef{providerauth.StaticHeaderDriverRef()},
		},
	}
	commands := make([]CreateCommand, 0, len(definitions))
	for _, definition := range definitions {
		origin, err := originidentity.ParseProviderOrigin(definition.origin)
		if err != nil {
			return nil, fmt.Errorf("parse built-in UpstreamEndpoint %q: %w", definition.id, err)
		}
		commands = append(commands, CreateCommand{
			ID: definition.id, DisplayName: definition.name, Origin: origin,
			RealmID: definition.realm, BackendProtocols: definition.protocols,
			Capabilities: []protocolspec.ProviderCapability{
				protocolspec.ProviderCapabilityMessages,
				protocolspec.ProviderCapabilityStreaming,
				protocolspec.ProviderCapabilityToolCalls,
			},
			Drivers: definition.drivers,
		})
	}
	return commands, nil
}
