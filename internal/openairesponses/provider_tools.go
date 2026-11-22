package openairesponses

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// providerToolName is the name a client tool has on the Responses wire. A
// name the API accepts is kept, as every Claude Code built-in is; any other
// becomes vm_tool_ and 32 hex digits of SHA-256 over kind, namespace and
// name. It is the Chat catalog's rule as a pure function, so a call in the
// history maps to the same name whether or not its tool is still declared.
func providerToolName(kind protocolcore.ToolKind, namespace, name string) string {
	candidate := name
	if namespace != "" {
		candidate = namespace + "__" + name
	}
	if validProviderToolName(candidate) {
		return candidate
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{string(kind), namespace, name}, "\x00")))
	return "vm_tool_" + hex.EncodeToString(digest[:16])
}

// validProviderToolName is the Responses function name rule,
// ^[A-Za-z0-9_-]{1,64}$.
func validProviderToolName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// providerCallID is the call_id a client call and its result carry upstream.
// An id the API accepts passes unchanged, so ids this path minted round-trip;
// any other becomes call_vm_ and 24 hex digits of SHA-256, the same for the
// call and its output. rewritten reports the second case.
func providerCallID(wireID string) (callID string, rewritten bool) {
	if validProviderToolName(wireID) {
		return wireID, false
	}
	digest := sha256.Sum256([]byte("vibermate/call-id\x00" + wireID))
	return "call_vm_" + hex.EncodeToString(digest[:12]), true
}

// structuredOutputName names a json_schema text format with the Chat encoder's
// algorithm, which satisfies the Responses name rule.
func structuredOutputName(schema []byte) string {
	digest := sha256.Sum256(schema)
	return fmt.Sprintf("vibermate_output_%x", digest[:12])
}

// planTool is one function tool the body declares.
type planTool struct {
	name        string
	description string
	parameters  []byte
	strict      bool
}

// planTools lays out the tools the body declares: Request.LoadedTools() in
// request order (design M6, 6.5). A deferred tool nothing has loaded yet is
// left out and declared; a top-level $schema or $id is removed from each
// schema. Two tools that would share a provider name fail closed.
func planTools(request protocolcore.Request, notices *planNotices) ([]planTool, map[string]string, error) {
	if len(request.ToolNamespaces) != 0 {
		return nil, nil, protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedClientInput,
			"$.tool_namespaces",
			errors.New("tool namespaces cannot be represented by the configured provider dialect"),
		)
	}
	loaded := request.LoadedTools()
	sent := make(map[string]struct{}, len(loaded))
	for _, tool := range loaded {
		sent[tool.Name] = struct{}{}
	}
	tools := make([]planTool, 0, len(loaded))
	// byClient maps each declared client name to its provider name.
	byClient := make(map[string]string, len(loaded))
	byProvider := make(map[string]struct{}, len(loaded))
	normalized := false
	for index, tool := range request.Tools {
		path := fmt.Sprintf("$.tools[%d]", index)
		if _, ok := sent[tool.Name]; !ok {
			if err := notices.add(protocolcore.NoticeDeferredToolNotForwarded, path); err != nil {
				return nil, nil, err
			}
			continue
		}
		if tool.EffectiveKind() != protocolcore.ToolKindFunction {
			return nil, nil, protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				path,
				errors.New("tool kind cannot be represented by the configured provider dialect"),
			)
		}
		name := providerToolName(protocolcore.ToolKindFunction, "", tool.Name)
		if _, collision := byProvider[name]; collision {
			return nil, nil, protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedClientInput,
				path+".name",
				errors.New("provider tool name collides with another tool"),
			)
		}
		byProvider[name] = struct{}{}
		byClient[tool.Name] = name
		parameters, removed, err := removeTopLevelSchemaIdentity(tool.InputSchema.Bytes())
		if err != nil {
			return nil, nil, protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, path+".input_schema", err)
		}
		normalized = normalized || removed
		tools = append(tools, planTool{
			name:        name,
			description: tool.Description,
			parameters:  parameters,
			strict:      tool.StrictKnown && tool.Strict,
		})
		if tool.EagerInputStreaming {
			if err := notices.add(protocolcore.NoticeEagerToolInputStreamingNotForwarded, path+".eager_input_streaming"); err != nil {
				return nil, nil, err
			}
		}
	}
	if normalized {
		if err := notices.add(protocolcore.NoticeToolSchemaNormalized, "$.tools"); err != nil {
			return nil, nil, err
		}
	}
	return tools, byClient, nil
}

// removeTopLevelSchemaIdentity removes the top-level $schema and $id members
// of an object schema by splicing its bytes: every other member keeps its
// bytes and order, and nested schemas are untouched. removed reports a
// change.
func removeTopLevelSchemaIdentity(schema []byte) ([]byte, bool, error) {
	members, err := topLevelMembers(schema)
	if err != nil {
		return nil, false, err
	}
	kept := members[:0:0]
	for _, member := range members {
		identity, err := schemaIdentityMember(schema[member.start:member.nameEnd])
		if err != nil {
			return nil, false, err
		}
		if !identity {
			kept = append(kept, member)
		}
	}
	if len(kept) == len(members) {
		return schema, false, nil
	}
	spliced := make([]byte, 0, len(schema))
	spliced = append(spliced, '{')
	for index, member := range kept {
		if index > 0 {
			spliced = append(spliced, ',')
		}
		spliced = append(spliced, schema[member.start:member.end]...)
	}
	spliced = append(spliced, '}')
	return spliced, true, nil
}

