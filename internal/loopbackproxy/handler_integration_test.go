package loopbackproxy_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/pathcapability"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func TestLoopbackProxyAuthenticatesMITMAndDispatchesByPathCapability(
	t *testing.T,
) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)

	secured := fixture.ConnectTLS(
		t,
		fixture.grant.ProxyCapability.Value(),
		"api.anthropic.com:443",
		"api.anthropic.com",
	)
	defer secured.Close()
	response := writeInnerRequest(t, secured, &http.Request{
		Method:        http.MethodPost,
		URL:           mustURL(t, "/v1/messages"),
		Host:          "api.anthropic.com:443",
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(`{"model":"client"}`)),
		ContentLength: int64(len(`{"model":"client"}`)),
	})
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		response.Header.Get("Content-Type") != "application/json" ||
		string(body) != `{"result":"proxied"}` {
		t.Fatalf(
			"semantic response status=%d headers=%v body=%q",
			response.StatusCode,
			response.Header,
			body,
		)
	}
	requests := fixture.exchanges.Requests()
	if len(requests) != 1 ||
		requests[0].AccessID() != fixture.accessID ||
		requests[0].ReplayClass() != exchange.ReplayGenerationCostOnly ||
		string(requests[0].Body()) != `{"model":"client"}` ||
		!strings.HasPrefix(requests[0].ExchangeID(), fixture.grant.Run.ID+"/") {
		t.Fatalf("semantic Exchange requests = %+v", requests)
	}

	wrongMethod := writeInnerRequest(t, secured, &http.Request{
		Method: http.MethodGet,
		URL:    mustURL(t, "/v1/messages"),
		Host:   "api.anthropic.com:443",
		Header: make(http.Header),
	})
	wrongBody, _ := io.ReadAll(wrongMethod.Body)
	_ = wrongMethod.Body.Close()
	if wrongMethod.StatusCode != http.StatusUnprocessableEntity ||
		!bytes.Contains(wrongBody, []byte(`"reasonCode":"path_unsupported"`)) ||
		len(fixture.exchanges.Requests()) != 1 {
		t.Fatalf(
			"wrong method status=%d body=%s Exchanges=%d",
			wrongMethod.StatusCode,
			wrongBody,
			len(fixture.exchanges.Requests()),
		)
	}

	opaque := writeInnerRequest(t, secured, &http.Request{
		Method: http.MethodGet,
		URL:    mustURL(t, "/v1/unknown?page=1"),
		Host:   "api.anthropic.com:443",
		Header: http.Header{
			"Authorization":       []string{"Bearer client-owned"},
			"Proxy-Authorization": []string{"Basic must-not-leave"},
		},
	})
	opaqueBody, _ := io.ReadAll(opaque.Body)
	_ = opaque.Body.Close()
	if opaque.StatusCode != http.StatusAccepted ||
		string(opaqueBody) != "original" {
		t.Fatalf("opaque response status=%d body=%q", opaque.StatusCode, opaqueBody)
	}
	original := fixture.original.Request()
	if original.Kind() != offlinehold.EgressOpaque ||
		original.Origin().String() != "https://api.anthropic.com:443" ||
		original.Path() != "/v1/unknown" ||
		original.RawQuery() != "page=1" ||
		original.Headers().Get("Authorization") != "Bearer client-owned" ||
		original.Headers().Get("Proxy-Authorization") != "" ||
		!strings.HasPrefix(original.RequestID(), fixture.grant.Run.ID+"/") {
		t.Fatalf("original request = %+v headers=%v", original, original.Headers())
	}
	page, err := fixture.connections.List(
		context.Background(),
		connectionevent.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	foundProviderRoute := false
	for _, record := range page.Items {
		if record.Phase == connectionevent.PhaseConnected &&
			record.RouteHost == "api.example.com" &&
			record.CredentialBindingID == "account-proxy" {
			foundProviderRoute = true
		}
		expectedConfidence := connectionevent.SourceConfidenceConfigured
		if record.Phase == connectionevent.PhaseAttempted {
			expectedConfidence = connectionevent.SourceConfidenceUnknown
		}
		if record.RequestedHost != "api.anthropic.com:443" ||
			record.SourceConfidence != expectedConfidence {
			t.Fatalf("connection record = %+v", record)
		}
	}
	if !foundProviderRoute {
		t.Fatalf("ConnectionEvent page has no provider route: %+v", page)
	}
}

