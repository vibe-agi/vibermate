package toolpolicy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

const capturedFileToolsPath = "testdata/claude-code-2.1.329-file-tools.json"

type toolCall struct {
	name      string
	arguments string
}

// capturedDefinitions are Claude Code 2.1.329's Read, Edit, Write and
// NotebookEdit exactly as the wire carried them, minus descriptions.
func capturedDefinitions(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(capturedFileToolsPath)
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Name        string          `json:"name"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	definitions := make(map[string]json.RawMessage, len(entries))
	for _, entry := range entries {
		definitions[entry.Name] = entry.InputSchema
	}
	for _, name := range []string{"Read", "Edit", "Write", "NotebookEdit"} {
		if _, present := definitions[name]; !present {
			t.Fatalf("%s lacks %s", capturedFileToolsPath, name)
		}
	}
	if len(definitions) != len(recognizedToolShapes) {
		t.Fatalf("%s holds %d tools, the shapes %d", capturedFileToolsPath, len(definitions), len(recognizedToolShapes))
	}
	return definitions
}

func toolDefinitions(t *testing.T, schemas map[string]json.RawMessage) []protocolcore.ToolDefinition {
	t.Helper()
	definitions := make([]protocolcore.ToolDefinition, 0, len(schemas))
	for name, raw := range schemas {
		schema, err := protocolcore.NewJSONObject(raw, protocolcore.MaxToolJSONBytes)
		if err != nil {
			t.Fatalf("%s schema: %v", name, err)
		}
		definitions = append(definitions, protocolcore.ToolDefinition{Name: name, InputSchema: schema})
	}
	return definitions
}

func basisRequest(
	t *testing.T,
	mode environment.ToolPolicyMode,
	root string,
	basis clientadapter.ProtectionBasis,
	schemas map[string]json.RawMessage,
	calls []toolCall,
) exchange.ToolDecisionRequest {
	t.Helper()
	decisionContext, err := exchange.NewToolDecisionContextForBasis(
		environment.PolicySet{ToolMode: mode}, root, basis, toolDefinitions(t, schemas), nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	intents := make([]protocolcore.ToolIntent, 0, len(calls))
	for index, call := range calls {
		arguments, err := protocolcore.NewJSONObject([]byte(call.arguments), protocolcore.MaxToolJSONBytes)
		if err != nil {
			t.Fatalf("%s arguments: %v", call.name, err)
		}
		key, err := protocolcore.NewCallKey("anthropic-messages", "tool-call-"+string(rune('a'+index)))
		if err != nil {
			t.Fatal(err)
		}
		intents = append(intents, protocolcore.ToolIntent{
			ResponseID: "response-tools", Ordinal: index,
			Call: protocolcore.ToolCall{Key: key, Name: call.name, Arguments: arguments},
		})
	}
	environmentID, err := environment.NewEnvironmentID("environment-tools")
	if err != nil {
		t.Fatal(err)
	}
	routeID, err := environment.NewUpstreamRouteID("route-tools")
	if err != nil {
		t.Fatal(err)
	}
	request, err := exchange.NewToolDecisionRequest(
		"exchange-tools", environmentID, 1, environment.CandidateDigest{1},
		routeID, 1, decisionContext, intents,
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

// approvedWithoutQuestion runs one group through the gate in Review and in
// Strict and reports whether it was classified safe. Both modes must agree:
// safe is approved with no review call in Review and allowed in Strict;
// anything else reaches review in Review and is rejected in Strict.
func approvedWithoutQuestion(
	t *testing.T,
	root string,
	basis clientadapter.ProtectionBasis,
	schemas map[string]json.RawMessage,
	calls ...toolCall,
) bool {
	t.Helper()
	review := &reviewDouble{}
	gate, err := New(review)
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := gate.Decide(context.Background(), basisRequest(t, environment.ToolPolicyReview, root, basis, schemas, calls))
	if err != nil {
		t.Fatal(err)
	}
	strict, err := gate.Decide(context.Background(), basisRequest(t, environment.ToolPolicyStrict, root, basis, schemas, calls))
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case reviewed.Outcome == exchange.ToolDecisionApproved && review.calls == 0 &&
		strict.Outcome == exchange.ToolDecisionApproved && strict.ReasonCode == "":
		return true
	case review.calls == 1 &&
		strict.Outcome == exchange.ToolDecisionRejected && strict.ReasonCode == ReasonStrictPolicy:
		return false
	default:
		t.Fatalf("Review %+v with %d review calls and Strict %+v disagree", reviewed, review.calls, strict)
		return false
	}
}

// workspaceRoot is a resolved temporary directory, so absolute paths under it
// stay lexically inside the gate's resolved root on every platform.
func workspaceRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeWorkspaceFile(t *testing.T, root, relative, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func argumentsJSON(t *testing.T, values map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// mutateSchema decodes one captured schema, lets change edit it, and encodes
// it again.
func mutateSchema(t *testing.T, raw json.RawMessage, change func(map[string]any)) json.RawMessage {
	t.Helper()
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	change(schema)
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func schemaProperties(schema map[string]any) map[string]any {
	return schema["properties"].(map[string]any)
}

func withSchema(schemas map[string]json.RawMessage, name string, schema json.RawMessage) map[string]json.RawMessage {
	changed := make(map[string]json.RawMessage, len(schemas))
	for key, value := range schemas {
		changed[key] = value
	}
	changed[name] = schema
	return changed
}

func TestRecognizedToolShapesMatchTheCapturedDefinitions(t *testing.T) {
	t.Parallel()

	for name, raw := range capturedDefinitions(t) {
		shape, known := recognizedToolShapes[name]
		if !known {
			t.Fatalf("captured %s has no recognized shape", name)
		}
		schema, err := protocolcore.NewJSONObject(raw, protocolcore.MaxToolJSONBytes)
		if err != nil {
			t.Fatal(err)
		}
		declared, ok := recognizedDefinition(shape, protocolcore.ToolDefinition{Name: name, InputSchema: schema})
		if !ok || len(declared) != len(shape.properties) {
			t.Fatalf("captured %s: closed shape %v, declared %v", name, ok, declared)
		}
	}
	raw, err := os.ReadFile(capturedFileToolsPath)
	if err != nil {
		t.Fatal(err)
	}
	for index, character := range raw {
		if character > 0x7e || (character < 0x20 && character != '\n') {
			t.Fatalf("%s byte %d is not printable ASCII", capturedFileToolsPath, index)
		}
	}
	if strings.Contains(string(raw), `"description"`) {
		t.Fatalf("%s keeps a description", capturedFileToolsPath)
	}
}

func TestRecognizedBasisApprovesTheCapturedDefinitions(t *testing.T) {
	t.Parallel()

	root := workspaceRoot(t)
	notes := writeWorkspaceFile(t, root, "notes.txt", "hello\n")
	notebook := writeWorkspaceFile(t, root, "analysis.ipynb", `{"cells":[]}`)
	created := filepath.Join(root, "src", "new.go")
	if err := os.MkdirAll(filepath.Dir(created), 0o700); err != nil {
		t.Fatal(err)
	}
	read := toolCall{"Read", argumentsJSON(t, map[string]any{
		"file_path": notes, "offset": 1, "limit": 20, "pages": "1", "allow_large": true,
	})}
	edit := toolCall{"Edit", argumentsJSON(t, map[string]any{
		"file_path": notes, "old_string": "hello", "new_string": "hi", "replace_all": false,
	})}
	write := toolCall{"Write", argumentsJSON(t, map[string]any{"file_path": created, "content": "package src\n"})}
	notebookEdit := toolCall{"NotebookEdit", argumentsJSON(t, map[string]any{
		"notebook_path": notebook, "new_source": "print(1)", "cell_id": "a",
		"cell_type": "code", "edit_mode": "insert",
	})}
	minimalRead := toolCall{"Read", argumentsJSON(t, map[string]any{"file_path": notes})}
	schemas := capturedDefinitions(t)
	for name, calls := range map[string][]toolCall{
		"Read":           {read},
		"Read minimal":   {minimalRead},
		"Edit":           {edit},
		"Write":          {write},
		"NotebookEdit":   {notebookEdit},
		"the four tools": {read, edit, write, notebookEdit},
	} {
		t.Run(name, func(t *testing.T) {
			if !approvedWithoutQuestion(t, root, clientadapter.ProtectionRecognizedClient, schemas, calls...) {
				t.Fatal("the recognized basis asked about the captured definitions")
			}
			if !approvedWithoutQuestion(t, root, clientadapter.ProtectionExactRelease, schemas, calls...) {
				t.Fatal("the exact basis asked")
			}
			if approvedWithoutQuestion(t, root, clientadapter.ProtectionNone, schemas, calls...) {
				t.Fatal("no basis approved a structured tool")
			}
		})
	}
}

func TestRecognizedBasisRefusesAnyChangedDefinition(t *testing.T) {
	t.Parallel()

	root := workspaceRoot(t)
	notes := writeWorkspaceFile(t, root, "notes.txt", "hello\n")
	captured := capturedDefinitions(t)
	readCall := toolCall{"Read", argumentsJSON(t, map[string]any{"file_path": notes})}
	editCall := toolCall{"Edit", argumentsJSON(t, map[string]any{
		"file_path": notes, "old_string": "hello", "new_string": "hi",
	})}
	for _, testCase := range []struct {
		name   string
		tool   string
		call   toolCall
		change func(map[string]any)
		// exact is today's outcome on the exact basis, which reads only the
		// path field and must not change.
		exact bool
	}{
		{"an extra property", "Read", readCall, func(schema map[string]any) {
			schemaProperties(schema)["encoding"] = map[string]any{"type": "string"}
		}, true},
		{"additionalProperties missing", "Read", readCall, func(schema map[string]any) {
			delete(schema, "additionalProperties")
		}, true},
		{"additionalProperties true", "Read", readCall, func(schema map[string]any) {
			schema["additionalProperties"] = true
		}, true},
		{"additionalProperties an object", "Read", readCall, func(schema map[string]any) {
			schema["additionalProperties"] = map[string]any{"type": "string"}
		}, true},
		// Today's path check decodes every property type as a string, so an
		// array type already fails it.
		{"an array type", "Read", readCall, func(schema map[string]any) {
			schemaProperties(schema)["offset"] = map[string]any{"type": []any{"integer", "null"}}
		}, false},
		{"a property without type", "Read", readCall, func(schema map[string]any) {
			schemaProperties(schema)["pages"] = map[string]any{}
		}, true},
		{"an unaccepted property type", "Read", readCall, func(schema map[string]any) {
			schemaProperties(schema)["allow_large"] = map[string]any{"type": "string"}
		}, true},
		{"a top-level anyOf", "Read", readCall, func(schema map[string]any) {
			schema["anyOf"] = []any{map[string]any{"required": []any{"file_path"}}}
		}, true},
		{"a top-level patternProperties", "Read", readCall, func(schema map[string]any) {
			schema["patternProperties"] = map[string]any{"^x-": map[string]any{"type": "string"}}
		}, true},
		{"type other than object", "Read", readCall, func(schema map[string]any) {
			schema["type"] = "array"
		}, false},
		{"a missing core required name", "Edit", editCall, func(schema map[string]any) {
			schema["required"] = []any{"file_path", "old_string"}
		}, true},
		{"a required name that is not declared", "Read", readCall, func(schema map[string]any) {
			schema["required"] = []any{"file_path", "encoding"}
		}, true},
		{"a renamed path field", "Read", readCall, func(schema map[string]any) {
			properties := schemaProperties(schema)
			properties["path"] = properties["file_path"]
			delete(properties, "file_path")
			schema["required"] = []any{"path"}
		}, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			schemas := withSchema(captured, testCase.tool, mutateSchema(t, captured[testCase.tool], testCase.change))
			if approvedWithoutQuestion(t, root, clientadapter.ProtectionRecognizedClient, schemas, testCase.call) {
				t.Fatal("the recognized basis approved a changed definition")
			}
			if got := approvedWithoutQuestion(t, root, clientadapter.ProtectionExactRelease, schemas, testCase.call); got != testCase.exact {
				t.Fatalf("the exact basis changed: approved %v, want %v", got, testCase.exact)
			}
		})
	}
}

func TestRecognizedBasisRefusesUnknownOrUndeclaredArguments(t *testing.T) {
	t.Parallel()

	root := workspaceRoot(t)
	notes := writeWorkspaceFile(t, root, "notes.txt", "hello\n")
	captured := capturedDefinitions(t)
	withoutAllowLarge := withSchema(captured, "Read", mutateSchema(t, captured["Read"], func(schema map[string]any) {
		delete(schemaProperties(schema), "allow_large")
	}))
	for _, testCase := range []struct {
		name    string
		schemas map[string]json.RawMessage
		call    toolCall
	}{
		{"an unknown argument", captured,
			toolCall{"Read", argumentsJSON(t, map[string]any{"file_path": notes, "encoding": "utf-8"})}},
		{"a known argument the definition does not declare", withoutAllowLarge,
			toolCall{"Read", argumentsJSON(t, map[string]any{"file_path": notes, "allow_large": true})}},
		{"an argument of another tool", captured,
			toolCall{"Write", argumentsJSON(t, map[string]any{"file_path": notes, "content": "x", "old_string": "hello"})}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if approvedWithoutQuestion(t, root, clientadapter.ProtectionRecognizedClient, testCase.schemas, testCase.call) {
				t.Fatal("the recognized basis approved an argument the classifier never examined")
			}
			if !approvedWithoutQuestion(t, root, clientadapter.ProtectionExactRelease, testCase.schemas, testCase.call) {
				t.Fatal("the exact basis changed")
			}
		})
	}
	// The same definition without the extra argument is still approved.
	if !approvedWithoutQuestion(t, root, clientadapter.ProtectionRecognizedClient, withoutAllowLarge,
		toolCall{"Read", argumentsJSON(t, map[string]any{"file_path": notes})}) {
		t.Fatal("a declared subset of the shape was refused")
	}
}

func TestRecognizedBasisNeedsAbsolutePaths(t *testing.T) {
	t.Parallel()

	root := workspaceRoot(t)
	writeWorkspaceFile(t, root, "notes.txt", "hello\n")
	captured := capturedDefinitions(t)
	for _, call := range []toolCall{
		{"Read", `{"file_path":"notes.txt"}`},
		{"Read", `{"file_path":"./notes.txt"}`},
		{"Edit", `{"file_path":"notes.txt","old_string":"hello","new_string":"hi"}`},
		{"Write", `{"file_path":"new.txt","content":"x"}`},
	} {
		t.Run(call.name+" "+call.arguments, func(t *testing.T) {
			if approvedWithoutQuestion(t, root, clientadapter.ProtectionRecognizedClient, captured, call) {
				t.Fatal("the recognized basis approved a relative path")
			}
			if !approvedWithoutQuestion(t, root, clientadapter.ProtectionExactRelease, captured, call) {
				t.Fatal("the exact basis changed")
			}
		})
	}
}

func TestRecognizedBasisRefusesGlobAndGrep(t *testing.T) {
	t.Parallel()

	root := workspaceRoot(t)
	writeWorkspaceFile(t, root, "notes.txt", "marker\n")
	schemas := map[string]json.RawMessage{
		"Glob": json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"],"additionalProperties":false}`),
		"Grep": json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"},"glob":{"type":"string"}},"required":["pattern"],"additionalProperties":false}`),
	}
	for _, call := range []toolCall{
		{"Glob", argumentsJSON(t, map[string]any{"pattern": "*.txt", "path": root})},
		{"Glob", `{"pattern":"*.txt"}`},
		{"Grep", argumentsJSON(t, map[string]any{"pattern": "marker", "path": root, "glob": "*.txt"})},
		{"Grep", `{"pattern":"marker"}`},
	} {
		t.Run(call.name+" "+call.arguments, func(t *testing.T) {
			if approvedWithoutQuestion(t, root, clientadapter.ProtectionRecognizedClient, schemas, call) {
				t.Fatal("the recognized basis approved a tool no canary has captured")
			}
			if !approvedWithoutQuestion(t, root, clientadapter.ProtectionExactRelease, schemas, call) {
				t.Fatal("the exact basis changed")
			}
		})
	}
}

