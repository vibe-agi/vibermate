package exchange

import (
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
			"openai-model", "x-reasoning-included", "x-codex-turn-state",
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

// Only hashes and ownership live here, never the provider's opaque token.
// State is not persisted, serialized into diagnostics, or inferred from a
// client-supplied value. A Runtime restart deliberately loses these bindings.
type codexTurnStateBinding struct {
	scope, owner [32]byte
	expires      time.Time
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
	scope, _ := json.Marshal([]any{request.CaptureAdmissionRef(), request.admission.CredentialRevision(), session, headers.Get("Thread-Id")})
	owner, _ := json.Marshal([]any{account, selection.environmentID.String(), selection.routeID.String(),
		selection.routeRevision, selection.target.Origin().String(), selection.targetRef, selection.targetRevision,
		selection.protocolPlanID.String(), selection.protocolPlanRevision, decoded.EffectiveModel, turn})
	return codexTurnStateBinding{scope: sha256.Sum256(scope), owner: sha256.Sum256(owner)}, true
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
	if token == "" {
		return
	}
	key := sha256.Sum256([]byte(token))
	pipeline.mu.Lock()
	defer pipeline.mu.Unlock()
	stored, known := pipeline.turnStates[key]
	if known && stored.scope == binding.scope && stored.owner == binding.owner && pipeline.now().Before(stored.expires) {
		headers.Set(codexTurnStateHeader, token)
	} else if known && stored.scope == binding.scope {
		delete(pipeline.turnStates, key)
	}
}

func (pipeline *Pipeline) managedResponseHeaders(request ClientRequest, selection frozenSelection, frozen providertransport.Request, decoded protocolcore.Request, source http.Header) http.Header {
	headers := nativeResponseHeaders(source)
	token := codexTurnToken(headers)
	headers.Del(codexTurnStateHeader)
	account, _ := frozen.AccountRef()
	binding, ok := codexTurnBinding(request, selection, account, decoded)
	if token == "" || !ok {
		return headers
	}
	now := pipeline.now()
	binding.expires = now.Add(24 * time.Hour)
	key := sha256.Sum256([]byte(token))
	pipeline.mu.Lock()
	defer pipeline.mu.Unlock()
	// ponytail: bounded scan of at most 2048 bindings; use an eviction queue if
	// measured metadata throughput makes this scan significant.
	var oldest [32]byte
	oldestAt := binding.expires
	for existing, value := range pipeline.turnStates {
		if !now.Before(value.expires) || (value.scope == binding.scope && value.owner != binding.owner) {
			delete(pipeline.turnStates, existing)
			continue
		}
		if !value.expires.After(oldestAt) {
			oldest, oldestAt = existing, value.expires
		}
	}
	if len(pipeline.turnStates) >= 2048 {
		delete(pipeline.turnStates, oldest)
	}
	pipeline.turnStates[key] = binding
	headers.Set(codexTurnStateHeader, token)
	return headers
}
