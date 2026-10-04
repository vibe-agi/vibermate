package nativemcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"time"

	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// Pinned Codex 0.145/0.159/0.160 + RMCP1.8/3.2 callback shapes. Classification
// belongs here, before any domain hash or content producer is selected.
func classifySensitiveCallback(r rpc) (string, error) {
	if r.method != "elicitation/create" && r.method != "openai/form" && r.method != "openai/elicitation/create" {
		return "", errProtocol
	}
	mode, hasMode := text(r.params["mode"])
	if r.method == "openai/form" {
		if hasMode {
			return "", errProtocol
		}
		mode = "form"
	} else if !hasMode && r.method == "elicitation/create" {
		mode = "form"
	}
	switch mode {
	case "form":
		if !allowed(r.params, "mode", "message", "requestedSchema", "_meta") {
			return "", errProtocol
		}
		if _, ok := text(r.params["message"]); !ok {
			return "", errProtocol
		}
		var schema map[string]json.RawMessage
		if json.Unmarshal(r.params["requestedSchema"], &schema) != nil || schema == nil {
			return "", errProtocol
		}
		return "form", nil
	case "url":
		if r.method != "elicitation/create" || !allowed(r.params, "mode", "message", "url", "elicitationId", "_meta") {
			return "", errProtocol
		}
		message, ok := text(r.params["message"])
		if !ok || message == "" {
			return "", errProtocol
		}
		destination, ok := text(r.params["url"])
		if !ok {
			return "", errProtocol
		}
		u, err := url.Parse(destination)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return "", errProtocol
		}
		id, ok := text(r.params["elicitationId"])
		if !ok || id == "" || len(id) > 512 {
			return "", errProtocol
		}
		return "url", nil
	case "openai/userVerification":
		if r.method != "openai/elicitation/create" || !allowed(r.params, "mode", "_meta", "title", "description", "challenge") {
			return "", errProtocol
		}
		title, ok := text(r.params["title"])
		if !ok || title == "" || len(title) > 256 {
			return "", errProtocol
		}
		description, ok := text(r.params["description"])
		if !ok || len(description) > 4096 {
			return "", errProtocol
		}
		challenge, ok := text(r.params["challenge"])
		if !ok || !boundedProofBytes(challenge, 4096) {
			return "", errProtocol
		}
		return "verification", nil
	default:
		return "", errProtocol
	}
}
func boundedProofBytes(encoded string, max int) bool {
	if encoded == "" || len(encoded) > ((max+2)/3)*4 {
		return false
	}
	value, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	return err == nil && len(value) > 0 && len(value) <= max
}
func validateSensitiveReply(r rpc, kind string) error {
	if failure, present := r.object["error"]; present {
		var object map[string]json.RawMessage
		if json.Unmarshal(failure, &object) != nil || !allowed(object, "code", "message", "data") {
			return errProtocol
		}
		var code int64
		if json.Unmarshal(object["code"], &code) != nil {
			return errProtocol
		}
		if _, ok := text(object["message"]); !ok {
			return errProtocol
		}
		return nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(r.object["result"], &object) != nil || !allowed(object, "action", "content", "_meta") {
		return errProtocol
	}
	action, ok := text(object["action"])
	if !ok || action != "accept" && action != "decline" && action != "cancel" {
		return errProtocol
	}
	content, present := object["content"]
	if action != "accept" && present {
		return errProtocol
	}
	if kind == "url" && present {
		return errProtocol
	}
	if kind == "verification" {
		if _, meta := object["_meta"]; meta {
			return errProtocol
		}
		if action == "accept" {
			var proof map[string]json.RawMessage
			if !present || json.Unmarshal(content, &proof) != nil || !allowed(proof, "credentialId", "signature") {
				return errProtocol
			}
			credential, ok := text(proof["credentialId"])
			if !ok || credential == "" || len(credential) > 1024 {
				return errProtocol
			}
			signature, ok := text(proof["signature"])
			if !ok || !boundedProofBytes(signature, 128) {
				return errProtocol
			}
		}
	}
	return nil
}
func (e *executor) observeCallback(ctx context.Context, s *session, stream *streamRecord, r rpc) error {
	parentID, binding := stream.operation, stream.binding
	if binding.Kind() != nativetool.RequestToolsCall && binding.Kind() != nativetool.RequestGetStream || len(r.id) == 0 {
		return errProtocol
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || stream.retired || !stream.observed || stream.context.Err() != nil || s.parents[string(r.id)] != nil {
		return errSession
	}
	p := &callbackParent{operation: parentID, binding: binding}
	e.parentsMu.Lock()
	if e.parentPending >= e.limits.Pending {
		e.parentsMu.Unlock()
		return errBudget
	}
	e.parentPending++
	e.parentsMu.Unlock()
	charge := int64(2048 + len(r.id))
	if r.method == "ping" {
		charge += int64(len(r.raw))
	}
	if !e.budget.acquire(charge) {
		e.parentsMu.Lock()
		e.parentPending--
		e.parentsMu.Unlock()
		return errBudget
	}
	s.retained += charge
	p.release = func() {
		e.parentsMu.Lock()
		e.parentPending--
		e.parentsMu.Unlock()
		s.retained -= charge
		e.budget.release(charge)
	}
	registered, urlInstalled := false, false
	var facts nativetool.CallbackFacts
	defer func() {
		if !registered {
			if urlInstalled {
				delete(s.elicitations, p.elicitationID)
				charge := int64(128 + len(p.elicitationID))
				s.retained -= charge
				e.budget.release(charge)
			}
			if p.sensitive != nil {
				_ = s.hasher.RetireParent(*p.sensitive)
			}
			p.release()
		}
	}()
	if r.method == "ping" {
		if len(r.object) != 3 {
			return errProtocol
		}
		observer, err := nativetool.NewParentObserver(parentID, binding)
		if err != nil {
			return err
		}
		p.observer = observer
		p.request = append([]byte{}, r.raw...)
		facts, err = observer.ObserveRequest(r.raw)
		if err != nil {
			return err
		}
		p.digest = facts.RequestDigest
		// The ordinary branch admits only data-free ping; its digest is derived
		// through the reviewed observer once the exact reply arrives.
	} else {
		kind, err := classifySensitiveCallback(r)
		if err != nil {
			return err
		}
		if !s.callbacks[r.method+":"+kind] {
			return errProtocol
		}
		observation, err := nativetool.NewSensitiveObservation(r.method, []string{"/params"})
		if err != nil {
			return err
		}
		parent, err := s.hasher.ObserveParent(parentID, r.raw, observation)
		if err != nil {
			return err
		}
		p.sensitive = &parent
		p.kind = kind
		facts = nativetool.CallbackFacts{RPCIdentityDigest: parent.RPCIDDigest(), RequestDigest: parent.RequestDigest(), Kind: nativetool.CallbackKind(kind)}
		if kind == "url" {
			p.elicitationID, _ = text(r.params["elicitationId"])
			if len(s.elicitations) >= e.limits.Pending {
				return errBudget
			}
			if _, duplicate := s.elicitations[p.elicitationID]; duplicate {
				return errSession
			}
			charge := int64(128 + len(p.elicitationID))
			if !e.budget.acquire(charge) {
				return errBudget
			}
			s.retained += charge
			s.elicitations[p.elicitationID] = parentID
			urlInstalled = true
		}
	}
	authority, err := e.dependencies.Store.RegisterObservedCallback(ctx, stream.authority, facts, e.dependencies.Clock().UTC())
	if err != nil {
		return err
	}
	p.authority = authority
	s.parents[string(r.id)] = p
	registered = true
	return nil
}

// A closed negative decision/canceled handler retires the exact observation.
// A successful URL answer keeps its separate completion obligation alive.
func (e *executor) retireFailedReplyParent(s *session, identity string, child nativetool.ID) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	op, err := e.dependencies.Store.Get(ctx, child)
	if err != nil || op.CompletedAt == nil || op.State == "completed" {
		return
	}
	s.mu.Lock()
	p := s.parents[identity]
	s.mu.Unlock()
	if p == nil {
		return
	}
	if err := e.dependencies.Store.RetireObservedCallback(ctx, p.authority, e.dependencies.Clock().UTC()); err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.parents[identity] != p {
		return
	}
	if p.sensitive != nil {
		_ = s.hasher.RetireParent(*p.sensitive)
	}
	if p.release != nil {
		p.release()
	}
	delete(s.parents, identity)
	if p.kind == "url" {
		if _, ok := s.elicitations[p.elicitationID]; ok {
			delete(s.elicitations, p.elicitationID)
			charge := int64(128 + len(p.elicitationID))
			s.retained -= charge
			e.budget.release(charge)
		}
	}
}

func (e *executor) retireCompletedParents(ctx context.Context, s *session, operation nativetool.ID) {
	op, err := e.dependencies.Store.Get(ctx, operation)
	if err != nil || op.CompletedAt == nil {
		return
	}
	if op.Kind == nativetool.RequestGetStream {
		s.mu.Lock()
		stream := s.streams[operation]
		live := stream != nil && !stream.retired && stream.context.Err() == nil
		s.mu.Unlock()
		if live {
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for identity, parent := range s.parents {
		if parent.operation != operation {
			continue
		}
		if parent.sensitive != nil {
			_ = s.hasher.RetireParent(*parent.sensitive)
		}
		if parent.release != nil {
			parent.release()
		}
		delete(s.parents, identity)
	}
	for elicitation, parent := range s.elicitations {
		if parent == operation {
			charge := int64(128 + len(elicitation))
			s.retained -= charge
			e.budget.release(charge)
			delete(s.elicitations, elicitation)
		}
	}
}

func declaredCallbacks(r rpc, l Limits) (map[string]bool, error) {
	result := map[string]bool{}
	raw, present := r.params["capabilities"]
	if !present {
		return result, nil
	}
	capabilities, err := objectField(raw, l)
	if err != nil {
		return nil, err
	}
	if elicitation, present := capabilities["elicitation"]; present {
		modes, err := objectField(elicitation, l)
		if err != nil {
			return nil, err
		}
		if len(modes) == 0 {
			result["elicitation/create:form"] = true
		}
		for _, mode := range []string{"form", "url"} {
			if declaration, present := modes[mode]; present {
				if _, err = objectField(declaration, l); err != nil {
					return nil, err
				}
				result["elicitation/create:"+mode] = true
			}
		}
	}
	if raw, present := capabilities["extensions"]; present {
		extensions, err := objectField(raw, l)
		if err != nil {
			return nil, err
		}
		if declaration, present := extensions["openai/form"]; present {
			if _, err = objectField(declaration, l); err != nil {
				return nil, err
			}
			result["openai/form:form"] = true
		}
		if declaration, present := extensions["openai/elicitation"]; present {
			settings, err := objectField(declaration, l)
			if err != nil {
				return nil, err
			}
			for _, setting := range []string{"form", "userVerification"} {
				if declaration, present := settings[setting]; present {
					if _, err = objectField(declaration, l); err != nil {
						return nil, err
					}
					kind := "form"
					if setting == "userVerification" {
						kind = "verification"
					}
					result["openai/elicitation/create:"+kind] = true
				}
			}
		}
	}
	return result, nil
}
