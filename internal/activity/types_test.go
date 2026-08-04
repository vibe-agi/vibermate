package activity_test

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/activity"
)

func TestTransportEvidenceRejectsPresentationAndTransportContradictions(
	t *testing.T,
) {
	t.Parallel()

	profile := activity.TransportProfileEvidence{
		Ref:      "observed-client-strict-h1",
		Revision: 1,
		Source:   "observed_client",
	}
	valid := activity.TransportEvidence{
		Presentation: &activity.WirePresentationEvidence{
			RequestedRef:     "follow-client",
			EffectiveRef:     "follow-client",
			Revision:         1,
			Mode:             "follow_client",
			ClientProtocol:   "http/1.1",
			UpstreamProtocol: "http/1.1",
		},
		Requested:     &profile,
		Effective:     &profile,
		FallbackChain: []activity.TransportProfileEvidence{profile},
		HTTPTransport: "http1",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid evidence rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*activity.TransportEvidence)
	}{
		{
			name: "missing product-level presentation",
			mutate: func(value *activity.TransportEvidence) {
				value.Presentation = nil
			},
		},
		{
			name: "follow-client backed by a named transport",
			mutate: func(value *activity.TransportEvidence) {
				value.Requested.Source = "named_profile"
				value.Effective.Source = "named_profile"
				value.FallbackChain[0].Source = "named_profile"
			},
		},
		{
			name: "presentation protocol disagrees with transport",
			mutate: func(value *activity.TransportEvidence) {
				value.Presentation.ClientProtocol = "h2"
				value.Presentation.UpstreamProtocol = "h2"
			},
		},
		{
			name: "emulated product has no evidence digest",
			mutate: func(value *activity.TransportEvidence) {
				value.Presentation.Mode = "emulate_product"
				value.Presentation.Product = "claude_code"
				value.Requested.Source = "named_profile"
				value.Effective.Source = "named_profile"
				value.FallbackChain[0].Source = "named_profile"
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := valid.Clone()
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("contradictory transport evidence was accepted")
			}
		})
	}
}
