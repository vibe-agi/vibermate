package toolpolicy

import (
	"bytes"
	"encoding/json"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// recognizedToolShape is what a publisher-recognized Claude Code's built-in
// file tool must declare, in the request itself, before the classifier may
// approve a call to it. Captured from the 2.1.329 wire and re-checked against
// the newest release daily by TestInstalledClaudeWorkspaceToolsMatchTheClassifier.
//
// The property set is a reviewed union across proven releases. A new name
// enters only after review confirms it is path-neutral: it does not name a
// file, URL or command, and it does not widen which files the tool touches.
type recognizedToolShape struct {
	required   []string            // every name must be in the definition's required set
	properties map[string][]string // the closed set of property names -> accepted JSON types
	mutating   bool                // the tool changes the file it names
}

var recognizedToolShapes = map[string]recognizedToolShape{
	// Read's offset and limit also accept "number": the 2.1.329 wire says
	// integer, and a numeric widening is harmless.
	"Read": {required: []string{"file_path"}, properties: map[string][]string{
		"file_path": {"string"}, "offset": {"integer", "number"}, "limit": {"integer", "number"},
		"pages": {"string"}, "allow_large": {"boolean"}}},
	"Edit": {required: []string{"file_path", "old_string", "new_string"}, mutating: true,
		properties: map[string][]string{"file_path": {"string"}, "old_string": {"string"},
			"new_string": {"string"}, "replace_all": {"boolean"}}},
	"Write": {required: []string{"file_path", "content"}, mutating: true,
		properties: map[string][]string{"file_path": {"string"}, "content": {"string"}}},
	"NotebookEdit": {required: []string{"notebook_path", "new_source"}, mutating: true,
		properties: map[string][]string{"notebook_path": {"string"}, "cell_id": {"string"},
			"new_source": {"string"}, "cell_type": {"string"}, "edit_mode": {"string"}}},
	// Glob and Grep are deliberately absent. They join only with a committed
	// wire capture, a canary that exercises them, and a required absolute path.
}

// recognizedSchemaKeys are the only top-level input_schema keys a recognized
// definition may use. Any other key (anyOf, oneOf, allOf, if,
// patternProperties, dependentSchemas and so on) fails closed.
var recognizedSchemaKeys = map[string]struct{}{
	"$schema":              {},
	"type":                 {},
	"properties":           {},
	"required":             {},
	"additionalProperties": {},
	"description":          {},
	"title":                {},
}

// recognizedBuiltInCall proves one call of a recognized client's group: the
// tool has a recognized shape, the request's own definition is closed to that
// shape, and every argument the call uses is one the shape lists and the
// definition declares. The classifier reads only the path and patterns, so an
// argument it never examined must not ride along.
func recognizedBuiltInCall(
	definition protocolcore.ToolDefinition,
	intent protocolcore.ToolIntent,
) bool {
	shape, known := recognizedToolShapes[intent.Call.Name]
	if !known {
		return false
	}
	declared, ok := recognizedDefinition(shape, definition)
	if !ok {
		return false
	}
	var arguments map[string]json.RawMessage
	if json.Unmarshal(intent.Call.Arguments.Bytes(), &arguments) != nil ||
		arguments == nil {
		return false
	}
	for name := range arguments {
		if _, allowed := shape.properties[name]; !allowed {
			return false
		}
		if _, present := declared[name]; !present {
			return false
		}
	}
	return true
}

// recognizedDefinition reports whether definition has the closed shape and
// returns the property names it declares. The input_schema is decoded key by
// key, so a missing additionalProperties is told apart from false.
func recognizedDefinition(
	shape recognizedToolShape,
	definition protocolcore.ToolDefinition,
) (map[string]struct{}, bool) {
	var schema map[string]json.RawMessage
	if json.Unmarshal(definition.InputSchema.Bytes(), &schema) != nil ||
		schema == nil {
		return nil, false
	}
	for key := range schema {
		if _, allowed := recognizedSchemaKeys[key]; !allowed {
			return nil, false
		}
	}
	var schemaType string
	if json.Unmarshal(schema["type"], &schemaType) != nil ||
		schemaType != "object" {
		return nil, false
	}
	if !bytes.Equal(bytes.TrimSpace(schema["additionalProperties"]), []byte("false")) {
		return nil, false
	}
	var properties map[string]json.RawMessage
	if json.Unmarshal(schema["properties"], &properties) != nil {
		return nil, false
	}
	declared := make(map[string]struct{}, len(properties))
	for name, rawProperty := range properties {
		accepted, allowed := shape.properties[name]
		if !allowed {
			return nil, false
		}
		var property map[string]json.RawMessage
		if json.Unmarshal(rawProperty, &property) != nil || property == nil {
			return nil, false
		}
		// A missing type, or a type array, fails: the declared type must be
		// one JSON string the shape accepts.
		var propertyType string
		if json.Unmarshal(property["type"], &propertyType) != nil ||
			!acceptedType(accepted, propertyType) {
			return nil, false
		}
		declared[name] = struct{}{}
	}
	var required []string
	if json.Unmarshal(schema["required"], &required) != nil {
		return nil, false
	}
	requiredSet := make(map[string]struct{}, len(required))
	for _, name := range required {
		if _, present := declared[name]; !present {
			return nil, false
		}
		requiredSet[name] = struct{}{}
	}
	for _, name := range shape.required {
		if _, present := requiredSet[name]; !present {
			return nil, false
		}
	}
	return declared, true
}

func acceptedType(accepted []string, propertyType string) bool {
	for _, candidate := range accepted {
		if candidate == propertyType {
			return true
		}
	}
	return false
}
