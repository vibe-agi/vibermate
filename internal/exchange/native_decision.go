package exchange

import (
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// NewClientToolDecisionRequest freezes a native policy question. It never
// supplies workspace authority, a model route or permission to send.
func NewClientToolDecisionRequest(id nativetool.ID, binding nativetool.Binding, policy ToolDecisionContext, intents []protocolcore.ToolIntent, witness *nativetool.RegisteredParentWitness) (ToolDecisionRequest, error) {
	if _, err := nativetool.NewID(string(id)); err != nil || binding.Digest() == (nativetool.Digest{}) || policy.policySet.Validate() != nil || policy.policySet != binding.Plan().PolicySet() || policy.workspaceRoot != "" || policy.structuredWorkspaceTools || len(policy.tools) != 0 || len(policy.toolNamespaces) != 0 || !binding.MatchesIntents(intents) {
		return ToolDecisionRequest{}, nativetool.ErrInvalid
	}
	var frozenWitness *nativetool.RegisteredParentWitness
	switch binding.Kind() {
	case nativetool.RequestToolsCall:
		if len(intents) != 1 || witness != nil {
			return ToolDecisionRequest{}, nativetool.ErrInvalid
		}
	case nativetool.RequestServerReply:
		if len(intents) != 0 || witness == nil || witness.Verify(id, binding) != nil {
			return ToolDecisionRequest{}, nativetool.ErrDenied
		}
		cloned := *witness
		frozenWitness = &cloned
	default:
		return ToolDecisionRequest{}, nativetool.ErrInvalid
	}
	return ToolDecisionRequest{nativeOperation: id, nativeBinding: &binding, nativeContext: policy, nativeWitness: frozenWitness, intents: cloneToolIntents(intents)}, nil
}
func (request ToolDecisionRequest) NativeExecution() (nativetool.ID, nativetool.Binding, bool) {
	if request.nativeBinding == nil {
		return "", nativetool.Binding{}, false
	}
	return request.nativeOperation, *request.nativeBinding, true
}
func (request ToolDecisionRequest) NativeContext() (ToolDecisionContext, bool) {
	if request.nativeBinding == nil {
		return ToolDecisionContext{}, false
	}
	return request.nativeContext, true
}
