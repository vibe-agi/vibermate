package nativetool

import (
	"crypto/sha256"
	"strings"
	"testing"
)

func TestNativeCatalogCompleteCanonicalAndMembership(t *testing.T) {
	input := bindingFixture(t)
	scope := CatalogScope{Capture: input.Capture, Plan: input.Plan, AssignmentRevision: 1, OwnerEpoch: 1, SessionID: input.SessionID, SessionRevision: 1, CatalogGeneration: 1}
	observer, err := NewCatalogObserver(scope)
	if err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	response := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_file","description":"Read file","inputSchema":{"type":"object","properties":{"path":{"type":"string"}}},"annotations":{"readOnlyHint":true},"_meta":{"proof":"secret"}}]}}`)
	evidence, err := observer.Observe([]CatalogPageObservation{{request, response}})
	if err != nil {
		t.Fatal(err)
	}
	input.CatalogDigest = new(Digest)
	*input.CatalogDigest = evidence.Digest()
	input.CatalogEvidence = &evidence
	bound, err := NewBinding(input)
	if err != nil || bound.ToolName() != "read_file" {
		t.Fatalf("membership: %q %v", bound.ToolName(), err)
	}
	other, err := observer.Observe([]CatalogPageObservation{{request, []byte(strings.Replace(string(response), `"secret"`, `"other"`, 1))}})
	if err != nil || other.Digest() != evidence.Digest() {
		t.Fatalf("private metadata changed public digest: %v", err)
	}
	changed, err := observer.Observe([]CatalogPageObservation{{request, []byte(strings.Replace(string(response), `"Read file"`, `"Changed"`, 1))}})
	if err != nil || changed.Digest() == evidence.Digest() {
		t.Fatalf("description absent from identity: %v", err)
	}
	response[0] = '!'
	if bound.ToolName() != "read_file" {
		t.Fatal("mutable observation")
	}
	for _, bad := range []string{
		`{"jsonrpc":"2.0","id":"1","result":{"tools":[]}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"tools":[],"nextCursor":"more"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"x","inputSchema":{}},{"name":"x","inputSchema":{}}]}}`,
	} {
		if _, err := observer.Observe([]CatalogPageObservation{{request, []byte(bad)}}); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	input.OwnerEpoch++
	if _, err := NewBinding(input); err == nil {
		t.Fatal("foreign scope evidence admitted")
	}
}

func TestNativeCatalogSchemaStructuralProjection(t *testing.T) {
	input := bindingFixture(t)
	observer, err := NewCatalogObserver(CatalogScope{input.Capture, input.Plan, 1, 1, input.SessionID, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	observe := func(schema string) (CatalogEvidence, error) {
		return observer.Observe([]CatalogPageObservation{{request, []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_file","inputSchema":` + schema + `}]}}`)}})
	}
	ordinary, err := observe(`{"type":"object","properties":{"_meta":{"type":"string"}},"$defs":{"item":{"type":"string"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := observe(`{"type":"object","properties":{"_meta":{"type":"string"}},"$defs":{"item":{"type":"string"}},"_meta":{"proof":"SENTINEL"}}`)
	if err != nil || ordinary.Digest() != metadata.Digest() {
		t.Fatalf("extension metadata hashed: %v", err)
	}
	changed, err := observe(`{"type":"object","properties":{"_meta":{"type":"number"}},"$defs":{"item":{"type":"string"}}}`)
	if err != nil || ordinary.Digest() == changed.Digest() {
		t.Fatalf("literal property schema omitted: %v", err)
	}
	if _, err := observe(`{"type":"object","unclassifiedProof":"SENTINEL"}`); err == nil {
		t.Fatal("unclassified schema extension accepted")
	}
}

func TestNativeDecisionCancelRPCIdentity(t *testing.T) {
	call := bindingFixture(t)
	b, err := NewBinding(call)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := b.RPCIDDigest()
	call.Kind = RequestCancel
	call.Intents = nil
	call.ObservedCallID = nil
	call.ExactRPC = []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
	cancel, err := NewBinding(call)
	if err != nil {
		t.Fatal(err)
	}
	actual, ok := cancel.CancelledRPCIDDigest()
	if !ok || actual != target {
		t.Fatal("target RPC identity lost")
	}
	call.ExactRPC = []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"1"}}`)
	typed, err := NewBinding(call)
	if err != nil {
		t.Fatal(err)
	}
	different, _ := typed.CancelledRPCIDDigest()
	if different == target {
		t.Fatal("string and numeric target mixed")
	}
}

func TestNativeCatalogRequestCoverage(t *testing.T) {
	input := bindingFixture(t)
	observer, err := NewCatalogObserver(CatalogScope{input.Capture, input.Plan, 1, 1, input.SessionID, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	first := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	second := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"cursor":"c"}}`)
	pages := []CatalogPageObservation{{first, []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[],"nextCursor":"c"}}`)}, {second, []byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`)}}
	evidence, err := observer.Observe(pages)
	if err != nil {
		t.Fatal(err)
	}
	one := Digest(sha256.Sum256(first))
	two := Digest(sha256.Sum256(second))
	extra := Digest(sha256.Sum256([]byte("unrelated")))
	if evidence.MatchesRequests([]Digest{one}) || evidence.MatchesRequests([]Digest{one, extra}) || !evidence.MatchesRequests([]Digest{one, two, extra}) {
		t.Fatal("incomplete/extra request coverage incorrect")
	}
	pages[1].ExactRequest = first
	if _, err := observer.Observe(pages); err == nil {
		t.Fatal("duplicate page request accepted")
	}
}

func TestNativeCatalogExactPublicKeysIgnoreCaseAliases(t *testing.T) {
	input := bindingFixture(t)
	observer, err := NewCatalogObserver(CatalogScope{input.Capture, input.Plan, 1, 1, input.SessionID, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tool := `{"name":"read_file","description":"public description","inputSchema":{"type":"object","properties":{"Name":{"type":"number"},"_meta":{"type":"boolean"}}},"annotations":{"readOnlyHint":true,"destructiveHint":false}}`
	observe := func(result string) (CatalogEvidence, error) {
		return observer.Observe([]CatalogPageObservation{{request, []byte(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`)}})
	}
	baseline, err := observe(`{"tools":[` + tool + `]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []struct{ name, field string }{
		{"name", `"Name":"other"`},
		{"description", `"Description":"unclassified sentinel"`},
		{"schema", `"InputSchema":{"type":"object","properties":{"unclassified":{"type":"string"}}}`},
		{"annotations", `"Annotations":{"readOnlyHint":false,"destructiveHint":true}`},
	} {
		for _, before := range []bool{true, false} {
			t.Run(alias.name+map[bool]string{true: "_before", false: "_after"}[before], func(t *testing.T) {
				modified := strings.TrimSuffix(tool, "}") + `,` + alias.field + `}`
				if before {
					modified = `{` + alias.field + `,` + strings.TrimPrefix(tool, "{")
				}
				_, err := observe(`{"tools":[` + modified + `]}`)
				// Existing recursive name validation rejects case-fold collisions.
				// Keep that behavior rather than expanding the accepted wire shape.
				if err == nil {
					t.Fatal("case-folded public field collision admitted")
				}
			})
		}
	}
	for _, result := range []string{`{"Tools":[],"tools":[` + tool + `]}`, `{"tools":[` + tool + `],"Tools":[]}`} {
		if _, err := observe(result); err == nil {
			t.Error("case-folded tools collision admitted")
		}
	}
	for _, test := range []struct{ name, canonical, alias string }{
		{"description", strings.Replace(tool, `,"description":"public description"`, "", 1), `"Description":"unclassified sentinel"`},
		{"annotations", strings.Replace(tool, `,"annotations":{"readOnlyHint":true,"destructiveHint":false}`, "", 1), `"Annotations":{"readOnlyHint":true}`},
	} {
		for _, before := range []bool{true, false} {
			t.Run("case_only_"+test.name+map[bool]string{true: "_before", false: "_after"}[before], func(t *testing.T) {
				ordinary, err := observe(`{"tools":[` + test.canonical + `]}`)
				if err != nil {
					t.Fatal(err)
				}
				modified := strings.TrimSuffix(test.canonical, "}") + `,` + test.alias + `}`
				if before {
					modified = `{` + test.alias + `,` + strings.TrimPrefix(test.canonical, "{")
				}
				evidence, err := observe(`{"tools":[` + modified + `]}`)
				if err != nil || evidence.Digest() != ordinary.Digest() {
					t.Fatalf("case-only optional extension entered public digest: %v", err)
				}
				digest := evidence.Digest()
				input.CatalogDigest = &digest
				input.CatalogEvidence = &evidence
				bound, err := NewBinding(input)
				if err != nil || bound.ToolName() != "read_file" {
					t.Fatalf("exact canonical membership changed %q %v", bound.ToolName(), err)
				}
				injected := input
				injected.Intents = append(injected.Intents[:0:0], input.Intents...)
				injected.Intents[0].Call.Name = "other"
				injected.ExactRPC = []byte(strings.Replace(string(input.ExactRPC), `"name":"read_file"`, `"name":"other"`, 1))
				if _, err := NewBinding(injected); err == nil {
					t.Fatal("unknown alias evidence manufactured membership")
				}
			})
		}
	}
	for _, result := range []string{`{"NextCursor":"unclassified","tools":[` + tool + `]}`, `{"tools":[` + tool + `],"NextCursor":"unclassified"}`} {
		evidence, err := observe(result)
		if err != nil || evidence.Digest() != baseline.Digest() {
			t.Errorf("case-only cursor extension altered exact terminal catalog: %v", err)
		}
	}
	// Literal schema property names are public schema data, not field aliases.
	changed, err := observe(`{"tools":[` + strings.Replace(tool, `"Name":{"type":"number"}`, `"Name":{"type":"string"}`, 1) + `]}`)
	if err != nil || changed.Digest() == baseline.Digest() {
		t.Fatalf("literal schema property lost: %v", err)
	}
}

func TestNativeCatalogExactRequiredFieldsAndCursorCompletion(t *testing.T) {
	input := bindingFixture(t)
	observer, err := NewCatalogObserver(CatalogScope{input.Capture, input.Plan, 1, 1, input.SessionID, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	for _, test := range []struct{ name, result string }{
		{"case_only_tools", `{"Tools":[{"name":"read_file","inputSchema":{"type":"object"}}]}`},
		{"case_only_name", `{"tools":[{"Name":"read_file","inputSchema":{"type":"object"}}]}`},
		{"case_only_undeclared_member", `{"tools":[{"Name":"other","inputSchema":{"type":"object"}}]}`},
		{"case_only_schema", `{"tools":[{"name":"read_file","InputSchema":{"type":"object"}}]}`},
		{"cursor_alias_before", `{"NextCursor":"","nextCursor":"more","tools":[]}`},
		{"cursor_alias_after", `{"nextCursor":"more","NextCursor":"","tools":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := observer.Observe([]CatalogPageObservation{{request, []byte(`{"jsonrpc":"2.0","id":1,"result":` + test.result + `}`)}}); err == nil {
				t.Fatal("case alias satisfied required field or manufactured terminal completion")
			}
		})
	}
	// Request extensions remain rejected; case aliases cannot supply cursor.
	for _, params := range []string{`{"Cursor":"more"}`, `{"cursor":"","Cursor":"more"}`, `{"Cursor":"more","cursor":""}`} {
		if _, err := observer.Observe([]CatalogPageObservation{{[]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":` + params + `}`), []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)}}); err == nil {
			t.Fatal("request cursor alias admitted")
		}
	}
}
