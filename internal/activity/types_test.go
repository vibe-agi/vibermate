package activity_test

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/activity"
)

func TestExchangeActivityRequiresExactRunConnectionAndAccessRelations(
	t *testing.T,
) {
	t.Parallel()

	accessID, err := access.NewAccessID("activity-access")
	if err != nil {
		t.Fatal(err)
	}
	valid := activity.Event{
		Kind:              activity.KindExchangeCompleted,
		AccessID:          accessID,
		AccessName:        "Work Claude",
		AccessRevision:    3,
		SubjectID:         "exchange-1",
		Status:            activity.StatusSucceeded,
		SourceKind:        activity.SourceCaptureRun,
		SourceDisplayName: "claude",
		SourceRecognition: activity.SourceRecognitionConfigured,
		CaptureRunID:      "run-1",
		IngressProfileID:  "capture-run/run-1",
		ConnectionID:      "connection-1",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid relationship rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*activity.Event)
	}{
		{
			name: "missing connection",
			mutate: func(event *activity.Event) {
				event.ConnectionID = ""
			},
		},
		{
			name: "mismatched ingress identity",
			mutate: func(event *activity.Event) {
				event.IngressProfileID = "capture-run/run-2"
			},
		},
		{
			name: "unknown managed-run attribution",
			mutate: func(event *activity.Event) {
				event.SourceRecognition = activity.SourceRecognitionUnknown
			},
		},
		{
			name: "mixed manual and managed identities",
			mutate: func(event *activity.Event) {
				event.ManualCaptureID = "manual-1"
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := valid
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid relationship was accepted")
			}
		})
	}
}

func TestExchangeRelationshipEvidenceCannotLeakOntoAnotherActivityKind(
	t *testing.T,
) {
	t.Parallel()

	accessID, err := access.NewAccessID("activity-access")
	if err != nil {
		t.Fatal(err)
	}
	event := activity.Event{
		Kind:              activity.KindAccessApplied,
		AccessID:          accessID,
		AccessName:        "Work Claude",
		SubjectID:         "activity-access",
		Status:            activity.StatusSucceeded,
		SourceDisplayName: "claude",
	}
	if err := event.Validate(); err == nil {
		t.Fatal("Exchange relationship evidence was accepted on an Access event")
	}
}

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
