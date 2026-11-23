package providertransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/productbuild"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

type observedViberMateRequest struct {
	userAgent     string
	authorization string
	protocol      int
}

func TestClientStrictTLSPresentsViberMateOnBothProtocols(t *testing.T) {
	t.Parallel()
	wantUserAgent := "ViberMate/" + productbuild.Label()
	for _, test := range []struct {
		protocol      wireprofile.ApplicationProtocol
		transportRef  string
		httpTransport wireprofile.HTTPTransportKind
		major         int
	}{
		{wireprofile.ApplicationProtocolHTTP1, wireprofile.TransportProfileStandardH1Value, wireprofile.HTTPTransportHTTP1, 1},
		{wireprofile.ApplicationProtocolHTTP2, wireprofile.TransportProfileStandardH2Value, wireprofile.HTTPTransportHTTP2, 2},
	} {
		t.Run(string(test.protocol), func(t *testing.T) {
			t.Parallel()
			observed := make(chan observedViberMateRequest, 1)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				request *http.Request,
			) {
				observed <- observedViberMateRequest{
					userAgent:     request.Header.Get("User-Agent"),
					authorization: request.Header.Get("Authorization"),
					protocol:      request.ProtoMajor,
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"ok":true}`))
			}))
			// The server offers both protocols, so the negotiated one is the
			// variant's own choice.
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
				testSecretReader(t, "openai-key"),
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
			plan := testRequestPlanWithWireProfile(
				t,
				"https://"+net.JoinHostPort("api.example.com", strconv.Itoa(port))+"/v1",
				wireprofile.ViberMateUpstreamWireProfileRef(),
			)
			// The helper's client sends "test-client/1.0"; it must not reach
			// the provider.
			request := newTestRequestWithPlanAndClientProtocol(
				t,
				gate,
				"vibermate-"+string(test.protocol),
				testTarget("api.example.com", port),
				nil,
				[]byte(`{"model":"provider-model"}`),
				plan,
				test.protocol,
			)
			response, evidence, err := client.Do(context.Background(), request)
			if err != nil {
				t.Fatalf("Do() error = %v", err)
			}
			if _, err := io.ReadAll(response.Body); err != nil {
				t.Fatal(err)
			}
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			got := <-observed
			if got.userAgent != wantUserAgent ||
				got.authorization != "Bearer openai-key" ||
				got.protocol != test.major {
				t.Fatalf("observed request = %+v, want User-Agent %q over HTTP/%d", got, wantUserAgent, test.major)
			}
			presentation := evidence.Presentation
			if presentation.RequestedRef != "vibermate" ||
				presentation.EffectiveRef != "vibermate" ||
				presentation.Revision != 1 ||
				presentation.Mode != wireprofile.UpstreamWireModeOwnIdentity ||
				presentation.Product != "" ||
				presentation.ClientProtocol != test.protocol ||
				presentation.UpstreamProtocol != test.protocol ||
				presentation.EvidenceDigest != "" ||
				presentation.ProtocolAdjustment != "" {
				t.Fatalf("wire presentation evidence = %+v", presentation)
			}
			standard := transportprofile.ProfileEvidence{
				Ref: test.transportRef, Revision: 1, Source: wireprofile.TransportFingerprintStandard,
			}
			chain := evidence.Transport.FallbackChain()
			if evidence.Transport.Requested() != standard ||
				evidence.Transport.Effective() != standard ||
				len(chain) != 1 || chain[0] != standard ||
				evidence.Transport.UsedFallback() ||
				evidence.Transport.HTTPTransport() != test.httpTransport ||
				evidence.Transport.UpstreamNegotiatedALPN() != string(test.protocol) {
				t.Fatalf("transport evidence = %+v", evidence.Transport)
			}
		})
	}
}

func TestClientCleartextPresentsViberMateOverHTTP1(t *testing.T) {
	t.Parallel()
	observed := make(chan observedViberMateRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		observed <- observedViberMateRequest{
			userAgent:     request.Header.Get("User-Agent"),
			authorization: request.Header.Get("Authorization"),
			protocol:      request.ProtoMajor,
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"ok":true}`)
	}))
	defer server.Close()

	origin, err := originidentity.ParseProviderOrigin(server.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	gate := newStartedGate(t)
	authenticator, err := NewStaticBearerAuthenticator(
		testSecretReader(t, "local-key"),
	)
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

	request := newTestRequestWithPlanAndClientProtocol(
		t,
		gate,
		"vibermate-cleartext",
		targetFromProviderOrigin(origin),
		nil,
		[]byte(`{"model":"provider-model"}`),
		testRequestPlanWithWireProfile(t, server.URL+"/v1", wireprofile.ViberMateUpstreamWireProfileRef()),
		wireprofile.ApplicationProtocolHTTP1,
	)
	response, evidence, err := client.Do(context.Background(), request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	request.action.Release()
	select {
	case got := <-observed:
		if got.userAgent != "ViberMate/"+productbuild.Label() ||
			got.authorization != "Bearer local-key" ||
			got.protocol != 1 {
			t.Fatalf("cleartext request = %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleartext provider did not receive the request")
	}
	if evidence.Presentation.Mode != wireprofile.UpstreamWireModeOwnIdentity ||
		evidence.Presentation.UpstreamProtocol != wireprofile.ApplicationProtocolHTTP1 ||
		evidence.Presentation.ProtocolAdjustment != "" ||
		evidence.Transport.Requested().Ref != "" {
		t.Fatalf("cleartext evidence = %+v", evidence)
	}
}

// An own-identity profile does not take the follow-client lowering of an
// HTTP/2 client to HTTP/1.1 on a cleartext provider; the request is refused
// before any secret read or dial.
func TestNewRequestRefusesViberMateHTTP2OnCleartext(t *testing.T) {
	t.Parallel()
	catalog, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := catalog.Resolve(wireprofile.ViberMateUpstreamWireProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	origin, err := originidentity.ParseProviderOrigin("http://127.0.0.1:23333/v1")
	if err != nil {
		t.Fatal(err)
	}
	options := validRequestOptions(t)
	options.Target = targetFromProviderOrigin(origin)
	options.WireProfile = profile
	options.ClientProtocol = wireprofile.ApplicationProtocolHTTP1
	if _, err := NewRequest(options); err != nil {
		t.Fatalf("HTTP/1.1 client on cleartext: %v", err)
	}
	options.ClientProtocol = wireprofile.ApplicationProtocolHTTP2
	if _, err := NewRequest(options); err == nil ||
		!strings.Contains(err.Error(), "cleartext provider does not support HTTP/2") {
		t.Fatalf("HTTP/2 client on cleartext = %v, want the HTTP/2 refusal", err)
	}
}

func TestClientRejectsCodecUserAgentUnderViberMateProfile(t *testing.T) {
	t.Parallel()
	for _, headers := range []http.Header{
		{"User-Agent": []string{"codec-owned"}},
		{"user-agent": []string{"codec-owned"}},
		{"User-Agent": []string{"ViberMate/" + productbuild.Label()}},
	} {
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
		request := newTestRequestWithPlan(
			t,
			gate,
			"vibermate-codec-user-agent",
			testTarget("provider.example", 443),
			headers,
			[]byte(`{"model":"provider-model"}`),
			testRequestPlanWithWireProfile(
				t,
				"https://provider.example:443/v1",
				wireprofile.ViberMateUpstreamWireProfileRef(),
			),
		)
		response, _, err := client.Do(context.Background(), request)
		if err == nil || response != nil {
			t.Fatalf("codec User-Agent %v: response=%v error=%v", headers, response, err)
		}
		if secrets.readCount() != 0 || transport.callCount() != 0 {
			t.Fatalf(
				"codec User-Agent %v touched secret=%d transport=%d",
				headers,
				secrets.readCount(),
				transport.callCount(),
			)
		}
		shutdownClient(t, client)
	}
}
