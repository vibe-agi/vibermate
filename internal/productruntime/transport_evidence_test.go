package productruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

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
