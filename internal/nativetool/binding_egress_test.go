package nativetool

import (
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"testing"
)

func TestNativeV2EffectiveEvidenceUsesApprovedCanonicalNames(t *testing.T) {
	b, err := NewBinding(bindingFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	e := BindingEvidence{b.kind, b.capture, b.plan.EnvironmentID(), b.plan.EnvironmentRevision(), b.plan.EnvironmentDigest(), b.plan.Route(), b.plan.ContentRecording(), b.plan.PolicySet(), b.assignmentRevision, b.ownerEpoch, b.sessionID, b.sessionRevision, b.catalogGeneration, b.catalogDigest, b.parentOperationID, b.serverRequestDigest, b.requestDigest, b.paramsDigest, b.groupDigest, b.replayKey, b.toolName, b.rpcIDDigest, b.cancelledRPCIDDigest}
	resolved, err := b.Plan().ResolvedEgress()
	if err != nil {
		t.Fatal(err)
	}
	type canonicalEffective struct {
		Profile         egressprofile.ProfileRevision `json:"profile"`
		Source          egressprofile.SelectionSource `json:"source"`
		DefaultRevision uint64                        `json:"defaultRevision"`
	}
	expected := hashJSON(struct {
		Domain string
		BindingEvidence
		Egress canonicalEffective
	}{"native-binding/v2", e, canonicalEffective{resolved.Profile(), resolved.Source(), resolved.DefaultRevision()}})
	if expected.String() != "ff7b902cc9571bfad62ba9a2130b9ce13286f5637696fc28d89dff8a5be2f96a" {
		t.Fatalf("independent canonical V2 fixture changed: %s", expected)
	}
	if err := VerifyBindingEvidenceV2(e, resolved, expected); err != nil || b.Digest() != expected {
		t.Fatalf("actual V2 does not verify approved lower-case canonical fields: got=%s expected=%s err=%v", b.Digest(), expected, err)
	}
	upperCase := hashJSON(struct {
		Domain string
		BindingEvidence
		Egress struct {
			Profile         egressprofile.ProfileRevision
			Source          egressprofile.SelectionSource
			DefaultRevision uint64
		}
	}{"native-binding/v2", e, struct {
		Profile         egressprofile.ProfileRevision
		Source          egressprofile.SelectionSource
		DefaultRevision uint64
	}{resolved.Profile(), resolved.Source(), resolved.DefaultRevision()}})
	if VerifyBindingEvidenceV2(e, resolved, upperCase) == nil {
		t.Fatal("alternative V2 field aliases accepted")
	}
}

func TestNativeNewBindingRequiresFrozenEffectiveExit(t *testing.T) {
	input := authoredBindingFixture(t)
	if _, err := NewBinding(input); err == nil {
		t.Fatal("new native binding admitted an unresolved authored route")
	}
}