// jsonMember is one top-level member of an object: the byte range from its
// name's opening quote to the end of its value, and the end of its name.
type jsonMember struct {
	start, nameEnd, end int
}

// schemaIdentityMember reports a member named $schema or $id. A name without
// escapes is compared as it stands; only an escaped one is decoded.
func schemaIdentityMember(quoted []byte) (bool, error) {
	if !bytes.ContainsRune(quoted, '\\') {
		name := quoted[1 : len(quoted)-1]
		return string(name) == "$schema" || string(name) == "$id", nil
	}
	var name string
	if err := json.Unmarshal(quoted, &name); err != nil {
		return false, err
	}
	return name == "$schema" || name == "$id", nil
}

// topLevelMembers scans one JSON object without decoding its values.
func topLevelMembers(document []byte) ([]jsonMember, error) {
	position := skipJSONSpace(document, 0)
	if position >= len(document) || document[position] != '{' {
		return nil, errors.New("schema is not a JSON object")
	}
	position = skipJSONSpace(document, position+1)
	var members []jsonMember
	if position < len(document) && document[position] == '}' {
		if skipJSONSpace(document, position+1) != len(document) {
			return nil, errors.New("schema has trailing data")
		}
		return members, nil
	}
	for {
		if position >= len(document) || document[position] != '"' {
			return nil, errors.New("schema member name is invalid")
		}
		start := position
		nameEnd, err := skipJSONString(document, position)
		if err != nil {
			return nil, err
		}
		position = skipJSONSpace(document, nameEnd)
		if position >= len(document) || document[position] != ':' {
			return nil, errors.New("schema member has no value")
		}
		position = skipJSONSpace(document, position+1)
		valueEnd, err := skipJSONValue(document, position)
		if err != nil {
			return nil, err
		}
		members = append(members, jsonMember{start: start, nameEnd: nameEnd, end: valueEnd})
		position = skipJSONSpace(document, valueEnd)
		if position >= len(document) {
			return nil, errors.New("schema object is not closed")
		}
		switch document[position] {
		case ',':
			position = skipJSONSpace(document, position+1)
		case '}':
			if skipJSONSpace(document, position+1) != len(document) {
				return nil, errors.New("schema has trailing data")
			}
			return members, nil
		default:
			return nil, errors.New("schema object is malformed")
		}
	}
}

func skipJSONSpace(document []byte, position int) int {
	for position < len(document) {
		switch document[position] {
		case ' ', '\t', '\r', '\n':
			position++
		default:
			return position
		}
	}
	return position
}

// skipJSONString returns the index just past the string that starts at
// position.
func skipJSONString(document []byte, position int) (int, error) {
	for index := position + 1; index < len(document); index++ {
		switch document[index] {
		case '\\':
			index++
		case '"':
			return index + 1, nil
		}
	}
	return 0, errors.New("schema string is not closed")
}

// skipJSONValue returns the index just past the value that starts at
// position. The document was validated as JSON when it was decoded, so only
// strings and nesting need tracking.
func skipJSONValue(document []byte, position int) (int, error) {
	if position >= len(document) {
		return 0, errors.New("schema value is missing")
	}
	switch document[position] {
	case '"':
		return skipJSONString(document, position)
	case '{', '[':
		depth := 0
		for index := position; index < len(document); index++ {
			switch document[index] {
			case '"':
				end, err := skipJSONString(document, index)
				if err != nil {
					return 0, err
				}
				index = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return index + 1, nil
				}
			}
		}
		return 0, errors.New("schema value is not closed")
	default:
		index := position
		for index < len(document) {
			switch document[index] {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				return index, nil
			}
			index++
		}
		return index, nil
	}
}

// strictSchemaCompatible reports a schema the API can enforce strictly: every
// object node has additionalProperties false and requires each of its
// properties (design 6.7).
func strictSchemaCompatible(schema []byte) bool {
	var root any
	if err := json.Unmarshal(schema, &root); err != nil {
		return false
	}
	return strictNode(root)
}

func strictNode(node any) bool {
	switch value := node.(type) {
	case []any:
		for _, item := range value {
			if !strictNode(item) {
				return false
			}
		}
		return true
	case map[string]any:
	default:
		return true
	}
	object := node.(map[string]any)
	properties, hasProperties := object["properties"].(map[string]any)
	if objectSchema(object) || hasProperties {
		if additional, ok := object["additionalProperties"].(bool); !ok || additional {
			return false
		}
		required := map[string]struct{}{}
		if list, ok := object["required"].([]any); ok {
			for _, item := range list {
				if name, ok := item.(string); ok {
					required[name] = struct{}{}
				}
			}
		}
		for name := range properties {
			if _, ok := required[name]; !ok {
				return false
			}
		}
	}
	for _, property := range properties {
		if !strictNode(property) {
			return false
		}
	}
	for _, keyword := range []string{"items", "prefixItems", "anyOf", "oneOf", "allOf", "not"} {
		if child, ok := object[keyword]; ok && !strictNode(child) {
			return false
		}
	}
	for _, keyword := range []string{"$defs", "definitions"} {
		if definitions, ok := object[keyword].(map[string]any); ok {
			for _, definition := range definitions {
				if !strictNode(definition) {
					return false
				}
			}
		}
	}
	return true
}

func objectSchema(object map[string]any) bool {
	switch kind := object["type"].(type) {
	case string:
		return kind == "object"
	case []any:
		for _, item := range kind {
			if item == "object" {
				return true
			}
		}
	}
	return false
}
