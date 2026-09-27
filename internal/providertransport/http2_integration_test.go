package providertransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestResponseHeadTimeoutStartsAfterUpload(t *testing.T) {
	for _, protocol := range []wireprofile.ApplicationProtocol{wireprofile.ApplicationProtocolHTTP1, wireprofile.ApplicationProtocolHTTP2} {
		for _, scenario := range []string{"slow upload", "stalled head", "early head", "canceled upload"} {
			t.Run(string(protocol)+"/"+scenario, func(t *testing.T) {
				t.Parallel()
				const budget = 60 * time.Millisecond
				release := make(chan struct{})
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if scenario != "early head" {
						_, _ = io.Copy(io.Discard, r.Body)
					}
					if scenario == "stalled head" {
						<-release
						return
					}
					_, _ = io.WriteString(w, "ok")
					w.(http.Flusher).Flush()
				}))
				server.EnableHTTP2 = protocol == wireprofile.ApplicationProtocolHTTP2
				server.StartTLS()
				defer server.Close()
				defer close(release)
				roots := x509.NewCertPool()
				roots.AddCert(server.Certificate())
				connector, err := transportprofile.NewConnector(transportprofile.ConnectorOptions{
					Dialer: &mappingDialer{address: server.Listener.Addr().String()}, RootCAs: roots, HandshakeTimeout: time.Second,
				})
				if err != nil {
					t.Fatal(err)
				}
				timeouts := DefaultTransportTimeouts()
				timeouts.ResponseHead = budget
				transport := &profileTransport{timeouts: timeouts}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				reader, writer := io.Pipe()
				defer reader.Close()
				defer writer.Close()
				wroteHeaders := make(chan struct{}, 1)
				var wroteRequest atomic.Bool
				ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
					WroteHeaders: func() {
						select {
						case wroteHeaders <- struct{}{}:
						default:
						}
					},
					WroteRequest: func(info httptrace.WroteRequestInfo) {
						if info.Err == nil {
							wroteRequest.Store(true)
						}
					},
				})
				go func() {
					select {
					case <-ctx.Done():
						_ = writer.CloseWithError(ctx.Err())
						return
					case <-wroteHeaders:
					}
					if scenario == "canceled upload" {
						cancel()
						_ = writer.CloseWithError(context.Canceled)
						return
					}
					if scenario != "stalled head" {
						select {
						case <-ctx.Done():
							_ = writer.CloseWithError(ctx.Err())
							return
						case <-time.After(4 * budget):
						}
					}
					_, _ = io.WriteString(writer, "fixture")
					_ = writer.Close()
				}()
				target := testTarget("example.com", listenerPort(t, server.Listener.Addr()))
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+target.HTTPAuthority()+"/responses", reader)
				if err != nil {
					t.Fatal(err)
				}
				plan := testRequestPlan(t)
				variant, _ := plan.wireProfile.Variant(protocol)
				observation := captureTransportClientHello(t, []string{string(protocol)})
				observation, err = observation.WithDownstreamNegotiatedALPN(string(protocol))
				if err != nil {
					t.Fatal(err)
				}
				dispatch := TransportDispatch{target: target, plan: variant.TransportFingerprintPlan(), clientHello: observation}
				var response *http.Response
				if protocol == wireprofile.ApplicationProtocolHTTP2 {
					response, _, err = transport.roundTripHTTP2(request, dispatch, connector)
				} else {
					response, _, err = transport.roundTripHTTP1(request, dispatch, connector)
				}
				if response != nil {
					defer response.Body.Close()
				}
				switch scenario {
				case "stalled head":
					if !wroteRequest.Load() || err == nil {
						t.Fatalf("head timeout missing: %v", err)
					}
					if protocol == wireprofile.ApplicationProtocolHTTP2 && !errors.Is(err, ErrProviderResponseHeadTimeout) {
						t.Fatalf("lost head timeout cause: %v", err)
					}
				case "canceled upload":
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("lost upload cancellation: %v", err)
					}
				default:
					if err != nil {
						t.Fatalf("upload consumed the head budget: %v", err)
					}
					body, err := io.ReadAll(response.Body)
					if err != nil || string(body) != "ok" {
						t.Fatalf("response = %q, %v", body, err)
					}
				}
			})
		}
	}
}

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
