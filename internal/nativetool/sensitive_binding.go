package nativetool

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"slices"
	"sync"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
)

// These opaque values establish consistency only. The native parser owns
// authenticated observation; the repository still owns decisions and starts.
type SensitiveScope struct {
	Capture                        captureidentity.Reference
	Plan                           environment.ClientToolPlan
	AssignmentRevision, OwnerEpoch uint64
	SessionID                      ID
}
type SensitiveObservation struct {
	kind  string
	paths []string
}
type SensitiveBindingHasher struct {
	mu      sync.Mutex
	key     [32]byte
	scope   SensitiveScope
	parents map[Digest]*sensitiveParentRecord
	tools   map[ID]*sensitiveToolRecord
	closed  bool
}
type sensitiveParentRecord struct {
	operation    ID
	identity     []byte
	request, rpc Digest
	observation  SensitiveObservation
}
type SensitiveParent struct {
	hasher *SensitiveBindingHasher
	record *sensitiveParentRecord
}
type SensitiveBindingContext struct{ parent SensitiveParent }

func NewSensitiveObservation(kind string, paths []string) (SensitiveObservation, error) {
	switch kind {
	case "elicitation/create", "openai/form", "openai/elicitation/create", "tools/call":
	default:
		return SensitiveObservation{}, ErrInvalid
	}
	if len(paths) == 0 || len(paths) > 64 {
		return SensitiveObservation{}, ErrInvalid
	}
	for _, path := range paths {
		if len(path) == 0 || len(path) > 512 || path[0] != '/' {
			return SensitiveObservation{}, ErrInvalid
		}
	}
	return SensitiveObservation{kind, slices.Clone(paths)}, nil
}
func validSensitiveScope(s SensitiveScope) bool {
	_, err := NewID(string(s.SessionID))
	return err == nil && s.Capture.Validate() == nil && s.Plan.Validate() == nil && revision(s.AssignmentRevision) && revision(s.OwnerEpoch)
}
func NewSensitiveBindingHasher(scope SensitiveScope) (*SensitiveBindingHasher, error) {
	if !validSensitiveScope(scope) {
		return nil, ErrInvalid
	}
	h := &SensitiveBindingHasher{scope: scope, parents: make(map[Digest]*sensitiveParentRecord), tools: map[ID]*sensitiveToolRecord{}}
	if _, err := io.ReadFull(rand.Reader, h.key[:]); err != nil {
		return nil, err
	}
	return h, nil
}
func (h *SensitiveBindingHasher) mac(label string, operation ID, data []byte) Digest {
	public, _ := json.Marshal(struct {
		Capture           captureidentity.Reference
		Environment       environment.EnvironmentID
		Revision          environment.Revision
		Digest            environment.CandidateDigest
		Route             environment.ClientToolRoute
		Recording         environment.ContentRecordingPolicy
		Policy            environment.PolicySet
		Assignment, Owner uint64
		Session, Parent   ID
	}{h.scope.Capture, h.scope.Plan.EnvironmentID(), h.scope.Plan.EnvironmentRevision(), h.scope.Plan.EnvironmentDigest(), h.scope.Plan.Route(), h.scope.Plan.ContentRecording(), h.scope.Plan.PolicySet(), h.scope.AssignmentRevision, h.scope.OwnerEpoch, h.scope.SessionID, operation})
	mac := hmac.New(sha256.New, h.key[:])
	for _, part := range [][]byte{[]byte(label), public, data} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = mac.Write(length[:])
		_, _ = mac.Write(part)
	}
	var digest Digest
	copy(digest[:], mac.Sum(nil))
	return digest
}
func typedSensitiveID(raw []byte) ([]byte, error) {
	if !validRPCID(raw) {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if d.Decode(&value) != nil {
		return nil, ErrInvalid
	}
	return json.Marshal(value)
}
func (h *SensitiveBindingHasher) ObserveParent(operation ID, request []byte, observation SensitiveObservation) (SensitiveParent, error) {
	if h == nil {
		return SensitiveParent{}, ErrInvalid
	}
	if _, err := NewID(string(operation)); err != nil {
		return SensitiveParent{}, err
	}
	if _, err := NewSensitiveObservation(observation.kind, observation.paths); err != nil {
		return SensitiveParent{}, err
	}
	if observation.kind == "tools/call" {
		return SensitiveParent{}, ErrDenied
	}
	object, err := rpcObject(request)
	if err != nil {
		return SensitiveParent{}, err
	}
	var method string
	if json.Unmarshal(object["method"], &method) != nil || method != observation.kind || len(object["result"]) != 0 || len(object["error"]) != 0 {
		return SensitiveParent{}, ErrInvalid
	}
	if _, err := canonicalObject(object["params"]); err != nil {
		return SensitiveParent{}, err
	}
	identity, err := typedSensitiveID(object["id"])
	if err != nil {
		return SensitiveParent{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || len(h.parents) >= 1024 {
		return SensitiveParent{}, ErrDenied
	}
	rpc := h.mac("native-sensitive-rpc-id/v1", "", identity)
	if _, exists := h.parents[rpc]; exists {
		return SensitiveParent{}, ErrConflict
	}
	record := &sensitiveParentRecord{operation, identity, h.mac("native-sensitive-parent/v1", operation, request), rpc, observation}
	h.parents[rpc] = record
	return SensitiveParent{h, record}, nil
}
func (p SensitiveParent) RequestDigest() Digest {
	if p.record == nil {
		return Digest{}
	}
	return p.record.request
}
func (p SensitiveParent) RPCIDDigest() Digest {
	if p.record == nil {
		return Digest{}
	}
	return p.record.rpc
}
func (h *SensitiveBindingHasher) matches(input BindingInput, p SensitiveParent) bool {
	s := h.scope
	return !h.closed && p.hasher == h && p.record != nil && h.parents[p.record.rpc] == p.record && input.ParentOperationID != nil && *input.ParentOperationID == p.record.operation && input.Kind == RequestServerReply && sameCatalogScope(CatalogScope{Capture: s.Capture, Plan: s.Plan, AssignmentRevision: s.AssignmentRevision, OwnerEpoch: s.OwnerEpoch, SessionID: s.SessionID, SessionRevision: input.SessionRevision, CatalogGeneration: input.CatalogGeneration}, CatalogScope{input.Capture, input.Plan, input.AssignmentRevision, input.OwnerEpoch, input.SessionID, input.SessionRevision, input.CatalogGeneration})
}
func (h *SensitiveBindingHasher) BindReply(input BindingInput, parent SensitiveParent) (Binding, error) {
	if h == nil || parent.hasher != h {
		return Binding{}, ErrInvalid
	}
	input.SensitiveContext = &SensitiveBindingContext{parent}
	observation := parent.recordObservation()
	input.SensitiveObservation = &observation
	return NewBinding(input)
}
func (p SensitiveParent) recordObservation() SensitiveObservation {
	if p.record == nil {
		return SensitiveObservation{}
	}
	return p.record.observation
}
func sensitiveBindingDigests(input BindingInput) (Digest, Digest, Digest, Digest, Digest, error) {
	if input.SensitiveContext == nil {
		return Digest{}, Digest{}, Digest{}, Digest{}, Digest{}, ErrDenied
	}
	p := input.SensitiveContext.parent
	h := p.hasher
	if h == nil {
		return Digest{}, Digest{}, Digest{}, Digest{}, Digest{}, ErrDenied
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.matches(input, p) || input.SensitiveObservation == nil || input.SensitiveObservation.kind != p.record.observation.kind {
		return Digest{}, Digest{}, Digest{}, Digest{}, Digest{}, ErrDenied
	}
	object, err := rpcObject(input.ExactRPC)
	if err != nil {
		return Digest{}, Digest{}, Digest{}, Digest{}, Digest{}, err
	}
	identity, err := typedSensitiveID(object["id"])
	if err != nil || !bytes.Equal(identity, p.record.identity) {
		return Digest{}, Digest{}, Digest{}, Digest{}, Digest{}, ErrDenied
	}
	value := object["result"]
	if len(value) == 0 {
		value = object["error"]
	}
	canonical, err := canonicalObject(value)
	if err != nil {
		return Digest{}, Digest{}, Digest{}, Digest{}, Digest{}, err
	}
	return h.mac("native-sensitive-reply/v1", p.record.operation, input.ExactRPC), h.mac("native-sensitive-params/v1", p.record.operation, canonical), h.mac("native-sensitive-group/v1", p.record.operation, []byte("[]")), p.record.rpc, p.record.request, nil
}
func (p SensitiveParent) RegisteredReply(id ID, binding Binding, reply []byte) (RegisteredParentWitness, error) {
	if _, err := NewID(string(id)); err != nil || p.hasher == nil || p.record == nil {
		return RegisteredParentWitness{}, ErrInvalid
	}
	h := p.hasher
	h.mu.Lock()
	defer h.mu.Unlock()
	input := BindingInput{Kind: binding.Kind(), Plan: binding.Plan(), Capture: binding.Capture(), AssignmentRevision: binding.AssignmentRevision(), OwnerEpoch: binding.OwnerEpoch(), SessionID: binding.SessionID(), SessionRevision: binding.SessionRevision(), CatalogGeneration: binding.CatalogGeneration(), ParentOperationID: binding.parentOperationID}
	object, err := rpcObject(reply)
	if err != nil {
		return RegisteredParentWitness{}, err
	}
	identity, err := typedSensitiveID(object["id"])
	if err != nil || !h.matches(input, p) || !bytes.Equal(identity, p.record.identity) || binding.RequestDigest() != h.mac("native-sensitive-reply/v1", p.record.operation, reply) || binding.serverRequestDigest == nil || *binding.serverRequestDigest != p.record.request {
		return RegisteredParentWitness{}, ErrDenied
	}
	return RegisteredParentWitness{id, p.record.operation, CatalogScope{binding.Capture(), binding.Plan(), binding.AssignmentRevision(), binding.OwnerEpoch(), binding.SessionID(), binding.SessionRevision(), binding.CatalogGeneration()}, p.record.request, binding.RequestDigest(), copyPointer(binding.catalogDigest)}, nil
}
func (h *SensitiveBindingHasher) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	clear(h.key[:])
	clear(h.parents)
	clear(h.tools)
	h.closed = true
}

// RetireParent removes only an exact registered entry. The authenticated parser
// must establish durable terminal evidence before invoking it. It grants no
// binding or execution, and old contexts fail after removal.
func (h *SensitiveBindingHasher) RetireParent(parent SensitiveParent) error {
	if h == nil {
		return ErrInvalid
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || parent.hasher != h || parent.record == nil || h.parents[parent.record.rpc] != parent.record {
		return ErrDenied
	}
	delete(h.parents, parent.record.rpc)
	return nil
}
