package nativetool

import "testing"

func TestNativeBindingCanonicalDigestRemainsStable(t *testing.T) {
	b, err := NewBinding(bindingFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Digest().String(); got != "262b75772545c5086f638275afe3aa7839a03590ff545d0296d3a3415070cb10" {
		t.Fatalf("v1 canonical encoding changed: %s", got)
	}
	e := BindingEvidence{b.kind, b.capture, b.plan.EnvironmentID(), b.plan.EnvironmentRevision(), b.plan.EnvironmentDigest(), b.plan.Route(), b.plan.ContentRecording(), b.plan.PolicySet(), b.assignmentRevision, b.ownerEpoch, b.sessionID, b.sessionRevision, b.catalogGeneration, b.catalogDigest, b.parentOperationID, b.serverRequestDigest, b.requestDigest, b.paramsDigest, b.groupDigest, b.replayKey, b.toolName, b.rpcIDDigest, b.cancelledRPCIDDigest}
	if err := VerifyBindingEvidence(e, b.Digest()); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*BindingEvidence){func(e *BindingEvidence) { e.Params = Digest{1} }, func(e *BindingEvidence) { e.Policy.ToolMode = "strict" }, func(e *BindingEvidence) { e.Name = "other_public" }, func(e *BindingEvidence) { d := Digest{2}; e.Catalog = &d }, func(e *BindingEvidence) { e.Owner++ }, func(e *BindingEvidence) { e.CancelledRPCID = &Digest{3} }, func(e *BindingEvidence) { e.RPCID = nil }} {
		wrong := e
		mutate(&wrong)
		if VerifyBindingEvidence(wrong, b.Digest()) == nil {
			t.Fatal("substituted facts accepted")
		}
	}
}
