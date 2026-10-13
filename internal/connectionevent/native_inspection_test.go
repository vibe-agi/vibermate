package connectionevent

import (
	"testing"
	"time"
)

func TestNativeInspectionHasExplicitNonSemanticPurpose(t *testing.T) {
	good := Event{InspectionPurpose: InspectionPurposeClientTools, ConnectionID: "native", IngressID: "capture-run/native-synthetic", SourceLabel: "Synthetic", SourceConfidence: SourceConfidenceConfigured, EnvironmentID: "native", EnvironmentName: "Native", EnvironmentRevision: 1, RequestedHost: "native.example", RouteHost: "native.example", Port: 443, Decision: DecisionAllow, RuleID: "native", EgressScope: EgressScopeEnvironment, EgressSource: EgressSourceEnvironmentDefault, EgressPolicyRevision: 1, Decryption: DecryptionMITM, Phase: PhaseDecided, StartedAt: time.Now().UTC()}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func(*Event){func(e *Event) { e.InspectionPurpose = "guessed" }, func(e *Event) { e.InspectionPurpose = "" }, func(e *Event) { e.EnvironmentName = "" }, func(e *Event) { e.ClientEndpointID = "fake-model"; e.ClientEndpointRevision = 1 }, func(e *Event) { e.Decryption = DecryptionBlind }, func(e *Event) { e.Decryption = DecryptionCleartext }, func(e *Event) { e.Decision = DecisionDeny }} {
		event := good
		mutation(&event)
		if event.Validate() == nil {
			t.Fatalf("invalid native inspection accepted %+v", event)
		}
	}
}
