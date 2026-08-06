package desktopcontrol

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

func TestExchangeDetailProjectsOrderedRedactedEvidence(t *testing.T) {
	t.Parallel()

	newAttempt := func(id string, upstreamID string) egressaudit.Attempt {
		t.Helper()
		attempt, err := egressaudit.New(egressaudit.NewInput{
			ID:           id,
			ConnectionID: "connection-detail",
			Purpose:      egressaudit.PurposeProviderAttempt,
			PayloadClass: egressaudit.PayloadClientSemantic,
			Parent: egressaudit.ParentRef{
				Kind:       egressaudit.ParentUpstreamAttempt,
				ID:         upstreamID,
				ExchangeID: "exchange-detail",
			},
			Caller:       egressaudit.CallerCore,
			TargetOrigin: "https://provider.example:443",
			Decision: egressaudit.DecisionRef{
				PolicyID:       "policy-detail",
				PolicyRevision: 1,
				Authority:      egressaudit.AuthorityAccess,
				RuleID:         "rule-detail",
				ProxyID:        "company-proxy",
			},
			StartedAt: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatal(err)
		}
		return attempt
	}
	record := activity.Record{
		Sequence:          7,
		ID:                "activity-detail",
		OccurredAt:        time.Date(2026, 8, 3, 12, 0, 2, 0, time.UTC),
		Kind:              activity.KindExchangeCompleted,
		AccessID:          "access-detail",
		AccessName:        "Detail Access",
		AccessRevision:    4,
		SubjectID:         "exchange-detail",
		Status:            activity.StatusFailed,
		ReasonCode:        "provider_transport_failed",
		SourceKind:        activity.SourceSystemProxy,
		SourceDisplayName: "ViberMate runtime",
		SourceRecognition: activity.SourceRecognitionUnknown,
		IngressProfileID:  "system-proxy",
		ConnectionID:      "connection-detail",
	}
	detail, err := exchangeDetailOf(record, egressaudit.Page{
		Items: []egressaudit.Record{
			{Sequence: 12, Attempt: newAttempt("egress-2", "attempt-2")},
			{Sequence: 11, Attempt: newAttempt("egress-1", "attempt-1")},
			{Sequence: 10, Attempt: newAttempt("egress-0", "attempt-1")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.ID != record.SubjectID ||
		detail.AccessID != record.AccessID ||
		detail.Status != string(record.Status) ||
		detail.ProcessingTrace.Result != record.ReasonCode ||
		detail.ProcessingTrace.EgressProxyID != "company-proxy" ||
		len(detail.ProcessingTrace.PluginRunIDs) != 0 ||
		len(detail.ProcessingTrace.AttemptIDs) != 2 ||
		detail.ProcessingTrace.AttemptIDs[0] != "attempt-1" ||
		detail.ProcessingTrace.AttemptIDs[1] != "attempt-2" {
		t.Fatalf("Exchange detail = %+v", detail)
	}
	if _, err := exchangeDetailOf(record, egressaudit.Page{
		NextCursor: "more-evidence",
	}); err == nil {
		t.Fatal("a truncated Exchange detail was accepted")
	}
}

func TestActivityCursorIsCanonicalAndCollectionScoped(t *testing.T) {
	t.Parallel()

	cursor, err := activityCursor(42)
	if err != nil {
		t.Fatal(err)
	}
	if cursor == "42" {
		t.Fatal("Activity cursor exposed the sequence directly")
	}
	sequence, err := parseActivityCursor(cursor)
	if err != nil || sequence != 42 {
		t.Fatalf("parseActivityCursor() = %d, %v", sequence, err)
	}
	for _, invalid := range []string{
		"",
		"42",
		cursor + "=",
		base64.RawURLEncoding.EncodeToString([]byte("v1:42")),
		base64.RawURLEncoding.EncodeToString(
			[]byte(activityCursorPrefix + "042"),
		),
		base64.RawURLEncoding.EncodeToString(
			[]byte("v2:activity-requests:42"),
		),
		base64.RawURLEncoding.EncodeToString(
			[]byte("v1:connections:42"),
		),
	} {
		if _, err := parseActivityCursor(invalid); err == nil {
			t.Fatalf("parseActivityCursor(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestActivityListQueryIsClosedAndBounded(t *testing.T) {
	t.Parallel()

	defaults, err := parseActivityListQuery("")
	if err != nil || defaults.limit != 50 || defaults.beforeSequence != 0 {
		t.Fatalf("default Activity query = %+v, %v", defaults, err)
	}
	cursor, err := activityCursor(91)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseActivityListQuery(
		"limit=200&cursor=" + cursor +
			"&kind=exchange&captureRunId=run-one&accessId=work",
	)
	if err != nil ||
		parsed.limit != 200 ||
		parsed.beforeSequence != 91 ||
		parsed.captureRunID != "run-one" ||
		parsed.accessID != "work" {
		t.Fatalf("parsed Activity query = %+v, %v", parsed, err)
	}
	for _, invalid := range []string{
		"cursor=",
		"cursor=not-a-cursor",
		"limit=",
		"limit=0",
		"limit=201",
		"limit=one",
		"unknown=1",
		"beforeSequence=91",
		"limit=1&limit=2",
		"cursor=" + cursor + "&cursor=" + cursor,
		"captureRunId=",
		"captureRunId=run-one&captureRunId=run-two",
		"accessId=",
		"accessId=work&accessId=personal",
		"kind=connection",
		"kind=exchange&kind=exchange",
		"limit=10;cursor=" + cursor,
		"cursor=%zz",
	} {
		if _, err := parseActivityListQuery(invalid); err == nil {
			t.Fatalf("parseActivityListQuery(%q) unexpectedly succeeded", invalid)
		}
	}
}
