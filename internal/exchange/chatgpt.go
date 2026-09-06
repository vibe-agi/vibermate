package exchange

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

// Codex's HTTP Responses request contract is defined by
// openai/codex@ac192cd7937b0d73edc6dffe009940ae53782dd4:
// codex-rs/codex-api/src/endpoint/responses.rs and core/src/client.rs.
// These are protocol fields, not account credentials. Only copy them between
// native Codex endpoints, before scripts and the selected account's Set/Delete
// policy. Never inherit Cookie, account ID, attestation or the opaque turn-state
// token: the latter belongs to a previous upstream/account routing context.
func nativeChatGPTProtocolHeaders(request ClientRequest, selection frozenSelection) http.Header {
	headers := make(http.Header)
	if request.operation.id.String() != operationcatalog.OpenAICodexResponsesCreateID ||
		selection.codecPlan.ProviderDialect() != protocolspec.DialectOpenAIResponses ||
		!upstreamendpoint.IsChatGPTCodexOrigin(selection.target.Origin()) {
		return headers
	}
	source, _ := request.OriginalHeaders()
	hop := make(map[string]bool)
	for _, value := range headerValuesFold(source, "Connection") {
		for _, token := range strings.Split(value, ",") {
			hop[strings.ToLower(strings.TrimSpace(token))] = true
		}
	}
	for _, name := range []string{
		"Originator", "Version", "Session-Id", "Thread-Id", "X-Client-Request-Id",
		"X-Codex-Beta-Features", "X-Codex-Parent-Thread-Id", "X-Openai-Subagent",
		"X-Codex-Routing-Hint",
	} {
		if !hop[strings.ToLower(name)] {
			if values := headerValuesFold(source, name); len(values) != 0 {
				headers[name] = slices.Clone(values)
			}
		}
	}
	return headers
}

// A routing hint describes the actual model/tier in the body. Update inherited
// hints when model mapping or a script changes that body. An explicitly changed
// or deleted header remains the script's decision; account overrides run later.
func refreshChatGPTRoutingHint(headers http.Header, before, after []byte) {
	const name = "X-Codex-Routing-Hint"
	if len(headers.Values(name)) != 1 || bytes.Equal(before, after) {
		return
	}
	previous := chatGPTRoutingHint(before)
	if previous == "" || headers.Get(name) != previous {
		return
	}
	if next := chatGPTRoutingHint(after); next != "" {
		headers.Set(name, next)
	} else {
		headers.Del(name)
	}
}

func chatGPTRoutingHint(body []byte) string {
	var request struct {
		Model       string `json:"model"`
		ServiceTier string `json:"service_tier"`
	}
	if json.Unmarshal(body, &request) != nil || request.Model == "" ||
		strings.ContainsAny(request.Model+request.ServiceTier, ";\r\n") {
		return ""
	}
	hint := "model=" + request.Model
	if request.ServiceTier != "" {
		hint += ";tier=" + request.ServiceTier
	}
	return hint
}

func isNativeChatGPTResponses(request providertransport.Request) bool {
	origin := request.Target().Origin()
	return upstreamendpoint.IsChatGPTCodexOrigin(origin) && request.Method() == http.MethodPost &&
		request.RelativePath() == upstreamendpoint.ProviderRelativePath(origin, "v1/responses")
}

// A missing media type may be inferred only when the caller has an explicit
// protocol contract for it. An empty, malformed, or conflicting header is not
// missing. This selects a decoder; it never bypasses SSE/terminal validation or
// tool approval, and it never alters the upstream headers recorded as evidence.
func responseIsEventStream(headers http.Header, allowMissing bool) bool {
	values := headerValuesFold(headers, "Content-Type")
	if len(values) == 0 {
		return allowMissing
	}
	return len(values) == 1 && contentTypeMatches(values[0], "text/event-stream")
}

func headerValuesFold(headers http.Header, wanted string) []string {
	var values []string
	for name, entries := range headers {
		if strings.EqualFold(name, wanted) {
			values = append(values, entries...)
		}
	}
	return values
}
