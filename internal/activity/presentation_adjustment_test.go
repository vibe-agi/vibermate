package activity_test

import (
	"encoding/json"
	"testing"

	"github.com/vibe-agi/vibermate/internal/activity"
)

func TestCleartextPresentationAdjustmentRejectsContradictoryEvidence(t *testing.T) {
	valid := activity.TransportEvidence{Presentation: &activity.WirePresentationEvidence{RequestedRef: "follow-client", EffectiveRef: "follow-client", Revision: 1, Mode: "follow_client", ClientProtocol: "h2", UpstreamProtocol: "http/1.1", ProtocolAdjustment: "cleartext_http1"}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	var restored activity.TransportEvidence
	if err = json.Unmarshal(encoded, &restored); err != nil || restored.Validate() != nil || restored.Presentation.ProtocolAdjustment != "cleartext_http1" {
		t.Fatal("cleartext fact lost in JSON roundtrip")
	}
	profile := activity.TransportProfileEvidence{Ref: "observed-client-strict-h1", Revision: 1, Source: "observed_client"}
	for _, testCase := range []struct {
		name   string
		change func(*activity.TransportEvidence)
	}{
		{"missing fact", func(e *activity.TransportEvidence) { e.Presentation.ProtocolAdjustment = "" }},
		{"unknown fact", func(e *activity.TransportEvidence) { e.Presentation.ProtocolAdjustment = "tls_fallback" }},
		{"same protocol", func(e *activity.TransportEvidence) { e.Presentation.UpstreamProtocol = "h2" }},
		{"emulated", func(e *activity.TransportEvidence) {
			e.Presentation.Mode = "emulate_product"
			e.Presentation.Product = "claude_code"
		}},
		{"unavailable", func(e *activity.TransportEvidence) { e.Presentation.EffectiveRef = "" }},
		{"profile changed", func(e *activity.TransportEvidence) { e.Presentation.EffectiveRef = "claude-code" }},
		{"TLS requested", func(e *activity.TransportEvidence) { e.Requested = &profile }},
		{"TLS effective", func(e *activity.TransportEvidence) { e.Effective = &profile }},
		{"TLS fallback", func(e *activity.TransportEvidence) { e.FallbackChain = []activity.TransportProfileEvidence{profile} }},
		{"TLS reason", func(e *activity.TransportEvidence) { e.FallbackReason = "observation_unavailable" }},
		{"client ALPN", func(e *activity.TransportEvidence) { e.ClientOfferedALPN = []string{"h2"} }},
		{"downstream ALPN", func(e *activity.TransportEvidence) { e.DownstreamNegotiatedALPN = "h2" }},
		{"upstream ALPN offered", func(e *activity.TransportEvidence) { e.UpstreamOfferedALPN = []string{"http/1.1"} }},
		{"upstream ALPN", func(e *activity.TransportEvidence) { e.UpstreamNegotiatedALPN = "http/1.1" }},
		{"fingerprint transport", func(e *activity.TransportEvidence) { e.HTTPTransport = "http1" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			copy := valid.Clone()
			testCase.change(&copy)
			if copy.Validate() == nil {
				t.Fatal("contradictory cleartext evidence accepted")
			}
		})
	}
}
