package productruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
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
