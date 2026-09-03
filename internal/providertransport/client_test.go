package providertransport

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestClientWaitsForHoldLeaseBeforeSecretAndTransport(t *testing.T) {
	t.Parallel()

	gate := newStartedGate(t)
	if _, err := gate.Enter(context.Background(), gate.Snapshot().Revision); err != nil {
		t.Fatalf("Enter() error = %v", err)
	}
	secrets := testSecretReader(t, "provider-token")
	authenticator, err := NewStaticBearerAuthenticator(secrets)
	if err != nil {
		t.Fatal(err)
	}
	transport := &roundTripperStub{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("response")),
		},
	}
	client, err := NewClient(ClientOptions{
		Coordinator:   gate,
		Authenticator: authenticator,
		Transport:     transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)

	headers := http.Header{
		"Authorization":    []string{"Bearer client-token"},
		"Cookie":           []string{"session=client"},
		"Forwarded":        []string{"host=client.example"},
		"X-Forwarded-Host": []string{"client.example"},
		"Connection":       []string{"X-Remove"},
		"X-Remove":         []string{"remove-me"},
		"X-Keep":           []string{"keep-me"},
	}
	request := newTestRequest(
		t,
		gate,
		"held-request",
		testTarget("provider.example", 443),
		headers,
	)
	result := make(chan doResult, 1)
	go func() {
		response, evidence, doErr := client.Do(context.Background(), request)
		result <- doResult{response: response, evidence: evidence, err: doErr}
	}()
	waitForGateQueue(t, gate, 1)
	if secrets.readCount() != 0 || transport.callCount() != 0 {
		t.Fatalf(
			"held request touched secret=%d transport=%d",
			secrets.readCount(),
			transport.callCount(),
		)
	}

	if _, err := gate.Resume(
		context.Background(),
		gate.Snapshot().Revision,
		offlinehold.ResumeRequest{
			Targets: []offlinehold.ProbeTarget{request.ProbeTarget()},
		},
		proberSuccess{},
	); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	completed := waitDoResult(t, result)
	if completed.err != nil {
		t.Fatalf("Do() error = %v", completed.err)
	}
	if completed.evidence.Credential.DriverRef !=
		providerauth.StaticHeaderDriverRef().String() ||
		!completed.evidence.Credential.SecretRead {
		t.Fatalf("credential evidence = %+v", completed.evidence)
	}
	captured := transport.lastRequest()
	if captured.URL.String() != "https://provider.example/v1/chat/completions" ||
		captured.Host != "provider.example" {
		t.Fatalf("provider request identity URL=%s Host=%q", captured.URL, captured.Host)
	}
	if captured.Header.Get("Authorization") != "Bearer provider-token" ||
		captured.Header.Get("X-Keep") != "keep-me" {
		t.Fatalf("provider headers = %#v", captured.Header)
	}
	for _, removed := range []string{
		"Cookie",
		"Forwarded",
		"X-Forwarded-Host",
		"Connection",
		"X-Remove",
	} {
		if captured.Header.Get(removed) != "" {
			t.Fatalf("provider header %q leaked: %#v", removed, captured.Header)
		}
	}
	if snapshot := gate.Snapshot(); snapshot.ActiveEgress != 1 ||
		snapshot.State != offlinehold.StateReleasing {
		t.Fatalf("gate released lease before response terminal: %+v", snapshot)
	}
	body, err := io.ReadAll(completed.response.Body)
	if err != nil || string(body) != "response" {
		t.Fatalf("ReadAll() body=%q error=%v", body, err)
	}
	request.action.Release()
	waitForGateState(t, gate, offlinehold.StateOnline)
}

