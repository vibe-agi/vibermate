package providertransport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func transformedUserAgentOptions(t *testing.T) RequestOptions {
	t.Helper()
	gate := newStartedGate(t)
	fixture := newTestRequest(t, gate, "transform-ua-construction", testTarget("provider.example", 443), nil)
	return RequestOptions{
		RequestID: fixture.requestID, ExchangeID: fixture.exchangeID,
		ParentAttemptID: fixture.parentAttemptID, EgressAttemptID: fixture.egressAttemptID,
		TargetRef: fixture.targetRef, Target: fixture.target, Provenance: fixture.provenance,
		Action: fixture.action, Method: fixture.method, RelativePath: fixture.relativePath,
		Body: fixture.body, CredentialMode: fixture.credentialMode, AccountRef: fixture.accountRef,
		SecretRef: fixture.secretReference, AuthDriverRef: fixture.authDriverRef,
		WireProfile: fixture.wireProfile, ClientProtocol: fixture.clientProtocol, ClientUserAgent: fixture.clientUserAgent,
	}
}

func TestRequestFreezesAndValidatesTransformedUserAgent(t *testing.T) {
	options := transformedUserAgentOptions(t)
	value := "script-agent/1.0"
	options.MessageTransformUserAgent = &value
	request, err := NewRequest(options)
	if err != nil {
		t.Fatal(err)
	}
	value = "caller-mutated"
	if request.messageTransformUserAgent == nil || *request.messageTransformUserAgent != "script-agent/1.0" || request.clientUserAgent != "test-client/1.0" {
		t.Fatal("override aliases caller memory or replaced original observation")
	}
	for _, value := range []string{"bad\r\nInjected: value", "bad\tvalue", "non-ascii-\u5ba2\u6237\u7aef", strings.Repeat("x", 513)} {
		options.MessageTransformUserAgent = &value
		if _, err := NewRequest(options); err == nil {
			t.Fatal("invalid transformed UA accepted")
		}
	}
	for _, name := range []string{"User-Agent", "user-agent", "USER-AGENT"} {
		value := "valid-agent"
		options.MessageTransformUserAgent = &value
		options.Headers = http.Header{name: {"competing-authority"}}
		if _, err := NewRequest(options); err == nil {
			t.Fatal("header bag and transform both owned UA")
		}
	}
}

func TestTransformedUserAgentOverridesWireDefaultsAndCannotBeChangedByAuthDriver(t *testing.T) {
	for _, profile := range []wireprofile.UpstreamWireProfileRef{
		wireprofile.FollowClientUpstreamWireProfileRef(), wireprofile.ClaudeCodeUpstreamWireProfileRef(),
	} {
		for _, test := range []struct {
			name, value, want            string
			accountDelete, maliciousAuth bool
		}{
			{name: "replacement", value: "script-agent/1.0", want: "script-agent/1.0"},
			{name: "empty omission"},
			{name: "account deletes", value: "script-agent/1.0", accountDelete: true},
			{name: "unattested mutation rejected", value: "script-agent/1.0", maliciousAuth: true},
		} {
			t.Run(profile.String()+"/"+test.name, func(t *testing.T) {
				gate := newStartedGate(t)
				var deleted []string
				if test.accountDelete {
					deleted = []string{"User-Agent"}
				}
				auth, err := NewStaticBearerAuthenticator(testSecretReaderWithPolicy(t, "fixture-secret", nil, deleted))
				if err != nil {
					t.Fatal(err)
				}
				transport := &roundTripperStub{response: &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}}
				options := ClientOptions{Coordinator: gate, Authenticator: auth, Transport: transport}
				if test.maliciousAuth {
					options.Authenticator = userAgentMutatingAuthenticator{}
				}
				client, err := NewClient(options)
				if err != nil {
					t.Fatal(err)
				}
				defer shutdownClient(t, client)
				request := newTestRequestWithPlan(t, gate, "transformed-agent", testTarget("provider.example", 443), nil, []byte("{}"),
					testRequestPlanWithWireProfile(t, "https://provider.example:443/v1", profile))
				request.messageTransformUserAgent = &test.value
				response, _, err := client.Do(context.Background(), request)
				if test.maliciousAuth {
					if err == nil || response != nil || transport.callCount() != 0 {
						t.Fatal("arbitrary AuthDriver overrode transformed UA")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				wireRequest := transport.lastRequest()
				if wireRequest.Header.Get("User-Agent") != test.want {
					t.Fatalf("wrong outgoing UA: %q", wireRequest.Header.Get("User-Agent"))
				}
				wireRequest.Body, wireRequest.ContentLength = http.NoBody, 0
				var serialized bytes.Buffer
				if err := wireRequest.Write(&serialized); err != nil {
					t.Fatal(err)
				}
				if test.want == "" && strings.Contains(strings.ToLower(serialized.String()), "user-agent:") {
					t.Fatal("net/http synthesized UA after explicit omission")
				}
			})
		}
	}
}
