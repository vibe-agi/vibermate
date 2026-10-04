//go:build vibermate_acceptance_synthetic

package nativemcp

import (
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"strings"
	"testing"
)

const acceptanceDefinition = `{"name":"synthetic_read","description":"Synthetic public tool","inputSchema":{"properties":{"query":{"type":"string"}},"type":"object"}}`

func acceptanceObservedDefinition(t *testing.T, definition string) nativetool.CatalogDefinition {
	t.Helper()
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	c := f.captured(t, "POST", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, "")
	plan, ok := c.lease.Plan().ClientToolPlan()
	if !ok {
		t.Fatal("native plan missing")
	}
	c.lease.Release()
	id, _ := nativetool.NewID("11111111111111111111111111111111")
	observer, err := nativetool.NewCatalogObserver(nativetool.CatalogScope{Capture: f.capture, Plan: plan, AssignmentRevision: 1, OwnerEpoch: 1, SessionID: id, SessionRevision: 1, CatalogGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := observer.Observe([]nativetool.CatalogPageObservation{{ExactRequest: []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`), ExactResponse: []byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[` + definition + `]}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	return evidence.Definitions()[0]
}

func TestAcceptanceSyntheticProfilesExactDefinitionAndPrivatePaths(t *testing.T) {
	profiles := AcceptanceSyntheticContentProfiles()
	d := acceptanceObservedDefinition(t, acceptanceDefinition)
	p, ok := profiles.Resolve(environment.ClientToolContractCodexLegacy, d)
	if !ok || !p.valid(environment.ClientToolContractCodexLegacy, d) {
		t.Fatal("fixed observed definition not classified")
	}
	for _, definition := range []string{
		strings.Replace(acceptanceDefinition, "synthetic_read", "other", 1),
		strings.Replace(acceptanceDefinition, "Synthetic public tool", "Changed", 1),
		strings.Replace(acceptanceDefinition, `"string"`, `"number"`, 1),
		strings.Replace(acceptanceDefinition, `"query"`, `"other"`, 1),
		strings.Replace(acceptanceDefinition, `"type":"object"`, `"type":"object","required":["query"]`, 1),
		strings.TrimSuffix(acceptanceDefinition, "}") + `,"annotations":{"readOnlyHint":true}}`,
	} {
		if _, ok := profiles.Resolve(environment.ClientToolContractCodexLegacy, acceptanceObservedDefinition(t, definition)); ok {
			t.Fatal("mutated public definition classified")
		}
	}
	if _, ok := profiles.Resolve("other", d); ok {
		t.Fatal("other contract classified")
	}
	if _, ok := profiles.Resolve(environment.ClientToolContractCodexLegacy, nativetool.CatalogDefinition{}); ok {
		t.Fatal("zero definition classified")
	}
	canonical := acceptanceObservedDefinition(t, `{"inputSchema":{"type":"object","properties":{"query":{"type":"string"}}},"description":"Synthetic public tool","name":"synthetic_read"}`)
	if _, ok := profiles.Resolve(environment.ClientToolContractCodexLegacy, canonical); !ok {
		t.Fatal("canonical equivalent refused")
	}
	p.arguments[0] = "/changed"
	p.result[0] = "/changed"
	fresh, _ := profiles.Resolve(environment.ClientToolContractCodexLegacy, d)
	for _, raw := range []string{`{"proof":"sensitive"}`, `{"credentialId":"sensitive"}`} {
		if _, safe := retainedContent([]byte(raw), fresh.arguments, DefaultLimits()); safe {
			t.Fatal("sensitive argument retained")
		}
	}
	for _, raw := range []string{`{"content":[],"credentialId":"sensitive"}`, `{"content":[],"structuredContent":{"credentialId":"sensitive"}}`} {
		if _, safe := retainedToolResult([]byte(raw), fresh.result, DefaultLimits()); safe {
			t.Fatal("sensitive result retained")
		}
	}
}

func TestAcceptanceSyntheticDefinitionDigest(t *testing.T) {
	d := acceptanceObservedDefinition(t, acceptanceDefinition)
	if got := d.Digest().String(); got != "9ec74da54cc7f865d22dc1d020abf5a6d9a7df1cd7a1db07d509c6394329065f" {
		t.Fatalf("observer definition digest=%s", got)
	}
}
