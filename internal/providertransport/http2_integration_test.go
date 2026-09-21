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

	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestStrictTransportPreservesHTTP2AcrossTheProviderBoundary(t *testing.T) {
	t.Parallel()
	testStrictTransportStreaming(t, wireprofile.ApplicationProtocolHTTP2, []string{"h2", "http/1.1"})
}

func TestStrictTransportStreamsHTTP1WithoutALPN(t *testing.T) {
	t.Parallel()
	testStrictTransportStreaming(t, wireprofile.ApplicationProtocolHTTP1, nil)
}

func TestStrictTransportStreamsHTTP1WithALPN(t *testing.T) {
	t.Parallel()
	testStrictTransportStreaming(t, wireprofile.ApplicationProtocolHTTP1, []string{"http/1.1"})
}

func testStrictTransportStreaming(t *testing.T, protocol wireprofile.ApplicationProtocol, clientALPN []string) {
	t.Helper()
	wantMajor := 1
	wantTransport := wireprofile.HTTPTransportHTTP1
	if protocol == wireprofile.ApplicationProtocolHTTP2 {
		wantMajor = 2
		wantTransport = wireprofile.HTTPTransportHTTP2
	}
	var upstreamALPN []string
	var negotiatedALPN string
	if len(clientALPN) != 0 {
		upstreamALPN = []string{string(protocol)}
		negotiatedALPN = string(protocol)
	}

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
	server.EnableHTTP2 = protocol == wireprofile.ApplicationProtocolHTTP2
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{string(protocol)},
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
		testSecretReader(t, "h2-token"),
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

	observation := captureTransportClientHello(t, clientALPN)
	observation, err = observation.WithDownstreamNegotiatedALPN(
		negotiatedALPN,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan := testRequestPlan(t)
	secretRef, err := secretstore.ParseReference("secret://provider/account")
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
		Provenance:      plan.provenance,
		Action:          action,
		Method:          http.MethodPost,
		RelativePath:    "chat/completions",
		Headers:         http.Header{},
		Body:            []byte(`{"input":"hello"}`),
		CredentialMode:  providerauth.CredentialManaged,
		AccountRef:      testAccountRef(),
		SecretRef:       secretRef,
		AuthDriverRef:   providerauth.StaticHeaderDriverRef(),
		WireProfile:     plan.wireProfile,
		ClientProtocol:  protocol,
		ClientUserAgent: "h2-client/1.0",
		ClientHello:     observation,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, evidence, err := client.Do(context.Background(), request)
	if err != nil {
		t.Fatalf("send %s provider request: %v", protocol, err)
	}
	select {
	case <-firstFlushed:
	case <-time.After(time.Second):
		t.Fatal("provider did not flush the first response chunk")
	}
	first := make([]byte, len(firstChunk))
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatalf("read first provider response chunk: %v", err)
	}
	if string(first) != firstChunk {
		t.Fatalf("first response chunk = %q", first)
	}
	releaseOnce.Do(func() { close(releaseSecond) })
	rest, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read remaining provider response: %v", err)
	}
	if string(rest) != secondChunk {
		t.Fatalf("remaining response = %q", rest)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close provider response: %v", err)
	}
	wantAuthority := net.JoinHostPort("example.com", strconv.Itoa(port))
	select {
	case got := <-observed:
		if got.protocol != wantMajor || got.host != wantAuthority ||
			got.userAgent != "h2-client/1.0" {
			t.Fatalf("provider observed = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("provider did not observe the request")
	}
	if evidence.Presentation.ClientProtocol != protocol ||
		evidence.Presentation.UpstreamProtocol != protocol ||
		evidence.Transport.HTTPTransport() != wantTransport ||
		evidence.Transport.DownstreamNegotiatedALPN() != negotiatedALPN ||
		!slices.Equal(
			evidence.Transport.ClientOfferedALPN(),
			clientALPN,
		) ||
		!slices.Equal(
			evidence.Transport.UpstreamOfferedALPN(),
			upstreamALPN,
		) ||
		evidence.Transport.UpstreamNegotiatedALPN() != negotiatedALPN ||
		evidence.Transport.Effective().Ref == "" ||
		evidence.Transport.UsedFallback() {
		t.Fatalf("transport evidence = %+v", evidence)
	}
}

func captureTransportClientHello(t *testing.T, alpn []string) transportprofile.Observation {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	clientDone := make(chan error, 1)
	go func() {
		secured := tls.Client(clientSide, &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: "client.example",
			NextProtos: alpn,
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
		t.Fatal("test ClientHello did not stop")
	}
	return observation
}
