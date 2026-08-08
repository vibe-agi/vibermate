package connectionevent

import (
	"testing"
	"time"
)

// A record carries the host and the port as separate facts. A field named for
// the host that holds an authority says the port twice, and any reader that
// joins the two — a window, a report, a rule — renders it twice.
func TestARequestedHostIsAHostAndNotAnAuthority(t *testing.T) {
	t.Parallel()

	started := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	event := Event{
		ConnectionID:     "connection-authority",
		SourceConfidence: SourceConfidenceUnknown,
		RequestedHost:    "api.example.com:443",
		Port:             443,
		Decryption:       DecryptionNone,
		Phase:            PhaseAttempted,
		StartedAt:        started,
	}
	if err := event.Validate(); err == nil {
		t.Fatal("a requested host carrying a port was accepted")
	}
	event.RequestedHost = "api.example.com"
	if err := event.Validate(); err != nil {
		t.Fatalf("a plain host was rejected: %v", err)
	}
}

func TestAllowedMITMRequiresEnvironmentAndClientEndpointRelations(t *testing.T) {
	t.Parallel()

	event := Event{
		ConnectionID:         "connection-mitm",
		IngressID:            "capture-run/run-one",
		SourceLabel:          "claude",
		SourceConfidence:     SourceConfidenceConfigured,
		RequestedHost:        "api.example.com",
		RouteHost:            "gateway.example.com",
		Port:                 443,
		Decision:             DecisionAllow,
		RuleID:               "client_endpoint_exact",
		EgressScope:          EgressScopeEnvironment,
		EgressSource:         EgressSourceEnvironmentDefault,
		EgressPolicyRevision: 1,
		Decryption:           DecryptionMITM,
		Phase:                PhaseDecided,
		StartedAt:            time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
	}
	if err := event.Validate(); err == nil {
		t.Fatal("an allowed MITM decision without frozen relations was accepted")
	}
	event.EnvironmentID = "environment-one"
	event.EnvironmentName = "Work Claude"
	event.EnvironmentRevision = 3
	event.ClientEndpointID = "endpoint-one"
	event.ClientEndpointRevision = 2
	if err := event.Validate(); err != nil {
		t.Fatalf("a complete decision-time MITM Environment relation was rejected: %v", err)
	}
	event.ClientEndpointRevision = 1 << 63
	if err := event.Validate(); err == nil {
		t.Fatal("a ClientEndpoint revision outside the SQLite-safe range was accepted")
	}
	event.ClientEndpointRevision = 2
	event.Phase = PhaseConnected
	event.ObservedSNI = "api.example.com"
	if err := event.Validate(); err != nil {
		t.Fatalf("the frozen relation did not survive connection: %v", err)
	}
	event.Decryption = DecryptionBlind
	if err := event.Validate(); err == nil {
		t.Fatal("a blind connection carrying a ClientEndpoint relation was accepted")
	}
}

func TestBlindConnectionCanCarryEnvironmentWithoutClientEndpoint(t *testing.T) {
	t.Parallel()

	event := Event{
		ConnectionID:         "connection-blind-environment",
		IngressID:            "capture-run/run-one",
		SourceLabel:          "claude",
		SourceConfidence:     SourceConfidenceConfigured,
		EnvironmentID:        "system-transparent",
		EnvironmentName:      "Transparent",
		EnvironmentRevision:  1,
		RequestedHost:        "files.example.com",
		RouteHost:            "files.example.com",
		Port:                 443,
		Decision:             DecisionAllow,
		RuleID:               "network_default",
		EgressScope:          EgressScopeNetwork,
		EgressSource:         EgressSourceNetworkDefault,
		EgressPolicyRevision: 1,
		Decryption:           DecryptionBlind,
		Phase:                PhaseDecided,
		StartedAt:            time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("a blind connection lost its Environment assignment: %v", err)
	}
	event.ClientEndpointID = "unexpected-endpoint"
	if err := event.Validate(); err == nil {
		t.Fatal("partial ClientEndpoint evidence was accepted")
	}
	event.ClientEndpointRevision = 1
	if err := event.Validate(); err == nil {
		t.Fatal("a blind connection carrying complete ClientEndpoint evidence was accepted")
	}
}
