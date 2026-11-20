package exchange

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

// agentIDStandIn is the shipped script's derivation with its default key.
func agentIDStandIn(kind, original string) string {
	mac := hmac.New(sha256.New, []byte("replace this with your own key text"))
	mac.Write([]byte(kind + "\n" + strings.ToLower(original)))
	digest := mac.Sum(nil)
	if kind == "claude.device" {
		return hex.EncodeToString(digest)[:len(original)]
	}
	id := digest[:16]
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	text := hex.EncodeToString(id)
	return text[:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:]
}

// Exercise the shipped JS, real Exchange, final authentication and HTTP wire:
// what the provider receives, on each path a Claude or Codex request takes.
func TestAgentIDsScriptReachesProviderWire(t *testing.T) {
	source, err := os.ReadFile("../../javascript/hide-agent-ids/request.js")
	if err != nil {
		t.Fatal(err)
	}
	const (
		prompt  = "2491cf85-d7d6-4e5d-81c6-41a46d940c02"
		device  = "e0d7088cb33b8d2ca86155708c3a756349c724a3d1117efac990a9b3edab2bd4"
		install = "2f56266b-f376-4497-b53c-b1abea9e47ca"
		session = "01a1f977-87e9-7d02-b55f-3638e71b9fa4"
	)
	userID, _ := json.Marshal(map[string]string{"device_id": device, "account_uuid": "", "session_id": session})
	claudeBody, _ := json.Marshal(map[string]any{
		"model": "claude-client-alias", "max_tokens": 64,
		"system": []map[string]string{
			{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.329.034; cc_entrypoint=cli; cch=bb47f; cc_prompt_id=" + prompt + "; cc_prompt_index=0;"},
			{"type": "text", "text": "You are Claude Code."},
		},
		"messages": []map[string]any{{"role": "user", "content": "hello"}},
		"metadata": map[string]string{"user_id": string(userID)},
	})
	turnMetadata, _ := json.Marshal(map[string]string{"installation_id": install, "session_id": session})
	codexBody, _ := json.Marshal(map[string]any{
		"model": "codex-client-alias", "stream": false, "store": false, "prompt_cache_key": session,
		"input":           []map[string]any{{"type": "message", "role": "user", "content": "hello"}},
		"client_metadata": map[string]string{"session_id": session, "x-codex-installation-id": install, "x-codex-turn-metadata": string(turnMetadata)},
	})
	installStandIn := agentIDStandIn("codex.installation", install)
	for _, test := range []struct {
		name           string
		codex, managed bool
		hidden         []string
		standIns       []string
		// Own-credential Codex forwards X-Codex-Turn-Metadata as the script
		// left it; the managed transport never forwards it.
		turnHeader bool
	}{
		{name: "Claude, own credential", hidden: []string{prompt, device},
			standIns: []string{agentIDStandIn("claude.prompt", prompt), agentIDStandIn("claude.device", device)}},
		{name: "Codex, own credential", codex: true, hidden: []string{install},
			standIns: []string{installStandIn}, turnHeader: true},
		{name: "Codex, managed", codex: true, managed: true, hidden: []string{install},
			standIns: []string{installStandIn}},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := testAccount{id: "account.selected", revision: 3, epoch: 7}
			transform := messagetransform.Policy{RequestJavaScript: string(source)}
			options := testPlanOptions{destination: environment.DestinationKindOriginal,
				providerOrigin: "https://api.anthropic.com", backend: protocolspec.DialectAnthropicMessages,
				modelMode: environment.ModelModePassthrough, transform: transform}
			body := claudeBody
			headers := http.Header{"User-Agent": {"claude-cli/2.1.329 (external, cli)"}, "X-Claude-Code-Session-Id": {session},
				"Authorization": {"Bearer client-fixture"}, "Anthropic-Version": {"2023-06-01"}}
			responseBody := []byte(`{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-client-alias","content":[{"type":"text","text":"fixture"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`)
			if test.codex {
				options = testPlanOptions{clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
					destination: environment.DestinationKindOriginal, providerOrigin: "https://chatgpt.com",
					backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough, transform: transform}
				if test.managed {
					options.destination = environment.DestinationKindUpstream
					options.accounts, options.preferred = []testAccount{account}, account.id
				}
				body = codexBody
				headers = chatGPTClientHeaderFixture()
				headers.Set("Session-Id", session)
				headers.Set("X-Codex-Turn-Metadata", string(turnMetadata))
				responseBody = completeResponsesProviderResponse("codex-client-alias")
			}
			plan := mustEnvironmentRequestPlan(t, options)
			pipeline := newTestPipeline(t, newAccountAuthority(t, account), &providerDouble{}, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			observed := make(chan *http.Request, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				wireBody, readErr := io.ReadAll(request.Body)
				if readErr != nil {
					t.Error(readErr)
				}
				clone := request.Clone(context.Background())
				clone.Body = io.NopCloser(bytes.NewReader(wireBody))
				observed <- clone
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write(responseBody)
			}))
			defer server.Close()
			localURL, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(
				`{"https://api.openai.com/auth":{"chatgpt_account_id":"selected-account"}}`)) + ".fixture"
			material, err := providerauth.NewMaterial(token, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer material.Destroy()
			encoded, err := material.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			defer clear(encoded)
			authenticator, err := providertransport.NewStaticBearerAuthenticator(chatGPTFixtureSecrets{encoded})
			if err != nil {
				t.Fatal(err)
			}
			client, err := providertransport.NewClient(providertransport.ClientOptions{
				Coordinator: pipeline.actions.(offlinehold.Coordinator), Authenticator: authenticator,
				Transport: chatGPTLoopbackTransport{server.Client().Transport, localURL},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := client.Shutdown(ctx); err != nil {
					t.Error(err)
				}
			}()
			pipeline.provider = client
			downstream := &downstreamRecorder{}
			if _, err := pipeline.Execute(context.Background(), mustClientRequestWithOptionsAndHTTPProtocol(t,
				"exchange-agent-ids", plan, body, wireprofile.ApplicationProtocolHTTP1, WithOriginalHeaders(headers)), downstream); err != nil {
				t.Fatal(err)
			}
			var outbound *http.Request
			select {
			case outbound = <-observed:
			default:
				t.Fatal("no request reached the provider")
			}
			wireBody, _ := io.ReadAll(outbound.Body)
			var wireHeaders strings.Builder
			for name, values := range outbound.Header {
				wireHeaders.WriteString(name + ": " + strings.Join(values, ",") + "\n")
			}
			for _, original := range test.hidden {
				if bytes.Contains(wireBody, []byte(original)) || strings.Contains(wireHeaders.String(), original) {
					t.Fatalf("%s reached the provider:\n%s\n%s", original, wireHeaders.String(), wireBody)
				}
			}
			for _, standIn := range test.standIns {
				if !bytes.Contains(wireBody, []byte(standIn)) {
					t.Fatalf("stand-in %s is missing from the provider body: %s", standIn, wireBody)
				}
			}
			// The session ID is the agent's and the cache's: never rewritten.
			if !bytes.Contains(wireBody, []byte(session)) {
				t.Fatalf("the session ID did not reach the provider: %s", wireBody)
			}
			if value := outbound.Header.Get("X-Codex-Turn-Metadata"); test.turnHeader != strings.Contains(value, installStandIn) || (!test.turnHeader && value != "") {
				t.Fatalf("X-Codex-Turn-Metadata at the provider = %q", value)
			}
		})
	}
}
