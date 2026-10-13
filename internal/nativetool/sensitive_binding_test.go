package nativetool

import (
	"crypto/sha256"
	"testing"
)

// Losing the sensitive branch would persist direct hashes of elicitation data.
func TestNativeSensitiveBindingRejectsUnregisteredCallbackBeforeSHA(t *testing.T) {
	input := bindingFixture(t)
	input.Kind = RequestServerReply
	input.Intents = nil
	input.ObservedCallID = nil
	parent := ID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	digest := Digest{1}
	input.ParentOperationID = &parent
	input.ServerRequestDigest = &digest
	input.ExactRPC = []byte(`{"jsonrpc":"2.0","id":17,"result":{"action":"accept","content":{"proof":"synthetic-sensitive-sentinel"}}}`)
	if _, err := NewBinding(input); err == nil {
		t.Fatal("unregistered callback payload accepted into raw SHA binding")
	}
}

func TestNativeSensitiveMACFixedKeyAndRetirement(t *testing.T) {
	i := bindingFixture(t)
	scope := SensitiveScope{i.Capture, i.Plan, i.AssignmentRevision, i.OwnerEpoch, i.SessionID}
	h, err := NewSensitiveBindingHasher(scope)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	for index := range h.key {
		h.key[index] = 0x42
	}
	observation, _ := NewSensitiveObservation("elicitation/create", []string{"/params"})
	request := []byte(`{"jsonrpc":"2.0","id":17,"method":"elicitation/create","params":{"message":"fixed synthetic private value","requestedSchema":{"type":"object","properties":{}}}}`)
	parent, err := h.ObserveParent(ID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), request, observation)
	if err != nil {
		t.Fatal(err)
	}
	// Independently computed with Node crypto over hand-checked frozen public
	// scope + exact request and unsigned64-bit big-endian length frames.
	if parent.RequestDigest().String() != "bec7a04a7047b41f690ed0a9e7b526f20efa35d2b3f9e96ce417b3d1e97722de" {
		t.Fatal("fixed-key/domain-framed parent MAC differs from golden")
	}
	other, _ := NewSensitiveBindingHasher(scope)
	defer other.Close()
	if other.RetireParent(parent) == nil {
		t.Fatal("foreign hasher retired parent")
	}
	if err = h.RetireParent(parent); err != nil {
		t.Fatal(err)
	}
	if h.RetireParent(parent) == nil {
		t.Fatal("retired parent accepted twice")
	}
	if _, err = h.ObserveParent(ID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), request, observation); err != nil {
		t.Fatal("retired registry slot unavailable")
	}
}

func TestNativeSensitiveBindingUsesScopedMACBeforeSHA(t *testing.T) {
	input := bindingFixture(t)
	input.Kind = RequestServerReply
	input.Intents = nil
	input.ObservedCallID = nil
	parentID := ID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	unproved := Digest{1}
	input.ParentOperationID = &parentID
	input.ServerRequestDigest = &unproved
	request := []byte(`{"jsonrpc":"2.0","id":17,"method":"elicitation/create","params":{"message":"synthetic-sensitive-sentinel","requestedSchema":{"type":"object","properties":{}}}}`)
	input.ExactRPC = []byte(`{"jsonrpc":"2.0","id":17,"result":{"action":"accept","content":{"proof":"synthetic-sensitive-sentinel"}}}`)
	scope := SensitiveScope{input.Capture, input.Plan, input.AssignmentRevision, input.OwnerEpoch, input.SessionID}
	h, err := NewSensitiveBindingHasher(scope)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	observation, _ := NewSensitiveObservation("elicitation/create", []string{"/params"})
	p, err := h.ObserveParent(parentID, request, observation)
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.BindReply(input, p)
	if err != nil {
		t.Fatal(err)
	}
	if b.RequestDigest() == Digest(sha256.Sum256(input.ExactRPC)) || p.RequestDigest() == Digest(sha256.Sum256(request)) || p.RPCIDDigest() == rpcIdentityDigest([]byte(`17`)) {
		t.Fatal("sensitive branch leaked direct SHA")
	}
	w, err := p.RegisteredReply(ID("cccccccccccccccccccccccccccccccc"), b, input.ExactRPC)
	if err != nil || w.Verify(ID("cccccccccccccccccccccccccccccccc"), b) != nil {
		t.Fatalf("registered witness: %v", err)
	}
	for _, mutate := range []func(*BindingInput){func(i *BindingInput) { i.OwnerEpoch++ }, func(i *BindingInput) { i.SessionID = ID("dddddddddddddddddddddddddddddddd") }, func(i *BindingInput) { i.ParentOperationID = &i.SessionID }, func(i *BindingInput) { i.ExactRPC = []byte(`{"jsonrpc":"2.0","id":"17","result":{"action":"accept"}}`) }} {
		other := input
		mutate(&other)
		if _, err := h.BindReply(other, p); err == nil {
			t.Fatal("wrong owner/session/parent/typed RPC accepted")
		}
	}
	other, _ := NewSensitiveBindingHasher(scope)
	defer other.Close()
	q, err := other.ObserveParent(parentID, request, observation)
	if err != nil {
		t.Fatal(err)
	}
	if p.RequestDigest() == q.RequestDigest() {
		t.Fatal("session key did not separate MACs")
	}
	if _, err := other.BindReply(input, p); err == nil {
		t.Fatal("foreign registry parent accepted")
	}
	h.Close()
	if _, err := h.BindReply(input, p); err == nil {
		t.Fatal("closed key granted binding")
	}
}

func TestNativeKnownSensitiveToolBindingNeverRawHashes(t *testing.T) {
	i := bindingFixture(t)
	observation, err := NewSensitiveObservation("tools/call", []string{"/params/arguments/path"})
	if err != nil {
		t.Fatalf("known-sensitive tool observation unavailable: %v", err)
	}
	i.SensitiveObservation = &observation
	if _, err := NewBinding(i); err == nil {
		t.Fatal("known-sensitive tool without scoped witness accepted into raw SHA")
	}
}
