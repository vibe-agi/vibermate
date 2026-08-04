package providertransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
)

func TestStrictTransportPreservesHTTP2AcrossTheProviderBoundary(t *testing.T) {
	t.Parallel()

	const firstChunk = `{"chunk":1}`
	const secondChunk = `{"chunk":2}`
	firstFlushed := make(chan struct{})
	releaseSecond := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseSecond) }) })
	observed := make(chan struct {
		protocol  int
		host      string
		userAgent string
	}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		observed <- struct {
			protocol  int
			host      string
			userAgent string
		}{
			protocol:  request.ProtoMajor,
			host:      request.Host,
			userAgent: request.Header.Get("User-Agent"),
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(firstChunk))
		writer.(http.Flusher).Flush()
		close(firstFlushed)
		<-releaseSecond
		_, _ = writer.Write([]byte(secondChunk))
	}))
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{string(access.ApplicationProtocolHTTP2)},
	}
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
		&secretReaderStub{value: []byte("h2-token")},
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

	observation := captureHTTP2ClientHello(t)
	observation, err = observation.WithDownstreamNegotiatedALPN(
		string(access.ApplicationProtocolHTTP2),
	)
	if err != nil {
		t.Fatal(err)
	}
	plan := testRequestAccessPlan(t)
	secretRef, err := access.NewSecretRef("secret://provider/account")
	if err != nil {
		t.Fatal(err)
	}
	action, err := gate.BeginAction(
		context.Background(),
		offlinehold.ActionRequest{ActionID: "strict-h2"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(action.Release)
	port := listenerPort(t, server.Listener.Addr())
	request, err := NewRequest(RequestOptions{
		RequestID:       "strict-h2",
		ExchangeID:      "exchange-strict-h2",
		ParentAttemptID: "attempt-strict-h2",
		EgressAttemptID: "egress-strict-h2",
		TargetRef:       "target-strict-h2",
		Target:          testTarget("example.com", port),
		AccessRevision:  plan.Revision(),
		PlanHash:        plan.PlanHash(),
		Action:          action,
		Method:          http.MethodPost,
		RelativePath:    "chat/completions",
		Headers:         http.Header{},
		Body:            []byte(`{"input":"hello"}`),
		SecretRef:       secretRef,
		AuthDriverRef:   access.StaticHeaderAuthDriverRef(),
		WireProfile:     plan.UpstreamWireProfile(),
		ClientProtocol:  access.ApplicationProtocolHTTP2,
		ClientUserAgent: "h2-client/1.0",
		ClientHello:     observation,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, evidence, err := client.Do(context.Background(), request)
	if err != nil {
		t.Fatalf("send HTTP/2 provider request: %v", err)
	}
	select {
	case <-firstFlushed:
	case <-time.After(time.Second):
		t.Fatal("provider did not flush the first HTTP/2 response chunk")
	}
	first := make([]byte, len(firstChunk))
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatalf("read first HTTP/2 provider response chunk: %v", err)
	}
	if string(first) != firstChunk {
		t.Fatalf("first HTTP/2 response chunk = %q", first)
	}
	releaseOnce.Do(func() { close(releaseSecond) })
	rest, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read remaining HTTP/2 provider response: %v", err)
	}
	if string(rest) != secondChunk {
		t.Fatalf("remaining HTTP/2 response = %q", rest)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close HTTP/2 provider response: %v", err)
	}
	wantAuthority := net.JoinHostPort("example.com", strconv.Itoa(port))
	select {
	case got := <-observed:
		if got.protocol != 2 || got.host != wantAuthority ||
			got.userAgent != "h2-client/1.0" {
			t.Fatalf("provider observed = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("provider did not observe the HTTP/2 request")
	}
	if evidence.Presentation.ClientProtocol != access.ApplicationProtocolHTTP2 ||
		evidence.Presentation.UpstreamProtocol != access.ApplicationProtocolHTTP2 ||
		evidence.Transport.HTTPTransport() != access.HTTPTransportHTTP2 ||
		evidence.Transport.DownstreamNegotiatedALPN() != "h2" ||
		!slices.Equal(
			evidence.Transport.ClientOfferedALPN(),
			[]string{"h2", "http/1.1"},
		) ||
		!slices.Equal(
			evidence.Transport.UpstreamOfferedALPN(),
			[]string{"h2"},
		) ||
		evidence.Transport.UpstreamNegotiatedALPN() != "h2" {
		t.Fatalf("HTTP/2 evidence = %+v", evidence)
	}
}

func captureHTTP2ClientHello(t *testing.T) transportprofile.Observation {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	clientDone := make(chan error, 1)
	go func() {
		secured := tls.Client(clientSide, &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: "client.example",
			NextProtos: []string{
				string(access.ApplicationProtocolHTTP2),
				string(access.ApplicationProtocolHTTP1),
			},
		})
		clientDone <- secured.Handshake()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observation, replay, err := transportprofile.CaptureClientHello(
		ctx,
		serverSide,
		transportprofile.DefaultMaxClientHelloBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = replay.Close()
	_ = clientSide.Close()
	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("test HTTP/2 ClientHello did not stop")
	}
	return observation
}
