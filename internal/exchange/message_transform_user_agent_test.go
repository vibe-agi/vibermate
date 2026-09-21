package exchange

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
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

// Exercise the shipped JS, real Exchange, final authentication, and HTTP wire.
// The injected transport only connects to a local fixture, never the named origin.
func TestClientMetadataScriptReachesProviderWire(t *testing.T) {
	source, err := os.ReadFile("../../javascript/hide-client-metadata/request.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, script, wantUA              string
		managed, accountOverride, invalid bool
		tls, http2                        bool
	}{
		{name: "passthrough script", script: string(source), wantUA: "vibermate-client/0.0.0"},
		{name: "managed script", managed: true, script: string(source), wantUA: "vibermate-client/0.0.0"},
		{name: "passthrough HTTPS", tls: true, script: string(source), wantUA: "vibermate-client/0.0.0"},
		{name: "passthrough HTTPS HTTP2", http2: true, script: string(source), wantUA: "vibermate-client/0.0.0"},
		{name: "managed HTTPS HTTP2", managed: true, http2: true, script: string(source), wantUA: "vibermate-client/0.0.0"},
		{name: "passthrough no-op", script: `request.body = request.body;`, wantUA: "fixture-client/9.8.7"},
		{name: "managed no-op", managed: true, script: `request.body = request.body;`, wantUA: "fixture-client/9.8.7"},
		{name: "passthrough delete", script: `delete request.headers["user-agent"];`},
		{name: "managed empty", managed: true, script: `request.headers["user-agent"] = "";`},
		{name: "account final override", managed: true, accountOverride: true, script: string(source), wantUA: "account-fixture/2.0"},
		{name: "passthrough multiple rejected", script: `request.headers["user-agent"] = ["a", "b"];`, invalid: true},
		{name: "managed multiple rejected", managed: true, script: `request.headers["user-agent"] = ["a", "b"];`, invalid: true},
		{name: "non-ASCII rejected", managed: true, script: `request.headers["user-agent"] = "客户端";`, invalid: true},
		{name: "too long rejected", managed: true, script: `request.headers["user-agent"] = "a".repeat(513);`, invalid: true},
		{name: "injection rejected", script: `request.headers["user-agent"] = "agent\r\nX-Injected: yes";`, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := testAccount{id: "account.selected", revision: 3, epoch: 7}
			options := testPlanOptions{destination: environment.DestinationKindOriginal,
				providerOrigin: "https://api.anthropic.com", backend: protocolspec.DialectAnthropicMessages,
				modelMode: environment.ModelModePassthrough, transform: messagetransform.Policy{RequestJavaScript: test.script}}
			body := completeClientRequest()
			responseBody := []byte(`{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-client-alias","content":[{"type":"text","text":"unchanged fixture"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`)
			headers := http.Header{"User-Agent": {"fixture-client/9.8.7"}, "Version": {"9.8.7"},
				"X-Install-Id": {"installation-fixture"}, "Session-Id": {"session-fixture"},
				"Authorization": {"Bearer client-fixture"}, "Anthropic-Version": {"2023-06-01"}}
			if test.managed {
				options.destination = environment.DestinationKindUpstream
				options.clientProtocol = environment.ClientProtocolOpenAIResponses
				options.chatGPTClient = true
				options.providerOrigin = "https://chatgpt.com"
				options.backend = protocolspec.DialectOpenAIResponses
				options.accounts, options.preferred = []testAccount{account}, account.id
				body = []byte(`{"model":"codex-client-alias","stream":false,"input":[{"type":"message","role":"user","content":"hello"}]}`)
				responseBody = completeResponsesProviderResponse("codex-client-alias")
			}
			if test.http2 {
				options.downstreamProtocol = wireprofile.ApplicationProtocolHTTP2
			}
			plan := mustEnvironmentRequestPlan(t, options)
			pipeline := newTestPipeline(t, newAccountAuthority(t, account), &providerDouble{}, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			observed := make(chan *http.Request, 1)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				wireBody, readErr := io.ReadAll(request.Body)
				if readErr != nil {
					t.Error(readErr)
				}
				copy := request.Clone(context.Background())
				copy.Body = io.NopCloser(bytes.NewReader(wireBody))
				observed <- copy
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write(responseBody)
			}))
			protocol := wireprofile.ApplicationProtocolHTTP1
			if test.tls || test.http2 {
				server.EnableHTTP2 = test.http2
				server.StartTLS()
				if test.http2 {
					protocol = wireprofile.ApplicationProtocolHTTP2
				}
			} else {
				server.Start()
			}
			defer server.Close()
			localURL, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(
				`{"https://api.openai.com/auth":{"chatgpt_account_id":"selected-account"}}`)) + ".fixture"
			var set map[string]string
			if test.accountOverride {
				set = map[string]string{"User-Agent": test.wantUA}
			}
			material, err := providerauth.NewMaterial(token, set, nil)
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
			_, err = pipeline.Execute(context.Background(), mustClientRequestWithOptionsAndHTTPProtocol(t,
				"exchange-client-metadata", plan, body, protocol, WithOriginalHeaders(headers), WithClientUserAgent("fixture-client/9.8.7")), downstream)
			if test.invalid {
				if ReasonOf(err) != ReasonMessageTransformFailed || len(observed) != 0 || len(downstream.envelopesSnapshot()) != 0 {
					t.Fatalf("invalid UA reached wire/downstream: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case outbound := <-observed:
				if (test.http2 && outbound.ProtoMajor != 2) || ((test.http2 || test.tls) && outbound.TLS == nil) {
					t.Fatal("test did not exercise the requested TLS/HTTP2 wire")
				}
				if got := outbound.Header.Get("User-Agent"); got != test.wantUA {
					t.Fatalf("actual wire UA = %q, want %q", got, test.wantUA)
				}
				if test.wantUA == "" && len(outbound.Header.Values("User-Agent")) != 0 {
					t.Fatal("Go synthesized a UA after explicit omission")
				}
				if test.script == string(source) && outbound.Header.Get("Version") != "0.0.0" {
					t.Fatal("version replacement did not reach wire")
				}
				wireBody, _ := io.ReadAll(outbound.Body)
				if !test.managed {
					if !bytes.Equal(wireBody, body) || !bytes.Equal(downstream.bytesSnapshot(), responseBody) ||
						outbound.Header.Get("Authorization") != "Bearer client-fixture" || outbound.Header.Get("Anthropic-Version") != "2023-06-01" {
						t.Fatal("passthrough body/response/credentials/protocol changed")
					}
					if test.script == string(source) && outbound.Header.Get("X-Install-Id") != "00000000-0000-4000-8000-000000000001" {
						t.Fatal("install ID replacement did not reach wire")
					}
				} else if outbound.Header.Get("Authorization") != "Bearer "+token || outbound.Header.Get("Chatgpt-Account-Id") != "selected-account" || outbound.Header.Get("X-Install-Id") != "" {
					t.Fatal("managed account changed or discarded client metadata was reintroduced")
				}
				if outbound.Header.Get("Session-Id") != "session-fixture" || outbound.ContentLength != int64(len(wireBody)) {
					t.Fatal("session ID or framing changed")
				}
			default:
				t.Fatal("no provider request observed")
			}
			if headers.Get("User-Agent") != "fixture-client/9.8.7" || headers.Get("Version") != "9.8.7" {
				t.Fatal("original client evidence mutated")
			}
		})
	}
}

