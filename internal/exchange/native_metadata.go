package exchange

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

const codexTurnStateHeader = "X-Codex-Turn-State"

// Only protocol metadata crosses a managed credential boundary. In particular,
// never copy cookies, authentication, hop-by-hop fields or compressed-body
// validators onto our normalized downstream representation.
func nativeResponseHeaders(source http.Header) http.Header {
	result := make(http.Header)
	hop := make(map[string]bool)
	for _, connection := range headerValuesFold(source, "Connection") {
		for _, name := range strings.Split(connection, ",") {
			hop[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	for name, values := range source {
		lower := strings.ToLower(name)
		allowed := false
		switch lower {
		case "x-request-id", "request-id", "retry-after", "retry-after-ms", "x-should-retry", "x-models-etag",
			"openai-model", "x-openai-model", "x-reasoning-included", "x-codex-turn-state",
			"x-codex-promo-message", "x-codex-rate-limit-reached-type",
			"x-codex-credits-has-credits", "x-codex-credits-unlimited", "x-codex-credits-balance",
			"x-codex-safety-buffering-enabled", "x-codex-safety-buffering-faster-model":
			allowed = true
		default:
			// Codex discovers metered families by suffix, including model-specific
			// limits; enumerating individual models would lose new quota windows.
			if strings.HasPrefix(lower, "x-") {
				for _, suffix := range []string{"-primary-used-percent", "-primary-window-minutes",
					"-primary-reset-at", "-secondary-used-percent", "-secondary-window-minutes",
					"-secondary-reset-at", "-limit-name"} {
					allowed = allowed || strings.HasSuffix(lower, suffix)
				}
			}
		}
		if allowed && !hop[lower] {
			result[http.CanonicalHeaderKey(name)] = slices.Clone(values)
		}
	}
	return result
}

const (
	maxCodexTurnStates   = 2048
	maxCodexTurnAliases  = 16
	maxCodexTurnRequests = 2048
	codexTurnStateTTL    = 24 * time.Hour
)

type codexTurnStateBinding struct {
	scope, owner [32]byte
	hasTurn      bool
}

// Codex retains its first turn token even when a managed account changes. Only
// hashes of tokens actually returned in this scope can name the current owner.
// The current owner's token is an explicitly owned, bounded ephemeral buffer;
// it is zeroed on ownership change, cancellation, expiry, eviction and shutdown.
// Neither the buffer nor its aliases are persisted or exposed in diagnostics.
type codexTurnState struct {
	owner                [32]byte
	generation, admitted uint64
	token                []byte
	aliases              [][32]byte
	expires              time.Time
	expiry               *time.Timer
}

// Admission order, not response arrival order, establishes ownership. A late
// response must present the same operation witness and ownership generation.
type codexTurnRequestWitness struct {
	id          string
	sequence    uint64
	ctx, client context.Context
	stop        func() bool
	binding     codexTurnStateBinding
	generation  uint64
	bound       bool
}

func codexTurnBinding(request ClientRequest, selection frozenSelection, account providerauth.AccountRef, decoded protocolcore.Request) (codexTurnStateBinding, bool) {
	if !isNativeCodexSelection(request, selection) || account.Validate() != nil || !request.hasCorrelation {
		return codexTurnStateBinding{}, false
	}
	headers := nativeChatGPTProtocolHeaders(request, selection)
	session := headers.Get("Session-Id")
	if session == "" {
		session = headers.Get("Thread-Id")
	}
	if session == "" || len(session) > 1024 {
		return codexTurnStateBinding{}, false
	}
	turn := ""
	for _, value := range decoded.ProtocolEvidence {
		if value.Name == "openai_responses.turn_id" {
			turn = value.Value
		}
	}
	scope, _ := json.Marshal([]any{request.CaptureAdmissionRef(), request.admission.CredentialRevision(), session, headers.Get("Thread-Id"), turn})
	owner, _ := json.Marshal([]any{account, selection.environmentID.String(), selection.routeID.String(),
		selection.routeRevision, selection.target.Origin().String(), selection.targetRef, selection.targetRevision,
		selection.protocolPlanID.String(), selection.protocolPlanRevision, decoded.EffectiveModel, turn})
	return codexTurnStateBinding{scope: sha256.Sum256(scope), owner: sha256.Sum256(owner), hasTurn: turn != ""}, true
}

func codexTurnToken(headers http.Header) string {
	values := headerValuesFold(headers, codexTurnStateHeader)
	if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > 8192 {
		return ""
	}
	for _, b := range []byte(values[0]) {
		if b < 0x21 || b > 0x7e {
			return ""
		}
	}
	return values[0]
}

func (pipeline *Pipeline) replayCodexTurnState(request ClientRequest, selection frozenSelection, account providerauth.AccountRef, decoded protocolcore.Request, headers http.Header) {
	binding, ok := codexTurnBinding(request, selection, account, decoded)
	if !ok {
		return
	}
	source, _ := request.OriginalHeaders()
	for _, connection := range headerValuesFold(source, "Connection") {
		for _, name := range strings.Split(connection, ",") {
			if strings.EqualFold(strings.TrimSpace(name), codexTurnStateHeader) {
				return
			}
		}
	}
	token := codexTurnToken(source)
	if token == "" && len(headerValuesFold(source, codexTurnStateHeader)) != 0 {
		return
	}
	pipeline.mu.Lock()
	defer pipeline.mu.Unlock()
	witness := pipeline.turnStateRequests[request.exchangeID]
	if pipeline.closing || witness == nil || witness.ctx.Err() != nil || witness.client.Err() != nil {
		return
	}
	pipeline.expireCodexTurnStatesLocked()
	state := pipeline.turnStates[binding.scope]
	if token != "" && state != nil && !slices.Contains(state.aliases, sha256.Sum256([]byte(token))) {
		// An unknown/foreign client token proves no relationship to this state.
		return
	}
	if state != nil && witness.sequence < state.admitted && state.owner != binding.owner {
		return
	}
	if state != nil && token == "" && len(state.aliases) != 0 {
		// A fresh client without an anchor cannot inherit another request's
		// aliases, even if it supplies the same session and turn identifiers.
		if witness.sequence < state.admitted {
			return
		}
		pipeline.removeCodexTurnStateLocked(binding.scope)
		state = nil
	}
	if state != nil && state.owner != binding.owner && !binding.hasTurn {
		// Session/thread identity alone cannot prove that this is the same turn.
		// Preserve legacy same-owner replay, but never translate an old owner's
		// anchor into a new owner's token without explicit turn evidence.
		pipeline.removeCodexTurnStateLocked(binding.scope)
		state = nil
	}
	if state == nil {
		// A response may establish fresh state even if the incoming client had
		// an unrecognized token. That token is never forwarded or made an alias.
		state = pipeline.newCodexTurnStateLocked(binding, witness.sequence)
	} else if state.owner != binding.owner {
		clear(state.token)
		state.token = nil
		state.owner = binding.owner
		state.generation = witness.sequence
	}
	state.admitted = max(state.admitted, witness.sequence)
	witness.binding, witness.generation, witness.bound = binding, state.generation, true
	if token != "" && len(state.token) != 0 {
		headers.Set(codexTurnStateHeader, string(state.token))
	}
}

func (pipeline *Pipeline) managedResponseHeaders(request ClientRequest, selection frozenSelection, frozen providertransport.Request, decoded protocolcore.Request, source http.Header) http.Header {
	headers := nativeResponseHeaders(source)
	if isNativeCodexSelection(request, selection) {
		rewriteMappedModelHeaders(headers, decoded.RequestedModel, decoded.EffectiveModel)
	}
	token := codexTurnToken(headers)
	headers.Del(codexTurnStateHeader)
	account, _ := frozen.AccountRef()
	if isNativeCodexSelection(request, selection) {
		scopeManagedModelETag(headers, request, selection, account)
	}
	binding, ok := codexTurnBinding(request, selection, account, decoded)
	if token == "" || !ok {
		return headers
	}
	pipeline.mu.Lock()
	defer pipeline.mu.Unlock()
	pipeline.expireCodexTurnStatesLocked()
	witness := pipeline.turnStateRequests[request.exchangeID]
	state := pipeline.turnStates[binding.scope]
	if pipeline.closing || witness == nil || !witness.bound || witness.ctx.Err() != nil || witness.client.Err() != nil ||
		witness.binding != binding || state == nil || state.owner != binding.owner || state.generation != witness.generation {
		return headers
	}
	if len(state.token) == 0 {
		state.token = []byte(token)
	}
	key := sha256.Sum256([]byte(token))
	if !slices.Contains(state.aliases, key) {
		if len(state.aliases) == maxCodexTurnAliases {
			// Preserve the first client anchor across arbitrary account switches.
			state.aliases = slices.Delete(state.aliases, 1, 2)
		}
		state.aliases = append(state.aliases, key)
	}
	headers.Set(codexTurnStateHeader, token)
	return headers
}

func (pipeline *Pipeline) beginCodexTurnRequestLocked(id string, ctx, client context.Context) *codexTurnRequestWitness {
	if len(pipeline.turnStateRequests) >= maxCodexTurnRequests || pipeline.turnStateRequests[id] != nil || pipeline.turnStateSequence == ^uint64(0) {
		return nil
	}
	pipeline.turnStateSequence++
	witness := &codexTurnRequestWitness{id: id, sequence: pipeline.turnStateSequence, ctx: ctx, client: client}
	pipeline.turnStateRequests[id] = witness
	witness.stop = context.AfterFunc(ctx, func() {
		pipeline.mu.Lock()
		defer pipeline.mu.Unlock()
		pipeline.cancelCodexTurnRequestLocked(witness)
	})
	return witness
}

func (pipeline *Pipeline) cancelCodexTurnRequestLocked(witness *codexTurnRequestWitness) {
	if witness.bound {
		state := pipeline.turnStates[witness.binding.scope]
		if state != nil && state.owner == witness.binding.owner && state.generation == witness.generation {
			pipeline.removeCodexTurnStateLocked(witness.binding.scope)
		}
		witness.bound = false
	}
}

func (pipeline *Pipeline) finishCodexTurnRequestLocked(witness *codexTurnRequestWitness) {
	if witness == nil {
		return
	}
	witness.stop()
	if witness.ctx.Err() != nil || witness.client.Err() != nil {
		pipeline.cancelCodexTurnRequestLocked(witness)
	}
	if pipeline.turnStateRequests[witness.id] == witness {
		delete(pipeline.turnStateRequests, witness.id)
	}
}

func (pipeline *Pipeline) newCodexTurnStateLocked(binding codexTurnStateBinding, sequence uint64) *codexTurnState {
	if len(pipeline.turnStates) >= maxCodexTurnStates {
		var oldest [32]byte
		var candidate *codexTurnState
		for scope, state := range pipeline.turnStates {
			if candidate == nil || state.expires.Before(candidate.expires) || (state.expires.Equal(candidate.expires) && state.admitted < candidate.admitted) {
				oldest, candidate = scope, state
			}
		}
		pipeline.removeCodexTurnStateLocked(oldest)
	}
	state := &codexTurnState{owner: binding.owner, generation: sequence, admitted: sequence, expires: pipeline.now().Add(codexTurnStateTTL)}
	pipeline.turnStates[binding.scope] = state
	state.expiry = time.AfterFunc(codexTurnStateTTL, func() {
		pipeline.mu.Lock()
		defer pipeline.mu.Unlock()
		if pipeline.turnStates[binding.scope] == state {
			pipeline.removeCodexTurnStateLocked(binding.scope)
		}
	})
	return state
}

func (pipeline *Pipeline) expireCodexTurnStatesLocked() {
	now := pipeline.now()
	for scope, state := range pipeline.turnStates {
		if !now.Before(state.expires) {
			pipeline.removeCodexTurnStateLocked(scope)
		}
	}
}

func (pipeline *Pipeline) removeCodexTurnStateLocked(scope [32]byte) {
	if state := pipeline.turnStates[scope]; state != nil {
		state.expiry.Stop()
		clear(state.token)
		state.token = nil
		delete(pipeline.turnStates, scope)
	}
}

func (pipeline *Pipeline) clearCodexTurnStatesLocked() {
	for scope := range pipeline.turnStates {
		pipeline.removeCodexTurnStateLocked(scope)
	}
}
