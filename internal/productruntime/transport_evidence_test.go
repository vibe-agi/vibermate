package productruntime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestActualCleartextRequestPresentationIsAcceptedByActivity(t *testing.T) {
	ctx := context.Background()
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err = gate.Start(ctx, offlinehold.RuntimeBinding{InstanceID: "presentation-fixture"}); err != nil {
		t.Fatal(err)
	}
	action, err := gate.BeginAction(ctx, offlinehold.ActionRequest{ActionID: "presentation-action"})
	if err != nil {
		t.Fatal(err)
	}
	defer action.Release()
	origin, err := originidentity.ParseProviderOrigin("http://127.0.0.1:12345")
	if err != nil {
		t.Fatal(err)
	}
	target, err := providertransport.NewTarget(origin)
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := providertransport.NewOriginalRequestProvenance("presentation.fixture", 1, environment.CandidateDigest{1})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := catalog.Resolve(wireprofile.FollowClientUpstreamWireProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionFlow, 0)
	if err != nil {
		t.Fatal(err)
	}
	request, err := providertransport.NewRequest(providertransport.RequestOptions{
		RequestID: "presentation-request", TargetRef: "presentation-target", Target: target, Provenance: provenance, Action: action,
		Method: http.MethodPost, RelativePath: "v1/responses", Body: []byte(`{"model":"synthetic"}`), CredentialMode: providerauth.CredentialClientPassthrough, PassthroughOrigin: origin,
		WireProfile: profile, EgressProfile: egressprofile.Direct(), ResolvedEgress: resolved, ClientProtocol: wireprofile.ApplicationProtocolHTTP2, ExchangeID: "presentation-exchange", ParentAttemptID: "presentation-parent", EgressAttemptID: "presentation-egress",
	})
	if err != nil {
		t.Fatal(err)
	}
	presentation := request.WirePresentationEvidence()
	if presentation.ClientProtocol != wireprofile.ApplicationProtocolHTTP2 || presentation.UpstreamProtocol != wireprofile.ApplicationProtocolHTTP1 {
		t.Fatal("real Request producer did not lower cleartext protocol")
	}
	converted := activityTransportEvidence(presentation, transportprofile.Evidence{})
	if converted == nil {
		t.Fatal("real presentation disappeared")
	}
	if err = converted.Validate(); err != nil {
		t.Fatalf("real cleartext producer cannot enter Activity: %v", err)
	}
}

