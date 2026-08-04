package egressaudit_test

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

// The decision is real even though it is not yet configurable, so an audit
// reader is told which rule applied rather than being shown a blank.
func TestBuiltInDirectDecisionIsAValidDecision(t *testing.T) {
	t.Parallel()

	input := baseInput()
	input.Decision = egressaudit.BuiltInDirectDecision(
		egressaudit.AuthorityAccess,
	)
	attempt, err := egressaudit.New(input)
	if err != nil {
		t.Fatalf("the built-in decision was refused: %v", err)
	}
	if attempt.Decision().RuleID == "" || attempt.Decision().ProxyID == "" {
		t.Fatalf("built-in decision = %+v", attempt.Decision())
	}
}

// The Offline Hold admission classes and audit purposes are orthogonal, so the
// complete fine-grained catalog must project explicitly into Hold's coarser
// taxonomy. Iterating the production catalog makes a missing future mapping a
// test failure instead of an admission bypass.
func TestEveryPurposeMapsToAHoldKind(t *testing.T) {
	t.Parallel()

	expected := map[egressaudit.EgressPurpose]offlinehold.EgressKind{
		egressaudit.PurposeProviderAttempt:     offlinehold.EgressProvider,
		egressaudit.PurposeProfileOperation:    offlinehold.EgressAuxiliary,
		egressaudit.PurposeOriginalOrigin:      offlinehold.EgressOpaque,
		egressaudit.PurposeAgentProbe:          offlinehold.EgressAuxiliary,
		egressaudit.PurposeBlindTunnel:         offlinehold.EgressBlindTunnel,
		egressaudit.PurposeAuxiliaryLLM:        offlinehold.EgressAuxiliary,
		egressaudit.PurposeLanguageTransform:   offlinehold.EgressAuxiliary,
		egressaudit.PurposePluginCatalogSync:   offlinehold.EgressPlugin,
		egressaudit.PurposePluginArtifactFetch: offlinehold.EgressPlugin,
		egressaudit.PurposeUpdate:              offlinehold.EgressUpdate,
	}
	seen := make(map[egressaudit.EgressPurpose]struct{}, len(expected))
	for _, purpose := range egressaudit.Purposes() {
		if _, duplicate := seen[purpose]; duplicate {
			t.Fatalf("purpose %q appears more than once", purpose)
		}
		seen[purpose] = struct{}{}
		want, exists := expected[purpose]
		if !exists {
			t.Fatalf("purpose %q is absent from the expected Hold catalog", purpose)
		}
		kind, err := offlinehold.KindForPurpose(purpose)
		if err != nil {
			t.Fatalf("purpose %q has no Hold kind: %v", purpose, err)
		}
		if kind != want {
			t.Fatalf("purpose %q Hold kind = %q, want %q", purpose, kind, want)
		}
	}
	if len(seen) != len(expected) {
		t.Fatalf("Hold catalog covers %d purposes, want %d", len(seen), len(expected))
	}
	if _, err := offlinehold.KindForPurpose("invented"); err == nil {
		t.Fatal("an unknown purpose resolved a Hold kind")
	}
}

// The original-origin transport still receives a coarse Hold kind. Its legacy
// reverse mapping covers only the exact purposes that transport can create;
// plugin distribution must arrive with its typed purpose already frozen.
func TestOriginalTransportHoldKindMappingFailsClosedForPlugin(t *testing.T) {
	t.Parallel()

	for _, kind := range []offlinehold.EgressKind{
		offlinehold.EgressProvider,
		offlinehold.EgressOpaque,
		offlinehold.EgressAuxiliary,
		offlinehold.EgressBlindTunnel,
		offlinehold.EgressUpdate,
	} {
		if _, err := egressaudit.PurposeForEgressKind(string(kind)); err != nil {
			t.Fatalf("original transport Hold kind %q has no purpose: %v", kind, err)
		}
	}
	if _, err := egressaudit.PurposeForEgressKind(
		string(offlinehold.EgressPlugin),
	); err == nil {
		t.Fatal("a coarse plugin Hold kind selected a distribution purpose")
	}
	if _, err := egressaudit.PurposeForEgressKind("invented"); err == nil {
		t.Fatal("an unknown Hold kind resolved a purpose")
	}
}
