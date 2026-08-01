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

// The Offline Hold admission classes and the audit purposes are orthogonal
// taxonomies, so every class the transports can carry maps explicitly.
func TestEveryHoldKindMapsToAPurpose(t *testing.T) {
	t.Parallel()

	for _, kind := range []offlinehold.EgressKind{
		offlinehold.EgressProvider,
		offlinehold.EgressOpaque,
		offlinehold.EgressAuxiliary,
		offlinehold.EgressBlindTunnel,
		offlinehold.EgressUpdate,
	} {
		purpose, err := egressaudit.PurposeForEgressKind(string(kind))
		if err != nil {
			t.Fatalf("hold kind %q has no purpose: %v", kind, err)
		}
		if _, err := egressaudit.AuthorityForPurpose(purpose); err != nil {
			t.Fatalf("purpose %q has no authority: %v", purpose, err)
		}
	}
	if _, err := egressaudit.PurposeForEgressKind("invented"); err == nil {
		t.Fatal("an unknown hold kind resolved a purpose")
	}
}
