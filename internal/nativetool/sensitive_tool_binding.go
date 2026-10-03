package nativetool

import (
	"crypto/hmac"
	"encoding/json"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type SensitiveToolContext struct {
	hasher *SensitiveBindingHasher
	record *sensitiveToolRecord
}
type sensitiveToolRecord struct {
	operation ID
	binding   Binding
	active    bool
}
type SensitiveToolWitness struct {
	hasher *SensitiveBindingHasher
	record *sensitiveToolRecord
}

func (h *SensitiveBindingHasher) toolScope(input BindingInput) bool {
	s := h.scope
	return !h.closed && input.Kind == RequestToolsCall && input.Capture == s.Capture && input.AssignmentRevision == s.AssignmentRevision && input.OwnerEpoch == s.OwnerEpoch && input.SessionID == s.SessionID && sameCatalogScope(CatalogScope{s.Capture, s.Plan, s.AssignmentRevision, s.OwnerEpoch, s.SessionID, input.SessionRevision, input.CatalogGeneration}, CatalogScope{input.Capture, input.Plan, input.AssignmentRevision, input.OwnerEpoch, input.SessionID, input.SessionRevision, input.CatalogGeneration})
}
func (h *SensitiveBindingHasher) BindToolCall(operation ID, input BindingInput, observation SensitiveObservation) (Binding, SensitiveToolWitness, error) {
	if h == nil {
		return Binding{}, SensitiveToolWitness{}, ErrInvalid
	}
	if _, err := NewID(string(operation)); err != nil {
		return Binding{}, SensitiveToolWitness{}, err
	}
	if observation.kind != "tools/call" || input.CatalogEvidence == nil || len(input.Intents) != 1 {
		return Binding{}, SensitiveToolWitness{}, ErrDenied
	}
	h.mu.Lock()
	if !h.toolScope(input) || len(h.tools) >= 1024 || h.tools[operation] != nil {
		h.mu.Unlock()
		return Binding{}, SensitiveToolWitness{}, ErrDenied
	}
	record := &sensitiveToolRecord{operation: operation, active: true}
	h.tools[operation] = record
	h.mu.Unlock()
	input.SensitiveObservation = &observation
	input.SensitiveToolContext = &SensitiveToolContext{h, record}
	binding, err := NewBinding(input)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil || h.closed || h.tools[operation] != record {
		delete(h.tools, operation)
		record.active = false
		if err == nil {
			err = ErrDenied
		}
		return Binding{}, SensitiveToolWitness{}, err
	}
	record.binding = binding
	return binding, SensitiveToolWitness{h, record}, nil
}
func sensitiveToolBindingDigests(input BindingInput, params, group []byte) (Digest, Digest, Digest, Digest, error) {
	context := input.SensitiveToolContext
	if context == nil || context.hasher == nil || context.record == nil || input.SensitiveObservation == nil || input.SensitiveObservation.kind != "tools/call" || input.SensitiveContext != nil {
		return Digest{}, Digest{}, Digest{}, Digest{}, ErrDenied
	}
	h := context.hasher
	h.mu.Lock()
	defer h.mu.Unlock()
	record := context.record
	if !h.toolScope(input) || !record.active || h.tools[record.operation] != record {
		return Digest{}, Digest{}, Digest{}, Digest{}, ErrDenied
	}
	var object map[string]json.RawMessage
	_ = json.Unmarshal(input.ExactRPC, &object)
	identity, err := typedSensitiveID(object["id"])
	if err != nil {
		return Digest{}, Digest{}, Digest{}, Digest{}, err
	}
	return h.mac("native-sensitive-tool-request/v1", record.operation, input.ExactRPC), h.mac("native-sensitive-tool-params/v1", record.operation, params), h.mac("native-sensitive-tool-group/v1", record.operation, group), h.mac("native-sensitive-rpc-id/v1", "", identity), nil
}
func (w SensitiveToolWitness) Verify(operation ID, binding Binding, intents []protocolcore.ToolIntent) error {
	if w.hasher == nil || w.record == nil || !binding.sensitiveTool || binding.Kind() != RequestToolsCall || len(intents) != 1 {
		return ErrDenied
	}
	h := w.hasher
	h.mu.Lock()
	defer h.mu.Unlock()
	record := w.record
	if h.closed || !record.active || record.operation != operation || h.tools[operation] != record || record.binding.Digest() == (Digest{}) || record.binding.Digest() != binding.Digest() {
		return ErrDenied
	}
	input := BindingInput{Kind: binding.Kind(), Plan: binding.Plan(), Capture: binding.Capture(), AssignmentRevision: binding.AssignmentRevision(), OwnerEpoch: binding.OwnerEpoch(), SessionID: binding.SessionID(), SessionRevision: binding.SessionRevision(), CatalogGeneration: binding.CatalogGeneration()}
	if !h.toolScope(input) {
		return ErrDenied
	}
	group, _, err := intentEvidence(intents)
	if err != nil {
		return err
	}
	actual := h.mac("native-sensitive-tool-group/v1", operation, group)
	expected := binding.GroupDigest()
	if !hmac.Equal(actual[:], expected[:]) {
		return ErrDenied
	}
	return nil
}
func (h *SensitiveBindingHasher) RetireToolCall(witness SensitiveToolWitness) error {
	if h == nil {
		return ErrInvalid
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || witness.hasher != h || witness.record == nil || !witness.record.active || h.tools[witness.record.operation] != witness.record {
		return ErrDenied
	}
	witness.record.active = false
	delete(h.tools, witness.record.operation)
	return nil
}
