package nativetool

import (
	"crypto/sha256"
	"strings"
	"testing"
)

func TestNativeDecisionRegisteredParentWitness(t *testing.T) {
	input := bindingFixture(t)
	parent, err := NewBinding(input)
	if err != nil {
		t.Fatal(err)
	}
	parentID := ID(strings.Repeat("b", 32))
	replyID := ID(strings.Repeat("c", 32))
	observer, err := NewParentObserver(parentID, parent)
	if err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"jsonrpc":"2.0","id":"server-1","method":"ping"}`)
	reply := []byte(`{"jsonrpc":"2.0","id":"server-1","result":{}}`)
	witness, err := observer.ObserveReply(replyID, request, reply)
	if err != nil {
		t.Fatal(err)
	}
	input.Kind = RequestServerReply
	input.Intents = nil
	input.ObservedCallID = nil
	input.ExactRPC = reply
	input.ParentOperationID = &parentID
	digest := Digest(sha256.Sum256(request))
	input.ServerRequestDigest = &digest
	binding, err := NewBinding(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := witness.Verify(replyID, binding); err != nil {
		t.Fatal(err)
	}
	if err := witness.Verify(parentID, binding); err == nil {
		t.Fatal("wrong reply operation admitted")
	}
	input.OwnerEpoch++
	wrong, _ := NewBinding(input)
	if err := witness.Verify(replyID, wrong); err == nil {
		t.Fatal("wrong owner admitted")
	}
	input.OwnerEpoch--
	foreignCatalog := Digest(sha256.Sum256([]byte("foreign catalog")))
	input.CatalogDigest = &foreignCatalog
	wrong, _ = NewBinding(input)
	if err := witness.Verify(replyID, wrong); err == nil {
		t.Fatal("changed frozen catalog admitted")
	}
	for _, raw := range []string{`{"jsonrpc":"2.0","id":"server-1","method":"elicitation/create","params":{"proof":"sentinel"}}`, `{"jsonrpc":"2.0","id":"server-1","method":"unknown"}`, `{"jsonrpc":"2.0","id":"server-1","method":"ping","_meta":{"proof":"sentinel"}}`} {
		if _, err := observer.ObserveReply(replyID, []byte(raw), reply); err == nil {
			t.Fatal("unclassified/sensitive server request admitted")
		}
	}
	if _, err := observer.ObserveReply(replyID, request, []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)); err == nil {
		t.Fatal("typed ID mismatch admitted")
	}
}
