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
	"github.com/vibe-agi/vibermate/internal/blindtunnel"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/pathcapability"
	"github.com/vibe-agi/vibermate/internal/responseschat"
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
		requests[0].CaptureRunRef() != fixture.grant.Run.ID ||
		requests[0].ConnectionRef() == "" ||
		strings.Contains(requests[0].ExchangeID(), fixture.grant.Run.ID) {
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
		strings.Contains(original.RequestID(), fixture.grant.Run.ID) {
		t.Fatalf("original request = %+v headers=%v", original, original.Headers())
	}
	page, err := fixture.connections.List(
		context.Background(),
		connectionevent.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	// The connection record answers who connected where from here. The
	// per-request provider destination and credential decision belong to the
	// EgressAttempt, so they never appear here.
	for _, record := range page.Items {
		expectedConfidence := connectionevent.SourceConfidenceConfigured
		if record.Phase == connectionevent.PhaseAttempted {
			expectedConfidence = connectionevent.SourceConfidenceUnknown
		}
		if record.RequestedHost != "api.anthropic.com:443" ||
			record.SourceConfidence != expectedConfidence {
			t.Fatalf("connection record = %+v", record)
		}
		if record.CredentialBindingID != "" {
			t.Fatalf("connection record carries a credential decision: %+v", record)
		}
		if record.RouteHost != "" &&
			record.RouteHost != "api.anthropic.com" {
			t.Fatalf("connection record carries a provider route: %+v", record)
		}
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
			// An unregistered authority is now forwarded blind rather than
			// refused. It still reaches no certificate and no data plane,
			// which is what this test guards; the dial itself fails because
			// the host does not resolve.
			name:      "unregistered endpoint",
			token:     fixture.grant.ProxyCapability.Value(),
			authority: "unknown.example.test:443",
			status:    http.StatusBadGateway,
			reason:    "blind_tunnel_failed",
		},
		{
			// A case difference or a root dot is the same name and is
			// canonicalized. A name that is only dots is not a name.
			name:      "malformed authority",
			token:     fixture.grant.ProxyCapability.Value(),
			authority: "api.anthropic.com..:443",
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
	if denied != 2 {
		t.Fatalf("denied ConnectionEvents = %+v", page)
	}
	for _, record := range page.Items {
		if record.RequestedHost == "api.anthropic.com..:443" &&
			record.Decryption == connectionevent.DecryptionMITM {
			t.Fatalf("a malformed authority was decrypted: %+v", record)
		}
	}
}

func TestLoopbackProxyReturnsBounded426ForResponsesWebSocketUpgrade(
	t *testing.T,
) {
	t.Parallel()

	fixture := newResponsesProxyFixture(t)
	defer fixture.Close(t)
	secured := fixture.ConnectTLS(
		t,
		fixture.grant.ProxyCapability.Value(),
		"api.openai.com:443",
		"api.openai.com",
	)
	defer secured.Close()
	response := writeInnerRequest(t, secured, &http.Request{
		Method: http.MethodGet,
		URL:    mustURL(t, "/v1/responses"),
		Host:   "api.openai.com:443",
		Header: http.Header{
			"Connection":            []string{"keep-alive, Upgrade"},
			"Upgrade":               []string{"websocket"},
			"Sec-Websocket-Version": []string{"13"},
		},
	})
	body, err := io.ReadAll(io.LimitReader(response.Body, 1025))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUpgradeRequired ||
		len(body) == 0 ||
		len(body) > 1024 ||
		!bytes.Contains(
			body,
			[]byte(`"reasonCode":"responses_websocket_unsupported"`),
		) ||
		len(fixture.exchanges.Requests()) != 0 ||
		fixture.original.Count() != 0 {
		t.Fatalf(
			"WebSocket response status=%d body=%s Exchanges=%d original=%d",
			response.StatusCode,
			body,
			len(fixture.exchanges.Requests()),
			fixture.original.Count(),
		)
	}
}

func TestLoopbackProxyDoesNotApplyFixedFallbackToGenericClient(
	t *testing.T,
) {
	t.Parallel()

	fixture := newGenericResponsesProxyFixture(t)
	defer fixture.Close(t)
	secured := fixture.ConnectTLS(
		t,
		fixture.grant.ProxyCapability.Value(),
		"api.openai.com:443",
		"api.openai.com",
	)
	defer secured.Close()
	response := writeInnerRequest(t, secured, &http.Request{
		Method: http.MethodGet,
		URL:    mustURL(t, "/v1/responses"),
		Host:   "api.openai.com:443",
		Header: http.Header{
			"Connection": []string{"Upgrade"},
			"Upgrade":    []string{"websocket"},
		},
	})
	body, err := io.ReadAll(io.LimitReader(response.Body, 1025))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity ||
		!bytes.Contains(body, []byte(`"reasonCode":"path_unsupported"`)) ||
		bytes.Contains(
			body,
			[]byte(`"reasonCode":"responses_websocket_unsupported"`),
		) ||
		len(fixture.exchanges.Requests()) != 0 ||
		fixture.original.Count() != 0 {
		t.Fatalf(
			"generic WebSocket status=%d body=%s Exchanges=%d original=%d",
			response.StatusCode,
			body,
			len(fixture.exchanges.Requests()),
			fixture.original.Count(),
		)
	}
}

func TestLoopbackProxyDispatchesExactResponsesHTTPWithTypedEvidence(
	t *testing.T,
) {
	t.Parallel()

	fixture := newResponsesProxyFixture(t)
	defer fixture.Close(t)
	secured := fixture.ConnectTLS(
		t,
		fixture.grant.ProxyCapability.Value(),
		"api.openai.com:443",
		"api.openai.com",
	)
	defer secured.Close()
	const requestBody = `{"model":"client","input":"hello","stream":true}`
	response := writeInnerRequest(t, secured, &http.Request{
		Method:        http.MethodPost,
		URL:           mustURL(t, "/v1/responses"),
		Host:          "api.openai.com:443",
		ContentLength: int64(len(requestBody)),
		Body:          io.NopCloser(strings.NewReader(requestBody)),
		Header: http.Header{
			"Authorization": []string{"Bearer client-owned"},
			"Connection":    []string{"keep-alive, X-Client-Hop"},
			"X-Client-Hop":  []string{"must-not-cross"},
			"Content-Type":  []string{"application/json"},
		},
	})
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	requests := fixture.exchanges.Requests()
	if response.StatusCode != http.StatusOK ||
		string(body) != `{"result":"proxied"}` ||
		len(requests) != 1 ||
		requests[0].AccessID() != fixture.accessID ||
		requests[0].ReplayClass() != exchange.ReplayGenerationCostOnly ||
		string(requests[0].Body()) != requestBody {
		t.Fatalf(
			"Responses status=%d body=%s requests=%+v",
			response.StatusCode,
			body,
			requests,
		)
	}
	operation := requests[0].ClientOperation()
	if operation.ID().String() != operationcatalog.OpenAIResponsesCreateID ||
		operation.Revision() != 1 ||
		operation.Method() != http.MethodPost ||
		operation.Path() != "/v1/responses" ||
		operation.RawQuery() != "" {
		t.Fatalf("Responses operation evidence = %+v", operation)
	}
}

func TestLoopbackProxyKeepsFrozenResponsesEndpointAcrossProviderPlanAdvance(
	t *testing.T,
) {
	t.Parallel()

	fixture := newResponsesProxyFixture(t)
	defer fixture.Close(t)
	secured := fixture.ConnectTLS(
		t,
		fixture.grant.ProxyCapability.Value(),
		"api.openai.com:443",
		"api.openai.com",
	)
	defer secured.Close()

	advancedProjection, _ := testProjectionForDialectRevision(
		t,
		fixture.authority,
		access.DialectOpenAIResponses,
		2,
		"gpt-provider-revision-two",
	)
	origin, err := access.NewClientOrigin("https://api.openai.com:443")
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := advancedProjection.ResolveClientOrigin(origin)
	if err != nil {
		t.Fatal(err)
	}
	fixture.ingress.Advance(advanced)

	const requestBody = `{"model":"client","input":"hello","stream":false}`
	response := writeInnerRequest(t, secured, &http.Request{
		Method:        http.MethodPost,
		URL:           mustURL(t, "/v1/responses"),
		Host:          "api.openai.com:443",
		ContentLength: int64(len(requestBody)),
		Body:          io.NopCloser(strings.NewReader(requestBody)),
		Header:        http.Header{"Content-Type": []string{"application/json"}},
	})
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	requests := fixture.exchanges.Requests()
	if response.StatusCode != http.StatusOK ||
		string(body) != `{"result":"proxied"}` ||
		len(requests) != 1 ||
		requests[0].IngressBinding().AccessRevision() != 1 ||
		advanced.AccessRevision() != 2 ||
		requests[0].IngressBinding().AgentEndpointID() !=
			advanced.AgentEndpointID() ||
		requests[0].IngressBinding().ClientOrigin() !=
			advanced.ClientOrigin() ||
		requests[0].IngressBinding().ClientDialect() !=
			advanced.ClientDialect() {
		t.Fatalf(
			"provider-plan advance status=%d body=%s request=%+v current=%+v",
			response.StatusCode,
			body,
			requests,
			advanced,
		)
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
	pool.AppendCertsFromPEM(fixture.authority.Certificate().CertificatePEM())
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

func TestLoopbackProxyFailsClosedWhenEndpointIsRevokedBeforeClientHello(
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
	fixture.ingress.Revoke()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(
		fixture.authority.Certificate().CertificatePEM(),
	) {
		t.Fatal("append local Root")
	}
	secured := tls.Client(connection, &tls.Config{
		RootCAs:    pool,
		ServerName: "api.anthropic.com",
		MinVersion: tls.VersionTLS12,
	})
	if err := secured.Handshake(); err == nil {
		t.Fatal("revoked endpoint completed a new TLS handshake")
	}
	_ = secured.Close()
	if len(fixture.exchanges.Requests()) != 0 || fixture.original.Count() != 0 {
		t.Fatal("revoked pre-ClientHello connection reached a data plane")
	}
}

func TestLoopbackProxyRevalidatesEndpointOnEveryPersistentConnectionRequest(
	t *testing.T,
) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		fixture   func(*testing.T) *proxyFixture
		authority string
		path      string
	}{
		{
			name:      "Anthropic Messages",
			fixture:   newProxyFixture,
			authority: "api.anthropic.com:443",
			path:      "/v1/messages",
		},
		{
			name:      "OpenAI Responses",
			fixture:   newResponsesProxyFixture,
			authority: "api.openai.com:443",
			path:      "/v1/responses",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := test.fixture(t)
			defer fixture.Close(t)
			host := strings.TrimSuffix(test.authority, ":443")
			secured := fixture.ConnectTLS(
				t,
				fixture.grant.ProxyCapability.Value(),
				test.authority,
				host,
			)
			defer secured.Close()

			fixture.ingress.Revoke()
			response := writeInnerRequest(t, secured, &http.Request{
				Method: http.MethodPost,
				URL:    mustURL(t, test.path),
				Host:   test.authority,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body:          io.NopCloser(strings.NewReader(`{"model":"client"}`)),
				ContentLength: int64(len(`{"model":"client"}`)),
			})
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusMisdirectedRequest ||
				!bytes.Contains(
					body,
					[]byte(`"reasonCode":"agent_endpoint_changed"`),
				) ||
				!response.Close {
				t.Fatalf(
					"revoked endpoint response status=%d close=%v body=%s",
					response.StatusCode,
					response.Close,
					body,
				)
			}
			if len(fixture.exchanges.Requests()) != 0 ||
				fixture.original.Count() != 0 {
				t.Fatal("revoked persistent connection reached a data plane")
			}
		})
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
	egress      egressaudit.Repository
}

func newProxyFixture(t *testing.T) *proxyFixture {
	t.Helper()
	return newProxyFixtureForDialect(
		t,
		access.DialectAnthropicMessages,
		nil,
	)
}

func newResponsesProxyFixture(t *testing.T) *proxyFixture {
	t.Helper()
	adapter := fixedCodexAdapterEvidence()
	return newProxyFixtureForDialect(
		t,
		access.DialectOpenAIResponses,
		&adapter,
	)
}

func newGenericResponsesProxyFixture(t *testing.T) *proxyFixture {
	t.Helper()
	return newProxyFixtureForDialect(
		t,
		access.DialectOpenAIResponses,
		nil,
	)
}

func newProxyFixtureForDialect(
	t *testing.T,
	clientDialect access.Dialect,
	adapter *clientadapter.Evidence,
) *proxyFixture {
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
		CWD:             filepath.Join(directory, "workspace"),
		ExecutablePath:  "/usr/local/bin/claude",
		Lifetime:        5 * time.Minute,
		CatalogRevision: 1,
		Adapter:         adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := localca.Open(
		context.Background(),
		localca.DefaultOptions(
			filepath.Join(directory, "ca"),
			context.Background(),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	projection, accessID := testProjectionForDialect(
		t,
		authority,
		clientDialect,
	)
	ingress := &revocableIngress{delegate: projection}
	operations, err := operationcatalog.BuiltIn()
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
		BlindTunnels:     newTestBlindTunnels(t),
		EgressAudit:      store.EgressAttemptRepository(),
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
		egress:      store.EgressAttemptRepository(),
	}
}

func fixedCodexAdapterEvidence() clientadapter.Evidence {
	return clientadapter.Evidence{
		ID:              "codex-cli",
		Revision:        1,
		Version:         "0.145.0",
		CatalogRevision: 1,
		InstallShape:    clientadapter.InstallNPMWrapperNativeChild,
		ReleaseSHA256:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		LaunchRecipe:    clientadapter.LaunchSSLCertFile,
		Features:        clientadapter.FeatureResponsesWebSocketHTTPFallback,
	}
}

type revocableIngress struct {
	delegate loopbackproxy.IngressAuthority
	revoked  atomic.Bool
	mu       sync.RWMutex
	current  *access.IngressBinding
}

func (ingress *revocableIngress) AdmitLeaf(
	intent access.LeafIssuanceIntent,
) (access.LeafIssuanceAdmission, error) {
	if ingress.revoked.Load() {
		return access.LeafIssuanceAdmission{},
			access.ErrLeafIssuanceUnauthorized
	}
	return ingress.delegate.AdmitLeaf(intent)
}

func (ingress *revocableIngress) ResolveClientOrigin(
	origin access.ClientOrigin,
) (access.IngressBinding, error) {
	if ingress.revoked.Load() {
		return access.IngressBinding{}, access.ErrAgentEndpointNotConfigured
	}
	ingress.mu.RLock()
	current := ingress.current
	if current != nil {
		binding := *current
		ingress.mu.RUnlock()
		if binding.ClientOrigin() == origin {
			return binding, nil
		}
		return access.IngressBinding{}, access.ErrAgentEndpointNotConfigured
	}
	ingress.mu.RUnlock()
	return ingress.delegate.ResolveClientOrigin(origin)
}

func (ingress *revocableIngress) Revoke() {
	ingress.revoked.Store(true)
}

func (ingress *revocableIngress) Advance(binding access.IngressBinding) {
	ingress.mu.Lock()
	defer ingress.mu.Unlock()
	copy := binding
	ingress.current = &copy
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
	if !pool.AppendCertsFromPEM(
		fixture.authority.Certificate().CertificatePEM(),
	) {
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
	authority *localca.Authority,
) (*access.AtomicSnapshotProjection, access.AccessID) {
	t.Helper()
	return testProjectionForDialect(
		t,
		authority,
		access.DialectAnthropicMessages,
	)
}

func testProjectionForDialect(
	t *testing.T,
	authority *localca.Authority,
	clientDialect access.Dialect,
) (*access.AtomicSnapshotProjection, access.AccessID) {
	t.Helper()
	return testProjectionForDialectRevision(
		t,
		authority,
		clientDialect,
		1,
		"gpt-4.1-mini",
	)
}

func testProjectionForDialectRevision(
	t *testing.T,
	authority *localca.Authority,
	clientDialect access.Dialect,
	revision access.Revision,
	modelValue string,
) (*access.AtomicSnapshotProjection, access.AccessID) {
	t.Helper()
	accessID, _ := access.NewAccessID("access-proxy")
	endpointID, _ := access.NewAgentEndpointID("endpoint-proxy")
	profileID, _ := access.NewEndpointProfileID("profile-proxy")
	targetID, _ := access.NewProviderTargetID("target-proxy")
	accountID, _ := access.NewAccountBindingID("account-proxy")
	routeID, _ := access.NewRouteSetID("route-proxy")
	egressID, _ := access.NewEgressPolicyID("egress-proxy")
	clientOriginValue := "https://api.anthropic.com:443"
	codecPairID := anthropicchat.CodecPairID
	codecRevision := access.Revision(anthropicchat.CodecRevision)
	if clientDialect == access.DialectOpenAIResponses {
		clientOriginValue = "https://api.openai.com:443"
		codecPairID = responseschat.CodecPairID
		codecRevision = access.Revision(responseschat.CodecRevision)
	}
	clientOrigin, _ := access.NewClientOrigin(clientOriginValue)
	providerOrigin, _ := access.NewProviderOrigin("https://api.openai.com:443/v1")
	model, _ := access.NewModelName(modelValue)
	secret, _ := access.NewSecretRef("secret://provider/proxy")
	codecID, _ := access.NewCodecPairID(codecPairID)
	operations, err := operationcatalog.BuiltIn()
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
			Revision:        codecRevision,
			ClientDialect:   clientDialect,
			ProviderDialect: access.DialectOpenAIChat,
			ClientOperationIDs: operations.SemanticOperationIDs(
				clientDialect,
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
			Revision:          revision,
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
			ClientDialect: clientDialect,
		},
		Profiles: []access.EndpointProfile{{
			ID:                      profileID,
			Revision:                revision,
			AccessID:                accessID,
			Name:                    "OpenAI",
			BackendDialect:          access.DialectOpenAIChat,
			TargetID:                targetID,
			TransportProfileRef:     access.ObservedClientH1TransportProfileRef(),
			DefaultModelPolicy:      access.ModelPolicy{Revision: revision, Mode: access.ModelPolicyModeFixed, FixedModel: model},
			AccountBindingIDs:       []access.AccountBindingID{accountID},
			DefaultAccountBindingID: accountID,
		}},
		ProviderTargets: []access.ProviderTarget{{
			ID:           targetID,
			Revision:     revision,
			AccessID:     accessID,
			ProfileID:    profileID,
			Origin:       providerOrigin,
			Protocol:     access.DialectOpenAIChat,
			Capabilities: []access.ProviderCapability{access.ProviderCapabilityMessages, access.ProviderCapabilityStreaming, access.ProviderCapabilityToolCalls},
		}},
		AccountBindings: []access.ProviderAccountBinding{{
			ID:            accountID,
			Revision:      revision,
			AccessID:      accessID,
			ProfileID:     profileID,
			Label:         "Primary",
			SecretRef:     secret,
			AuthDriverRef: access.StaticHeaderAuthDriverRef(),
			Enabled:       true,
		}},
		RouteSets: []access.RouteSet{{
			ID:                  routeID,
			Revision:            revision,
			AccessID:            accessID,
			CandidateProfileIDs: []access.EndpointProfileID{profileID},
		}},
		EgressPolicy: access.AccessEgressPolicy{
			ID:       egressID,
			Revision: revision,
			AccessID: accessID,
			Mode:     access.EgressModeDirect,
		},
		PluginPlan: access.PluginPlan{
			Revision: revision,
			AccessID: accessID,
			Mode:     access.PluginPlanModePassThrough,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := access.NewSnapshotProjection(
		authority.Identity().Revision(),
		authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.Restore([]access.AccessPlanSnapshot{plan}); err != nil {
		t.Fatal(err)
	}
	return projection, accessID
}

// blindTunnelFixture pairs a started coordinator with the production dialer so
// a blind tunnel in tests goes through the same egress admission as production.
type blindTunnelFixture struct {
	gate   *offlinehold.Gate
	dialer *blindtunnel.Dialer
}

func newTestBlindTunnels(t *testing.T) *blindTunnelFixture {
	t.Helper()

	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Start(
		context.Background(),
		offlinehold.RuntimeBinding{InstanceID: "proxy-test"},
	); err != nil {
		t.Fatal(err)
	}
	dialer, err := blindtunnel.NewDialer(gate)
	if err != nil {
		t.Fatal(err)
	}
	return &blindTunnelFixture{gate: gate, dialer: dialer}
}

func (fixture *blindTunnelFixture) BeginAction(
	ctx context.Context,
	request offlinehold.ActionRequest,
) (*offlinehold.ActionLease, error) {
	return fixture.gate.BeginAction(ctx, request)
}

func (fixture *blindTunnelFixture) Dial(
	ctx context.Context,
	request blindtunnel.DialRequest,
) (net.Conn, offlinehold.Lease, error) {
	return fixture.dialer.Dial(ctx, request)
}
