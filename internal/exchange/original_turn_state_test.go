package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

func TestOriginalAccountNativeTurnStatePreservesExplicitPassthrough(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, script := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/script=%v", stream, script), func(t *testing.T) {
				requestHeaders := http.Header{codexTurnStateHeader: {"original-request-state"}}
				responseHeaders := http.Header{codexTurnStateHeader: {"original-response-state"}, "X-Models-Etag": {`"original-catalog"`}}
				forwarded, headers := runOriginalNativeToken(t, stream, script, requestHeaders, responseHeaders)
				if forwarded.Headers().Get(codexTurnStateHeader) != "original-request-state" {
					t.Fatal("original request state was changed")
				}
				if headers.Get(codexTurnStateHeader) != "original-response-state" {
					t.Fatalf("explicit original response state lost: %v", headers)
				}
				if headers.Get("X-Models-Etag") != `"original-catalog"` {
					t.Fatal("original validator was scoped")
				}
			})
		}
	}
}

func TestOriginalAccountNativeTurnStateFiltersMalformedAndHopHeaders(t *testing.T) {
	for _, side := range []string{"request", "response"} {
		for _, scenario := range []string{"multiple", "space", "nonascii", "oversized", "hop", "mixed-case-duplicate"} {
			t.Run(side+"/"+scenario, func(t *testing.T) {
				incoming := http.Header{codexTurnStateHeader: {"original-request-state"}}
				outgoing := http.Header{codexTurnStateHeader: {"original-response-state"}}
				target := incoming
				if side == "response" {
					target = outgoing
				}
				switch scenario {
				case "multiple":
					target[codexTurnStateHeader] = []string{"one", "two"}
				case "space":
					target.Set(codexTurnStateHeader, "bad token")
				case "nonascii":
					target.Set(codexTurnStateHeader, "雪")
				case "oversized":
					target.Set(codexTurnStateHeader, strings.Repeat("x", 8193))
				case "hop":
					target.Set("Connection", "keep-alive, x-codex-turn-state")
				case "mixed-case-duplicate":
					target["x-cOdex-tUrn-state"] = []string{"second"}
				}
				forwarded, headers := runOriginalNativeToken(t, false, true, incoming, outgoing)
				got := headers.Get(codexTurnStateHeader)
				if side == "request" {
					got = forwarded.Headers().Get(codexTurnStateHeader)
				}
				if got != "" {
					t.Fatalf("invalid %s token escaped (%d bytes)", side, len(got))
				}
			})
		}
	}
}

func runOriginalNativeToken(t *testing.T, stream, script bool, incoming, outgoing http.Header) (providertransport.Request, http.Header) {
	t.Helper()
	account := testAccount{id: "managed-unused", revision: 3, epoch: 7}
	policy := messagetransform.Policy{}
	if script {
		policy = messagetransform.Policy{RequestJavaScript: `if(request.headers["x-codex-turn-state"]!==undefined)throw new Error("private header exposed"); request.headers["x-codex-turn-state"]="script-invented"; request.headers["x-script-ran"]="yes";`, ResponseJavaScript: `if(response.headers["x-codex-turn-state"]!==undefined)throw new Error("private header exposed"); response.headers["x-script-ran"]="yes";`}
	}
	options := testPlanOptions{clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true, destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com", backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough, originalAccount: true, accounts: []testAccount{account}, preferred: account.id, transform: policy}
	plan := mustEnvironmentRequestPlan(t, options)
	body := completeResponsesProviderResponse("gpt-native")
	response := jsonResponse(http.StatusOK, body)
	if stream {
		event, _ := json.Marshal(map[string]any{"type": "response.completed", "response": json.RawMessage(body)})
		response = streamResponse(http.StatusOK, strings.NewReader("event: response.completed\ndata: "+string(event)+"\n\n"))
	}
	for name, values := range outgoing {
		response.Header[name] = append([]string(nil), values...)
	}
	provider := &providerDouble{results: []providerResult{{response: response}}}
	pipeline := newTestPipeline(t, newAccountAuthority(t, account), provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	headers := incoming.Clone()
	headers.Set("Authorization", "Bearer original-native-owner")
	headers.Set("Chatgpt-Account-Id", "original-workspace")
	headers.Set("Cookie", "original-cookie")
	headers.Set("Session-Id", "original-session")
	headers.Set("Thread-Id", "original-thread")
	admission, err := captureadmission.NewManual("original-capture", 1, "Original native state")
	if err != nil {
		t.Fatal(err)
	}
	request := mustClientRequestWithOptions(t, "original-token", plan, []byte(fmt.Sprintf(`{"model":"gpt-native","stream":%v,"client_metadata":{"turn_id":"original-turn"},"input":"hello"}`, stream)), WithOriginalHeaders(headers), WithIngressCorrelation(admission, "original-connection"))
	downstream := &downstreamRecorder{}
	if _, err := pipeline.Execute(context.Background(), request, downstream); err != nil {
		t.Fatal(err)
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 1 {
		t.Fatal("unexpected transport count")
	}
	forwarded := requests[0]
	if forwarded.CredentialMode() != providerauth.CredentialClientPassthrough {
		t.Fatal("original credentials were substituted")
	}
	if _, ok := forwarded.AccountRef(); ok {
		t.Fatal("original request acquired a managed account")
	}
	if forwarded.Headers().Get("Authorization") != "Bearer original-native-owner" || forwarded.Headers().Get("Chatgpt-Account-Id") != "original-workspace" || forwarded.Headers().Get("Cookie") != "original-cookie" {
		t.Fatal("original credential bytes changed")
	}
	if forwarded.Target().Origin().String() != "https://chatgpt.com" || strings.TrimPrefix(forwarded.RelativePath(), "/") != "backend-api/codex/responses" {
		t.Fatal("original route changed")
	}
	pipeline.mu.Lock()
	cached, witnesses := len(pipeline.turnStates), len(pipeline.turnStateRequests)
	pipeline.mu.Unlock()
	if cached != 0 || witnesses != 0 {
		t.Fatal("original tokens entered managed cache/retained witness")
	}
	// Even a passthrough Request with no AccountRef cannot authorize a fixed
	// managed selection. Authorization comes from the actual frozen policy too.
	options.originalAccount = false
	managedPlan := mustEnvironmentRequestPlan(t, options)
	selection, err := selectFrozenPlan(managedPlan)
	if err != nil {
		t.Fatal(err)
	}
	denied := pipeline.managedResponseHeaders(request, selection, forwarded, protocolcore.Request{}, http.Header{codexTurnStateHeader: {"unowned"}})
	if denied.Get(codexTurnStateHeader) != "" {
		t.Fatal("missing managed AccountRef authorized passthrough")
	}
	envelopes := downstream.envelopesSnapshot()
	if len(envelopes) != 1 {
		t.Fatal("missing response envelope")
	}
	return forwarded, envelopes[0].Headers()
}