func TestLoopbackProxyFailsClosedBeforeCertificateOrDataPlane(t *testing.T) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)

	for _, test := range []struct {
		name      string
		token     string
		authority string
		status    int
		reason    string
	}{
		{
			name:      "invalid capability",
			token:     base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
			authority: "api.anthropic.com:443",
			status:    http.StatusForbidden,
			reason:    "capture_run_rejected",
		},
		{
			name:      "unregistered endpoint",
			token:     fixture.grant.ProxyCapability.Value(),
			authority: "unknown.example.test:443",
			status:    http.StatusForbidden,
			reason:    "agent_endpoint_not_configured",
		},
		{
			name:      "noncanonical authority",
			token:     fixture.grant.ProxyCapability.Value(),
			authority: "API.anthropic.com:443",
			status:    http.StatusBadRequest,
			reason:    "connect_authority_invalid",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			connection, response := fixture.Connect(
				t,
				test.token,
				test.authority,
			)
			defer connection.Close()
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode != test.status ||
				!bytes.Contains(body, []byte(`"reasonCode":"`+test.reason+`"`)) {
				t.Fatalf(
					"CONNECT status=%d body=%s",
					response.StatusCode,
					body,
				)
			}
		})
	}
	if len(fixture.exchanges.Requests()) != 0 || fixture.original.Count() != 0 {
		t.Fatal("failed CONNECT reached a data plane")
	}
	page, err := fixture.connections.List(
		context.Background(),
		connectionevent.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	denied := 0
	for _, record := range page.Items {
		if record.Phase == connectionevent.PhaseDecided &&
			record.Decision == connectionevent.DecisionDeny &&
			record.Outcome == connectionevent.OutcomeDenied {
			denied++
		}
	}
	if denied != 3 || len(page.Items) != 6 {
		t.Fatalf("denied ConnectionEvents = %+v", page)
	}
}

func TestLoopbackProxyRejectsSNIMismatchAndDrainsHijackedConnections(
	t *testing.T,
) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)

	connection, response := fixture.Connect(
		t,
		fixture.grant.ProxyCapability.Value(),
		"api.anthropic.com:443",
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(fixture.authority.Root().CertificatePEM())
	wrongSNI := tls.Client(connection, &tls.Config{
		RootCAs:    pool,
		ServerName: "other.example.test",
		MinVersion: tls.VersionTLS12,
	})
	if err := wrongSNI.Handshake(); err == nil {
		t.Fatal("SNI mismatch completed TLS handshake")
	}
	_ = wrongSNI.Close()

	open := fixture.ConnectTLS(
		t,
		fixture.grant.ProxyCapability.Value(),
		"api.anthropic.com:443",
		"api.anthropic.com",
	)
	fixture.handler.BeginShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := fixture.handler.Drain(ctx); err != nil {
		t.Fatalf("drain hijacked connection: %v", err)
	}
	if _, err := open.Write([]byte("x")); err == nil {
		buffer := make([]byte, 1)
		_ = open.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, readErr := open.Read(buffer); readErr == nil {
			t.Fatal("drained TLS connection remained usable")
		}
	}
	_ = open.Close()
}

func TestLoopbackProxyRevalidatesEndpointOnEveryPersistentConnectionRequest(
	t *testing.T,
) {
	t.Parallel()

	fixture := newProxyFixture(t)
	defer fixture.Close(t)
	secured := fixture.ConnectTLS(
		t,
		fixture.grant.ProxyCapability.Value(),
		"api.anthropic.com:443",
		"api.anthropic.com",
	)
	defer secured.Close()

	fixture.ingress.Revoke()
	response := writeInnerRequest(t, secured, &http.Request{
		Method:        http.MethodPost,
		URL:           mustURL(t, "/v1/messages"),
		Host:          "api.anthropic.com:443",
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(`{"model":"client"}`)),
		ContentLength: int64(len(`{"model":"client"}`)),
	})
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMisdirectedRequest ||
		!bytes.Contains(body, []byte(`"reasonCode":"agent_endpoint_changed"`)) ||
		!response.Close {
		t.Fatalf(
			"revoked endpoint response status=%d close=%v body=%s",
			response.StatusCode,
			response.Close,
			body,
		)
	}
	if len(fixture.exchanges.Requests()) != 0 || fixture.original.Count() != 0 {
		t.Fatal("revoked persistent connection reached a data plane")
	}
}

type proxyFixture struct {
	listener    net.Listener
	server      *http.Server
	handler     *loopbackproxy.Handler
	store       *runtimepersistence.Store
	runs        *capturerun.Manager
	grant       capturerun.LaunchGrant
	authority   *localca.Authority
	exchanges   *exchangeRecorder
	original    *originalRecorder
	ingress     *revocableIngress
	accessID    access.AccessID
	connections *connectionevent.Manager
}

