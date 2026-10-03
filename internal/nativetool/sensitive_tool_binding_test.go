package nativetool

import (
	"crypto/sha256"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func sensitiveToolFixture(t *testing.T) (*SensitiveBindingHasher, BindingInput, SensitiveObservation) {
	t.Helper()
	input := bindingFixture(t)
	observer, err := NewCatalogObserver(CatalogScope{input.Capture, input.Plan, input.AssignmentRevision, input.OwnerEpoch, input.SessionID, input.SessionRevision, input.CatalogGeneration})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := observer.Observe([]CatalogPageObservation{{ExactRequest: []byte(`{"jsonrpc":"2.0","id":22,"method":"tools/list"}`), ExactResponse: []byte(`{"jsonrpc":"2.0","id":22,"result":{"tools":[{"name":"read_file","inputSchema":{"type":"object","properties":{"path":{"type":"string"}}}}]}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	digest := evidence.Digest()
	input.CatalogDigest = &digest
	input.CatalogEvidence = &evidence
	h, err := NewSensitiveBindingHasher(SensitiveScope{input.Capture, input.Plan, input.AssignmentRevision, input.OwnerEpoch, input.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	observation, _ := NewSensitiveObservation("tools/call", []string{"/params/arguments/path"})
	return h, input, observation
}
func TestNativeSensitiveToolWitnessExactGroupScopeAndRetirement(t *testing.T) {
	h, input, observation := sensitiveToolFixture(t)
	defer h.Close()
	for index := range h.key {
		h.key[index] = 0x42
	}
	operation := ID("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	binding, witness, err := h.BindToolCall(operation, input, observation)
	if err != nil {
		t.Fatal(err)
	}
	// Independent Node-crypto vectors over hand-checked scope/request/params/
	// complete group, not computed through the implementation's helpers.
	for _, vector := range []struct {
		digest Digest
		want   string
	}{{binding.RequestDigest(), "b203cd0a2ef7b208259890cf43348a0a96f9f91437f206ac7ded20e47ec1ac68"}, {binding.ParamsDigest(), "4653e6ea3a5e42f39688536efa2062f41dd9cb3d4d7d1408aaf9d493ce5dcc36"}, {binding.GroupDigest(), "3ba02f5161a67caeb001074f2757cd39cb5ec66a24f2615fafbb053ae007046a"}} {
		if vector.digest.String() != vector.want {
			t.Fatalf("tool MAC vector %s want%s", vector.digest.String(), vector.want)
		}
	}
	if err = witness.Verify(operation, binding, input.Intents); err != nil {
		t.Fatal(err)
	}
	group, _, _ := intentEvidence(input.Intents)
	params, _ := validateEnvelope(input)
	if binding.RequestDigest() == Digest(sha256.Sum256(input.ExactRPC)) || binding.ParamsDigest() == Digest(sha256.Sum256(params)) || binding.GroupDigest() == Digest(sha256.Sum256(group)) {
		t.Fatal("known tool raw whole-message SHA evaluated")
	}
	if binding.MatchesIntents(input.Intents) {
		t.Fatal("ordinary matcher admitted MAC tool")
	}
	if witness.Verify(ID("dddddddddddddddddddddddddddddddd"), binding, input.Intents) == nil {
		t.Fatal("wrong operation witness")
	}
	if (SensitiveToolWitness{}).Verify(operation, binding, input.Intents) == nil {
		t.Fatal("missing witness")
	}
	changed := append([]protocolcore.ToolIntent{}, input.Intents...)
	changed[0].Call.Name = "other"
	if witness.Verify(operation, binding, changed) == nil {
		t.Fatal("tool substitution admitted")
	}
	changed = append([]protocolcore.ToolIntent{}, input.Intents...)
	changed[0].Call.Arguments, _ = protocolcore.NewJSONObject([]byte(`{"path":"different"}`), 1024)
	if witness.Verify(operation, binding, changed) == nil {
		t.Fatal("argument substitution admitted")
	}
	changed = append([]protocolcore.ToolIntent{}, input.Intents...)
	changed[0].ResponseID = "different"
	if witness.Verify(operation, binding, changed) == nil {
		t.Fatal("group identity substituted")
	}
	other, otherInput, otherObservation := sensitiveToolFixture(t)
	defer other.Close()
	foreign, foreignWitness, err := other.BindToolCall(operation, otherInput, otherObservation)
	if err != nil {
		t.Fatal(err)
	}
	if witness.Verify(operation, foreign, input.Intents) == nil || foreignWitness.Verify(operation, binding, input.Intents) == nil {
		t.Fatal("foreign scope/key witness admitted")
	}
	if other.RetireToolCall(witness) == nil {
		t.Fatal("foreign retirement admitted")
	}
	if err = h.RetireToolCall(witness); err != nil {
		t.Fatal(err)
	}
	if witness.Verify(operation, binding, input.Intents) == nil || h.RetireToolCall(witness) == nil {
		t.Fatal("retired witness usable")
	}
}
