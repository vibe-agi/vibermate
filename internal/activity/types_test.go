package activity_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestExchangeActivityRequiresFrozenExecutionAndSourceRelations(
	t *testing.T,
) {
	t.Parallel()

	valid := validExchangeEvent(t)
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
			name: "missing Route revision",
			mutate: func(event *activity.Event) {
				event.RouteRevision = 0
			},
		},
		{
			name: "invalid Environment digest",
			mutate: func(event *activity.Event) {
				event.EnvironmentDigest = "not-a-digest"
			},
		},
		{
			name: "partial Account reference",
			mutate: func(event *activity.Event) {
				event.CredentialEpoch = 0
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

func TestExchangeExecutionEvidenceCannotLeakOntoAnotherActivityKind(
	t *testing.T,
) {
	t.Parallel()

	environmentID, err := environment.NewEnvironmentID("activity-environment")
	if err != nil {
		t.Fatal(err)
	}
	event := activity.Event{
		Kind:                activity.KindEnvironmentApplied,
		EnvironmentID:       environmentID,
		EnvironmentRevision: 3,
		EnvironmentDigest:   strings.Repeat("a", 64),
		SubjectID:           "activity-environment",
		Status:              activity.StatusSucceeded,
		SourceDisplayName:   "claude",
	}
	if err := event.Validate(); err == nil {
		t.Fatal("Exchange relationship evidence was accepted on an Environment event")
	}
}

func TestActivityRecordJSONCarriesOnlyEnvironmentFirstFrozenReferences(
	t *testing.T,
) {
	t.Parallel()

	event := validExchangeEvent(t)
	record := activity.Record{
		Sequence:               1,
		ID:                     "activity-json",
		OccurredAt:             testTime,
		Kind:                   event.Kind,
		EnvironmentID:          event.EnvironmentID.String(),
		EnvironmentRevision:    uint64(event.EnvironmentRevision),
		EnvironmentDigest:      event.EnvironmentDigest,
		ClientEndpointID:       event.ClientEndpointID.String(),
		ClientEndpointRevision: uint64(event.ClientEndpointRevision),
		ProtocolPlanID:         event.ProtocolPlanID.String(),
		ProtocolPlanRevision:   uint64(event.ProtocolPlanRevision),
		RouteID:                event.RouteID.String(),
		RouteRevision:          uint64(event.RouteRevision),
		AccountID:              event.AccountID,
		AccountRevision:        event.AccountRevision,
		CredentialEpoch:        event.CredentialEpoch,
		SubjectID:              event.SubjectID,
		Status:                 event.Status,
		SourceKind:             event.SourceKind,
		SourceDisplayName:      event.SourceDisplayName,
		SourceRecognition:      event.SourceRecognition,
		CaptureRunID:           event.CaptureRunID,
		ConnectionID:           event.ConnectionID,
		Conversation:           conversationPtr(event.Conversation),
	}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, required := range []string{
		`"environmentId":"activity-environment"`,
		`"clientEndpointId":"claude-messages"`,
		`"protocolPlanId":"anthropic-plan"`,
		`"routeId":"claude-official"`,
		`"accountId":"anthropic-work"`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Activity JSON %s does not contain %s", text, required)
		}
	}
	for _, forbidden := range []string{"accessId", "profileId", "agentEndpointId"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Activity JSON retained obsolete field %q: %s", forbidden, text)
		}
	}
}

var testTime = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

func validExchangeEvent(t *testing.T) activity.Event {
	t.Helper()
	environmentID, err := environment.NewEnvironmentID("activity-environment")
	if err != nil {
		t.Fatal(err)
	}
	endpointID, err := environment.NewClientEndpointID("claude-messages")
	if err != nil {
		t.Fatal(err)
	}
	protocolPlanID, err := environment.NewClientProtocolPlanID("anthropic-plan")
	if err != nil {
		t.Fatal(err)
	}
	routeID, err := environment.NewUpstreamRouteID("claude-official")
	if err != nil {
		t.Fatal(err)
	}
	return activity.Event{
		Kind:                   activity.KindExchangeCompleted,
		EnvironmentID:          environmentID,
		EnvironmentRevision:    3,
		EnvironmentDigest:      strings.Repeat("a", 64),
		ClientEndpointID:       endpointID,
		ClientEndpointRevision: 4,
		ProtocolPlanID:         protocolPlanID,
		ProtocolPlanRevision:   5,
		RouteID:                routeID,
		RouteRevision:          6,
		AccountID:              "anthropic-work",
		AccountRevision:        7,
		CredentialEpoch:        8,
		SubjectID:              "exchange-1",
		Status:                 activity.StatusSucceeded,
		SourceKind:             activity.SourceCaptureRun,
		SourceDisplayName:      "claude",
		SourceRecognition:      activity.SourceRecognitionConfigured,
		CaptureRunID:           "run-1",
		ConnectionID:           "connection-1",
		Conversation: agentconversation.Ref{
			ProjectionID: "capture_run:run-1:main",
			DisplayName:  "claude",
			Kind:         agentconversation.KindMain,
			Evidence:     agentconversation.EvidenceCaptureRun,
		},
	}
}

func conversationPtr(value agentconversation.Ref) *agentconversation.Ref {
	return &value
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

func TestExchangePageRequestHasOneTypedCaptureAuthority(t *testing.T) {
	t.Parallel()

	for _, valid := range []activity.PageRequest{
		{Limit: 1},
		{Limit: 200, CaptureRunID: "run-one"},
		{Limit: 50, ManualCaptureID: "manual-one"},
		{Limit: 50, ManualCaptureID: "manual-one", EnvironmentID: "work"},
	} {
		if err := valid.Validate(); err != nil {
			t.Fatalf("valid PageRequest rejected: %+v: %v", valid, err)
		}
	}

	for _, invalid := range []activity.PageRequest{
		{},
		{Limit: 201},
		{Limit: 50, CaptureRunID: "run/one"},
		{Limit: 50, ManualCaptureID: "manual/one"},
		{Limit: 50, CaptureRunID: "run-one", ManualCaptureID: "manual-one"},
		{Limit: 50, EnvironmentID: "not an Environment"},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid PageRequest accepted: %+v", invalid)
		}
	}
}
