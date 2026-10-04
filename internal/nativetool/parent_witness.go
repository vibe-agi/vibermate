package nativetool

import (
	"crypto/sha256"
	"encoding/json"
)

// ParentObserver checks the closed, data-free ping shape. Store-issued callback
// registration separately establishes authenticated observation authority.
// Sensitive callbacks use the profile-specific HMAC producer before raw hashes.
type ParentObserver struct {
	operation ID
	binding   Binding
}
type RegisteredParentWitness struct {
	reply, parent     ID
	scope             CatalogScope
	request, response Digest
	catalog           *Digest
}

func NewParentObserver(id ID, binding Binding) (*ParentObserver, error) {
	if _, err := NewID(string(id)); err != nil || binding.Digest() == (Digest{}) || binding.Kind() != RequestToolsCall && binding.Kind() != RequestGetStream {
		return nil, ErrInvalid
	}
	return &ParentObserver{id, binding}, nil
}
func (observer *ParentObserver) ObserveRequest(request []byte) (CallbackFacts, error) {
	if observer == nil {
		return CallbackFacts{}, ErrInvalid
	}
	object, err := rpcObject(request)
	if err != nil {
		return CallbackFacts{}, err
	}
	var method string
	if json.Unmarshal(object["method"], &method) != nil || method != "ping" || len(object) != 3 || !validRPCID(object["id"]) {
		return CallbackFacts{}, ErrDenied
	}
	return CallbackFacts{RPCIdentityDigest: rpcIdentityDigest(object["id"]), RequestDigest: Digest(sha256.Sum256(request)), Kind: CallbackPing}, nil
}
func (observer *ParentObserver) ObserveReply(id ID, request, reply []byte) (RegisteredParentWitness, error) {
	if observer == nil {
		return RegisteredParentWitness{}, ErrInvalid
	}
	if _, err := NewID(string(id)); err != nil || id == observer.operation {
		return RegisteredParentWitness{}, ErrInvalid
	}
	object, err := rpcObject(request)
	if err != nil {
		return RegisteredParentWitness{}, err
	}
	var method string
	if json.Unmarshal(object["method"], &method) != nil || method != "ping" || len(object) != 3 || !validRPCID(object["id"]) {
		return RegisteredParentWitness{}, ErrDenied
	}
	response, err := rpcObject(reply)
	if err != nil {
		return RegisteredParentWitness{}, err
	}
	// No callback data or metadata enters the ordinary branch.
	if len(response) != 3 || !sameRPCID(object["id"], response["id"]) || string(response["result"]) != "{}" {
		return RegisteredParentWitness{}, ErrDenied
	}
	b := observer.binding
	catalog, hasCatalog := b.CatalogDigest()
	var frozenCatalog *Digest
	if hasCatalog {
		frozenCatalog = &catalog
	}
	return RegisteredParentWitness{id, observer.operation, CatalogScope{b.Capture(), b.Plan(), b.AssignmentRevision(), b.OwnerEpoch(), b.SessionID(), b.SessionRevision(), b.CatalogGeneration()}, Digest(sha256.Sum256(request)), Digest(sha256.Sum256(reply)), frozenCatalog}, nil
}
func (w RegisteredParentWitness) Verify(id ID, b Binding) error {
	parent, ok := b.ParentOperationID()
	request, hasRequest := b.ServerRequestDigest()
	scope := CatalogScope{b.Capture(), b.Plan(), b.AssignmentRevision(), b.OwnerEpoch(), b.SessionID(), b.SessionRevision(), b.CatalogGeneration()}
	catalog, hasCatalog := b.CatalogDigest()
	if w.reply == "" || id != w.reply || !ok || parent != w.parent || !hasRequest || request != w.request || b.RequestDigest() != w.response || b.Kind() != RequestServerReply || !sameCatalogScope(w.scope, scope) || hasCatalog != (w.catalog != nil) || hasCatalog && catalog != *w.catalog {
		return ErrDenied
	}
	return nil
}