func TestSeparateMessageTransformUserAgentPreservesUnchangedPolicy(t *testing.T) {
	value := func(text string) *string { return &text }
	for _, test := range []struct {
		name          string
		before, after http.Header
		want          *string
	}{
		{name: "nil input", after: http.Header{"User-Agent": {"edited"}}, want: value("edited")},
		{name: "no UA", before: http.Header{}, after: http.Header{}},
		{name: "unchanged", before: http.Header{"user-agent": {"original"}}, after: http.Header{"User-Agent": {"original"}}},
		{name: "added", before: http.Header{}, after: http.Header{"User-Agent": {"edited"}}, want: value("edited")},
		{name: "deleted", before: http.Header{"User-Agent": {"original"}}, after: http.Header{}, want: value("")},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := test.after.Clone()
			out, got, err := separateMessageTransformUserAgent(test.before, test.after)
			if err != nil || !reflect.DeepEqual(got, test.want) || !reflect.DeepEqual(original, test.after) {
				t.Fatalf("separation = %v, %v", got, err)
			}
			if test.want != nil && len(headerValuesFold(out, "User-Agent")) != 0 {
				t.Fatal("two UA authorities remain")
			}
		})
	}
}

func TestClientMetadataScriptPreservesReplacementCharacterBody(t *testing.T) {
	source, err := os.ReadFile("../../javascript/hide-client-metadata/request.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{"", string(source)} {
		name := "disabled"
		if script != "" {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				destination: environment.DestinationKindOriginal, providerOrigin: "https://api.anthropic.com",
				backend: protocolspec.DialectAnthropicMessages, modelMode: environment.ModelModePassthrough,
				transform: messagetransform.Policy{RequestJavaScript: script},
			})
			responseBody := []byte(`{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-client-alias","content":[{"type":"text","text":"valid � response"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`)
			provider := &providerDouble{results: []providerResult{{response: jsonResponse(http.StatusOK, responseBody)}}}
			pipeline := newTestPipeline(t, nil, provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			body := bytes.ReplaceAll(completeClientRequest(), []byte("hello"), []byte("explain �"))
			if !bytes.Contains(body, []byte("�")) {
				t.Fatal("fixture must contain a literal U+FFFD")
			}
			downstream := &downstreamRecorder{}
			_, err := pipeline.Execute(context.Background(), mustClientRequestWithOptions(t, "replacement-character", plan, body,
				WithOriginalHeaders(http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer synthetic"}})), downstream)
			requests := provider.requestsSnapshot()
			if err != nil || len(requests) != 1 || !bytes.Equal(requests[0].Body(), body) || !bytes.Equal(downstream.bytesSnapshot(), responseBody) {
				t.Fatalf("valid Unicode body failed to round-trip: %v", err)
			}
		})
	}
}
