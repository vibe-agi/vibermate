package anthropicchat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

const customToolInputSchema = `{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false}`

type clientToolIdentity struct {
	kind      protocolcore.ToolKind
	namespace string
	name      string
}

type providerToolEntry struct {
	providerName string
	identity     clientToolIdentity
	definition   protocolcore.ToolDefinition
	path         string
}

type providerToolCatalog struct {
	entries    []providerToolEntry
	byProvider map[string]providerToolEntry
	byClient   map[clientToolIdentity]providerToolEntry
}

func buildProviderToolCatalog(
	request protocolcore.Request,
) (providerToolCatalog, error) {
	catalog := providerToolCatalog{
		entries:    make([]providerToolEntry, 0, protocolcore.MaxToolCount),
		byProvider: make(map[string]providerToolEntry),
		byClient:   make(map[clientToolIdentity]providerToolEntry),
	}
	for index, definition := range request.Tools {
		if err := catalog.add(
			"",
			definition,
			fmt.Sprintf("$.tools[%d]", index),
		); err != nil {
			return providerToolCatalog{}, err
		}
	}
	for namespaceIndex, namespace := range request.ToolNamespaces {
		for toolIndex, definition := range namespace.Tools {
			if err := catalog.add(
				namespace.Name,
				definition,
				fmt.Sprintf(
					"$.tool_namespaces[%d].tools[%d]",
					namespaceIndex,
					toolIndex,
				),
			); err != nil {
				return providerToolCatalog{}, err
			}
		}
	}
	return catalog, nil
}

func (catalog *providerToolCatalog) add(
	namespace string,
	definition protocolcore.ToolDefinition,
	path string,
) error {
	identity := clientToolIdentity{
		kind:      definition.EffectiveKind(),
		namespace: namespace,
		name:      definition.Name,
	}
	if _, duplicate := catalog.byClient[identity]; duplicate {
		return errors.New("client tool identity is duplicated")
	}
	candidate := definition.Name
	if namespace != "" {
		candidate = namespace + "__" + definition.Name
	}
	if !isChatToolName(candidate) {
		candidate = hashedProviderToolName(identity)
	}
	if _, collision := catalog.byProvider[candidate]; collision {
		candidate = hashedProviderToolName(identity)
	}
	if _, collision := catalog.byProvider[candidate]; collision {
		return errors.New("provider tool identity collides")
	}
	entry := providerToolEntry{
		providerName: candidate,
		identity:     identity,
		definition:   definition.Clone(),
		path:         path,
	}
	catalog.entries = append(catalog.entries, entry)
	catalog.byProvider[candidate] = entry
	catalog.byClient[identity] = entry
	return nil
}

func (catalog providerToolCatalog) providerEntryForCall(
	call protocolcore.ToolCall,
) (providerToolEntry, error) {
	entry, exists := catalog.byClient[clientToolIdentity{
		kind:      call.EffectiveKind(),
		namespace: call.Namespace,
		name:      call.Name,
	}]
	if !exists {
		return providerToolEntry{}, errors.New(
			"tool call is not present in the request catalog",
		)
	}
	return entry, nil
}

func (catalog providerToolCatalog) clientEntryForProvider(
	providerName string,
) (providerToolEntry, error) {
	entry, exists := catalog.byProvider[providerName]
	if !exists {
		return providerToolEntry{}, errors.New(
			"provider tool name is not present in the request catalog",
		)
	}
	return entry, nil
}

func (catalog providerToolCatalog) namedEntry(
	name string,
) (providerToolEntry, error) {
	for _, entry := range catalog.entries {
		if entry.identity.namespace == "" &&
			entry.identity.name == name {
			return entry, nil
		}
	}
	return providerToolEntry{}, errors.New(
		"named tool choice is not present in the request catalog",
	)
}

func (entry providerToolEntry) providerArguments(
	call protocolcore.ToolCall,
) ([]byte, error) {
	switch entry.identity.kind {
	case protocolcore.ToolKindFunction:
		return call.Arguments.Bytes(), nil
	case protocolcore.ToolKindCustom:
		return json.Marshal(struct {
			Input string `json:"input"`
		}{Input: call.Input})
	default:
		return nil, errors.New("tool kind is unsupported")
	}
}

func (entry providerToolEntry) clientCall(
	key protocolcore.CallKey,
	arguments []byte,
	maxArgumentBytes int,
) (protocolcore.ToolCall, error) {
	switch entry.identity.kind {
	case protocolcore.ToolKindFunction:
		document, err := protocolcore.NewJSONObject(
			arguments,
			maxArgumentBytes,
		)
		if err != nil {
			return protocolcore.ToolCall{}, err
		}
		call := protocolcore.ToolCall{
			Kind:      protocolcore.ToolKindFunction,
			Key:       key,
			Namespace: entry.identity.namespace,
			Name:      entry.identity.name,
			Arguments: document,
		}
		return call, call.Validate()
	case protocolcore.ToolKindCustom:
		document, err := protocolcore.NewJSONObject(
			arguments,
			maxArgumentBytes,
		)
		if err != nil {
			return protocolcore.ToolCall{}, err
		}
		var wire struct {
			Input string `json:"input"`
		}
		if err := decodeStrict(document.Bytes(), &wire); err != nil {
			return protocolcore.ToolCall{}, err
		}
		call := protocolcore.ToolCall{
			Kind:      protocolcore.ToolKindCustom,
			Key:       key,
			Namespace: entry.identity.namespace,
			Name:      entry.identity.name,
			Input:     wire.Input,
		}
		return call, call.Validate()
	default:
		return protocolcore.ToolCall{}, errors.New("tool kind is unsupported")
	}
}

func isChatToolName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' ||
			character == '-' {
			continue
		}
		return false
	}
	return true
}

func hashedProviderToolName(identity clientToolIdentity) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		string(identity.kind),
		identity.namespace,
		identity.name,
	}, "\x00")))
	return "vm_tool_" + hex.EncodeToString(digest[:16])
}
