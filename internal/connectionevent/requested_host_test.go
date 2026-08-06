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

func TestAllowedMITMRequiresOneCompleteDecisionTimeAccessRelation(t *testing.T) {
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
		RuleID:               "agent_endpoint_exact",
		EgressScope:          EgressScopeAccess,
		EgressSource:         EgressSourceAccessDefault,
		EgressPolicyRevision: 1,
		Decryption:           DecryptionMITM,
		Phase:                PhaseDecided,
		StartedAt:            time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
	}
	if err := event.Validate(); err == nil {
		t.Fatal("an allowed MITM decision without an Access relation was accepted")
	}
	event.AccessID = "access-one"
	event.AccessName = "Work Claude"
	event.AccessRevision = 3
	event.AgentEndpointID = "endpoint-one"
	event.AgentEndpointRevision = 2
	if err := event.Validate(); err != nil {
		t.Fatalf("a complete decision-time MITM Access relation was rejected: %v", err)
	}
	event.Phase = PhaseConnected
	event.ObservedSNI = "api.example.com"
	if err := event.Validate(); err != nil {
		t.Fatalf("the frozen relation did not survive connection: %v", err)
	}
	event.Decryption = DecryptionBlind
	if err := event.Validate(); err == nil {
		t.Fatal("a blind connection carrying an Access relation was accepted")
	}
}