// The vibermate profile's real presentation and transport evidence must enter
// Activity; an Event that fails validation is dropped as invalid, so an
// unrecognised mode would silently erase every translated Exchange.
func TestViberMatePresentationIsAcceptedByActivity(t *testing.T) {
	t.Parallel()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := catalog.Resolve(wireprofile.ViberMateUpstreamWireProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []wireprofile.ApplicationProtocol{wireprofile.ApplicationProtocolHTTP1, wireprofile.ApplicationProtocolHTTP2} {
		t.Run(string(protocol), func(t *testing.T) {
			t.Parallel()
			request := presentationFixtureRequest(t, "https://example.com:"+port, profile, protocol)
			presentation := request.WirePresentationEvidence()
			if presentation.Mode != wireprofile.UpstreamWireModeOwnIdentity || presentation.UpstreamProtocol != protocol {
				t.Fatalf("vibermate presentation = %+v", presentation)
			}
			variant, ok := profile.Variant(protocol)
			if !ok {
				t.Fatalf("vibermate has no %q variant", protocol)
			}
			connector, err := transportprofile.NewConnector(transportprofile.ConnectorOptions{
				Dialer: &net.Dialer{}, RootCAs: roots,
			})
			if err != nil {
				t.Fatal(err)
			}
			connection, evidence, err := connector.Connect(context.Background(), transportprofile.ConnectRequest{
				Network: "tcp", Address: server.Listener.Addr().String(), TLSServerName: "example.com",
				Plan: variant.TransportFingerprintPlan(),
			})
			if err != nil {
				t.Fatalf("standard %q connect: %v", protocol, err)
			}
			_ = connection.Close()
			converted := activityTransportEvidence(presentation, evidence)
			if converted == nil || converted.Requested == nil || converted.Requested.Source != "standard" {
				t.Fatalf("vibermate Activity evidence = %+v", converted)
			}
			if err := converted.Validate(); err != nil {
				t.Fatalf("vibermate %q evidence cannot enter Activity: %v", protocol, err)
			}
		})
	}
	t.Run("cleartext", func(t *testing.T) {
		t.Parallel()
		request := presentationFixtureRequest(t, "http://127.0.0.1:12345", profile, wireprofile.ApplicationProtocolHTTP1)
		converted := activityTransportEvidence(request.WirePresentationEvidence(), transportprofile.Evidence{})
		if converted == nil || converted.Requested != nil {
			t.Fatalf("vibermate cleartext Activity evidence = %+v", converted)
		}
		if err := converted.Validate(); err != nil {
			t.Fatalf("vibermate cleartext evidence cannot enter Activity: %v", err)
		}
	})
}

func presentationFixtureRequest(
	t *testing.T,
	rawOrigin string,
	profile wireprofile.CompiledUpstreamWireProfile,
	protocol wireprofile.ApplicationProtocol,
) providertransport.Request {
	t.Helper()
	ctx := context.Background()
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err = gate.Start(ctx, offlinehold.RuntimeBinding{InstanceID: "presentation-fixture"}); err != nil {
		t.Fatal(err)
	}
	action, err := gate.BeginAction(ctx, offlinehold.ActionRequest{ActionID: "presentation-action-" + strconv.Quote(rawOrigin)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(action.Release)
	origin, err := originidentity.ParseProviderOrigin(rawOrigin)
	if err != nil {
		t.Fatal(err)
	}
	target, err := providertransport.NewTarget(origin)
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := providertransport.NewOriginalRequestProvenance("presentation.fixture", 1, environment.CandidateDigest{1})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionFlow, 0)
	if err != nil {
		t.Fatal(err)
	}
	request, err := providertransport.NewRequest(providertransport.RequestOptions{
		RequestID: "presentation-request", TargetRef: "presentation-target", Target: target, Provenance: provenance, Action: action,
		Method: http.MethodPost, RelativePath: "v1/responses", Body: []byte(`{"model":"synthetic"}`), CredentialMode: providerauth.CredentialClientPassthrough, PassthroughOrigin: origin,
		WireProfile: profile, EgressProfile: egressprofile.Direct(), ResolvedEgress: resolved, ClientProtocol: protocol, ExchangeID: "presentation-exchange", ParentAttemptID: "presentation-parent", EgressAttemptID: "presentation-egress",
	})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestActivityTransportEvidenceRetainsFailedProfileReason(t *testing.T) {
	t.Parallel()
	catalog, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := catalog.Resolve(wireprofile.FollowClientUpstreamWireProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	variant, ok := profile.Variant(wireprofile.ApplicationProtocolHTTP1)
	if !ok {
		t.Fatal("follow-client has no HTTP/1.1 variant")
	}
	connector, err := transportprofile.NewConnector(transportprofile.ConnectorOptions{
		Dialer: transportEvidenceNoDialer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, evidence, err := connector.Connect(context.Background(), transportprofile.ConnectRequest{
		Network: "tcp", Address: "example.com:443", TLSServerName: "example.com",
		Plan: variant.TransportFingerprintPlan(),
	})
	if connection != nil || !errors.Is(err, transportprofile.ErrClientHelloUnavailable) {
		t.Fatalf("missing observation connection=%v error=%v", connection, err)
	}
	converted := activityTransportEvidence(providertransport.WirePresentationEvidence{
		RequestedRef: profile.Ref().String(), EffectiveRef: profile.Ref().String(),
		Revision: profile.Revision(), Mode: profile.Mode(),
		ClientProtocol: wireprofile.ApplicationProtocolHTTP1, UpstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
	}, evidence)
	if converted == nil {
		t.Fatal("failed transport lost its Activity evidence")
	}
	if err := converted.Validate(); err != nil {
		t.Fatalf("failed transport cannot be persisted: %v", err)
	}
	encoded, err := json.Marshal(converted)
	if err != nil {
		t.Fatal(err)
	}
	var restored activity.TransportEvidence
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.FallbackReason != string(transportprofile.FallbackObservationUnavailable) ||
		restored.Effective != nil || len(restored.FallbackChain) != 1 {
		t.Fatalf("failed transport serialization=%s", encoded)
	}
}

type transportEvidenceNoDialer struct{}

func (transportEvidenceNoDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("unexpected dial during preflight test")
}
