package nativetool

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"regexp"
	"sort"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// Catalog observations establish consistency of trusted Go inputs. Only the
// later authenticated transport producer can establish their network origin.
type CatalogScope struct {
	Capture                            captureidentity.Reference
	Plan                               environment.ClientToolPlan
	AssignmentRevision, OwnerEpoch     uint64
	SessionID                          ID
	SessionRevision, CatalogGeneration uint64
}
type CatalogPageObservation struct{ ExactRequest, ExactResponse []byte }
type CatalogObserver struct{ scope CatalogScope }
type catalogDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations map[string]any  `json:"annotations,omitempty"`
}
type CatalogEvidence struct {
	scope       CatalogScope
	definitions []catalogDefinition
	digest      Digest
	requests    []Digest
}

// CatalogDefinition exposes only the validated complete public projection.
// It carries no authenticated-source, decision or send capability.
type CatalogDefinition struct{ definition catalogDefinition }

func (d CatalogDefinition) Definition() protocolcore.ToolDefinition {
	schema, _ := protocolcore.NewJSONObject(d.definition.InputSchema, MaxRPCBytes)
	return protocolcore.ToolDefinition{Name: d.definition.Name, Description: d.definition.Description, InputSchema: schema}
}
func (d CatalogDefinition) Annotations() protocolcore.JSONDocument {
	encoded, _ := json.Marshal(d.definition.Annotations)
	if len(d.definition.Annotations) == 0 {
		encoded = []byte("{}")
	}
	document, _ := protocolcore.NewJSONObject(encoded, MaxRPCBytes)
	return document
}
func (d CatalogDefinition) Digest() Digest {
	if d.definition.Name == "" {
		return Digest{}
	}
	return hashJSON(struct {
		Domain     string
		Definition catalogDefinition
	}{"native-public-definition/v1", d.definition})
}
func cloneCatalogDefinition(d catalogDefinition) CatalogDefinition {
	d.InputSchema = bytes.Clone(d.InputSchema)
	annotations := make(map[string]any, len(d.Annotations))
	for key, value := range d.Annotations {
		annotations[key] = value
	}
	d.Annotations = annotations
	return CatalogDefinition{d}
}
func (e CatalogEvidence) Definitions() []CatalogDefinition {
	result := make([]CatalogDefinition, len(e.definitions))
	for index, d := range e.definitions {
		result[index] = cloneCatalogDefinition(d)
	}
	return result
}
func (e CatalogEvidence) Lookup(name string) (CatalogDefinition, bool) {
	for _, definition := range e.definitions {
		if definition.Name == name {
			return cloneCatalogDefinition(definition), true
		}
	}
	return CatalogDefinition{}, false
}

var publicName = regexp.MustCompile(`^[A-Za-z0-9_.:/-]+$`)

func validCatalogScope(scope CatalogScope) bool {
	_, err := NewID(string(scope.SessionID))
	return err == nil && scope.Capture.Validate() == nil && scope.Plan.Validate() == nil && revision(scope.AssignmentRevision) && revision(scope.OwnerEpoch) && revision(scope.SessionRevision) && revision(scope.CatalogGeneration)
}
func sameCatalogScope(a, b CatalogScope) bool {
	return a.Capture == b.Capture && a.Plan.EnvironmentID() == b.Plan.EnvironmentID() && a.Plan.EnvironmentRevision() == b.Plan.EnvironmentRevision() && a.Plan.EnvironmentDigest() == b.Plan.EnvironmentDigest() && a.Plan.Route() == b.Plan.Route() && a.Plan.ContentRecording() == b.Plan.ContentRecording() && a.Plan.PolicySet() == b.Plan.PolicySet() && a.AssignmentRevision == b.AssignmentRevision && a.OwnerEpoch == b.OwnerEpoch && a.SessionID == b.SessionID && a.SessionRevision == b.SessionRevision && a.CatalogGeneration == b.CatalogGeneration
}
func NewCatalogObserver(scope CatalogScope) (*CatalogObserver, error) {
	if !validCatalogScope(scope) {
		return nil, ErrInvalid
	}
	return &CatalogObserver{scope}, nil
}
func (e CatalogEvidence) Scope() CatalogScope { return e.scope }
func (e CatalogEvidence) Digest() Digest      { return e.digest }

