package loopbackproxy_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/vibe-agi/vibermate/internal/blindtunnel"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

func TestLoopbackProxyAuthenticatesMITMAndDispatchesByEnvironmentOperation(
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
		Method: http.MethodPost,
		URL:    mustURL(t, "/v1/messages"),
		Host:   "api.anthropic.com:443",
		Header: http.Header{
			"Authorization": []string{"Bearer fixture-client-auth"},
			"Content-Type":  []string{"application/json"},
			"Cookie":        []string{"session=fixture"},
			"User-Agent":    []string{"agent-client/1.0"},
		},
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
	if len(requests) != 1 {
		t.Fatalf("semantic Exchange requests = %+v", requests)
	}
	originalHeaders, hasOriginalHeaders := requests[0].OriginalHeaders()
	requestPlan := requests[0].RequestPlan()
	if requestPlan.EnvironmentID() != fixture.environment.ID ||
		requestPlan.EnvironmentRevision() != fixture.environment.Revision ||
		requestPlan.Endpoint().ID() != fixture.environment.ClientEndpoints[0].ID ||
		requestPlan.Route().ID() != "route-proxy" ||
		requests[0].ReplayClass() != exchange.ReplayGenerationCostOnly ||
		string(requests[0].Body()) != `{"model":"client"}` ||
		requests[0].CaptureRunRef() != fixture.grant.Run.ID ||
		requests[0].CaptureAdmissionRef() != "capture-run/"+fixture.grant.Run.ID ||
		requests[0].ManualCaptureRef() != "" ||
		requests[0].ClientUserAgent() != "agent-client/1.0" ||
		!hasOriginalHeaders ||
		originalHeaders.Get("Authorization") != "Bearer fixture-client-auth" ||
		originalHeaders.Get("Cookie") != "session=fixture" ||
		requests[0].ConnectionRef() == "" ||
		strings.Contains(requests[0].ExchangeID(), fixture.grant.Run.ID) {
		t.Fatalf("semantic Exchange requests = %+v", requests)
	}
	connectionPage, err := fixture.connections.List(
		context.Background(),
		connectionevent.PageRequest{Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	foundEnvironmentRelation := false
	for _, record := range connectionPage.Items {
		if record.ConnectionID != requests[0].ConnectionRef() ||
			(record.Phase != connectionevent.PhaseConnected &&
				record.Phase != connectionevent.PhaseClosed) {
			continue
		}
		if record.EnvironmentID != fixture.environment.ID ||
			record.EnvironmentName == "" ||
			record.EnvironmentRevision != fixture.environment.Revision ||
			record.ClientEndpointID != fixture.environment.ClientEndpoints[0].ID ||
			record.ClientEndpointRevision == 0 ||
			record.Decryption != connectionevent.DecryptionMITM {
			t.Fatalf("semantic connection relation = %+v", record)
		}
		foundEnvironmentRelation = true
	}
	if !foundEnvironmentRelation {
		t.Fatal("semantic connection left no Environment relation")
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

	if _, err := io.WriteString(
		secured,
		"POST /v1/messages HTTP/1.1\r\n"+
			"Host: api.anthropic.com:443\r\n"+
			"Content-Type: application/json\r\n"+
			"Content-Length: 18\r\n"+
			"User-Agent: agent-client/1.0\r\n"+
			"User-Agent: other-client/1.0\r\n\r\n"+
			`{"model":"client"}`,
	); err != nil {
		t.Fatal(err)
	}
	ambiguousUserAgent, err := http.ReadResponse(
		bufio.NewReader(secured),
		&http.Request{Method: http.MethodPost},
	)
	if err != nil {
		t.Fatal(err)
	}
	ambiguousBody, _ := io.ReadAll(ambiguousUserAgent.Body)
	_ = ambiguousUserAgent.Body.Close()
	if ambiguousUserAgent.StatusCode != http.StatusBadRequest ||
		!bytes.Contains(ambiguousBody, []byte(`"reasonCode":"request_body_invalid"`)) ||
		len(fixture.exchanges.Requests()) != 1 {
		t.Fatalf(
			"ambiguous User-Agent status=%d body=%s Exchanges=%d",
			ambiguousUserAgent.StatusCode,
			ambiguousBody,
			len(fixture.exchanges.Requests()),
		)
	}

	opaque := writeInnerRequest(t, secured, &http.Request{
		Method: http.MethodGet,
		URL:    mustURL(t, "/api/hello"),
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
		original.Origin().String() != "https://api.anthropic.com" ||
		original.Path() != "/api/hello" ||
		original.RawQuery() != "" ||
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
		expectedIngressID := "capture-run/" + fixture.grant.Run.ID
		expectedLabel := fixture.grant.Run.ExecutableLabel
		if record.Phase == connectionevent.PhaseAttempted {
			expectedConfidence = connectionevent.SourceConfidenceUnknown
			expectedIngressID = ""
			expectedLabel = ""
		}
		if record.RequestedHost != "api.anthropic.com" ||
			record.Port != 443 ||
			record.SourceConfidence != expectedConfidence ||
			record.IngressID != expectedIngressID ||
			record.SourceLabel != expectedLabel {
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

func TestLoopbackProxyNegotiatesHTTP2AndPreservesTheRequestProtocol(
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
		ServerName: "api.anthropic.com",
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{string(wireprofile.ApplicationProtocolHTTP2)},
	})
	if err := secured.Handshake(); err != nil {
		_ = secured.Close()
		t.Fatalf("HTTP/2 TLS handshake: %v", err)
	}
	defer secured.Close()
	if got := secured.ConnectionState().NegotiatedProtocol; got != "h2" {
		t.Fatalf("negotiated protocol = %q", got)
	}
	h2Transport := &http2.Transport{DisableCompression: true}
	clientConnection, err := h2Transport.NewClientConn(secured)
	if err != nil {
		t.Fatalf("create HTTP/2 client connection: %v", err)
	}
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"https://api.anthropic.com/v1/messages",
		strings.NewReader(`{"model":"client"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "api.anthropic.com:443"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "agent-h2/1.0")
	response, err = clientConnection.RoundTrip(request)
	if err != nil {
		t.Fatalf("send inner HTTP/2 request: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		string(body) != `{"result":"proxied"}` {
		t.Fatalf("HTTP/2 response status=%d body=%q", response.StatusCode, body)
	}
	requests := fixture.exchanges.Requests()
	if len(requests) != 1 ||
		requests[0].ClientHTTPProtocol() != wireprofile.ApplicationProtocolHTTP2 ||
		requests[0].ClientUserAgent() != "agent-h2/1.0" {
		t.Fatalf("HTTP/2 Exchange requests = %+v", requests)
	}
}

func TestLoopbackProxyPreservesHTTP1WithoutALPN(
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
	if got := secured.ConnectionState().NegotiatedProtocol; got != "" {
		t.Fatalf("negotiated protocol = %q", got)
	}
	response := writeInnerRequest(t, secured, &http.Request{
		Method: http.MethodPost,
		URL:    mustURL(t, "/v1/messages"),
		Host:   "api.anthropic.com:443",
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body:          io.NopCloser(strings.NewReader(`{"model":"client"}`)),
		ContentLength: int64(len(`{"model":"client"}`)),
	})
	_ = response.Body.Close()
	requests := fixture.exchanges.Requests()
	if len(requests) != 1 ||
		requests[0].ClientHTTPProtocol() != wireprofile.ApplicationProtocolHTTP1 {
		t.Fatalf("preserved requests = %+v", requests)
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
			reason:    "capture_admission_rejected",
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
		requests[0].RequestPlan().EnvironmentID() != fixture.environment.ID ||
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

func TestLoopbackProxyHotSwitchesCompatibleEnvironmentOnNextRequest(
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

	advanced := testEnvironmentForDialectRevision(
		t,
		protocolspec.DialectOpenAIResponses,
		"environment-responses-two",
		1,
		1,
	)
	fixture.publishEnvironment(t, advanced)
	switchResult := fixture.switchEnvironment(t, advanced.ID)
	if switchResult.Boundary != captureassignment.BoundaryHotSwitch ||
		len(switchResult.ClosedConnections) != 0 {
		t.Fatalf("compatible switch = %+v", switchResult)
	}

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
		requests[0].RequestPlan().EnvironmentID() != advanced.ID ||
		requests[0].RequestPlan().EnvironmentRevision() != advanced.Revision ||
		requests[0].RequestPlan().Route().Revision() != advanced.Revision {
		t.Fatalf(
			"Environment hot switch status=%d body=%s request=%+v current=%+v",
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
	ingressID, err := capturerun.AdmissionRef(fixture.grant.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		page, listErr := fixture.connections.List(
			context.Background(),
			connectionevent.PageRequest{
				Limit:               10,
				IngressID:           ingressID,
				LatestPerConnection: true,
			},
		)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(page.Items) == 1 &&
			page.Items[0].Phase == connectionevent.PhaseFailed {
			failed := page.Items[0]
			if failed.EnvironmentID != fixture.environment.ID ||
				failed.EnvironmentName == "" ||
				failed.ClientEndpointID != fixture.environment.ClientEndpoints[0].ID ||
				failed.Decryption != connectionevent.DecryptionMITM {
				t.Fatalf("failed handshake lost its Environment relation: %+v", failed)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed handshake was not journaled: %+v", page)
		}
		runtime.Gosched()
	}

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

func TestLoopbackProxyClosesConnectionWhenEnvironmentChangesBeforeClientHello(
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
	result := fixture.switchEnvironment(t, environment.SystemTransparentID)
	if result.Boundary != captureassignment.BoundaryReconnectRequired ||
		len(result.ClosedConnections) != 1 {
		t.Fatalf("pre-ClientHello Environment switch = %+v", result)
	}
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
		t.Fatal("obsolete Environment connection completed a new TLS handshake")
	}
	_ = secured.Close()
	if len(fixture.exchanges.Requests()) != 0 || fixture.original.Count() != 0 {
		t.Fatal("obsolete pre-ClientHello connection reached a data plane")
	}
}

func TestLoopbackProxyReconnectsWhenEnvironmentProtocolCompatibilityChanges(
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
			fixture := test.fixture(t)
			defer fixture.Close(t)
			host := strings.TrimSuffix(test.authority, ":443")
			secured := fixture.ConnectTLS(
				t,
				fixture.grant.ProxyCapability.Value(),
				test.authority,
				host,
			)
			incompatible := testEnvironmentForDialectRevision(
				t,
				fixture.clientDialect,
				"environment-incompatible",
				1,
				2,
			)
			fixture.publishEnvironment(t, incompatible)
			result := fixture.switchEnvironment(t, incompatible.ID)
			if result.Boundary != captureassignment.BoundaryReconnectRequired ||
				len(result.ClosedConnections) != 1 {
				t.Fatalf("incompatible Environment switch = %+v", result)
			}
			_ = secured.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
			if _, err := secured.Write([]byte("obsolete")); err == nil {
				buffer := make([]byte, 1)
				if _, err := secured.Read(buffer); err == nil {
					t.Fatal("obsolete Environment connection remained usable")
				}
			}
			_ = secured.Close()

			reconnected := fixture.ConnectTLS(
				t,
				fixture.grant.ProxyCapability.Value(),
				test.authority,
				host,
			)
			defer reconnected.Close()
			response := writeInnerRequest(t, reconnected, &http.Request{
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
			requests := fixture.exchanges.Requests()
			if response.StatusCode != http.StatusOK ||
				string(body) != `{"result":"proxied"}` ||
				len(requests) != 1 ||
				requests[0].RequestPlan().EnvironmentID() != incompatible.ID {
				t.Fatalf(
					"reconnected response status=%d close=%v body=%s requests=%+v",
					response.StatusCode,
					response.Close,
					body,
					requests,
				)
			}
		})
	}
}

func TestLoopbackProxyPinsEnvironmentRequestPlanForTheWholeInnerRequest(t *testing.T) {
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
	started, releaseExchange := fixture.exchanges.Block()
	request := &http.Request{
		Method: http.MethodPost,
		URL:    mustURL(t, "/v1/messages"),
		Host:   "api.anthropic.com:443",
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body:          io.NopCloser(strings.NewReader(`{"model":"client"}`)),
		ContentLength: int64(len(`{"model":"client"}`)),
	}
	type responseResult struct {
		response *http.Response
		err      error
	}
	completed := make(chan responseResult, 1)
	go func() {
		if err := request.Write(secured); err != nil {
			completed <- responseResult{err: err}
			return
		}
		response, err := http.ReadResponse(bufio.NewReader(secured), request)
		completed <- responseResult{response: response, err: err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("inner request did not reach the blocked Exchange")
	}
	requests := fixture.exchanges.Requests()
	if len(requests) != 1 ||
		requests[0].RequestPlan().EnvironmentID() != fixture.environment.ID {
		t.Fatalf("blocked request plan = %+v", requests)
	}
	next := testEnvironmentForDialectRevision(
		t,
		protocolspec.DialectAnthropicMessages,
		"environment-request-next",
		1,
		1,
	)
	fixture.publishEnvironment(t, next)
	switchResult := fixture.switchEnvironment(t, next.ID)
	if switchResult.Boundary != captureassignment.BoundaryHotSwitch ||
		len(switchResult.ClosedConnections) != 0 {
		t.Fatalf("request-time hot switch = %+v", switchResult)
	}
	requests = fixture.exchanges.Requests()
	if requests[0].RequestPlan().EnvironmentID() != fixture.environment.ID {
		t.Fatalf("in-flight request plan changed after switch: %+v", requests[0].RequestPlan())
	}
	releaseExchange()
	result := <-completed
	if result.err != nil {
		t.Fatal(result.err)
	}
	_ = result.response.Body.Close()

	response := writeInnerRequest(t, secured, &http.Request{
		Method: http.MethodPost,
		URL:    mustURL(t, "/v1/messages"),
		Host:   "api.anthropic.com:443",
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
	requests = fixture.exchanges.Requests()
	if response.StatusCode != http.StatusOK ||
		string(body) != `{"result":"proxied"}` ||
		len(requests) != 2 ||
		requests[1].RequestPlan().EnvironmentID() != next.ID ||
		requests[1].RequestPlan().EnvironmentRevision() != next.Revision {
		t.Fatalf(
			"post-switch request status=%d close=%t body=%s requests=%+v",
			response.StatusCode,
			response.Close,
			body,
			requests,
		)
	}
}

type proxyFixture struct {
	listener      net.Listener
	server        *http.Server
	handler       *loopbackproxy.Handler
	store         *runtimepersistence.Store
	runs          *capturerun.Manager
	manuals       *manualcapture.Manager
	grant         capturerun.LaunchGrant
	capture       captureidentity.Reference
	authority     *localca.Authority
	environments  *environment.AtomicProjection
	assignments   *captureassignment.Manager
	compiler      environment.Compiler
	clientDialect protocolspec.Dialect
	environment   environment.Environment
	exchanges     *exchangeRecorder
	original      *originalRecorder
	connections   *connectionevent.Manager
	egress        egressaudit.Repository
	approvals     *toolapproval.Authority
	rules         *connectionpolicy.Manager
}

func newProxyFixture(t *testing.T) *proxyFixture {
	t.Helper()
	return newProxyFixtureForDialect(
		t,
		protocolspec.DialectAnthropicMessages,
		nil,
	)
}

func newResponsesProxyFixture(t *testing.T) *proxyFixture {
	t.Helper()
	adapter := fixedCodexAdapterEvidence()
	return newProxyFixtureForDialect(
		t,
		protocolspec.DialectOpenAIResponses,
		&adapter,
	)
}

func newGenericResponsesProxyFixture(t *testing.T) *proxyFixture {
	t.Helper()
	return newProxyFixtureForDialect(
		t,
		protocolspec.DialectOpenAIResponses,
		nil,
	)
}

// newProxyFixtureWithPolicy exercises a specific rule set. The default fixture
// uses the same Monitor mode a fresh production installation uses.
func newProxyFixtureWithPolicy(
	t *testing.T,
	policy connectionpolicy.Snapshot,
) *proxyFixture {
	t.Helper()
	return newProxyFixtureForDialectWithPolicy(
		t,
		protocolspec.DialectAnthropicMessages,
		nil,
		policy,
	)
}

func allowEverythingTestPolicy(t *testing.T) connectionpolicy.Snapshot {
	t.Helper()

	set := connectionpolicy.Snapshot{
		Revision: 1,
		Mode:     connectionpolicy.ModeMonitor,
	}
	return set
}

func newProxyFixtureForDialect(
	t *testing.T,
	clientDialect protocolspec.Dialect,
	adapter *clientadapter.Evidence,
) *proxyFixture {
	t.Helper()
	return newProxyFixtureForDialectWithPolicy(
		t,
		clientDialect,
		adapter,
		allowEverythingTestPolicy(t),
	)
}

func newProxyFixtureForDialectWithPolicy(
	t *testing.T,
	clientDialect protocolspec.Dialect,
	adapter *clientadapter.Evidence,
	policy connectionpolicy.Snapshot,
) *proxyFixture {
	return newProxyFixtureForDialectWithPolicyAndRawEvidence(
		t,
		clientDialect,
		adapter,
		policy,
		nil,
	)
}

func newProxyFixtureWithRawEvidence(
	t *testing.T,
	raw rawevidence.RequestRecorder,
) *proxyFixture {
	t.Helper()
	return newProxyFixtureForDialectWithPolicyAndRawEvidence(
		t,
		protocolspec.DialectAnthropicMessages,
		nil,
		allowEverythingTestPolicy(t),
		raw,
	)
}

func newProxyFixtureForDialectWithPolicyAndRawEvidence(
	t *testing.T,
	clientDialect protocolspec.Dialect,
	adapter *clientadapter.Evidence,
	policy connectionpolicy.Snapshot,
	raw rawevidence.RequestRecorder,
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
	manualOptions := manualcapture.DefaultOptions(store.ManualCaptureRepository())
	manuals, err := manualcapture.NewManager(context.Background(), manualOptions)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := runs.Create(context.Background(), capturerun.CreateCommand{
		CWD:                     filepath.Join(directory, "workspace"),
		CanonicalExecutablePath: filepath.Join(directory, "bin", "claude"),
		ExecutableLabel:         "claude",
		Lifetime:                5 * time.Minute,
		CatalogRevision:         1,
		Adapter:                 adapter,
		Recognition:             fixtureRecognition(adapter),
		Workspace:               proxyWorkspaceScope(t),
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
	capture, err := captureidentity.New(
		captureidentity.KindManagedRun,
		grant.Run.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	compiler := testEnvironmentCompiler(t, clientDialect)
	aggregate := testEnvironmentForDialectRevision(
		t,
		clientDialect,
		"environment-proxy",
		1,
		1,
	)
	snapshot, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	projection := environment.NewAtomicProjection()
	if err := projection.Restore([]environment.EnvironmentSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	assignments, err := captureassignment.NewManager(captureassignment.Options{
		Repository:           store.CaptureAssignmentRepository(),
		Environments:         projection,
		Activity:             proxyCaptureActivity{},
		LeafCacheInvalidator: authority,
		Clock:                captureassignment.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assignments.Create(
		context.Background(),
		captureassignment.CreateCommand{
			Capture:       capture,
			EnvironmentID: snapshot.ID(),
			Source:        captureassignment.SourceLaunch,
		},
	); err != nil {
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
	// The proxy reads the same durable rules the product does, so a remembered
	// answer in a test travels the path it travels in the product.
	rules, err := connectionpolicy.NewManager(
		context.Background(),
		connectionpolicy.ManagerOptions{
			Repository: store.ConnectionRuleRepository(),
			Clock:      toolapproval.SystemClock{},
			Shipped:    policy,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := toolapproval.New(
		context.Background(),
		toolapproval.Options{
			Repository: store.ToolApprovalRepository(),
			Clock:      toolapproval.SystemClock{},
			Random:     rand.Reader,
			Config:     toolapproval.DefaultConfig(),
			Remembered: rules,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	admissions, err := captureadmission.NewAuthorizer(runs, manuals)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := loopbackproxy.New(loopbackproxy.Options{
		OwnerContext:     context.Background(),
		Admissions:       admissions,
		Assignments:      assignments,
		Exchanges:        exchanges,
		Original:         original,
		Certificates:     authority,
		Connections:      connections,
		Policy:           rules.Source(),
		Approvals:        approvals,
		BlindTunnels:     newTestBlindTunnels(t),
		EgressAudit:      store.EgressAttemptRepository(),
		RawEvidence:      raw,
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
		listener: listener, server: server, handler: handler, store: store,
		runs: runs, manuals: manuals, grant: grant, capture: capture,
		authority: authority, environments: projection, assignments: assignments,
		compiler: compiler, clientDialect: clientDialect, environment: aggregate,
		exchanges: exchanges, original: original,
		connections: connections, egress: store.EgressAttemptRepository(),
		approvals: approvals, rules: rules,
	}
}

type proxyCaptureActivity struct{}

func (proxyCaptureActivity) Active(
	context.Context,
	captureidentity.Reference,
) (bool, error) {
	return true, nil
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
	if err := fixture.assignments.Shutdown(ctx); err != nil {
		t.Errorf("shutdown Capture assignment manager: %v", err)
	}
	if err := fixture.runs.Shutdown(ctx); err != nil {
		t.Errorf("shutdown CaptureRun manager: %v", err)
	}
	if err := fixture.manuals.Shutdown(ctx); err != nil {
		t.Errorf("shutdown ManualCapture manager: %v", err)
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
	mu           sync.Mutex
	requests     []exchange.ClientRequest
	failure      error
	blockStarted chan struct{}
	blockRelease chan struct{}
	startOnce    sync.Once
}

func (recorder *exchangeRecorder) Execute(
	ctx context.Context,
	request exchange.ClientRequest,
	downstream exchange.Downstream,
) (exchange.Result, error) {
	recorder.mu.Lock()
	recorder.requests = append(recorder.requests, request)
	failure := recorder.failure
	blockStarted := recorder.blockStarted
	blockRelease := recorder.blockRelease
	recorder.mu.Unlock()
	if failure != nil {
		return exchange.Result{}, failure
	}
	if blockRelease != nil {
		recorder.startOnce.Do(func() { close(blockStarted) })
		select {
		case <-blockRelease:
		case <-ctx.Done():
			return exchange.Result{}, context.Cause(ctx)
		}
	}
	envelope, err := exchange.NewResponseEnvelope(
		exchange.ResponseModeJSON,
		http.StatusOK,
		http.Header{"Content-Type": []string{"application/json"}},
	)
	if err != nil {
		return exchange.Result{}, err
	}
	if err := downstream.Begin(ctx, envelope); err != nil {
		return exchange.Result{}, err
	}
	if _, err := downstream.Write(ctx, []byte(`{"result":"proxied"}`)); err != nil {
		return exchange.Result{}, err
	}
	return exchange.Result{
		Outcome:   exchange.AttemptSucceeded,
		RouteHost: "relay.example.test",
		AccountID: "account-proxy",
	}, nil
}

func (recorder *exchangeRecorder) Block() (<-chan struct{}, func()) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.blockRelease != nil {
		panic("exchange recorder is already blocked")
	}
	started := make(chan struct{})
	release := make(chan struct{})
	recorder.blockStarted = started
	recorder.blockRelease = release
	return started, func() { close(release) }
}

func (recorder *exchangeRecorder) FailWith(err error) {
	recorder.mu.Lock()
	recorder.failure = err
	recorder.mu.Unlock()
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

func testEnvironmentCompiler(
	t *testing.T,
	clientDialect protocolspec.Dialect,
) environment.Compiler {
	t.Helper()
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	codecID, err := protocolspec.NewCodecPairID(
		"test.loopback." + string(clientDialect),
	)
	if err != nil {
		t.Fatal(err)
	}
	protocols, err := protocolspec.NewCatalog(
		operations.Definitions(),
		[]protocolspec.CodecPairDefinition{{
			ID: codecID, Revision: 1,
			ClientDialect:      clientDialect,
			ProviderDialect:    clientDialect,
			ClientOperationIDs: operations.SemanticOperationIDs(clientDialect),
			RequiredCapabilities: []protocolspec.ProviderCapability{
				protocolspec.ProviderCapabilityMessages,
				protocolspec.ProviderCapabilityStreaming,
				protocolspec.ProviderCapabilityToolCalls,
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	wires, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := environment.NewCompiler(nil, nil, protocols, wires)
	if err != nil {
		t.Fatal(err)
	}
	return compiler
}

func testEnvironmentForDialectRevision(
	t *testing.T,
	clientDialect protocolspec.Dialect,
	environmentID string,
	revision environment.Revision,
	adapterRevision environment.Revision,
) environment.Environment {
	t.Helper()
	clientOriginValue := "https://api.anthropic.com:443"
	clientProtocol := environment.ClientProtocolAnthropicMessages
	if clientDialect == protocolspec.DialectOpenAIResponses {
		clientOriginValue = "https://api.openai.com:443"
		clientProtocol = environment.ClientProtocolOpenAIResponses
	}
	clientOrigin, err := originidentity.ParseClientOrigin(clientOriginValue)
	if err != nil {
		t.Fatal(err)
	}
	providerOrigin, err := originidentity.ParseProviderOrigin(clientOriginValue)
	if err != nil {
		t.Fatal(err)
	}
	return environment.Environment{
		ID: environment.EnvironmentID(environmentID), Name: "Proxy Environment",
		State: environment.StateActive, Revision: revision,
		ContentRecording: environment.DefaultContentRecordingPolicy(),
		ClientEndpoints: []environment.ClientEndpoint{{
			ID: "endpoint-proxy", Revision: 1, ClientOrigin: clientOrigin,
			ProtocolPlans: []environment.ClientProtocolPlan{{
				ID: "plan-proxy", Revision: 1, ClientProtocol: clientProtocol,
				ClientAdapterPolicy: environment.ClientAdapterPolicy{
					ID: "adapter-proxy", Revision: adapterRevision,
				},
				Mode: environment.PlanModeManaged,
				UpstreamPlan: environment.UpstreamPlan{
					DefaultRouteID: "route-proxy",
					RouteSet: environment.RouteSet{
						ID: "routes-proxy", Revision: revision,
						CandidateRouteIDs: []environment.UpstreamRouteID{"route-proxy"},
					},
					Routes: []environment.UpstreamRoute{{
						ID: "route-proxy", Revision: revision,
						ProviderTarget: environment.ProviderTarget{
							ID: "target-proxy", Revision: revision,
							Origin: providerOrigin, RealmID: "realm-proxy",
							Capabilities: []protocolspec.ProviderCapability{
								protocolspec.ProviderCapabilityMessages,
								protocolspec.ProviderCapabilityStreaming,
								protocolspec.ProviderCapabilityToolCalls,
							},
						},
						BackendProtocol: string(clientDialect),
						AccountPolicy: environment.RouteAccountPolicy{
							Revision:       revision,
							Mode:           environment.AccountModeClientPassthrough,
							FailoverPolicy: environment.FailoverOff,
						},
						ModelPolicy:    environment.ModelPolicy{Revision: revision, Mode: "passthrough"},
						WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue,
					}},
				},
			}},
		}},
	}
}

func (fixture *proxyFixture) publishEnvironment(
	t *testing.T,
	aggregate environment.Environment,
) environment.EnvironmentSnapshot {
	t.Helper()
	snapshot, err := fixture.compiler.Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.environments.Publish(snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func (fixture *proxyFixture) switchEnvironment(
	t *testing.T,
	target environment.EnvironmentID,
) captureassignment.SwitchResult {
	t.Helper()
	current, err := fixture.assignments.Resolve(context.Background(), fixture.capture)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.assignments.Switch(
		context.Background(),
		captureassignment.SwitchCommand{
			Capture: fixture.capture, ExpectedRevision: current.Revision,
			TargetEnvironmentID: target,
			Source:              captureassignment.SourceOperatorSwitch,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return result
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

// fixtureRecognition keeps the fixture honest: a run carries verified
// recognition exactly when it carries evidence.
func fixtureRecognition(
	adapter *clientadapter.Evidence,
) clientadapter.Recognition {
	if adapter == nil {
		return clientadapter.RecognitionUnknown
	}
	return clientadapter.RecognitionVerified
}

func proxyWorkspaceScope(t *testing.T) workspaceidentity.Scope {
	t.Helper()
	machineID, err := workspaceidentity.ParseMachineID(
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32)),
	)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID, err := workspaceidentity.ParseWorkspaceID(
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x62}, 32)),
	)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := workspaceidentity.NewScope(
		machineID,
		workspaceID,
		"workspace",
		workspaceidentity.EvidenceLocalLauncher,
		1,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}