func TestClientPassthroughPreservesClientCredentialsOnlyForExactOrigin(
	t *testing.T,
) {
	t.Parallel()

	gate := newStartedGate(t)
	secrets := testSecretReader(t, "must-not-be-read")
	authenticator, err := NewStaticBearerAuthenticator(secrets)
	if err != nil {
		t.Fatal(err)
	}
	transport := &roundTripperStub{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("response")),
	}}
	client, err := NewClient(ClientOptions{
		Coordinator:   gate,
		Authenticator: authenticator,
		Transport:     transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)

	plan := testOriginalRequestPlan(t)
	target, err := NewTarget(plan.providerOrigin)
	if err != nil {
		t.Fatal(err)
	}
	action, err := gate.BeginAction(
		context.Background(),
		offlinehold.ActionRequest{ActionID: "original-client-auth"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(action.Release)
	frozen, err := NewRequest(RequestOptions{
		RequestID:       "original-client-auth",
		ExchangeID:      "exchange-original-client-auth",
		ParentAttemptID: "attempt-original-client-auth",
		EgressAttemptID: "egress-original-client-auth",
		TargetRef:       "target-original-client-auth",
		Target:          target,
		Provenance:      plan.provenance,
		Action:          action,
		Method:          http.MethodPost,
		RelativePath:    "v1/messages",
		RawQuery:        "beta=true",
		Headers: http.Header{
			"Authorization": []string{"Bearer client-oauth"},
			"Cookie":        []string{"session=client"},
			"X-Api-Key":     []string{"client-api-key"},
			"Forwarded":     []string{"host=client.example"},
			"Connection":    []string{"X-Remove"},
			"X-Remove":      []string{"remove-me"},
			"X-Keep":        []string{"keep-me"},
			"User-Agent":    []string{"raw-second-authority"},
		},
		Body:              []byte(`{"model":"client-model"}`),
		CredentialMode:    providerauth.CredentialClientPassthrough,
		PassthroughOrigin: plan.providerOrigin,
		WireProfile:       plan.wireProfile,
		ClientProtocol:    wireprofile.ApplicationProtocolHTTP1,
		ClientUserAgent:   "client-cli/1.0",
		EgressPolicy: egressnetwork.Policy{
			Proxy: egressnetwork.ProxyPolicy{
				Kind:     egressnetwork.ProxySOCKS5,
				Endpoint: "proxy.example:1080",
			},
			Resolver: egressnetwork.ResolverPolicy{
				Kind: egressnetwork.ResolverSystem,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, evidence, err := client.Do(context.Background(), frozen)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer response.Body.Close()
	request := transport.lastRequest()
	dispatch := transport.lastDispatch()
	if secrets.readCount() != 0 ||
		request.URL.RawQuery != "beta=true" ||
		request.Header.Get("Authorization") != "Bearer client-oauth" ||
		request.Header.Get("Cookie") != "session=client" ||
		request.Header.Get("X-Api-Key") != "client-api-key" ||
		request.Header.Get("X-Keep") != "keep-me" ||
		request.Header.Get("User-Agent") != "client-cli/1.0" ||
		request.Header.Get("Forwarded") != "" ||
		request.Header.Get("X-Remove") != "" ||
		evidence.Credential.DriverRef !=
			string(providerauth.CredentialClientPassthrough) ||
		evidence.Credential.SecretRead ||
		dispatch.egressPolicy != frozen.EgressPolicy() ||
		response.StatusCode != http.StatusOK {
		t.Fatalf(
			"passthrough request=%v evidence=%+v response=%d secretReads=%d",
			request.Header,
			evidence,
			response.StatusCode,
			secrets.readCount(),
		)
	}
}

func TestClientPassthroughRejectsRedirectWithoutReturningLocationOrResponse(
	t *testing.T,
) {
	t.Parallel()

	gate := newStartedGate(t)
	authenticator, err := NewStaticBearerAuthenticator(
		testSecretReader(t, "must-not-be-read"),
	)
	if err != nil {
		t.Fatal(err)
	}
	transport := &roundTripperStub{response: &http.Response{
		StatusCode: http.StatusTemporaryRedirect,
		Header: http.Header{
			"Location": []string{"https://other.example/path?secret=value"},
		},
		Body: io.NopCloser(strings.NewReader("redirect")),
	}}
	client, err := NewClient(ClientOptions{
		Coordinator:   gate,
		Authenticator: authenticator,
		Transport:     transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)

	plan := testOriginalRequestPlan(t)
	target, err := NewTarget(plan.providerOrigin)
	if err != nil {
		t.Fatal(err)
	}
	action, err := gate.BeginAction(
		context.Background(),
		offlinehold.ActionRequest{ActionID: "original-redirect"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(action.Release)
	frozen, err := NewRequest(RequestOptions{
		RequestID:       "original-redirect",
		ExchangeID:      "exchange-original-redirect",
		ParentAttemptID: "attempt-original-redirect",
		EgressAttemptID: "egress-original-redirect",
		TargetRef:       "target-original-redirect",
		Target:          target,
		Provenance:      plan.provenance,
		Action:          action,
		Method:          http.MethodPost,
		RelativePath:    "v1/messages",
		Headers: http.Header{
			"Authorization": []string{"Bearer client-oauth"},
		},
		Body:              []byte(`{"model":"client-model"}`),
		CredentialMode:    providerauth.CredentialClientPassthrough,
		PassthroughOrigin: plan.providerOrigin,
		WireProfile:       plan.wireProfile,
		ClientProtocol:    wireprofile.ApplicationProtocolHTTP1,
		ClientUserAgent:   "client-cli/1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, _, err := client.Do(context.Background(), frozen)
	if !errors.Is(err, ErrRedirectNotAllowed) || response != nil {
		t.Fatalf("Do() response=%v error=%v", response, err)
	}
	if strings.Contains(err.Error(), "other.example") ||
		strings.Contains(err.Error(), "secret=value") {
		t.Fatalf("redirect error exposed Location: %v", err)
	}
	if gate.Snapshot().ActiveEgress != 0 {
		t.Fatalf("redirect leaked an egress lease: %+v", gate.Snapshot())
	}
}

func TestClientStrictTLSUsesFrozenSNIAndAuthority(t *testing.T) {
	t.Parallel()

	var handlerCalls atomic.Int32
	observed := make(chan struct {
		host          string
		serverName    string
		authorization string
		userAgent     string
		path          string
	}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		handlerCalls.Add(1)
		observed <- struct {
			host          string
			serverName    string
			authorization string
			userAgent     string
			path          string
		}{
			host:          request.Host,
			serverName:    request.TLS.ServerName,
			authorization: request.Header.Get("Authorization"),
			userAgent:     request.Header.Get("User-Agent"),
			path:          request.URL.Path,
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
	}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	dialer := &mappingDialer{address: server.Listener.Addr().String()}
	transport, err := newStrictTransport(
		roots,
		dialer,
		DefaultTransportTimeouts(),
	)
	if err != nil {
		t.Fatal(err)
	}
	gate := newStartedGate(t)
	secrets := testSecretReader(t, "tls-token")
	authenticator, err := NewStaticBearerAuthenticator(secrets)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{
		Coordinator:   gate,
		Authenticator: authenticator,
		Transport:     transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)

	port := listenerPort(t, server.Listener.Addr())
	target := testTarget("example.com", port)
	wirePlan := testRequestPlanWithWireProfile(
		t,
		"https://provider.example:443/v1",
		wireprofile.ClaudeCodeUpstreamWireProfileRef(),
	)
	response, _, err := client.Do(
		context.Background(),
		newTestRequestWithPlan(
			t,
			gate,
			"tls-success",
			target,
			nil,
			[]byte(`{"input":"hello"}`),
			wirePlan,
		),
	)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	got := <-observed
	wantAuthority := net.JoinHostPort("example.com", strconv.Itoa(port))
	if got.host != wantAuthority ||
		got.serverName != "example.com" ||
		got.authorization != "Bearer tls-token" ||
		got.userAgent != "claude-cli/2.1.220 (external, sdk-cli)" ||
		got.path != "/v1/chat/completions" {
		t.Fatalf("observed TLS request = %+v, authority=%q", got, wantAuthority)
	}
	if handlerCalls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", handlerCalls.Load())
	}
}

func TestClientStrictTLSAppliesCapturedUserAgentProfile(t *testing.T) {
	t.Parallel()

	observedUserAgent := make(chan string, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		observedUserAgent <- request.Header.Get("User-Agent")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport, err := newStrictTransport(
		roots,
		&mappingDialer{address: server.Listener.Addr().String()},
		DefaultTransportTimeouts(),
	)
	if err != nil {
		t.Fatal(err)
	}
	gate := newStartedGate(t)
	authenticator, err := NewStaticBearerAuthenticator(
		testSecretReader(t, "tls-token"),
	)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{
		Coordinator:   gate,
		Authenticator: authenticator,
		Transport:     transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)

	port := listenerPort(t, server.Listener.Addr())
	target := testTarget("example.com", port)
	plan := testRequestPlanWithWireProfile(
		t,
		"https://"+net.JoinHostPort("example.com", strconv.Itoa(port))+"/v1",
		wireprofile.ClaudeCodeUpstreamWireProfileRef(),
	)
	request := newTestRequestWithPlan(
		t,
		gate,
		"captured-user-agent",
		target,
		nil,
		[]byte(`{"model":"provider-model"}`),
		plan,
	)
	response, evidence, err := client.Do(context.Background(), request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if got := <-observedUserAgent; got != "claude-cli/2.1.220 (external, sdk-cli)" {
		t.Fatalf("upstream User-Agent = %q", got)
	}
	presentation := evidence.Presentation
	if presentation.RequestedRef != wireprofile.ClaudeCodeUpstreamWireProfileRef().String() ||
		presentation.EffectiveRef != wireprofile.ClaudeCodeUpstreamWireProfileRef().String() ||
		presentation.Revision != 1 ||
		presentation.Mode != wireprofile.UpstreamWireModeEmulateProduct ||
		presentation.Product != wireprofile.UpstreamWireProductClaudeCode ||
		presentation.ClientProtocol != wireprofile.ApplicationProtocolHTTP1 ||
		presentation.UpstreamProtocol != wireprofile.ApplicationProtocolHTTP1 ||
		presentation.EvidenceDigest == "" {
		t.Fatalf("wire presentation evidence = %+v", presentation)
	}
}

func TestFollowClientUserAgentPolicyPreservesTheObservedValue(t *testing.T) {
	t.Parallel()

	plan := testRequestPlan(t)
	variant, available := plan.wireProfile.Variant(
		wireprofile.ApplicationProtocolHTTP1,
	)
	if !available {
		t.Fatal("follow-client profile has no HTTP/1.1 variant")
	}
	headers := make(http.Header)
	const clientUserAgent = "agent-client/1.0"
	if err := applyUpstreamWireHeaders(
		headers,
		variant,
		clientUserAgent,
	); err != nil {
		t.Fatal(err)
	}
	if got := headers.Get("User-Agent"); got != clientUserAgent {
		t.Fatalf("follow-client User-Agent = %q", got)
	}
	if err := validateUpstreamWireHeaders(
		headers,
		variant,
		clientUserAgent,
	); err != nil {
		t.Fatal(err)
	}
}

func TestClientUsesExplicitLoopbackCleartextTransport(t *testing.T) {
	t.Parallel()

	observed := make(chan struct {
		host          string
		authorization string
		path          string
		protocol      string
	}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		observed <- struct {
			host          string
			authorization string
			path          string
			protocol      string
		}{
			host:          request.Host,
			authorization: request.Header.Get("Authorization"),
			path:          request.URL.Path,
			protocol:      request.Proto,
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"ok":true}`)
	}))
	defer server.Close()

	origin, err := originidentity.ParseProviderOrigin(server.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	target := targetFromProviderOrigin(origin)
	gate := newStartedGate(t)
	secrets := testSecretReader(t, "loopback-token")
	authenticator, err := NewStaticBearerAuthenticator(secrets)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewProductionClient(
		gate,
		authenticator,
		DefaultTransportTimeouts(),
		&sequentialInstanceIDs{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)

	request := newTestRequestWithClientProtocol(
		t,
		gate,
		"loopback-cleartext-request",
		target,
		nil,
		wireprofile.ApplicationProtocolHTTP2,
	)
	if request.ProbeTarget().Transport !=
		offlinehold.ProbeTransportLoopbackCleartext ||
		request.ProbeTarget().TLSServerName != "" {
		t.Fatalf("loopback probe target = %+v", request.ProbeTarget())
	}
	response, evidence, err := client.Do(context.Background(), request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	request.action.Release()
	if string(body) != `{"ok":true}` {
		t.Fatalf("response body = %q", body)
	}
	if evidence.Presentation.ClientProtocol != wireprofile.ApplicationProtocolHTTP2 ||
		evidence.Presentation.UpstreamProtocol != wireprofile.ApplicationProtocolHTTP1 {
		t.Fatalf("loopback presentation = %+v", evidence.Presentation)
	}
	if evidence.Transport.Requested().Ref != "" ||
		evidence.Transport.Effective().Ref != "" {
		t.Fatalf(
			"cleartext request claimed TLS fingerprint evidence: %+v",
			evidence.Transport,
		)
	}
	select {
	case capture := <-observed:
		if capture.host != origin.HTTPAuthority() ||
			capture.authorization != "Bearer loopback-token" ||
			capture.path != "/v1/chat/completions" ||
			capture.protocol != "HTTP/1.1" {
			t.Fatalf("loopback request = %+v", capture)
		}
	case <-time.After(time.Second):
		t.Fatal("loopback provider did not receive the request")
	}
}

func TestNewRequestUsesHTTP1ForHTTP2ClientOnLoopback(t *testing.T) {
	t.Parallel()

	options := validRequestOptions(t)
	origin, err := originidentity.ParseProviderOrigin("http://127.0.0.1:23333/v1")
	if err != nil {
		t.Fatal(err)
	}
	options.Target = targetFromProviderOrigin(origin)
	options.ClientProtocol = wireprofile.ApplicationProtocolHTTP2

	request, err := NewRequest(options)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	evidence := request.WirePresentationEvidence()
	if evidence.ClientProtocol != wireprofile.ApplicationProtocolHTTP2 ||
		evidence.UpstreamProtocol != wireprofile.ApplicationProtocolHTTP1 {
		t.Fatalf("loopback presentation = %+v", evidence)
	}
}

func TestNewTargetPreservesCompiledLoopbackIdentity(t *testing.T) {
	t.Parallel()

	origin, err := originidentity.ParseProviderOrigin("http://127.0.0.1:23333/v1")
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewTarget(origin)
	if err != nil {
		t.Fatalf("NewTarget() error = %v", err)
	}
	if target.Origin().String() != "http://127.0.0.1:23333/v1" ||
		target.TransportKind() !=
			originidentity.ProviderTransportLoopbackCleartext ||
		target.NetworkHost() != "127.0.0.1" ||
		target.HTTPAuthority() != "127.0.0.1:23333" ||
		target.TLSServerName() != "" {
		t.Fatalf("loopback target = %+v", target)
	}
}

func TestTLSHostnameFailureSendsNoHTTPAuthorization(t *testing.T) {
	t.Parallel()

	var handlerCalls atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
		handlerCalls.Add(1)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport, err := newStrictTransport(
		roots,
		&mappingDialer{address: server.Listener.Addr().String()},
		DefaultTransportTimeouts(),
	)
	if err != nil {
		t.Fatal(err)
	}
	gate := newStartedGate(t)
	authenticator, err := NewStaticBearerAuthenticator(
		testSecretReader(t, "must-not-reach-server"),
	)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{
		Coordinator:   gate,
		Authenticator: authenticator,
		Transport:     transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)

	port := listenerPort(t, server.Listener.Addr())
	_, _, err = client.Do(
		context.Background(),
		newTestRequest(
			t,
			gate,
			"tls-failure",
			testTarget("untrusted.invalid", port),
			nil,
		),
	)
	if err == nil {
		t.Fatal("Do() succeeded with a mismatched TLS hostname")
	}
	if handlerCalls.Load() != 0 {
		t.Fatalf("HTTP handler observed %d requests after TLS rejection", handlerCalls.Load())
	}
	if gate.Snapshot().ActiveEgress != 0 {
		t.Fatalf("failed TLS operation leaked an egress lease: %+v", gate.Snapshot())
	}
}

func TestClientRejectsRedirectWithoutReturningLocationOrResponse(t *testing.T) {
	t.Parallel()

	gate := newStartedGate(t)
	authenticator, err := NewStaticBearerAuthenticator(
		testSecretReader(t, "provider-token"),
	)
	if err != nil {
		t.Fatal(err)
	}
	transport := &roundTripperStub{response: &http.Response{
		StatusCode: http.StatusTemporaryRedirect,
		Header: http.Header{
			"Location": []string{"https://other.example/path?secret=value"},
		},
		Body: io.NopCloser(strings.NewReader("redirect")),
	}}
	client, err := NewClient(ClientOptions{
		Coordinator:   gate,
		Authenticator: authenticator,
		Transport:     transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)

	response, _, err := client.Do(
		context.Background(),
		newTestRequest(
			t,
			gate,
			"redirect",
			testTarget("provider.example", 443),
			nil,
		),
	)
	if !errors.Is(err, ErrRedirectNotAllowed) || response != nil {
		t.Fatalf("Do() response=%v error=%v", response, err)
	}
	if strings.Contains(err.Error(), "other.example") ||
		strings.Contains(err.Error(), "secret=value") {
		t.Fatalf("redirect error exposed Location: %v", err)
	}
	if gate.Snapshot().ActiveEgress != 0 {
		t.Fatalf("redirect leaked an egress lease: %+v", gate.Snapshot())
	}
}

func TestClientShutdownCancelsHeldRequestBeforeSecretRead(t *testing.T) {
	t.Parallel()

	gate := newStartedGate(t)
	if _, err := gate.Enter(context.Background(), gate.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	secrets := testSecretReader(t, "provider-token")
	authenticator, err := NewStaticBearerAuthenticator(secrets)
	if err != nil {
		t.Fatal(err)
	}
	transport := &roundTripperStub{}
	client, err := NewClient(ClientOptions{
		Coordinator:   gate,
		Authenticator: authenticator,
		Transport:     transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan doResult, 1)
	go func() {
		response, evidence, doErr := client.Do(
			context.Background(),
			newTestRequest(
				t,
				gate,
				"shutdown-held",
				testTarget("provider.example", 443),
				nil,
			),
		)
		result <- doResult{response: response, evidence: evidence, err: doErr}
	}()
	waitForGateQueue(t, gate, 1)
	shutdownClient(t, client)
	completed := waitDoResult(t, result)
	if completed.err == nil || completed.response != nil {
		t.Fatalf("held Do() after shutdown = %+v", completed)
	}
	if secrets.readCount() != 0 || transport.callCount() != 0 {
		t.Fatalf(
			"shutdown-held request touched secret=%d transport=%d",
			secrets.readCount(),
			transport.callCount(),
		)
	}
	if gate.Snapshot().QueuedRequests != 0 {
		t.Fatalf("shutdown-held request remained queued: %+v", gate.Snapshot())
	}
}

func TestNewRequestOwnsBodyAndHeaders(t *testing.T) {
	t.Parallel()

	body := []byte(`{"value":1}`)
	headers := http.Header{"X-Test": []string{"original"}}
	gate := newStartedGate(t)
	request := newTestRequestWithBody(
		t,
		gate,
		"alias",
		testTarget("provider.example", 443),
		headers,
		body,
	)
	body[2] = 'X'
	headers.Set("X-Test", "mutated")
	gotBody := request.Body()
	gotHeaders := request.Headers()
	gotBody[2] = 'Y'
	gotHeaders.Set("X-Test", "changed")
	if string(request.Body()) != `{"value":1}` ||
		request.Headers().Get("X-Test") != "original" {
		t.Fatalf("frozen request was mutated through an alias")
	}
}

func TestClientRejectsCodecUserAgentBeforeSecretOrTransport(t *testing.T) {
	t.Parallel()

	gate := newStartedGate(t)
	secrets := testSecretReader(t, "provider-token")
	authenticator, err := NewStaticBearerAuthenticator(secrets)
	if err != nil {
		t.Fatal(err)
	}
	transport := &roundTripperStub{}
	client, err := NewClient(ClientOptions{
		Coordinator:   gate,
		Authenticator: authenticator,
		Transport:     transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)

	request := newTestRequest(
		t,
		gate,
		"codec-user-agent",
		testTarget("provider.example", 443),
		http.Header{"User-Agent": []string{"codec-owned"}},
	)
	response, _, err := client.Do(context.Background(), request)
	if err == nil || response != nil {
		t.Fatalf("codec User-Agent request response=%v error=%v", response, err)
	}
	if secrets.readCount() != 0 || transport.callCount() != 0 {
		t.Fatalf(
			"conflicting User-Agent touched secret=%d transport=%d",
			secrets.readCount(),
			transport.callCount(),
		)
	}
}

func TestClientRejectsAuthDriverUserAgentMutation(t *testing.T) {
	t.Parallel()

	gate := newStartedGate(t)
	transport := &roundTripperStub{}
	client, err := NewClient(ClientOptions{
		Coordinator:   gate,
		Authenticator: userAgentMutatingAuthenticator{},
		Transport:     transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)

	response, _, err := client.Do(
		context.Background(),
		newTestRequest(
			t,
			gate,
			"auth-user-agent",
			testTarget("provider.example", 443),
			nil,
		),
	)
	if err == nil || response != nil {
		t.Fatalf("mutating AuthDriver response=%v error=%v", response, err)
	}
	if transport.callCount() != 0 {
		t.Fatal("mutating AuthDriver reached the provider transport")
	}
}

func TestStrictTransportDisablesAmbientProxyAndTLSBypass(t *testing.T) {
	t.Parallel()

	transport, err := newProductionStrictTransport(DefaultTransportTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	if transport.dialers == nil {
		t.Fatal("strict transport has no typed traffic egress dialer builder")
	}
	transport.CloseIdleConnections()
}

type doResult struct {
	response *http.Response
	evidence Evidence
	err      error
}

type secretReaderStub struct {
	mu               sync.Mutex
	value            []byte
	err              error
	revision         secretstore.Revision
	expectedRevision secretstore.Revision
	reads            int
}

func testSecretReader(t *testing.T, credential string) *secretReaderStub {
	t.Helper()
	return testSecretReaderWithPolicy(t, credential, nil, nil)
}

func testSecretReaderWithPolicy(
	t *testing.T,
	credential string,
	setHeaders map[string]string,
	deleteHeaders []string,
) *secretReaderStub {
	t.Helper()
	material, err := providerauth.NewMaterial(credential, setHeaders, deleteHeaders)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := material.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(encoded) })
	return &secretReaderStub{value: encoded}
}

type userAgentMutatingAuthenticator struct{}

func (userAgentMutatingAuthenticator) Ref() providerauth.DriverRef {
	return providerauth.StaticHeaderDriverRef()
}

func (userAgentMutatingAuthenticator) Apply(
	_ context.Context,
	request *http.Request,
	_ secretstore.Reference,
	_ secretstore.Revision,
	_ Target,
) (CredentialEvidence, error) {
	request.Header.Set("User-Agent", "auth-owned")
	return CredentialEvidence{
		DriverRef: providerauth.StaticHeaderDriverRef().String(),
	}, nil
}

func (reader *secretReaderStub) Read(
	_ context.Context,
	_ secretstore.Reference,
) (*secretstore.Value, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.reads++
	if reader.err != nil {
		return nil, reader.err
	}
	return secretstore.NewValue(reader.value)
}

func (reader *secretReaderStub) ReadAtRevision(
	_ context.Context,
	_ secretstore.Reference,
	expected secretstore.Revision,
) (*secretstore.Value, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.reads++
	reader.expectedRevision = expected
	if reader.err != nil {
		return nil, reader.err
	}
	if reader.revision != 0 && reader.revision != expected {
		return nil, secretstore.ErrRevisionConflict
	}
	return secretstore.NewValue(reader.value)
}

func (reader *secretReaderStub) lastExpectedRevision() secretstore.Revision {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.expectedRevision
}

func (reader *secretReaderStub) readCount() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.reads
}

type roundTripperStub struct {
	mu       sync.Mutex
	response *http.Response
	err      error
	request  *http.Request
	dispatch TransportDispatch
	calls    int
}

func (transport *roundTripperStub) RoundTrip(
	request *http.Request,
	dispatch TransportDispatch,
) (*http.Response, transportprofile.Evidence, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.calls++
	cloned := request.Clone(context.Background())
	cloned.Header = request.Header.Clone()
	cloned.Host = request.Host
	transport.request = cloned
	transport.dispatch = dispatch
	return transport.response, transportprofile.Evidence{}, transport.err
}

func (transport *roundTripperStub) callCount() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.calls
}

func (transport *roundTripperStub) lastRequest() *http.Request {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	cloned := transport.request.Clone(context.Background())
	cloned.Header = transport.request.Header.Clone()
	cloned.Host = transport.request.Host
	return cloned
}

func (transport *roundTripperStub) lastDispatch() TransportDispatch {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.dispatch
}

type mappingDialer struct {
	address string
}

func (dialer *mappingDialer) DialContext(
	ctx context.Context,
	network string,
	_ string,
) (net.Conn, error) {
	var system net.Dialer
	return system.DialContext(ctx, network, dialer.address)
}

type proberSuccess struct{}

func (proberSuccess) Probe(context.Context, offlinehold.ProbeRequest) error {
	return nil
}

func newStartedGate(t *testing.T) *offlinehold.Gate {
	t.Helper()
	gate, err := offlinehold.New(offlinehold.Config{
		MaxHeldRequests:    8,
		MaxHeldBytes:       1 << 20,
		MaxHoldDuration:    time.Second,
		ReleaseConcurrency: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Start(
		context.Background(),
		offlinehold.RuntimeBinding{InstanceID: "provider-transport-test"},
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		gate.BeginShutdown()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := gate.Drain(ctx); err != nil {
			t.Errorf("gate Drain() error = %v", err)
		}
	})
	return gate
}

func newTestRequest(
	t *testing.T,
	gate *offlinehold.Gate,
	id string,
	target Target,
	headers http.Header,
) Request {
	t.Helper()
	return newTestRequestWithBody(
		t,
		gate,
		id,
		target,
		headers,
		[]byte(`{"model":"gpt-provider-model"}`),
	)
}

func newTestRequestWithBody(
	t *testing.T,
	gate *offlinehold.Gate,
	id string,
	target Target,
	headers http.Header,
	body []byte,
) Request {
	return newTestRequestWithPlan(
		t,
		gate,
		id,
		target,
		headers,
		body,
		testRequestPlan(t),
	)
}

func newTestRequestWithClientProtocol(
	t *testing.T,
	gate *offlinehold.Gate,
	id string,
	target Target,
	headers http.Header,
	protocol wireprofile.ApplicationProtocol,
) Request {
	return newTestRequestWithPlanAndClientProtocol(
		t,
		gate,
		id,
		target,
		headers,
		[]byte(`{"model":"gpt-provider-model"}`),
		testRequestPlan(t),
		protocol,
	)
}

func newTestRequestWithPlan(
	t *testing.T,
	gate *offlinehold.Gate,
	id string,
	target Target,
	headers http.Header,
	body []byte,
	plan testProviderPlan,
) Request {
	return newTestRequestWithPlanAndClientProtocol(
		t,
		gate,
		id,
		target,
		headers,
		body,
		plan,
		wireprofile.ApplicationProtocolHTTP1,
	)
}

func newTestRequestWithPlanAndClientProtocol(
	t *testing.T,
	gate *offlinehold.Gate,
	id string,
	target Target,
	headers http.Header,
	body []byte,
	plan testProviderPlan,
	protocol wireprofile.ApplicationProtocol,
) Request {
	t.Helper()
	secretRef, err := secretstore.ParseReference("secret://provider/account")
	if err != nil {
		t.Fatal(err)
	}
	action, err := gate.BeginAction(
		context.Background(),
		offlinehold.ActionRequest{ActionID: id},
	)
	if err != nil {
		t.Fatalf("BeginAction() error = %v", err)
	}
	t.Cleanup(action.Release)
	request, err := NewRequest(RequestOptions{
		RequestID:       id,
		ExchangeID:      "exchange-test",
		ParentAttemptID: "attempt-test",
		EgressAttemptID: "egress-" + id,
		TargetRef:       "target-test",
		Target:          target,
		Provenance:      plan.provenance,
		Action:          action,
		Method:          http.MethodPost,
		RelativePath:    "chat/completions",
		Headers:         headers,
		Body:            body,
		CredentialMode:  providerauth.CredentialManaged,
		AccountRef:      testAccountRef(),
		SecretRef:       secretRef,
		AuthDriverRef:   providerauth.StaticHeaderDriverRef(),
		WireProfile:     plan.wireProfile,
		ClientProtocol:  protocol,
		ClientUserAgent: "test-client/1.0",
	})
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	return request
}

type testProviderPlan struct {
	provenance     RequestProvenance
	providerOrigin originidentity.ProviderOrigin
	wireProfile    wireprofile.CompiledUpstreamWireProfile
}

func testRequestPlan(t *testing.T) testProviderPlan {
	t.Helper()
	return testRequestPlanWithOrigin(
		t,
		"https://provider.example:443/v1",
	)
}

func testRequestPlanWithOrigin(
	t *testing.T,
	rawProviderOrigin string,
) testProviderPlan {
	return testRequestPlanWithWireProfile(
		t,
		rawProviderOrigin,
		wireprofile.FollowClientUpstreamWireProfileRef(),
	)
}

func testRequestPlanWithWireProfile(
	t *testing.T,
	rawProviderOrigin string,
	wireProfileRef wireprofile.UpstreamWireProfileRef,
) testProviderPlan {
	t.Helper()
	providerOrigin, err := originidentity.ParseProviderOrigin(rawProviderOrigin)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := catalog.Resolve(wireProfileRef)
	if err != nil {
		t.Fatal(err)
	}
	return testProviderPlan{
		provenance: testRequestProvenance(t), providerOrigin: providerOrigin,
		wireProfile: profile,
	}
}

func testOriginalRequestPlan(t *testing.T) testProviderPlan {
	t.Helper()
	plan := testRequestPlanWithWireProfile(
		t,
		"https://agent.example:443",
		wireprofile.FollowClientUpstreamWireProfileRef(),
	)
	environmentID, err := environment.NewEnvironmentID("provider-transport-test")
	if err != nil {
		t.Fatal(err)
	}
	digest := environment.CandidateDigest(sha256.Sum256([]byte("provider-transport-environment")))
	plan.provenance, err = NewOriginalRequestProvenance(environmentID, 3, digest)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func testRequestProvenance(t *testing.T) RequestProvenance {
	t.Helper()
	environmentID, err := environment.NewEnvironmentID("provider-transport-test")
	if err != nil {
		t.Fatal(err)
	}
	routeID, err := environment.NewUpstreamRouteID("provider-route")
	if err != nil {
		t.Fatal(err)
	}
	digest := environment.CandidateDigest(sha256.Sum256([]byte("provider-transport-environment")))
	provenance, err := NewUpstreamRequestProvenance(environmentID, 3, digest, routeID, 7)
	if err != nil {
		t.Fatal(err)
	}
	return provenance
}

func testAccountRef() providerauth.AccountRef {
	return providerauth.AccountRef{
		ID: "provider-account", Revision: 2, CredentialEpoch: 5, RealmID: "provider-realm",
	}
}

func testTarget(host string, port int) Target {
	authority := host
	if port != 443 {
		authority = net.JoinHostPort(host, strconv.Itoa(port))
	}
	origin, err := originidentity.ParseProviderOrigin("https://" + authority + "/v1")
	if err != nil {
		panic(err)
	}
	return Target{origin: origin}
}

func targetFromProviderOrigin(origin originidentity.ProviderOrigin) Target {
	target, err := NewTarget(origin)
	if err != nil {
		panic(err)
	}
	return target
}

func listenerPort(t *testing.T, address net.Addr) int {
	t.Helper()
	_, portString, err := net.SplitHostPort(address.String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func waitForGateQueue(t *testing.T, gate *offlinehold.Gate, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if gate.Snapshot().QueuedRequests == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("gate queue = %d, want %d", gate.Snapshot().QueuedRequests, count)
}

func waitForGateState(
	t *testing.T,
	gate *offlinehold.Gate,
	state offlinehold.State,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if gate.Snapshot().State == state {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("gate state = %q, want %q", gate.Snapshot().State, state)
}

func waitDoResult(t *testing.T, result <-chan doResult) doResult {
	t.Helper()
	select {
	case completed := <-result:
		return completed
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for provider request")
		return doResult{}
	}
}

func shutdownClient(t *testing.T, client *Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}