func (observer *CatalogObserver) Observe(pages []CatalogPageObservation) (CatalogEvidence, error) {
	if observer == nil || !validCatalogScope(observer.scope) || len(pages) == 0 || len(pages) > 32 {
		return CatalogEvidence{}, ErrInvalid
	}
	total := 0
	cursor := ""
	definitions := make([]catalogDefinition, 0)
	seen := map[string]bool{}
	cursors := map[string]bool{}
	requests := []Digest{}
	requestSet := map[Digest]bool{}
	for index, page := range pages {
		total += len(page.ExactRequest) + len(page.ExactResponse)
		if total > 8<<20 {
			return CatalogEvidence{}, ErrInvalid
		}
		request, err := rpcObject(page.ExactRequest)
		if err != nil {
			return CatalogEvidence{}, err
		}
		response, err := rpcObject(page.ExactResponse)
		if err != nil {
			return CatalogEvidence{}, err
		}
		var method string
		requestCursor := ""
		if json.Unmarshal(request["method"], &method) != nil || method != "tools/list" || !sameRPCID(request["id"], response["id"]) || len(request["result"]) > 0 || len(request["error"]) > 0 || len(response["method"]) > 0 || len(response["error"]) > 0 {
			return CatalogEvidence{}, ErrInvalid
		}
		for key := range request {
			if key != "jsonrpc" && key != "id" && key != "method" && key != "params" && key != "_meta" {
				return CatalogEvidence{}, ErrDenied
			}
		}
		if raw := request["params"]; len(raw) > 0 {
			if _, err := canonicalObject(raw); err != nil {
				return CatalogEvidence{}, ErrInvalid
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			for key := range fields {
				if key != "cursor" && key != "_meta" {
					return CatalogEvidence{}, ErrDenied
				}
			}
			if value, present := fields["cursor"]; present && json.Unmarshal(value, &requestCursor) != nil {
				return CatalogEvidence{}, ErrInvalid
			}
		}
		if requestCursor != cursor {
			return CatalogEvidence{}, ErrInvalid
		}
		requestDigest := Digest(sha256.Sum256(page.ExactRequest))
		if requestSet[requestDigest] {
			return CatalogEvidence{}, ErrInvalid
		}
		requestSet[requestDigest] = true
		requests = append(requests, requestDigest)
		// Wire fields are exact-key protocol data. Struct decoding would treat
		// case-only extensions as canonical fields before public projection.
		var result map[string]json.RawMessage
		if _, err := canonicalObject(response["result"]); err != nil || json.Unmarshal(response["result"], &result) != nil {
			return CatalogEvidence{}, ErrInvalid
		}
		var tools []json.RawMessage
		if json.Unmarshal(result["tools"], &tools) != nil || tools == nil {
			return CatalogEvidence{}, ErrInvalid
		}
		nextCursor := ""
		if value, present := result["nextCursor"]; present && json.Unmarshal(value, &nextCursor) != nil {
			return CatalogEvidence{}, ErrInvalid
		}
		if (nextCursor == "") != (index == len(pages)-1) || len(nextCursor) > MaxObservedIDBytes || (nextCursor != "" && cursors[nextCursor]) {
			return CatalogEvidence{}, ErrInvalid
		}
		cursors[nextCursor] = true
		cursor = nextCursor
		for _, raw := range tools {
			var tool map[string]json.RawMessage
			var name, description string
			if json.Unmarshal(raw, &tool) != nil || json.Unmarshal(tool["name"], &name) != nil || len(name) > 256 || !publicName.MatchString(name) || seen[name] {
				return CatalogEvidence{}, ErrInvalid
			}
			if value, present := tool["description"]; present && json.Unmarshal(value, &description) != nil {
				return CatalogEvidence{}, ErrInvalid
			}
			schema, err := publicCatalogSchema(tool["inputSchema"])
			if err != nil {
				return CatalogEvidence{}, ErrInvalid
			}
			var annotations map[string]json.RawMessage
			if value, present := tool["annotations"]; present && json.Unmarshal(value, &annotations) != nil {
				return CatalogEvidence{}, ErrInvalid
			}
			definition := catalogDefinition{Name: name, Description: description, InputSchema: schema, Annotations: map[string]any{}}
			for _, name := range []string{"title", "readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
				value, ok := annotations[name]
				if !ok {
					continue
				}
				if name == "title" {
					var s string
					if json.Unmarshal(value, &s) != nil {
						return CatalogEvidence{}, ErrInvalid
					}
					definition.Annotations[name] = s
				} else {
					var b bool
					if json.Unmarshal(value, &b) != nil {
						return CatalogEvidence{}, ErrInvalid
					}
					definition.Annotations[name] = b
				}
			}
			mapped, err := protocolcore.NewJSONObject(schema, MaxRPCBytes)
			if err != nil || (protocolcore.ToolDefinition{Name: name, Description: description, InputSchema: mapped}).Validate() != nil {
				return CatalogEvidence{}, ErrInvalid
			}
			seen[name] = true
			definitions = append(definitions, definition)
			if len(definitions) > 1024 {
				return CatalogEvidence{}, ErrInvalid
			}
		}
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	digest := hashJSON(struct {
		Domain string
		Tools  []catalogDefinition
	}{"native-public-catalog/v1", definitions})
	return CatalogEvidence{observer.scope, definitions, digest, requests}, nil
}

// MatchesRequests checks complete page coverage against separate persisted
// list bindings. Extra candidates never invent missing evidence pages.
func (e CatalogEvidence) MatchesRequests(candidates []Digest) bool {
	if e.digest == (Digest{}) || len(e.requests) == 0 {
		return false
	}
	seen := map[Digest]bool{}
	for _, d := range candidates {
		seen[d] = true
	}
	for _, d := range e.requests {
		if !seen[d] {
			return false
		}
	}
	return true
}
func rpcObject(raw []byte) (map[string]json.RawMessage, error) {
	if _, err := protocolcore.NewJSONObject(raw, MaxRPCBytes); err != nil {
		return nil, ErrInvalid
	}
	var object map[string]json.RawMessage
	_ = json.Unmarshal(raw, &object)
	var version string
	if json.Unmarshal(object["jsonrpc"], &version) != nil || version != "2.0" {
		return nil, ErrInvalid
	}
	return object, nil
}
func sameRPCID(a, b []byte) bool {
	if !validRPCID(a) || !validRPCID(b) {
		return false
	}
	decode := func(raw []byte) any {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var v any
		_ = d.Decode(&v)
		return v
	}
	return decode(a) == decode(b)
}
func (e CatalogEvidence) member(input BindingInput) (string, error) {
	scope := CatalogScope{input.Capture, input.Plan, input.AssignmentRevision, input.OwnerEpoch, input.SessionID, input.SessionRevision, input.CatalogGeneration}
	if e.digest == (Digest{}) || input.CatalogDigest == nil || *input.CatalogDigest != e.digest || !sameCatalogScope(e.scope, scope) {
		return "", ErrInvalid
	}
	for _, definition := range e.definitions {
		if definition.Name == input.Intents[0].Call.Name {
			return definition.Name, nil
		}
	}
	return "", ErrInvalid
}

// Project only explicitly supported JSON-Schema positions. A property named
// _meta is a literal property, while _meta at a schema keyword position is
// extension metadata and is suppressed. Unknown keyword semantics reject
// before the public catalog hash; this is not free-text secret detection.
func publicCatalogSchema(raw []byte) ([]byte, error) {
	if _, err := canonicalObject(raw); err != nil {
		return nil, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var schema any
	if decoder.Decode(&schema) != nil {
		return nil, ErrInvalid
	}
	projected, err := projectCatalogSchema(schema, 0)
	if err != nil {
		return nil, err
	}
	return json.Marshal(projected)
}
func projectCatalogSchema(value any, depth int) (any, error) {
	if depth > 64 {
		return nil, ErrInvalid
	}
	if b, ok := value.(bool); ok {
		return b, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, ErrInvalid
	}
	result := map[string]any{}
	for keyword, value := range object {
		switch keyword {
		case "_meta":
			continue
		case "$schema":
			s, ok := value.(string)
			if !ok || (s != "http://json-schema.org/draft-07/schema#" && s != "https://json-schema.org/draft/2019-09/schema" && s != "https://json-schema.org/draft/2020-12/schema") {
				return nil, ErrInvalid
			}
			result[keyword] = s
		case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas":
			children, ok := value.(map[string]any)
			if !ok {
				return nil, ErrInvalid
			}
			mapped := map[string]any{}
			for name, child := range children {
				projected, err := projectCatalogSchema(child, depth+1)
				if err != nil {
					return nil, err
				}
				mapped[name] = projected
			}
			result[keyword] = mapped
		case "additionalProperties", "unevaluatedProperties", "propertyNames", "contains", "not", "if", "then", "else", "additionalItems", "unevaluatedItems":
			projected, err := projectCatalogSchema(value, depth+1)
			if err != nil {
				return nil, err
			}
			result[keyword] = projected
		case "items":
			if children, ok := value.([]any); ok {
				mapped := []any{}
				for _, child := range children {
					projected, err := projectCatalogSchema(child, depth+1)
					if err != nil {
						return nil, err
					}
					mapped = append(mapped, projected)
				}
				result[keyword] = mapped
			} else {
				projected, err := projectCatalogSchema(value, depth+1)
				if err != nil {
					return nil, err
				}
				result[keyword] = projected
			}
		case "allOf", "anyOf", "oneOf", "prefixItems":
			children, ok := value.([]any)
			if !ok || len(children) == 0 {
				return nil, ErrInvalid
			}
			mapped := []any{}
			for _, child := range children {
				projected, err := projectCatalogSchema(child, depth+1)
				if err != nil {
					return nil, err
				}
				mapped = append(mapped, projected)
			}
			result[keyword] = mapped
		case "type":
			valid := func(v any) bool {
				s, ok := v.(string)
				if !ok {
					return false
				}
				switch s {
				case "object", "array", "string", "number", "integer", "boolean", "null":
					return true
				}
				return false
			}
			if types, ok := value.([]any); ok {
				if len(types) == 0 {
					return nil, ErrInvalid
				}
				seen := map[any]bool{}
				for _, v := range types {
					if !valid(v) || seen[v] {
						return nil, ErrInvalid
					}
					seen[v] = true
				}
			} else if !valid(value) {
				return nil, ErrInvalid
			}
			result[keyword] = value
		case "$id", "$ref", "$anchor", "$dynamicRef", "$dynamicAnchor", "$comment", "title", "description", "format", "pattern", "contentEncoding", "contentMediaType":
			if _, ok := value.(string); !ok {
				return nil, ErrInvalid
			}
			result[keyword] = value
		case "required":
			values, ok := value.([]any)
			if !ok {
				return nil, ErrInvalid
			}
			seen := map[string]bool{}
			for _, v := range values {
				s, ok := v.(string)
				if !ok || seen[s] {
					return nil, ErrInvalid
				}
				seen[s] = true
			}
			result[keyword] = value
		case "enum":
			values, ok := value.([]any)
			if !ok || len(values) == 0 {
				return nil, ErrInvalid
			}
			result[keyword] = values
		case "const", "default", "examples":
			result[keyword] = value
		case "readOnly", "writeOnly", "deprecated", "uniqueItems":
			if _, ok := value.(bool); !ok {
				return nil, ErrInvalid
			}
			result[keyword] = value
		case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf":
			if _, ok := value.(json.Number); !ok {
				return nil, ErrInvalid
			}
			result[keyword] = value
		case "minLength", "maxLength", "minItems", "maxItems", "minProperties", "maxProperties", "minContains", "maxContains":
			n, ok := value.(json.Number)
			if !ok {
				return nil, ErrInvalid
			}
			i, err := n.Int64()
			if err != nil || i < 0 {
				return nil, ErrInvalid
			}
			result[keyword] = value
		case "dependentRequired":
			fields, ok := value.(map[string]any)
			if !ok {
				return nil, ErrInvalid
			}
			for _, v := range fields {
				values, ok := v.([]any)
				if !ok {
					return nil, ErrInvalid
				}
				for _, entry := range values {
					if _, ok := entry.(string); !ok {
						return nil, ErrInvalid
					}
				}
			}
			result[keyword] = fields
		default:
			return nil, ErrInvalid
		}
	}
	return result, nil
}
