package productruntime

import (
	"errors"
	"fmt"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/anthropicresponses"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/responseschat"
)

// chatGPTTargetRule says how a protocol path treats a ChatGPT-plan target.
type chatGPTTargetRule string

// chatGPTTargetNative adds no rule of its own: the Environment compiler's
// realm and backend-protocol checks decide, as they do for every route. A
// same-dialect path forwards the client's own wire, and a path whose provider
// dialect is OpenAI Chat can never select a ChatGPT target, because that
// realm offers only OpenAI Responses.
const chatGPTTargetNative chatGPTTargetRule = "native"

// chatGPTTargetRefused marks a translated path that must never reach a
// ChatGPT plan (design 13.1): the Environment compiler refuses such a route
// at every candidate compile (environment.ErrTranslatedChatGPTRoute) and the
// Exchange refuses a restored one after Select.
const chatGPTTargetRefused chatGPTTargetRule = "refused"

// protocolPathSpec is one production protocol path: the catalog definition
// the Environment compiler accepts and the constructor of the path the
// Exchange selects for it. Keeping both in one entry means a catalog entry
// without a path, or a path without a catalog entry, cannot be written.
type protocolPathSpec struct {
	Definition protocolspec.CodecPairDefinition
	// Build constructs the path. A non-nil resources overrides the codecs'
	// default semantic resource limits; nil keeps them.
	Build         func(resources *protocolcore.ResourceLimits) (*protocolpath.Path, error)
	ChatGPTTarget chatGPTTargetRule
}

// productionProtocolPaths is the single table of protocol paths the runtime
// offers. The Environment compiler's catalog and the Exchange's selector are
// both built from it.
func productionProtocolPaths() ([]protocolPathSpec, error) {
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		return nil, fmt.Errorf("build client operation catalog: %w", err)
	}
	// Every production pair needs the same provider capabilities.
	required := []protocolspec.ProviderCapability{
		protocolspec.ProviderCapabilityMessages,
		protocolspec.ProviderCapabilityStreaming,
		protocolspec.ProviderCapabilityToolCalls,
	}
	definition := func(
		rawID string,
		revision protocolspec.Revision,
		client, provider protocolspec.Dialect,
	) (protocolspec.CodecPairDefinition, error) {
		id, err := protocolspec.NewCodecPairID(rawID)
		if err != nil {
			return protocolspec.CodecPairDefinition{}, err
		}
		return protocolspec.CodecPairDefinition{
			ID:                   id,
			Revision:             revision,
			ClientDialect:        client,
			ProviderDialect:      provider,
			ClientOperationIDs:   operations.SemanticOperationIDs(client),
			RequiredCapabilities: append([]protocolspec.ProviderCapability(nil), required...),
		}, nil
	}
	entries := []struct {
		id       string
		revision protocolspec.Revision
		client   protocolspec.Dialect
		provider protocolspec.Dialect
		build    func(*protocolcore.ResourceLimits) (*protocolpath.Path, error)
		chatGPT  chatGPTTargetRule
	}{
		{
			id: anthropicchat.CodecPairID, revision: anthropicchat.CodecRevision,
			client: protocolspec.DialectAnthropicMessages, provider: protocolspec.DialectOpenAIChat,
			build: func(resources *protocolcore.ResourceLimits) (*protocolpath.Path, error) {
				options := anthropicchat.DefaultOptions()
				if resources != nil {
					options.Resources = resources
				}
				return anthropicchat.NewProtocolPath(options)
			},
		},
		{
			id: anthropicchat.MessagesCodecPairID, revision: anthropicchat.MessagesCodecRevision,
			client: protocolspec.DialectAnthropicMessages, provider: protocolspec.DialectAnthropicMessages,
			build: func(resources *protocolcore.ResourceLimits) (*protocolpath.Path, error) {
				options := anthropicchat.DefaultOptions()
				if resources != nil {
					options.Resources = resources
				}
				return anthropicchat.NewMessagesProtocolPath(options)
			},
		},
		{
			id: responseschat.CodecPairID, revision: responseschat.CodecRevision,
			client: protocolspec.DialectOpenAIResponses, provider: protocolspec.DialectOpenAIChat,
			build: func(resources *protocolcore.ResourceLimits) (*protocolpath.Path, error) {
				options := responseschat.DefaultOptions()
				if resources != nil {
					options.Resources = resources
				}
				return responseschat.NewProtocolPath(options)
			},
		},
		{
			id: responseschat.ResponsesPassthroughCodecPairID, revision: responseschat.ResponsesPassthroughCodecRevision,
			client: protocolspec.DialectOpenAIResponses, provider: protocolspec.DialectOpenAIResponses,
			build: func(resources *protocolcore.ResourceLimits) (*protocolpath.Path, error) {
				options := openairesponses.DefaultOptions()
				if resources != nil {
					options.Resources = resources
				}
				return responseschat.NewResponsesPassthroughProtocolPath(options)
			},
		},
		{
			id: anthropicresponses.CodecPairID, revision: anthropicresponses.CodecRevision,
			client: protocolspec.DialectAnthropicMessages, provider: protocolspec.DialectOpenAIResponses,
			build: func(resources *protocolcore.ResourceLimits) (*protocolpath.Path, error) {
				options := anthropicresponses.DefaultOptions()
				if resources != nil {
					options.Resources = resources
				}
				return anthropicresponses.NewProtocolPath(options)
			},
			chatGPT: chatGPTTargetRefused,
		},
	}
	specs := make([]protocolPathSpec, 0, len(entries))
	for _, entry := range entries {
		pair, err := definition(entry.id, entry.revision, entry.client, entry.provider)
		if err != nil {
			return nil, err
		}
		rule := entry.chatGPT
		if rule == "" {
			rule = chatGPTTargetNative
		}
		specs = append(specs, protocolPathSpec{
			Definition: pair, Build: entry.build, ChatGPTTarget: rule,
		})
	}
	return specs, nil
}

// productionProtocolCatalog builds the Environment compiler's protocol
// catalog from the production table.
func productionProtocolCatalog() (protocolspec.Catalog, error) {
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		return protocolspec.Catalog{}, fmt.Errorf("build client operation catalog: %w", err)
	}
	specs, err := productionProtocolPaths()
	if err != nil {
		return protocolspec.Catalog{}, err
	}
	definitions := make([]protocolspec.CodecPairDefinition, 0, len(specs))
	for _, spec := range specs {
		definitions = append(definitions, spec.Definition)
	}
	return protocolspec.NewCatalog(operations.Definitions(), definitions)
}

// productionProtocolSelector builds every production path, in table order,
// into the Exchange's selector.
func productionProtocolSelector(resources *protocolcore.ResourceLimits) (*protocolpath.Selector, error) {
	specs, err := productionProtocolPaths()
	if err != nil {
		return nil, err
	}
	if len(specs) == 0 {
		return nil, errors.New("production protocol path table is empty")
	}
	paths := make([]*protocolpath.Path, 0, len(specs))
	for _, spec := range specs {
		path, err := spec.Build(resources)
		if err != nil {
			return nil, fmt.Errorf("build %s protocol path: %w", spec.Definition.ID.String(), err)
		}
		paths = append(paths, path)
	}
	selector, err := protocolpath.NewSelector(paths...)
	if err != nil {
		return nil, fmt.Errorf("build protocol path selector: %w", err)
	}
	return selector, nil
}
