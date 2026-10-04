package nativetool

import "encoding/json"

type CancellationContext struct {
	hasher *SensitiveBindingHasher
	target Binding
}

func (h *SensitiveBindingHasher) liveCancellationTarget(target Binding) bool {
	for _, record := range h.tools {
		if record.active && record.binding.Digest() == target.Digest() {
			return true
		}
	}
	return false
}

func (h *SensitiveBindingHasher) BindCancellation(input BindingInput, target Binding) (Binding, error) {
	if h == nil || !target.sensitiveTool || input.Kind != RequestCancel {
		return Binding{}, ErrDenied
	}
	h.mu.Lock()
	valid := !h.closed && h.liveCancellationTarget(target) && target.Capture() == h.scope.Capture && target.OwnerEpoch() == h.scope.OwnerEpoch && target.SessionID() == h.scope.SessionID && target.AssignmentRevision() == h.scope.AssignmentRevision && target.Plan().EnvironmentDigest() == h.scope.Plan.EnvironmentDigest() && target.Plan().Route() == h.scope.Plan.Route()
	h.mu.Unlock()
	if !valid {
		return Binding{}, ErrDenied
	}
	input.CancellationContext = &CancellationContext{h, target}
	return NewBinding(input)
}
func cancellationIdentity(input BindingInput, ordinary Digest) (Digest, error) {
	context := input.CancellationContext
	if context == nil {
		return ordinary, nil
	}
	if input.Kind != RequestCancel || context.hasher == nil {
		return Digest{}, ErrDenied
	}
	h := context.hasher
	h.mu.Lock()
	defer h.mu.Unlock()
	target := context.target
	if h.closed || !h.liveCancellationTarget(target) || input.Capture != target.Capture() || input.SessionID != target.SessionID() || input.OwnerEpoch != target.OwnerEpoch() || input.AssignmentRevision != target.AssignmentRevision() || input.Plan.EnvironmentDigest() != target.Plan().EnvironmentDigest() || input.Plan.Route() != target.Plan().Route() {
		return Digest{}, ErrDenied
	}
	var envelope, params map[string]json.RawMessage
	_ = json.Unmarshal(input.ExactRPC, &envelope)
	_ = json.Unmarshal(envelope["params"], &params)
	identity, err := typedSensitiveID(params["requestId"])
	if err != nil {
		return Digest{}, err
	}
	digest := h.mac("native-sensitive-rpc-id/v1", "", identity)
	expected, ok := target.RPCIDDigest()
	if !ok || digest != expected {
		return Digest{}, ErrDenied
	}
	return digest, nil
}