func newProxyFixture(t *testing.T) *proxyFixture {
	t.Helper()
	directory := t.TempDir()
	store, err := runtimepersistence.Open(
		context.Background(),
		runtimepersistence.Options{
			DatabasePath:           filepath.Join(directory, "data", "runtime.db"),
			BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
			CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	runOptions := capturerun.DefaultOptions(store.CaptureRunRepository())
	runs, err := capturerun.NewManager(context.Background(), runOptions)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := runs.Create(context.Background(), capturerun.CreateCommand{
		CWD:            filepath.Join(directory, "workspace"),
		ExecutablePath: "/usr/local/bin/claude",
		Lifetime:       5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := localca.Open(
		context.Background(),
		localca.DefaultOptions(filepath.Join(directory, "ca")),
	)
	if err != nil {
		t.Fatal(err)
	}
	projection, accessID := testProjection(t)
	ingress := &revocableIngress{delegate: projection}
	operations, err := operationcatalog.M0()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := pathcapability.NewCatalog(operations.Definitions())
	if err != nil {
		t.Fatal(err)
	}
	exchanges := &exchangeRecorder{}
	original := &originalRecorder{}
	connections, err := connectionevent.New(
		context.Background(),
		connectionevent.DefaultOptions(store.ConnectionEventRepository()),
	)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := loopbackproxy.New(loopbackproxy.Options{
		OwnerContext:     context.Background(),
		Runs:             runs,
		Ingress:          ingress,
		Paths:            paths,
		Exchanges:        exchanges,
		Original:         original,
		Certificates:     authority,
		Connections:      connections,
		ExchangeIDs:      loopbackproxy.NewCryptographicExchangeIDSource(),
		HandshakeTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() {
		_ = server.Serve(listener)
	}()
	return &proxyFixture{
		listener:    listener,
		server:      server,
		handler:     handler,
		store:       store,
		runs:        runs,
		grant:       grant,
		authority:   authority,
		exchanges:   exchanges,
		original:    original,
		ingress:     ingress,
		accessID:    accessID,
		connections: connections,
	}
}

type revocableIngress struct {
	delegate access.IngressResolver
	revoked  atomic.Bool
}

func (ingress *revocableIngress) ResolveClientOrigin(
	origin access.ClientOrigin,
) (access.IngressBinding, error) {
	if ingress.revoked.Load() {
		return access.IngressBinding{}, access.ErrAgentEndpointNotConfigured
	}
	return ingress.delegate.ResolveClientOrigin(origin)
}

func (ingress *revocableIngress) Revoke() {
	ingress.revoked.Store(true)
}

func (fixture *proxyFixture) Connect(
	t *testing.T,
	token string,
	authority string,
) (net.Conn, *http.Response) {
	t.Helper()
	connection, err := net.DialTimeout(
		"tcp",
		fixture.listener.Addr().String(),
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	credentials := base64.StdEncoding.EncodeToString(
		[]byte("capture:" + token),
	)
	if _, err := io.WriteString(
		connection,
		"CONNECT "+authority+" HTTP/1.1\r\n"+
			"Host: "+authority+"\r\n"+
			"Proxy-Authorization: Basic "+credentials+"\r\n\r\n",
	); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{
		Method: http.MethodConnect,
	})
	if err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	return &bufferedConn{Conn: connection, reader: reader}, response
}

func (fixture *proxyFixture) ConnectTLS(
	t *testing.T,
	token, authority, serverName string,
) *tls.Conn {
	t.Helper()
	connection, response := fixture.Connect(t, token, authority)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		_ = connection.Close()
		t.Fatalf("CONNECT status=%d body=%s", response.StatusCode, body)
	}
	_ = response.Body.Close()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(fixture.authority.Root().CertificatePEM()) {
		t.Fatal("append local Root")
	}
	secured := tls.Client(connection, &tls.Config{
		RootCAs:    pool,
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
	})
	if err := secured.Handshake(); err != nil {
		_ = secured.Close()
		t.Fatalf("TLS handshake: %v", err)
	}
	return secured
}

func (fixture *proxyFixture) Close(t *testing.T) {
	t.Helper()
	_ = fixture.server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := fixture.handler.Shutdown(ctx); err != nil {
		t.Errorf("shutdown proxy handler: %v", err)
	}
	if err := fixture.connections.Shutdown(ctx); err != nil {
		t.Errorf("shutdown ConnectionEvent manager: %v", err)
	}
	if err := fixture.runs.Shutdown(ctx); err != nil {
		t.Errorf("shutdown CaptureRun manager: %v", err)
	}
	if err := fixture.authority.Shutdown(ctx); err != nil {
		t.Errorf("shutdown local CA: %v", err)
	}
	if err := fixture.store.Shutdown(ctx); err != nil {
		t.Errorf("shutdown runtime store: %v", err)
	}
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (connection *bufferedConn) Read(destination []byte) (int, error) {
	return connection.reader.Read(destination)
}

func writeInnerRequest(
	t *testing.T,
	connection net.Conn,
	request *http.Request,
) *http.Response {
	t.Helper()
	if err := request.Write(connection); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func mustURL(t *testing.T, target string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

type exchangeRecorder struct {
	mu       sync.Mutex
	requests []exchange.ClientRequest
}

func (recorder *exchangeRecorder) Execute(
	ctx context.Context,
	request exchange.ClientRequest,
	downstream exchange.Downstream,
) (exchange.Result, error) {
	recorder.mu.Lock()
	recorder.requests = append(recorder.requests, request)
	recorder.mu.Unlock()
	if err := downstream.Begin(ctx, exchange.ResponseModeJSON); err != nil {
		return exchange.Result{}, err
	}
	if _, err := downstream.Write(ctx, []byte(`{"result":"proxied"}`)); err != nil {
		return exchange.Result{}, err
	}
	return exchange.Result{
		Outcome:             exchange.AttemptSucceeded,
		RouteHost:           "api.example.com",
		CredentialBindingID: "account-proxy",
	}, nil
}

func (recorder *exchangeRecorder) Requests() []exchange.ClientRequest {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]exchange.ClientRequest(nil), recorder.requests...)
}

type originalRecorder struct {
	mu      sync.Mutex
	request originaltransport.Request
	count   int
}

func (recorder *originalRecorder) Do(
	_ context.Context,
	request originaltransport.Request,
) (*http.Response, error) {
	recorder.mu.Lock()
	recorder.request = request
	recorder.count++
	recorder.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusAccepted,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader("original")),
	}, nil
}

func (recorder *originalRecorder) Request() originaltransport.Request {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.request
}

func (recorder *originalRecorder) Count() int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.count
}

func testProjection(
	t *testing.T,
) (*access.AtomicSnapshotProjection, access.AccessID) {
	t.Helper()
	accessID, _ := access.NewAccessID("access-proxy")
	endpointID, _ := access.NewAgentEndpointID("endpoint-proxy")
	profileID, _ := access.NewEndpointProfileID("profile-proxy")
	targetID, _ := access.NewProviderTargetID("target-proxy")
	accountID, _ := access.NewAccountBindingID("account-proxy")
	routeID, _ := access.NewRouteSetID("route-proxy")
	egressID, _ := access.NewEgressPolicyID("egress-proxy")
	clientOrigin, _ := access.NewClientOrigin("https://api.anthropic.com:443")
	providerOrigin, _ := access.NewProviderOrigin("https://api.openai.com:443/v1")
	model, _ := access.NewModelName("gpt-4.1-mini")
	secret, _ := access.NewSecretRef("secret://provider/proxy")
	codecID, _ := access.NewCodecPairID(anthropicchat.CodecPairID)
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
			Revision:        anthropicchat.CodecRevision,
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
	plan, err := compiler.Compile(access.Aggregate{
		Binding: access.AccessBinding{
			ID:                accessID,
			Revision:          1,
			Name:              "Proxy Access",
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
			ID:                      profileID,
			Revision:                1,
			AccessID:                accessID,
			Name:                    "OpenAI",
			BackendDialect:          access.DialectOpenAIChat,
			TargetID:                targetID,
			TransportProfileRef:     access.ObservedClientH1TransportProfileRef(),
			DefaultModelPolicy:      access.ModelPolicy{Revision: 1, Mode: access.ModelPolicyModeFixed, FixedModel: model},
			AccountBindingIDs:       []access.AccountBindingID{accountID},
			DefaultAccountBindingID: accountID,
		}},
		ProviderTargets: []access.ProviderTarget{{
			ID:           targetID,
			Revision:     1,
			AccessID:     accessID,
			ProfileID:    profileID,
			Origin:       providerOrigin,
			Protocol:     access.DialectOpenAIChat,
			Capabilities: []access.ProviderCapability{access.ProviderCapabilityMessages, access.ProviderCapabilityStreaming, access.ProviderCapabilityToolCalls},
		}},
		AccountBindings: []access.ProviderAccountBinding{{
			ID:            accountID,
			Revision:      1,
			AccessID:      accessID,
			ProfileID:     profileID,
			Label:         "Primary",
			SecretRef:     secret,
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
	projection := access.NewSnapshotProjection()
	if err := projection.Restore([]access.AccessPlanSnapshot{plan}); err != nil {
		t.Fatal(err)
	}
	return projection, accessID
}