// Every existing guard still applies on the recognized basis.
func TestRecognizedBasisKeepsTheContainmentRules(t *testing.T) {
	t.Parallel()

	root := workspaceRoot(t)
	outside := workspaceRoot(t)
	secret := writeWorkspaceFile(t, outside, "secret.txt", "secret\n")
	captured := capturedDefinitions(t)
	for name, call := range map[string]toolCall{
		"a path outside the workspace": {"Read", argumentsJSON(t, map[string]any{"file_path": secret})},
		"an undefined tool":            {"Bash", `{"command":"cat notes.txt"}`},
		"a missing existing file":      {"Edit", argumentsJSON(t, map[string]any{"file_path": filepath.Join(root, "missing.txt"), "old_string": "a", "new_string": "b"})},
		"a non-string path":            {"Read", `{"file_path":7}`},
	} {
		t.Run(name, func(t *testing.T) {
			if approvedWithoutQuestion(t, root, clientadapter.ProtectionRecognizedClient, captured, call) {
				t.Fatal("the recognized basis bypassed a containment rule")
			}
		})
	}
	link := filepath.Join(root, "outside")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	escape := toolCall{"Read", argumentsJSON(t, map[string]any{"file_path": filepath.Join(link, "secret.txt")})}
	if approvedWithoutQuestion(t, root, clientadapter.ProtectionRecognizedClient, captured, escape) {
		t.Fatal("the recognized basis followed a symlink out of the workspace")
	}
}
