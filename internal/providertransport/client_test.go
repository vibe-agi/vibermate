package providertransport

import (
	"context"
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

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
)

func TestClientWaitsForHoldLeaseBeforeSecretAndTransport(t *testing.T) {
	t.Parallel()

	gate := newStartedGate(t)
	if _, err := gate.Enter(context.Background(), gate.Snapshot().Revision); err != nil {
		t.Fatalf("Enter() error = %v", err)
	}
	secrets := &secretReaderStub{value: []byte("provider-token")}
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
		access.StaticHeaderAuthDriverRef().String() ||
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

func TestClientStrictTLSUsesFrozenSNIAndAuthority(t *testing.T) {
	t.Parallel()

	var handlerCalls atomic.Int32
	observed := make(chan struct {
		host          string
		serverName    string
		authorization string
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
			path          string
		}{
			host:          request.Host,
			serverName:    request.TLS.ServerName,
			authorization: request.Header.Get("Authorization"),
			path:          request.URL.Path,
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	server.EnableHTTP2 = true
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
	secrets := &secretReaderStub{value: []byte("tls-token")}
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
	response, _, err := client.Do(
		context.Background(),
		newTestRequest(t, gate, "tls-success", target, nil),
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
		got.path != "/v1/chat/completions" {
		t.Fatalf("observed TLS request = %+v, authority=%q", got, wantAuthority)
	}
	if handlerCalls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", handlerCalls.Load())
	}
}

func TestClientUsesExplicitLoopbackCleartextTransport(t *testing.T) {
	t.Parallel()

	observed := make(chan struct {
		host          string
		authorization string
		path          string
	}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		observed <- struct {
			host          string
			authorization string
			path          string
		}{
			host:          request.Host,
			authorization: request.Header.Get("Authorization"),
			path:          request.URL.Path,
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"ok":true}`)
	}))
	defer server.Close()

	origin, err := access.NewProviderOrigin(server.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	target := targetFromProviderOrigin(origin)
	gate := newStartedGate(t)
	secrets := &secretReaderStub{value: []byte("loopback-token")}
	authenticator, err := NewStaticBearerAuthenticator(secrets)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewProductionClient(
		gate,
		authenticator,
		DefaultTransportTimeouts(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)

	request := newTestRequest(
		t,
		gate,
		"loopback-cleartext-request",
		target,
		nil,
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
			capture.path != "/v1/chat/completions" {
			t.Fatalf("loopback request = %+v", capture)
		}
	case <-time.After(time.Second):
		t.Fatal("loopback provider did not receive the request")
	}
}

func TestNewTargetPreservesCompiledLoopbackIdentity(t *testing.T) {
	t.Parallel()

	plan := testRequestAccessPlanWithOrigin(
		t,
		"http://127.0.0.1:23333/v1",
	)
	targets := plan.ProviderTargets()
	if len(targets) != 1 {
		t.Fatalf("compiled targets = %d", len(targets))
	}
	target, err := NewTarget(targets[0])
	if err != nil {
		t.Fatalf("NewTarget() error = %v", err)
	}
	if target.Origin() != "http://127.0.0.1:23333/v1" ||
		target.TransportKind() !=
			access.ProviderTransportLoopbackCleartext ||
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
		&secretReaderStub{value: []byte("must-not-reach-server")},
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
		&secretReaderStub{value: []byte("provider-token")},
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
	secrets := &secretReaderStub{value: []byte("provider-token")}
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

func TestStrictTransportDisablesAmbientProxyAndTLSBypass(t *testing.T) {
	t.Parallel()

	transport, err := newProductionStrictTransport(DefaultTransportTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	if transport.connector == nil {
		t.Fatal("strict transport has no typed TLS profile connector")
	}
	transport.CloseIdleConnections()
}

type doResult struct {
	response *http.Response
	evidence Evidence
	err      error
}

type secretReaderStub struct {
	mu    sync.Mutex
	value []byte
	err   error
	reads int
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
	calls    int
}

func (transport *roundTripperStub) RoundTrip(
	request *http.Request,
	_ TransportDispatch,
) (*http.Response, transportprofile.Evidence, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.calls++
	cloned := request.Clone(context.Background())
	cloned.Header = request.Header.Clone()
	cloned.Host = request.Host
	transport.request = cloned
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
	t.Helper()
	secretRef, err := access.NewSecretRef("secret://provider/account")
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
	plan := testRequestAccessPlan(t)
	request, err := NewRequest(RequestOptions{
		RequestID:      id,
		TargetRef:      "target-test",
		Target:         target,
		AccessRevision: plan.Revision(),
		PlanHash:       plan.PlanHash(),
		Action:         action,
		Method:         http.MethodPost,
		RelativePath:   "chat/completions",
		Headers:        headers,
		Body:           body,
		SecretRef:      secretRef,
		AuthDriverRef:  access.StaticHeaderAuthDriverRef(),
		TransportPlan:  plan.TransportFingerprintPlan(),
	})
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	return request
}

func testRequestAccessPlan(
	t *testing.T,
) access.AccessPlanSnapshot {
	t.Helper()
	return testRequestAccessPlanWithOrigin(
		t,
		"https://provider.example:443/v1",
	)
}

func testRequestAccessPlanWithOrigin(
	t *testing.T,
	rawProviderOrigin string,
) access.AccessPlanSnapshot {
	t.Helper()
	accessID, err := access.NewAccessID("provider-transport-test")
	if err != nil {
		t.Fatal(err)
	}
	endpointID, err := access.NewAgentEndpointID("agent")
	if err != nil {
		t.Fatal(err)
	}
	profileID, err := access.NewEndpointProfileID("profile")
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := access.NewProviderTargetID("target")
	if err != nil {
		t.Fatal(err)
	}
	accountID, err := access.NewAccountBindingID("account")
	if err != nil {
		t.Fatal(err)
	}
	routeID, err := access.NewRouteSetID("route")
	if err != nil {
		t.Fatal(err)
	}
	egressID, err := access.NewEgressPolicyID("egress")
	if err != nil {
		t.Fatal(err)
	}
	clientOrigin, err := access.NewClientOrigin("https://agent.example:443")
	if err != nil {
		t.Fatal(err)
	}
	providerOrigin, err := access.NewProviderOrigin(rawProviderOrigin)
	if err != nil {
		t.Fatal(err)
	}
	model, err := access.NewModelName("provider-model")
	if err != nil {
		t.Fatal(err)
	}
	secretRef, err := access.NewSecretRef("secret://provider/account")
	if err != nil {
		t.Fatal(err)
	}
	codecID, err := access.NewCodecPairID(
		"anthropic-messages-to-openai-chat",
	)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := operationcatalog.M0()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := access.NewCatalog(access.CatalogOptions{
		Capabilities: access.PlanCapabilities{
			MaxEndpointProfiles: 1,
			MaxAccountBindings:  1,
			MaxRouteSets:        1,
		},
		ClientOperations: operations.Definitions(),
		CodecPairs: []access.CodecPairDefinition{{
			ID:              codecID,
			Revision:        1,
			ClientDialect:   access.DialectAnthropicMessages,
			ProviderDialect: access.DialectOpenAIChat,
			ClientOperationIDs: operations.SemanticOperationIDs(
				access.DialectAnthropicMessages,
			),
			RequiredCapabilities: []access.ProviderCapability{
				access.ProviderCapabilityMessages,
				access.ProviderCapabilityStreaming,
				access.ProviderCapabilityToolCalls,
			},
		}},
		AuthDrivers: []access.AuthDriverDefinition{{
			Ref:      access.StaticHeaderAuthDriverRef(),
			Revision: 1,
		}},
		EgressModes: []access.EgressModeDefinition{{
			Mode:     access.EgressModeDirect,
			Revision: 1,
		}},
		PluginPlanModes: []access.PluginPlanModeDefinition{{
			Mode:     access.PluginPlanModePassThrough,
			Revision: 1,
		}},
		ModelPolicyModes: []access.ModelPolicyModeDefinition{{
			Mode:     access.ModelPolicyModeFixed,
			Revision: 1,
		}},
		TransportProfiles: []access.TransportFingerprintDefinition{
			access.ObservedClientH1TransportFingerprintDefinition(),
			access.StandardH1TransportFingerprintDefinition(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := access.NewCompiler(catalog)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := compiler.Compile(access.Aggregate{
		Binding: access.AccessBinding{
			ID:                accessID,
			Revision:          1,
			Name:              "Provider transport test",
			Status:            access.AccessStatusEnabled,
			AgentEndpointID:   endpointID,
			DefaultRouteSetID: routeID,
			ProfileIDs:        []access.EndpointProfileID{profileID},
			EgressPolicyID:    egressID,
		},
		AgentEndpoint: access.AgentEndpoint{
			ID:            endpointID,
			Revision:      1,
			AccessID:      accessID,
			ClientOrigin:  clientOrigin,
			ClientDialect: access.DialectAnthropicMessages,
		},
		Profiles: []access.EndpointProfile{{
			ID:                  profileID,
			Revision:            1,
			AccessID:            accessID,
			Name:                "Provider profile",
			BackendDialect:      access.DialectOpenAIChat,
			TargetID:            targetID,
			TransportProfileRef: access.ObservedClientH1TransportProfileRef(),
			DefaultModelPolicy: access.ModelPolicy{
				Revision:   1,
				Mode:       access.ModelPolicyModeFixed,
				FixedModel: model,
			},
			AccountBindingIDs:       []access.AccountBindingID{accountID},
			DefaultAccountBindingID: accountID,
		}},
		ProviderTargets: []access.ProviderTarget{{
			ID:        targetID,
			Revision:  1,
			AccessID:  accessID,
			ProfileID: profileID,
			Origin:    providerOrigin,
			Protocol:  access.DialectOpenAIChat,
			Capabilities: []access.ProviderCapability{
				access.ProviderCapabilityMessages,
				access.ProviderCapabilityStreaming,
				access.ProviderCapabilityToolCalls,
			},
		}},
		AccountBindings: []access.ProviderAccountBinding{{
			ID:            accountID,
			Revision:      1,
			AccessID:      accessID,
			ProfileID:     profileID,
			Label:         "Provider",
			SecretRef:     secretRef,
			AuthDriverRef: access.StaticHeaderAuthDriverRef(),
			Enabled:       true,
		}},
		RouteSets: []access.RouteSet{{
			ID:                  routeID,
			Revision:            1,
			AccessID:            accessID,
			CandidateProfileIDs: []access.EndpointProfileID{profileID},
		}},
		EgressPolicy: access.AccessEgressPolicy{
			ID:       egressID,
			Revision: 1,
			AccessID: accessID,
			Mode:     access.EgressModeDirect,
		},
		PluginPlan: access.PluginPlan{
			Revision: 1,
			AccessID: accessID,
			Mode:     access.PluginPlanModePassThrough,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func testTarget(host string, port int) Target {
	authority := host
	if port != 443 {
		authority = net.JoinHostPort(host, strconv.Itoa(port))
	}
	return Target{
		origin:        "https://" + authority + "/v1",
		scheme:        "https",
		httpAuthority: authority,
		networkHost:   host,
		tlsServerName: host,
		basePath:      "/v1",
		port:          uint16(port),
		transportKind: access.ProviderTransportStrictTLS,
	}
}

func targetFromProviderOrigin(origin access.ProviderOrigin) Target {
	return Target{
		origin:        origin.String(),
		scheme:        origin.Scheme(),
		httpAuthority: origin.HTTPAuthority(),
		networkHost:   origin.NetworkHost(),
		tlsServerName: origin.TLSServerName(),
		basePath:      origin.BasePath(),
		port:          origin.Port(),
		transportKind: origin.TransportKind(),
	}
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
