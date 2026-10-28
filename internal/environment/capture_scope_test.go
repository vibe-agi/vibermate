package environment

import (
	"errors"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturescope"
)

// An empty capture scope is the default, so stored revisions without the
// field keep their digests (ADR 0025); rules are stored in one order.
func TestCaptureScopeIsCanonicalAndPartOfTheDigest(t *testing.T) {
	t.Parallel()
	implicit := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	empty := implicit.Clone()
	empty.CaptureScope = &capturescope.Policy{}
	implicitDigest, err := Digest(implicit)
	if err != nil {
		t.Fatal(err)
	}
	if emptyDigest, err := Digest(empty); err != nil || emptyDigest != implicitDigest {
		t.Fatalf("an empty capture scope changed the authority digest: %v", err)
	}

	first := implicit.Clone()
	first.CaptureScope = &capturescope.Policy{Rules: []capturescope.Rule{
		{ID: "lan", Kind: capturescope.KindPrefix, Value: "192.168.0.0/16"},
		{ID: "intranet", Kind: capturescope.KindSuffix, Value: "corp.example"},
	}}
	second := implicit.Clone()
	second.CaptureScope = &capturescope.Policy{Rules: []capturescope.Rule{
		{ID: "intranet", Kind: capturescope.KindSuffix, Value: "corp.example"},
		{ID: "lan", Kind: capturescope.KindPrefix, Value: "192.168.0.0/16"},
	}}
	firstDigest, err := Digest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := Digest(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatal("rule order changed the authority digest")
	}
	if firstDigest == implicitDigest {
		t.Fatal("a capture scope did not change the authority digest")
	}

	snapshot := mustCompile(t, first)
	scope := snapshot.CaptureScope()
	if scope == nil || len(scope.Rules) != 2 || scope.Rules[0].ID != "intranet" {
		t.Fatalf("compiled capture scope = %+v", scope)
	}
	scope.Rules[0].Value = "mutated.example"
	if snapshot.CaptureScope().Rules[0].Value != "corp.example" {
		t.Fatal("the snapshot handed out its own capture scope")
	}
	if mustCompile(t, implicit).CaptureScope() != nil {
		t.Fatal("an Environment without a capture scope compiled one")
	}
}

func TestCaptureScopeRefusesInvalidAndProtectedRules(t *testing.T) {
	t.Parallel()
	base := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	compiler := testCompiler(t, accountCatalogFor(base))
	if _, err := compiler.Compile(base); err != nil {
		t.Fatalf("the fixture itself does not publish: %v", err)
	}
	for name, rule := range map[string]capturescope.Rule{
		"public prefix":            {ID: "public", Kind: capturescope.KindPrefix, Value: "8.8.8.0/24"},
		"single-label suffix":      {ID: "tld", Kind: capturescope.KindSuffix, Value: "example"},
		"covers the endpoint":      {ID: "relay", Kind: capturescope.KindSuffix, Value: "relay.example"},
		"names the endpoint":       {ID: "relay-host", Kind: capturescope.KindHost, Value: "relay.example"},
		"covers the endpoint port": {ID: "relay-https", Kind: capturescope.KindHost, Value: "relay.example", Port: 443},
	} {
		value := base.Clone()
		value.CaptureScope = &capturescope.Policy{Rules: []capturescope.Rule{rule}}
		if _, err := compiler.Compile(value); !errors.Is(err, ErrInvalidEnvironment) ||
			!strings.Contains(err.Error(), "capture scope") {
			t.Errorf("%s: got %v, want a capture scope refusal", name, err)
		}
	}
	allowed := base.Clone()
	allowed.CaptureScope = &capturescope.Policy{Rules: []capturescope.Rule{
		{ID: "other-port", Kind: capturescope.KindHost, Value: "relay.example", Port: 8080},
	}}
	if _, err := compiler.Compile(allowed); err != nil {
		t.Fatalf("a rule on another port of the endpoint's host was refused: %v", err)
	}
}
