// Package anthropicresponses composes the Anthropic Messages client side with
// the OpenAI Responses provider side into the protocol path that serves
// Claude Code from a Responses upstream (design 4.2). The pair owns only its
// policy: which client input profile and which provider profile it uses. It
// owns no transport, Environment selection, credentials or codec registry.
package anthropicresponses

import (
	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

const (
	CodecPairID   = "anthropic-messages-to-openai-responses"
	CodecRevision = 1
)

// Options configures both sides of the pair.
type Options struct {
	// Non-nil Resources controls both sides; nil keeps each side's own.
	Resources *protocolcore.ResourceLimits
	Client    anthropicchat.Options
	Provider  openairesponses.ProviderSideOptions
}

// DefaultOptions decodes Claude Code requests with the translated input
// profile and encodes them for the public Responses API.
func DefaultOptions() Options {
	client := anthropicchat.DefaultOptions()
	client.ClientInput = anthropicchat.TranslatedClientInputProfile()
	return Options{
		Client:   client,
		Provider: openairesponses.DefaultProviderSideOptions(),
	}
}

// NewProtocolPath builds the composed path. It serves every semantic
// Anthropic Messages operation of the built-in catalog.
func NewProtocolPath(options Options) (*protocolpath.Path, error) {
	if options.Resources != nil {
		if err := options.Resources.Validate(); err != nil {
			return nil, err
		}
		clientLimits := *options.Resources
		providerLimits := *options.Resources
		options.Client.Resources = &clientLimits
		options.Provider.Resources = &providerLimits
	}
	client, err := anthropicchat.NewClientSide(options.Client)
	if err != nil {
		return nil, err
	}
	provider, err := openairesponses.NewProviderSide(options.Provider)
	if err != nil {
		return nil, err
	}
	identifier, err := protocolspec.NewCodecPairID(CodecPairID)
	if err != nil {
		return nil, err
	}
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		return nil, err
	}
	return protocolpath.Compose(protocolpath.ComposeOptions{
		ID:                 identifier,
		Revision:           protocolspec.Revision(CodecRevision),
		ClientOperationIDs: operations.SemanticOperationIDs(protocolspec.DialectAnthropicMessages),
		Client:             client,
		Provider:           provider,
	})
}
