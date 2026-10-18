package nativemcp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type NativeIntentPreview struct {
	ToolName  string          `json:"toolName"`
	Action    string          `json:"action"`
	Target    string          `json:"target"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Truncated bool            `json:"truncated"`
}
type PendingPreview struct {
	OperationID   nativetool.ID
	BindingDigest nativetool.Digest
	Revision      uint64
	Intents       []NativeIntentPreview
	ExpiresAt     time.Time
}
type pendingPreviewEntry struct {
	capture captureidentity.Reference
	digest  nativetool.Digest
	owner   *ownerScope
	data    []byte
}

const previewMaximumBytes = 64 << 10
const previewMaximumEntries = 1024
const previewTotalBytes = 16 << 20

// Content classification is frozen during binding. Unknown or sensitive tool
// arguments have no display projection; neither a digest nor a tool annotation
// establishes that bytes may be exposed to the management UI.
func (e *executor) previewIntents(b nativetool.Binding, intents []protocolcore.ToolIntent, r rpc, s *session) ([]NativeIntentPreview, error) {
	if len(intents) > 128 {
		return nil, errBudget
	}
	switch b.Kind() {
	case nativetool.RequestToolsCall:
		if r.retention == nil || !r.retention.known {
			return nil, nil
		}
		out := make([]NativeIntentPreview, 0, len(intents))
		for _, intent := range intents {
			body, safe := retainedContent(intent.Call.Arguments.Bytes(), r.retention.profile.arguments, e.limits)
			if !safe {
				return nil, nil
			}
			item := NativeIntentPreview{ToolName: intent.Call.Name, Action: "tool_call", Target: b.Plan().Route().TargetOrigin.String()}
			if len(body) > 60<<10 {
				item.Truncated = true
			} else {
				item.Arguments = body
			}
			out = append(out, item)
		}
		return out, nil
	case nativetool.RequestServerReply:
		s.mu.Lock()
		defer s.mu.Unlock()
		parent := s.parents[string(r.id)]
		if parent == nil || parent.consumed {
			return nil, nil
		}
		// Only the live registered parent and its already-verified child binding
		// supply this summary. No callback parameter or reply field is projected.
		parentID, ok := b.ParentOperationID()
		if !ok || parentID != parent.operation || b.Plan().EnvironmentDigest() != parent.binding.Plan().EnvironmentDigest() || b.Plan().Route().TargetOrigin != parent.binding.Plan().Route().TargetOrigin {
			return nil, nil
		}
		// The ordinary summary requires the positively classified closed ping
		// observer. Its exact request/reply witness and issued callback binding
		// have already been verified before this preview is retained.
		if parent.observer != nil && parent.kind == string(nativetool.CallbackPing) {
			return []NativeIntentPreview{{Action: "ping_reply", Target: parent.binding.Plan().Route().TargetOrigin.String()}}, nil
		}
		if parent.sensitive == nil {
			return nil, nil
		}
		action := ""
		switch parent.kind {
		case "form":
			action = "form_reply"
		case "url":
			action = "url_reply"
		case "verification":
			action = "verification_reply"
		}
		if action == "" {
			return nil, nil
		}
		return []NativeIntentPreview{{Action: action, Target: parent.binding.Plan().Route().TargetOrigin.String()}}, nil
	}
	return nil, nil
}
func (e *executor) retainPreview(id nativetool.ID, b nativetool.Binding, intents []protocolcore.ToolIntent, r rpc, s *session) error {
	if b.Plan().PolicySet().ToolMode != environment.ToolPolicyReview {
		return nil
	}
	display, err := e.previewIntents(b, intents, r, s)
	if err != nil || len(display) == 0 {
		return err
	}
	encoded, err := json.Marshal(display)
	if err != nil || len(encoded) > previewMaximumBytes {
		clear(encoded)
		return errBudget
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || s.owner.revoked || s.owner.context.Err() != nil {
		clear(encoded)
		return context.Canceled
	}
	if e.previews == nil {
		e.previews = map[nativetool.ID]*pendingPreviewEntry{}
	}
	if e.previews[id] != nil || len(e.previews) >= previewMaximumEntries || len(encoded) > previewTotalBytes-e.previewBytes {
		clear(encoded)
		return errBudget
	}
	e.previews[id] = &pendingPreviewEntry{capture: b.Capture(), digest: b.Digest(), owner: s.owner, data: encoded}
	e.previewBytes += len(encoded)
	return nil
}
func (e *executor) discardPreviewLocked(id nativetool.ID) {
	if entry := e.previews[id]; entry != nil {
		e.previewBytes -= len(entry.data)
		clear(entry.data)
		delete(e.previews, id)
	}
}
func (e *executor) discardPreview(id nativetool.ID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.discardPreviewLocked(id)
}
func (e *executor) PendingPreview(ctx context.Context, capture captureidentity.Reference, id nativetool.ID, digest nativetool.Digest) (PendingPreview, error) {
	op, err := e.dependencies.Store.Get(ctx, id)
	if err != nil {
		return PendingPreview{}, err
	}
	if op.Capture != capture || op.BindingDigest == nil || *op.BindingDigest != digest || op.ApprovalID == nil {
		return PendingPreview{}, nativetool.ErrNotFound
	}
	approval, err := e.dependencies.Store.GetApproval(ctx, *op.ApprovalID)
	if err != nil {
		return PendingPreview{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if approval.State != "pending" || !approval.ExpiresAt.After(e.dependencies.Clock().UTC()) {
		e.discardPreviewLocked(id)
		return PendingPreview{}, nativetool.ErrConflict
	}
	entry := e.previews[id]
	if entry == nil || entry.capture != capture || entry.digest != digest || approval.OperationID != id || approval.BindingDigest != digest || entry.owner.revoked || entry.owner.context.Err() != nil || e.closed || e.scopes[capture.Key()] != entry.owner {
		return PendingPreview{}, nativetool.ErrNotFound
	}
	var intents []NativeIntentPreview
	if json.Unmarshal(entry.data, &intents) != nil {
		return PendingPreview{}, nativetool.ErrUnavailable
	}
	return PendingPreview{OperationID: id, BindingDigest: digest, Revision: approval.Revision, Intents: intents, ExpiresAt: approval.ExpiresAt}, nil
}
