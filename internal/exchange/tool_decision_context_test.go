package exchange

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
)

// The bool constructor keeps its exact-release meaning, and the bool accessor
// stays true only for the exact basis, so no existing or future reader of the
// bool can mistake a recognized client for an exact release (ADR 0026).
func TestBoolToolDecisionContextMeansExactRelease(t *testing.T) {
	t.Parallel()

	policy := environment.PolicySet{ToolMode: environment.ToolPolicyReview}
	root := t.TempDir()
	for _, testCase := range []struct {
		structured bool
		want       clientadapter.ProtectionBasis
	}{
		{true, clientadapter.ProtectionExactRelease},
		{false, clientadapter.ProtectionNone},
	} {
		context, err := NewToolDecisionContext(policy, root, testCase.structured, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := context.StructuredWorkspaceToolsBasis(); got != testCase.want {
			t.Fatalf("NewToolDecisionContext(%v) basis = %s, want %s", testCase.structured, got, testCase.want)
		}
		if context.StructuredWorkspaceTools() != testCase.structured {
			t.Fatalf("NewToolDecisionContext(%v) StructuredWorkspaceTools() = %v",
				testCase.structured, context.StructuredWorkspaceTools())
		}
	}

	for _, testCase := range []struct {
		basis clientadapter.ProtectionBasis
		exact bool
	}{
		{clientadapter.ProtectionNone, false},
		{clientadapter.ProtectionExactRelease, true},
		{clientadapter.ProtectionRecognizedClient, false},
	} {
		context, err := NewToolDecisionContextForBasis(policy, root, testCase.basis, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if context.StructuredWorkspaceToolsBasis() != testCase.basis {
			t.Fatalf("basis %s was not kept: %s", testCase.basis, context.StructuredWorkspaceToolsBasis())
		}
		if context.StructuredWorkspaceTools() != testCase.exact {
			t.Fatalf("basis %s: StructuredWorkspaceTools() = %v, want %v",
				testCase.basis, context.StructuredWorkspaceTools(), testCase.exact)
		}
	}

	for _, invalid := range []clientadapter.ProtectionBasis{
		clientadapter.ProtectionRecognizedClient + 1,
		255,
	} {
		if _, err := NewToolDecisionContextForBasis(policy, root, invalid, nil, nil); err == nil {
			t.Fatalf("basis %d was accepted", invalid)
		}
	}
}
